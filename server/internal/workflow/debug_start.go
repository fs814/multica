package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"strings"
	"time"
)

type DraftTestRequest struct {
	SchemaVersion               string          `json:"schema_version"`
	ExpectedRevision            int64           `json:"expected_revision"`
	ExpectedDebugPolicyRevision int64           `json:"expected_debug_policy_revision"`
	BaseDraftVersionID          *string         `json:"base_draft_version_id"`
	Definition                  json.RawMessage `json:"definition"`
	Input                       json.RawMessage `json:"input"`
	ProjectID                   *string         `json:"project_id"`
	ImageAttachmentID           *string         `json:"image_attachment_id"`
	IdempotencyKey              string          `json:"idempotency_key"`
	ExecutionAcknowledged       bool            `json:"execution_acknowledged"`
}
type DraftEnvironmentResolver func(context.Context, *db.Queries, pgtype.UUID, pgtype.UUID, *string) (json.RawMessage, error)

func debugHash(b []byte) string { sum := sha256.Sum256(b); return hex.EncodeToString(sum[:]) }
func debugUUID(s *string) (pgtype.UUID, error) {
	if s == nil {
		return pgtype.UUID{}, nil
	}
	var id pgtype.UUID
	if err := id.Scan(*s); err != nil || !id.Valid {
		return id, newEngineError("debug_bad_request", "invalid UUID")
	}
	return id, nil
}

// StartDraftTest materializes the immutable snapshot and first engine transition
// in one READ COMMITTED transaction. A successful replay precedes all admission
// decisions, including a newly disabled feature and a changed template revision.
func (e *Engine) StartDraftTest(ctx context.Context, ws, user, templateID pgtype.UUID, in DraftTestRequest) (*StartRunResult, error) {
	if in.SchemaVersion != "1" || !in.ExecutionAcknowledged || in.ExpectedRevision < 1 || in.ExpectedDebugPolicyRevision < 1 || strings.TrimSpace(in.IdempotencyKey) == "" || len(in.IdempotencyKey) > 128 {
		return nil, newEngineError("debug_bad_request", "invalid draft trial request")
	}
	if len(in.Definition) > 256*1024 || len(in.Input) > 64*1024 {
		return nil, newEngineError("debug_payload_too_large", "draft trial payload exceeds its limit")
	}
	var err error
	if in.Definition, err = canonicalJSON(in.Definition); err != nil {
		return nil, newEngineError("debug_bad_request", "invalid definition JSON")
	}
	if in.Input, err = canonicalJSON(in.Input); err != nil {
		return nil, newEngineError("debug_bad_request", "invalid input JSON")
	}
	if _, err = debugUUID(in.ProjectID); err != nil {
		return nil, err
	}
	base, err := debugUUID(in.BaseDraftVersionID)
	if err != nil {
		return nil, err
	}
	key := debugHash(mustJSON([]string{"draft_test", uuidString(ws), uuidString(user), "workflow-templates/" + uuidString(templateID) + "/test-runs", in.IdempotencyKey}))
	raw, err := json.Marshal(in)
	if err != nil {
		return nil, err
	}
	hash := debugHash(raw)
	replay := func(ctx context.Context, q *db.Queries) (*StartRunResult, error) {
		if err := debugMember(ctx, q, ws, user, false); err != nil {
			return nil, err
		}
		old, err := q.GetWorkflowRunByIdempotencyKey(ctx, db.GetWorkflowRunByIdempotencyKeyParams{WorkspaceID: ws, IdempotencyKey: key})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		if old.ExecutionMode != ExecutionDraftTest || old.AccountableUserID != user || old.TemplateID != templateID || old.DebugRequestHash.String != hash {
			return nil, newEngineError(ErrCodeIdempotencyConflict, "idempotency key was used for another request")
		}
		if old.DetailsPurgedAt.Valid {
			return nil, newEngineError("debug_details_expired", "draft trial details have expired")
		}
		return &StartRunResult{Run: old, AlreadyExisted: true}, nil
	}
	if old, err := replay(ctx, e.Queries); old != nil || err != nil {
		return old, err
	}
	if e.TxStarter == nil {
		return nil, newEngineError("debug_unavailable", "transactional draft trials unavailable")
	}
	for attempt := 0; attempt < 3; attempt++ {
		var result *StartRunResult
		effects := &txEffects{}
		err = e.debugTransaction(ctx, effects, func(ctx context.Context, q *db.Queries) error {
			if err := q.EnsureWorkflowDebugQuota(ctx, ws); err != nil {
				return err
			}
			quota, err := q.LockWorkflowDebugQuota(ctx, ws)
			if err != nil {
				return err
			}
			// This must remain a separate statement after acquiring the quota lock.
			if old, err := replay(ctx, q); old != nil || err != nil {
				result = old
				return err
			}
			if !e.DebugReady || e.ResolveDraftEnvironment == nil {
				return newEngineError("debug_unavailable", "draft trial capability is not ready")
			}
			if err := q.EnsureWorkflowDebugPolicy(ctx, ws); err != nil {
				return err
			}
			policy, err := q.GetWorkflowDebugPolicy(ctx, ws)
			if err != nil {
				return err
			}
			if policy.Revision != in.ExpectedDebugPolicyRevision {
				return newEngineError("debug_policy_changed", "trial settings changed; confirm again")
			}
			if !policy.Enabled {
				return newEngineError("debug_unavailable", "draft trials are disabled")
			}
			if err := debugMember(ctx, q, ws, user, true); err != nil {
				return err
			}
			template, err := q.GetWorkflowTemplateForUpdate(ctx, db.GetWorkflowTemplateForUpdateParams{ID: templateID, WorkspaceID: ws})
			if errors.Is(err, pgx.ErrNoRows) {
				return newEngineError(ErrCodeNotFound, "workflow template not found")
			}
			if err != nil {
				return err
			}
			if template.CreatedByType == "system" {
				return newEngineError("debug_forbidden", "copy a built-in workflow before testing")
			}
			if template.Status == "archived" || template.Revision != in.ExpectedRevision {
				return newEngineError("debug_template_changed", "template changed; refresh its editing baseline")
			}
			draft, err := q.GetWorkflowDraftVersionForUpdate(ctx, db.GetWorkflowDraftVersionForUpdateParams{WorkspaceID: ws, TemplateID: templateID})
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			if (err == nil && draft.ID != base) || (errors.Is(err, pgx.ErrNoRows) && base.Valid) {
				return newEngineError("debug_template_changed", "draft identity changed")
			}
			def, err := ParseDefinition(in.Definition)
			if err != nil {
				return err
			}
			duration, err := debugDuration(def.Limits.MaxDurationSeconds, DebugPolicyFromRow(policy), DefaultWorkspacePolicy)
			if err != nil {
				return err
			}
			// Apply only the unset duration for validation. The immutable snapshot keeps
			// the user's graph intact; run.policy carries the effective execution limit.
			validated := *def
			validated.Limits = def.Limits
			validated.Limits.MaxDurationSeconds = duration
			if err = Validate(&validated, DefaultWorkspacePolicy, e.Schemas); err != nil {
				return err
			}
			entry, _ := def.NodeByKey(def.EntryNode)
			if entry.EffectiveInputMode() == InputModeScripts {
				return newEngineError(ErrCodeInvalidSubmission, "publish the script pipeline before running it")
			}
			var input map[string]any
			if json.Unmarshal(in.Input, &input) != nil || input == nil {
				return newEngineError("debug_bad_request", "input must be an object")
			}
			title, _ := input["title"].(string)
			description, _ := input["description"].(string)
			if strings.TrimSpace(title) == "" || strings.TrimSpace(description) == "" {
				return newEngineError(ErrCodeInvalidDefinition, "title and description are required")
			}
			if err = ValidateRunInput(in.Input, entry); entry.Type == NodeTypeInput && err != nil {
				return err
			}
			var image *ImageAttachmentRef
			if entry.Type == NodeTypeInput && entry.EffectiveInputMode() == InputModeImage {
				id := ""
				if in.ImageAttachmentID != nil {
					id = *in.ImageAttachmentID
				}
				image, err = resolveWorkflowImageAttachment(ctx, q, ws, id)
				if err != nil {
					return err
				}
			} else if in.ImageAttachmentID != nil {
				return newEngineError(ErrCodeInvalidDefinition, "an image requires image input mode")
			}
			environment, err := e.ResolveDraftEnvironment(ctx, q, ws, user, in.ProjectID)
			if err != nil {
				return err
			}
			environment, err = canonicalJSON(environment)
			if err != nil {
				return err
			}
			payloadBytes := int64(len(in.Definition) + len(in.Input) + len(environment))
			usage, err := q.GetWorkflowDebugUsage(ctx, db.GetWorkflowDebugUsageParams{WorkspaceID: ws, UserID: user})
			if err != nil {
				return err
			}
			if err = checkDebugQuota(policy, usage, quota.PayloadBytes, payloadBytes); err != nil {
				return err
			}
			schema := int32(1)
			if def.SchemaVersion == GraphSchemaVersion {
				schema = 2
			}
			acceptedAt, err := q.WorkflowDebugDatabaseTime(ctx)
			if err != nil {
				return err
			}
			snapshot, err := q.CreateWorkflowExecutionSnapshot(ctx, db.CreateWorkflowExecutionSnapshotParams{WorkspaceID: ws, TemplateID: templateID, BaseRevision: in.ExpectedRevision, BaseDraftVersionID: base, Definition: in.Definition, GraphSchemaVersion: schema, DefinitionHash: debugHash(in.Definition), EnvironmentSnapshot: environment, CreatedBy: user})
			if err != nil {
				return err
			}
			runContext, err := marshalWorkflowRunContext(image)
			if err != nil {
				return err
			}
			run, err := q.CreateWorkflowDraftTestRun(ctx, db.CreateWorkflowDraftTestRunParams{WorkspaceID: ws, TemplateID: templateID, IdempotencyKey: key, AccountableUserID: user, Input: in.Input, Context: runContext, Policy: mustJSON(validated.EffectiveLimits()), ExecutionSnapshotID: snapshot.ID, DebugDeadlineAt: pgtype.Timestamptz{Time: acceptedAt.Time.Add(time.Duration(duration) * time.Second), Valid: true}, DebugRequestHash: pgtype.Text{String: hash, Valid: true}, DebugPolicyRevision: pgtype.Int8{Int64: policy.Revision, Valid: true}, DebugRetentionSeconds: pgtype.Int4{Int32: policy.RetentionSeconds, Valid: true}, DebugPayloadBytes: pgtype.Int8{Int64: payloadBytes, Valid: true}, CreatedAt: acceptedAt})
			if err != nil {
				return err
			}
			if _, err = q.AddWorkflowDebugPayloadBytes(ctx, db.AddWorkflowDebugPayloadBytesParams{WorkspaceID: ws, Bytes: payloadBytes}); err != nil {
				return err
			}
			if err = e.recordEvent(ctx, q, eventSpec{WorkspaceID: ws, RunID: run.ID, Type: EventRunStarted, IdempotencyKey: key + ":started", ActorType: "member", ActorID: user, Payload: mustJSON(map[string]any{"execution_mode": ExecutionDraftTest, "snapshot_id": uuidString(snapshot.ID)})}); err != nil {
				return err
			}
			running, err := q.MarkWorkflowRunRunning(ctx, db.MarkWorkflowRunRunningParams{ID: run.ID, WorkspaceID: ws})
			if err != nil {
				return err
			}
			if _, err = e.activateNode(ctx, q, activateInput{Run: running, Def: def, Node: entry, Attempt: 1, ActorType: "member", ActorID: user, Effects: effects}); err != nil {
				return err
			}
			effects.runChanged(running)
			final, err := q.GetWorkflowRun(ctx, db.GetWorkflowRunParams{ID: run.ID, WorkspaceID: ws})
			if err != nil {
				return err
			}
			result = &StartRunResult{Run: final}
			return nil
		})
		if err == nil {
			effects.flush(ctx, e.Notifier)
			return result, nil
		}
		var pgerr *pgconn.PgError
		if !errors.As(err, &pgerr) || (pgerr.Code != "40001" && pgerr.Code != "40P01" && pgerr.Code != "23505") {
			return nil, err
		}
	}
	return nil, &EngineError{Code: "debug_unavailable", Message: "draft trial transaction must be retried", Retryable: true}
}
func (e *Engine) debugTransaction(ctx context.Context, effects *txEffects, fn func(context.Context, *db.Queries) error) error {
	tx, err := e.TxStarter.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SET TRANSACTION ISOLATION LEVEL READ COMMITTED"); err != nil {
		return fmt.Errorf("set trial isolation: %w", err)
	}
	ctx = context.WithValue(ctx, txEffectsContextKey{}, effects)
	q := e.Queries.WithTx(tx)
	if err = fn(ctx, q); err != nil {
		return err
	}
	if err = e.projectRunIssueStatuses(ctx, q, effects); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
