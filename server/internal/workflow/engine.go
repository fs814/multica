package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Engine executes the workflow commands from plan section 7: StartRun,
// ActivateStep, RecordTaskTerminal, SubmitResult, DecideAcceptance, CancelRun.
//
// Every command follows the same shape, and the ordering is what makes the
// system safe under replay and crash:
//
//  1. begin a transaction
//  2. lock the Run row, then the Step row (always that order, so concurrent
//     commands on one Run serialize instead of deadlocking)
//  3. validate the transition against the state machine
//  4. write the state change AND the workflow_event in the same transaction —
//     the event's unique idempotency key is what makes a replayed command roll
//     back rather than advance state twice
//  5. create the Agent Task in that SAME transaction when a Step activates, so
//     there is no window where a Step is queued with no Task or a Task exists
//     with no owning Step
//  6. commit, and only then emit realtime notifications
//
// Step 5 is the reason this package talks to the task queue through a
// transaction-scoped *db.Queries rather than calling TaskService: "Step
// activation and Task enqueue must share one application transaction ...
// dual-write repair must not be the normal path" (plan section 7).
type Engine struct {
	Queries   *db.Queries
	TxStarter TxStarter

	// Router resolves which Agent runs an Agent node.
	Router Router
	// Notifier emits post-commit realtime invalidation and daemon wakeups. Kept
	// behind an interface so the engine stays unit-testable without a hub.
	Notifier Notifier
	// Schemas validates submission schema names at publish time.
	Schemas SchemaRegistry
	// Now is injectable so tests can exercise timeout logic deterministically.
	Now func() time.Time
	// Metrics is optional; production wires a bounded-label collector and tests
	// may leave it nil.
	Metrics Metrics
}

type Metrics interface {
	RecordEvent(event string)
	RecordReconciliation(outcome string)
	ObserveFanOut(children int)
	ObserveRun(status string, seconds float64)
	ObserveStep(nodeType, status string, seconds float64)
	ObserveAcceptanceWait(seconds float64)
	RecordCallback(outcome string)
	SetOldestQueuedCallback(seconds float64)
	SetOldestStalledRun(seconds float64)
}

// TxStarter matches the existing service-layer interface (service.TxStarter), so
// *pgxpool.Pool satisfies it and the engine wires up the same way every other
// service does.
type TxStarter interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// Router chooses the Agent for an Agent node, applying the candidate gates from
// plan section 8 (workspace, archive, invocation permission, accountable human,
// runtime, provider, concurrency, skills, budget).
//
// It returns the chosen agent, its runtime, and a human-readable reason that is
// persisted on the Step for audit.
type Router interface {
	Route(ctx context.Context, q *db.Queries, req RouteRequest) (RouteResult, error)
}

// RouteRequest carries everything the router needs to pick an Agent.
type RouteRequest struct {
	WorkspaceID       pgtype.UUID
	Run               db.WorkflowRun
	Node              *Node
	AccountableUserID pgtype.UUID
	// PriorAgentByNode lets RoutingPreviousStep reuse the Agent that ran an
	// earlier node, so a fix lands with whoever wrote the code.
	PriorAgentByNode map[string]pgtype.UUID
	// RequiresVision is true only for a validated image intake. It forces every
	// agent that receives the attachment reference to advertise the vision label.
	RequiresVision bool
}

// RouteResult is the routing outcome.
type RouteResult struct {
	AgentID   pgtype.UUID
	RuntimeID pgtype.UUID
	Reason    string
}

// Notifier receives post-commit notifications. Implementations publish to the
// realtime bus and wake the daemon that owns the runtime.
type Notifier interface {
	// WorkflowEvent projects the exact persisted event envelope after commit.
	WorkflowEvent(ctx context.Context, event EventEnvelope)
	// WorkflowChanged invalidates client-side queries for a Run.
	WorkflowChanged(ctx context.Context, workspaceID, runID string)
	// IssueChanged reconciles the Issue projection after a Run status change.
	// It is emitted only after the transaction that changed both rows commits.
	IssueChanged(ctx context.Context, issue db.Issue, prevStatus string)
	// TaskEnqueued tells the runtime's daemon a task is waiting. Called only
	// after the transaction commits, so the daemon can never observe a task the
	// transaction later rolled back.
	TaskEnqueued(ctx context.Context, task db.AgentTaskQueue)
}

func (e *Engine) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

// runInTx runs fn inside a transaction, mirroring TaskService.runInTx so the
// rollback/commit discipline is identical across the codebase.
func (e *Engine) runInTx(ctx context.Context, effects *txEffects, fn func(context.Context, *db.Queries) error) error {
	txCtx := context.WithValue(ctx, txEffectsContextKey{}, effects)
	if e.TxStarter == nil {
		if err := fn(txCtx, e.Queries); err != nil {
			return err
		}
		return e.projectRunIssueStatuses(txCtx, e.Queries, effects)
	}
	tx, err := e.TxStarter.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)
	q := e.Queries.WithTx(tx)
	if err := fn(txCtx, q); err != nil {
		return err
	}
	if err := e.projectRunIssueStatuses(txCtx, q, effects); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// projectRunIssueStatuses applies the final Run state to its Issue before the
// command commits. A single command can record several intermediate Run changes
// (for example running -> completed when an Input node reaches End immediately),
// so this deliberately reloads each distinct Run once and projects only its
// final state.
//
// The Issue is a projection of Workflow execution, never its source of truth.
// Missing/deleted Issues are therefore ignored, while real database failures
// abort the transaction so a Run and its existing Issue cannot diverge.
func (e *Engine) projectRunIssueStatuses(ctx context.Context, q *db.Queries, effects *txEffects) error {
	if effects == nil || len(effects.runs) == 0 {
		return nil
	}

	seen := make(map[string]struct{}, len(effects.runs))
	for _, changed := range effects.runs {
		key := uuidString(changed.WorkspaceID) + ":" + uuidString(changed.ID)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}

		run, err := q.GetWorkflowRun(ctx, db.GetWorkflowRunParams{
			ID:          changed.ID,
			WorkspaceID: changed.WorkspaceID,
		})
		if err != nil {
			return fmt.Errorf("reload workflow run for issue projection: %w", err)
		}
		status, ok := IssueStatusForRun(RunStatus(run.Status))
		if !ok || !run.IssueID.Valid {
			continue
		}

		issue, err := q.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{
			ID:          run.IssueID,
			WorkspaceID: run.WorkspaceID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return fmt.Errorf("load workflow issue for status projection: %w", err)
		}
		if issue.Status == status {
			continue
		}

		updated, err := q.UpdateIssueStatus(ctx, db.UpdateIssueStatusParams{
			ID:          issue.ID,
			Status:      status,
			WorkspaceID: issue.WorkspaceID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return fmt.Errorf("project workflow run status onto issue: %w", err)
		}
		effects.issueChanged(updated, issue.Status)
	}
	return nil
}

// isUniqueViolation reports whether err is a Postgres unique-index violation.
// The engine relies on this to turn a replayed command into an idempotency
// conflict rather than a 500: the duplicate is the fence working as designed.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23505"
	}
	return false
}

// recordEvent appends the audit event for a state change. It MUST be called in
// the same transaction as the change it describes: that pairing plus the unique
// index on (workspace_id, idempotency_key) is the whole replay-safety argument.
func (e *Engine) recordEvent(ctx context.Context, q *db.Queries, ev eventSpec) error {
	payload := ev.Payload
	if payload == nil {
		payload = []byte("{}")
	}
	recorded, err := q.CreateWorkflowEvent(ctx, db.CreateWorkflowEventParams{
		WorkspaceID:    ev.WorkspaceID,
		RunID:          ev.RunID,
		StepID:         ev.StepID,
		EventType:      ev.Type,
		IdempotencyKey: ev.IdempotencyKey,
		ActorType:      ev.ActorType,
		ActorID:        ev.ActorID,
		Payload:        payload,
	})
	if err != nil {
		if isUniqueViolation(err) {
			return newEngineError(ErrCodeIdempotencyConflict,
				fmt.Sprintf("command %q was already applied", ev.IdempotencyKey))
		}
		return fmt.Errorf("record workflow event %q: %w", ev.Type, err)
	}
	envelope := newEventEnvelope(recorded)
	if effects, ok := ctx.Value(txEffectsContextKey{}).(*txEffects); ok && effects != nil {
		effects.eventRecorded(envelope)
	}
	if callbackEventType(ev.Type) {
		run, getErr := q.GetWorkflowRun(ctx, db.GetWorkflowRunParams{ID: ev.RunID, WorkspaceID: ev.WorkspaceID})
		if getErr != nil {
			return fmt.Errorf("load workflow run for callback: %w", getErr)
		}
		if run.CallbackDestinationID.Valid {
			callbackPayload := mustJSON(envelope)
			if _, createErr := q.CreateWorkflowCallbackDelivery(ctx, db.CreateWorkflowCallbackDeliveryParams{
				WorkspaceID:   ev.WorkspaceID,
				DestinationID: run.CallbackDestinationID,
				WorkflowRunID: run.ID,
				EventType:     ev.Type,
				EventKey:      envelope.EventID,
				Payload:       callbackPayload,
			}); createErr != nil && !errors.Is(createErr, pgx.ErrNoRows) {
				return fmt.Errorf("queue workflow callback %q: %w", ev.Type, createErr)
			}
		}
	}
	if e.Metrics != nil {
		e.Metrics.RecordEvent(ev.Type)
		switch ev.Type {
		case EventRunCompleted, EventRunFailed, EventRunBlocked, EventRunCancelled:
			if run, getErr := q.GetWorkflowRun(ctx, db.GetWorkflowRunParams{ID: ev.RunID, WorkspaceID: ev.WorkspaceID}); getErr == nil {
				e.Metrics.ObserveRun(run.Status, e.now().Sub(run.CreatedAt.Time).Seconds())
			}
		case EventStepPassed, EventStepFailed, EventStepBlocked, EventStepSkipped:
			if ev.StepID.Valid {
				if step, getErr := q.GetWorkflowStepInstance(ctx, db.GetWorkflowStepInstanceParams{ID: ev.StepID, WorkspaceID: ev.WorkspaceID}); getErr == nil {
					e.Metrics.ObserveStep(step.NodeType, step.Status, e.now().Sub(step.CreatedAt.Time).Seconds())
				}
			}
		case EventAcceptanceDecided:
			if ev.StepID.Valid {
				if step, getErr := q.GetWorkflowStepInstance(ctx, db.GetWorkflowStepInstanceParams{ID: ev.StepID, WorkspaceID: ev.WorkspaceID}); getErr == nil {
					e.Metrics.ObserveAcceptanceWait(e.now().Sub(step.CreatedAt.Time).Seconds())
				}
			}
		}
	}
	return nil
}

func callbackEventType(eventType string) bool {
	switch eventType {
	case EventRunStarted, EventRunCompleted, EventRunFailed, EventRunBlocked,
		EventRunCancelled, EventAcceptanceRequested, EventAcceptanceDecided:
		return true
	default:
		return false
	}
}

type eventSpec struct {
	WorkspaceID    pgtype.UUID
	RunID          pgtype.UUID
	StepID         pgtype.UUID
	Type           string
	IdempotencyKey string
	ActorType      string
	ActorID        pgtype.UUID
	Payload        []byte
}

// EventEnvelopeV1 is the single persisted workflow event projection used by
// callbacks and realtime. Websocket delivery is an invalidation hint and may
// be lost; event_id points consumers back to the durable workflow_event row.
type EventEnvelope struct {
	SchemaVersion string          `json:"schema_version"`
	EventID       string          `json:"event_id"`
	EventType     string          `json:"event_type"`
	OccurredAt    string          `json:"occurred_at"`
	WorkspaceID   string          `json:"workspace_id"`
	Source        string          `json:"source"`
	Subject       string          `json:"subject"`
	CorrelationID string          `json:"correlation_id"`
	CausationID   string          `json:"causation_id"`
	Payload       json.RawMessage `json:"payload"`
}

func newEventEnvelope(event db.WorkflowEvent) EventEnvelope {
	runID := uuidString(event.RunID)
	return EventEnvelope{
		SchemaVersion: "1",
		EventID:       uuidString(event.ID),
		EventType:     event.EventType,
		OccurredAt:    event.CreatedAt.Time.UTC().Format(time.RFC3339Nano),
		WorkspaceID:   uuidString(event.WorkspaceID),
		Source:        "multica.workflow",
		Subject:       "workflow_run/" + runID,
		CorrelationID: runID,
		CausationID:   event.IdempotencyKey,
		Payload:       json.RawMessage(event.Payload),
	}
}

// Event type names written to workflow_event.event_type. Bounded and normalized
// (plan section 12): these become metric labels, so they must not embed IDs.
const (
	EventRunStarted          = "run.started"
	EventRunCompleted        = "run.completed"
	EventRunFailed           = "run.failed"
	EventRunBlocked          = "run.blocked"
	EventRunCancelled        = "run.cancelled"
	EventStepActivated       = "step.activated"
	EventStepQueued          = "step.queued"
	EventStepSubmitted       = "step.submitted"
	EventStepPassed          = "step.passed"
	EventStepFailed          = "step.failed"
	EventStepBlocked         = "step.blocked"
	EventStepSkipped         = "step.skipped"
	EventAcceptanceRequested = "acceptance.requested"
	EventAcceptanceDecided   = "acceptance.decided"
)

// ---------------------------------------------------------------------------
// StartRun
// ---------------------------------------------------------------------------

// StartRunInput describes a new Run.
type StartRunInput struct {
	Instance    *InstanceStart
	WorkspaceID pgtype.UUID
	TemplateID  pgtype.UUID
	// TemplateVersionID pins the graph. Zero means "resolve the template's
	// current published version", which StartRun does inside the transaction so
	// a concurrent publish cannot change the answer mid-flight.
	TemplateVersionID pgtype.UUID
	IssueID           pgtype.UUID
	// EnforceIssueSnapshot turns the issue-pool candidate snapshot into an
	// optimistic fence after the existing Issue row is locked.
	EnforceIssueSnapshot   bool
	ExpectedIssueProjectID pgtype.UUID
	ExpectedIssueUpdatedAt pgtype.Timestamptz
	Source                 string
	SourceEventID          string
	// IdempotencyKey is required. Callers derive it from the originating event
	// (intake event id, autopilot run id) so a replay collides.
	IdempotencyKey        string
	AccountableUserID     pgtype.UUID
	RequestHash           string
	CallbackDestinationID pgtype.UUID
	// AutopilotRunID links a Workflow-bound Autopilot receipt inside the same
	// transaction as the Issue, Workflow Run, first event, Step, and Task. It is
	// intentionally narrow rather than a generic commit hook: no other caller
	// may add side effects to StartRun's transaction.
	AutopilotRunID pgtype.UUID
	// Issue is created in the same transaction as the Run and its first Task.
	// Leave nil only for legacy callers that intentionally start an unattached
	// Run; new manual, Autopilot, and external callers must provide it.
	Issue     *RunIssueInput
	Input     json.RawMessage
	ActorType string
	ActorID   pgtype.UUID
}

type RunIssueSubscriber struct {
	UserType string
	UserID   pgtype.UUID
	Reason   string
}

// RunIssueInput is the common Issue allocation contract shared by every
// Workflow entry point. It deliberately contains no Workflow state: the Run is
// canonical and projects status after activation in the same transaction.
type RunIssueInput struct {
	Title        string
	Description  string
	Status       string
	Priority     string
	AssigneeType pgtype.Text
	AssigneeID   pgtype.UUID
	CreatorType  string
	CreatorID    pgtype.UUID
	ProjectID    pgtype.UUID
	OriginType   pgtype.Text
	OriginID     pgtype.UUID
	Subscribers  []RunIssueSubscriber
}

// StartRunResult reports the created Run and whether this call created it.
type StartRunResult struct {
	Run   db.WorkflowRun
	Issue *db.Issue
	// AlreadyExisted is true when the idempotency key matched an existing Run.
	// Callers return 200 with the original Run rather than an error: a duplicate
	// intake delivery is expected, not exceptional.
	AlreadyExisted bool
}

// StartRun materializes a pinned template version into a durable Run and
// activates its entry node.
//
// Idempotency is handled by reading first and catching the unique violation
// second. The read alone would race (two concurrent replays both see nothing);
// the constraint alone would work but turns the normal duplicate-delivery case
// into an error path. Doing both means the common case is a cheap read and the
// racing case still cannot create two Runs.
func (e *Engine) StartRun(ctx context.Context, in StartRunInput) (*StartRunResult, error) {
	if in.IdempotencyKey == "" {
		return nil, newEngineError(ErrCodeInvariantViolation, "StartRun requires an idempotency key")
	}
	if existing, err := e.Queries.GetWorkflowRunByIdempotencyKey(ctx, db.GetWorkflowRunByIdempotencyKeyParams{
		WorkspaceID:    in.WorkspaceID,
		IdempotencyKey: in.IdempotencyKey,
	}); err == nil {
		if in.RequestHash != "" && existing.RequestHash.Valid && existing.RequestHash.String != in.RequestHash {
			return nil, newEngineError(ErrCodeIdempotencyConflict, "the idempotency key was already used with a different payload")
		}
		return &StartRunResult{Run: existing, AlreadyExisted: true}, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("lookup run by idempotency key: %w", err)
	}

	var result StartRunResult
	effects := &txEffects{}
	err := e.runInTx(ctx, effects, func(ctx context.Context, q *db.Queries) error {
		instance, err := e.resolveInstanceStart(ctx, q, &in)
		if err != nil {
			return err
		}
		version, err := e.resolveVersion(ctx, q, in.WorkspaceID, in.TemplateID, in.TemplateVersionID)
		if err != nil {
			return err
		}
		def, err := ParseDefinition(version.Definition)
		if err != nil {
			return err
		}
		entry, ok := def.NodeByKey(def.EntryNode)
		if !ok {
			// Cannot happen for a published version (Validate proves the entry
			// node exists), so this means the row was tampered with or written by
			// a newer server.
			return newEngineError(ErrCodeInvariantViolation,
				fmt.Sprintf("pinned version %d has no entry node %q", version.Version, def.EntryNode))
		}

		// Snapshot the effective limits so tightening workspace policy later
		// cannot retroactively invalidate this Run.
		policyJSON, err := json.Marshal(def.EffectiveLimits())
		if err != nil {
			return fmt.Errorf("marshal policy snapshot: %w", err)
		}
		input := in.Input
		if len(input) == 0 {
			input = []byte("{}")
		}
		var imageAttachment *ImageAttachmentRef

		// Check the submitted values against the pinned graph's declared fields
		// BEFORE the Run row exists.
		//
		// Order matters. Validating after CreateWorkflowRun would leave a committed
		// idempotency key behind on failure (the insert is in this transaction, so
		// it rolls back — but only because the check aborts the whole tx, which is
		// fragile to reorder). Validating here means the failure never touches
		// durable state: no Run, no step, no event, and the submitter can retry the
		// same idempotency key with a corrected form.
		//
		// Checked against the PINNED version's declaration, not the template's
		// current one: an in-flight publish must not change what an already-issued
		// dialog was allowed to submit.
		if entry.Type == NodeTypeInput {
			if err := ValidateRunInput(input, entry); err != nil {
				return err
			}
			if entry.EffectiveInputMode() == InputModeImage {
				imageID := entry.ImageAttachmentID
				if in.Instance != nil && in.Instance.ImageAttachmentID != nil {
					imageID = *in.Instance.ImageAttachmentID
				}
				imageAttachment, err = resolveWorkflowImageAttachment(
					ctx,
					q,
					in.WorkspaceID,
					imageID,
				)
				if err != nil {
					return err
				}
			}
		}
		runContext, err := marshalWorkflowRunContext(imageAttachment)
		if err != nil {
			return fmt.Errorf("marshal workflow run context: %w", err)
		}

		issueID := in.IssueID
		if in.Issue != nil {
			number, err := q.IncrementIssueCounter(ctx, in.WorkspaceID)
			if err != nil {
				return fmt.Errorf("allocate workflow issue number: %w", err)
			}
			status := in.Issue.Status
			if status == "" {
				status = "todo"
			}
			priority := in.Issue.Priority
			if priority == "" {
				priority = "none"
			}
			position, err := q.NextWorkflowIssuePosition(ctx, db.NextWorkflowIssuePositionParams{WorkspaceID: in.WorkspaceID, Status: status})
			if err != nil {
				return fmt.Errorf("position workflow issue: %w", err)
			}
			issue, err := q.CreateIssueWithOrigin(ctx, db.CreateIssueWithOriginParams{
				WorkspaceID:  in.WorkspaceID,
				Title:        in.Issue.Title,
				Description:  textOrNull(in.Issue.Description),
				Status:       status,
				Priority:     priority,
				AssigneeType: in.Issue.AssigneeType,
				AssigneeID:   in.Issue.AssigneeID,
				CreatorType:  in.Issue.CreatorType,
				CreatorID:    in.Issue.CreatorID,
				Position:     position,
				Number:       number,
				ProjectID:    in.Issue.ProjectID,
				OriginType:   in.Issue.OriginType,
				OriginID:     in.Issue.OriginID,
			})
			if err != nil {
				return fmt.Errorf("create workflow issue: %w", err)
			}
			for _, sub := range in.Issue.Subscribers {
				reason := sub.Reason
				if reason == "" {
					reason = "manual"
				}
				if _, err := q.AddIssueSubscriber(ctx, db.AddIssueSubscriberParams{
					IssueID: issue.ID, UserType: sub.UserType, UserID: sub.UserID, Reason: reason,
				}); err != nil {
					return fmt.Errorf("subscribe workflow issue owner: %w", err)
				}
			}
			issueID = issue.ID
			result.Issue = &issue
		} else if issueID.Valid {
			locked, err := q.GetIssueForWorkflowStart(ctx, db.GetIssueForWorkflowStartParams{
				ID: issueID, WorkspaceID: in.WorkspaceID,
			})
			if errors.Is(err, pgx.ErrNoRows) {
				return newEngineError(ErrCodeInvariantViolation, "the existing issue is outside the run workspace or no longer exists")
			}
			if err != nil {
				return fmt.Errorf("lock existing workflow issue: %w", err)
			}
			if locked.EffectiveStatus == "done" || locked.EffectiveStatus == "cancelled" {
				return newEngineError(ErrCodeInvalidTransition, "a terminal issue cannot start a workflow run")
			}
			if in.EnforceIssueSnapshot && (locked.ProjectID != in.ExpectedIssueProjectID ||
				!locked.UpdatedAt.Time.Equal(in.ExpectedIssueUpdatedAt.Time)) {
				return newEngineError(ErrCodeIdempotencyConflict, "the issue changed after issue-pool review")
			}
			activeRuns, err := q.ListActiveWorkflowRunsForIssue(ctx, db.ListActiveWorkflowRunsForIssueParams{
				IssueID: issueID, WorkspaceID: in.WorkspaceID,
			})
			if err != nil {
				return fmt.Errorf("check active workflow runs for issue: %w", err)
			}
			if len(activeRuns) > 0 {
				return newEngineError(ErrCodeInvalidTransition, "the issue already has an active workflow run")
			}
			hasActiveTask, err := q.HasActiveAgentTaskForWorkflowIssue(ctx, issueID)
			if err != nil {
				return fmt.Errorf("check active tasks for workflow issue: %w", err)
			}
			if hasActiveTask {
				return newEngineError(ErrCodeInvalidTransition, "the issue already has an active agent task")
			}
			if in.AccountableUserID.Valid {
				if _, err := q.AddIssueSubscriber(ctx, db.AddIssueSubscriberParams{
					IssueID: issueID, UserType: "member", UserID: in.AccountableUserID, Reason: "manual",
				}); err != nil {
					return fmt.Errorf("subscribe existing workflow issue owner: %w", err)
				}
			}
		}

		var instanceID pgtype.UUID
		var instanceRevision pgtype.Int8
		var instanceName, inputSource pgtype.Text
		var inputProject pgtype.UUID
		if instance != nil {
			instanceID = instance.ID
			instanceRevision = pgtype.Int8{Int64: instance.Revision, Valid: true}
			instanceName = textOrNull(instance.Name)
			inputSource = textOrNull(in.Instance.Mode)
			inputProject = in.Issue.ProjectID
		}
		run, err := q.CreateWorkflowRun(ctx, db.CreateWorkflowRunParams{
			InputInstanceID: instanceID, InputInstanceRevision: instanceRevision,
			InputInstanceName: instanceName, InputSource: inputSource, InputProjectID: inputProject,
			WorkspaceID:           in.WorkspaceID,
			TemplateID:            in.TemplateID,
			TemplateVersionID:     version.ID,
			Source:                in.Source,
			IssueID:               issueID,
			SourceEventID:         textOrNull(in.SourceEventID),
			IdempotencyKey:        in.IdempotencyKey,
			AccountableUserID:     in.AccountableUserID,
			Input:                 input,
			Context:               runContext,
			Policy:                policyJSON,
			RequestHash:           textOrNull(in.RequestHash),
			CallbackDestinationID: in.CallbackDestinationID,
		})
		if err != nil {
			if isUniqueViolation(err) {
				// Lost the race against a concurrent replay; the winner's Run is
				// authoritative.
				return newEngineError(ErrCodeIdempotencyConflict, "a run with this idempotency key already exists")
			}
			return fmt.Errorf("create workflow run: %w", err)
		}

		if err := e.recordEvent(ctx, q, eventSpec{
			WorkspaceID:    in.WorkspaceID,
			RunID:          run.ID,
			Type:           EventRunStarted,
			IdempotencyKey: in.IdempotencyKey + ":run.started",
			ActorType:      defaultActorType(in.ActorType),
			ActorID:        in.ActorID,
			Payload:        mustJSON(map[string]any{"template_version": version.Version, "entry_node": def.EntryNode}),
		}); err != nil {
			return err
		}

		running, err := q.MarkWorkflowRunRunning(ctx, db.MarkWorkflowRunRunningParams{
			ID:          run.ID,
			WorkspaceID: in.WorkspaceID,
		})
		if err != nil {
			return fmt.Errorf("mark run running: %w", err)
		}

		// Create and activate the entry step in the same transaction: a Run that
		// commits without a first step would be a "running Run with no active
		// step", i.e. work for the reconciler on the happy path.
		if _, err := e.activateNode(ctx, q, activateInput{
			Run:       running,
			Def:       def,
			Node:      entry,
			Attempt:   1,
			ActorType: defaultActorType(in.ActorType),
			ActorID:   in.ActorID,
			Effects:   effects,
		}); err != nil {
			return err
		}

		if in.AutopilotRunID.Valid {
			if _, err := q.UpdateAutopilotRunWorkflowRunning(ctx, db.UpdateAutopilotRunWorkflowRunningParams{
				ID:            in.AutopilotRunID,
				WorkflowRunID: running.ID,
				IssueID:       running.IssueID,
			}); err != nil {
				return fmt.Errorf("link autopilot workflow run: %w", err)
			}
		}

		result.Run = running
		effects.runChanged(running)
		return nil
	})
	if err != nil {
		if IsIdempotencyConflict(err) {
			// Re-read the winner so the caller still gets the Run it asked for.
			if existing, rerr := e.Queries.GetWorkflowRunByIdempotencyKey(ctx, db.GetWorkflowRunByIdempotencyKeyParams{
				WorkspaceID:    in.WorkspaceID,
				IdempotencyKey: in.IdempotencyKey,
			}); rerr == nil {
				if in.RequestHash != "" && existing.RequestHash.Valid && existing.RequestHash.String != in.RequestHash {
					return nil, newEngineError(ErrCodeIdempotencyConflict, "the idempotency key was already used with a different payload")
				}
				return &StartRunResult{Run: existing, AlreadyExisted: true}, nil
			}
		}
		return nil, err
	}

	effects.flush(ctx, e.Notifier)
	return &result, nil
}
func (e *Engine) resolveVersion(ctx context.Context, q *db.Queries, workspaceID, templateID, versionID pgtype.UUID) (db.WorkflowTemplateVersion, error) {
	if versionID.Valid {
		v, err := q.GetWorkflowTemplateVersion(ctx, db.GetWorkflowTemplateVersionParams{
			ID:          versionID,
			WorkspaceID: workspaceID,
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return db.WorkflowTemplateVersion{}, newEngineError(ErrCodeNotFound, "pinned template version not found in this workspace")
			}
			return db.WorkflowTemplateVersion{}, fmt.Errorf("get template version: %w", err)
		}
		// Only a published version may execute: a draft is still mutable, and a
		// Run pinned to it could observe its graph change mid-flight.
		if v.Status != "published" || v.TemplateID != templateID {
			return db.WorkflowTemplateVersion{}, newEngineError(ErrCodeInvalidDefinition,
				fmt.Sprintf("template version %d is %s, not published", v.Version, v.Status))
		}
		return v, nil
	}
	v, err := q.GetPublishedWorkflowTemplateVersion(ctx, db.GetPublishedWorkflowTemplateVersionParams{
		TemplateID:  templateID,
		WorkspaceID: workspaceID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return db.WorkflowTemplateVersion{}, newEngineError(ErrCodeNotFound, "template has no published version")
		}
		return db.WorkflowTemplateVersion{}, fmt.Errorf("get published template version: %w", err)
	}
	return v, nil
}

// ---------------------------------------------------------------------------
// Step activation
// ---------------------------------------------------------------------------

// txEffects collects side effects that must not happen until the transaction
// commits. Notifications are the main case: telling a daemon about a task, or a
// client about a state change, before commit would advertise state that a
// rollback then erases — and the daemon would claim a task that no longer exists.
//
// This is a per-command value passed by pointer rather than engine state,
// because the Engine is shared across concurrent requests.
type txEffects struct {
	tasks  []db.AgentTaskQueue
	runs   []db.WorkflowRun
	issues []issueChange
	events []EventEnvelope
}

type txEffectsContextKey struct{}

type issueChange struct {
	issue      db.Issue
	prevStatus string
}

func (t *txEffects) taskEnqueued(task db.AgentTaskQueue) {
	t.tasks = append(t.tasks, task)
}

func (t *txEffects) runChanged(run db.WorkflowRun) {
	t.runs = append(t.runs, run)
}

func (t *txEffects) issueChanged(issue db.Issue, prevStatus string) {
	t.issues = append(t.issues, issueChange{issue: issue, prevStatus: prevStatus})
}

func (t *txEffects) eventRecorded(event EventEnvelope) {
	t.events = append(t.events, event)
}

// flush emits every collected notification. Called only after a successful
// commit.
func (t *txEffects) flush(ctx context.Context, n Notifier) {
	if n == nil {
		return
	}
	for _, event := range t.events {
		n.WorkflowEvent(ctx, event)
	}
	for _, run := range t.runs {
		n.WorkflowChanged(ctx, uuidString(run.WorkspaceID), uuidString(run.ID))
	}
	for _, change := range t.issues {
		n.IssueChanged(ctx, change.issue, change.prevStatus)
	}
	for _, task := range t.tasks {
		n.TaskEnqueued(ctx, task)
	}
}

type activateInput struct {
	Run     db.WorkflowRun
	Def     *Definition
	Node    *Node
	Attempt int32
	// ReworkContext is injected into the step input on a rework attempt so the
	// Agent sees why the previous attempt was rejected.
	ReworkContext map[string]any
	// Fan-out children retain the parent barrier and one deterministic item.
	// Empty on ordinary sequential traversal.
	ParentStepID   pgtype.UUID
	ExpansionKey   string
	ExpansionValue string
	Reconcile      bool
	ActorType      string
	ActorID        pgtype.UUID
	// Effects collects post-commit notifications.
	Effects *txEffects
}

// activateNode creates a Step attempt and drives it as far as it can go inside
// the current transaction.
//
// Non-agent nodes resolve immediately: an End node completes the Run, a
// condition/fan_out passes through. Agent nodes route, create the Agent Task,
// and bind it to the Step — all before commit, which is what guarantees the
// "at most one active Task per attempt" invariant survives a crash at any point.
func (e *Engine) activateNode(ctx context.Context, q *db.Queries, in activateInput) (db.WorkflowStepInstance, error) {
	run := in.Run
	limits := e.limitsFor(run, in.Def)

	// Enforce the total-step ceiling before creating another step, so a
	// pathological rework loop cannot outrun its budget.
	if limits.MaxTotalSteps > 0 {
		existing, err := q.ListWorkflowStepInstances(ctx, db.ListWorkflowStepInstancesParams{
			RunID:       run.ID,
			WorkspaceID: run.WorkspaceID,
		})
		if err != nil {
			return db.WorkflowStepInstance{}, fmt.Errorf("count steps: %w", err)
		}
		if len(existing) >= limits.MaxTotalSteps {
			return db.WorkflowStepInstance{}, newEngineError(ErrCodeBudgetExceeded,
				fmt.Sprintf("run reached its %d-step ceiling", limits.MaxTotalSteps))
		}
	}

	if !in.ParentStepID.Valid && in.Attempt > int32(in.Node.EffectiveMaxAttempts(limits)) {
		return db.WorkflowStepInstance{}, newEngineError(ErrCodeReworkLimit,
			fmt.Sprintf("node %q exhausted its %d attempts", in.Node.Key, in.Node.EffectiveMaxAttempts(limits)))
	}

	// The Step's durable input. This is both the audit record of what the Step
	// was asked to do and the source the Agent Task's prompt is built from, so
	// the run input and the upstream deliverable are resolved HERE rather than at
	// dispatch: a non-agent node's row should show the same context an agent node
	// would have received, and a reader inspecting a Step must not have to
	// re-derive what the agent saw.
	//
	// Resolved against the entry input node's declaration when the pinned graph has
	// one, so a declared field reaches the prompt as a labelled value. A graph
	// without an input node passes nil and gets exactly the old title/description
	// behaviour — which is the whole backward-compatibility contract, since every
	// version published before this feature is immutable.
	entryInput, _ := in.Def.EntryInputNode()
	runInput := ParseRunInputFor(run.Input, entryInput)
	imageAttachment := imageAttachmentFromRunContext(run.Context)
	upstream, err := e.latestSubmissionForRun(ctx, q, run)
	if err != nil {
		return db.WorkflowStepInstance{}, err
	}

	stepInput := map[string]any{
		"instruction": in.Node.Instruction,
		"node_name":   in.Node.Name,
	}
	if runInput.Title != "" || runInput.Description != "" || len(runInput.Fields) > 0 || imageAttachment != nil {
		// Echoed onto every Step, not just the entry node: the graph's
		// instruction is generic ("analyze the defect") and this is the actual
		// defect. A later step that only saw the instruction would be guessing.
		echo := map[string]any{
			"title":       runInput.Title,
			"description": runInput.Description,
		}
		if imageAttachment != nil {
			// Keep the audit record useful without embedding a storage URL, a signed
			// credential, or image bytes in a Run/Step row.
			echo["image_attachment"] = imageAttachment
		}
		if len(runInput.Fields) > 0 {
			// Only added when non-empty, so a Step row for a template with no input
			// node carries the same two keys it always did. A test that round-trips
			// step.input would otherwise see a new key on every legacy template.
			fields := make([]map[string]any, 0, len(runInput.Fields))
			for _, f := range runInput.Fields {
				fields = append(fields, map[string]any{
					"key":   f.Key,
					"label": f.Label,
					"value": f.Value,
				})
			}
			echo["fields"] = fields
		}
		stepInput["run_input"] = echo
	}
	if len(in.Node.AcceptanceCriteria) > 0 {
		stepInput["acceptance_criteria"] = in.Node.AcceptanceCriteria
	}
	if upstream != nil {
		// Summary + references only. The full artifact JSON stays on
		// workflow_submission where it already lives; copying it into every
		// downstream step input would duplicate an unbounded blob per hop.
		stepInput["upstream"] = map[string]any{
			"node_key":   upstream.NodeKey,
			"verdict":    upstream.Verdict,
			"summary":    upstream.Artifact.Summary,
			"references": upstream.Artifact.References,
		}
	}
	if in.ReworkContext != nil {
		stepInput["rework"] = in.ReworkContext
	}
	if in.ExpansionKey != "" {
		stepInput["fan_out"] = map[string]any{
			"expansion_key": in.ExpansionKey,
			"value":         in.ExpansionValue,
		}
	}

	now := e.now()
	step, err := q.CreateWorkflowStepInstance(ctx, db.CreateWorkflowStepInstanceParams{
		WorkspaceID:  run.WorkspaceID,
		RunID:        run.ID,
		NodeKey:      in.Node.Key,
		NodeType:     string(in.Node.Type),
		Attempt:      in.Attempt,
		Status:       string(StepReady),
		Input:        mustJSON(stepInput),
		ParentStepID: in.ParentStepID,
		ExpansionKey: textOrNull(in.ExpansionKey),
		ReadyAt:      pgtype.Timestamptz{Time: now, Valid: true},
		ActivationTimeoutAt: pgtype.Timestamptz{
			Time:  now.Add(activationTimeout),
			Valid: true,
		},
	})
	if err != nil {
		if isUniqueViolation(err) {
			// (run, node, attempt) collision: a duplicate activation. This is the
			// fence that stops a replay creating a second Agent Task.
			return db.WorkflowStepInstance{}, newEngineError(ErrCodeIdempotencyConflict,
				fmt.Sprintf("node %q attempt %d already exists", in.Node.Key, in.Attempt))
		}
		return db.WorkflowStepInstance{}, fmt.Errorf("create step instance: %w", err)
	}

	if err := e.recordEvent(ctx, q, eventSpec{
		WorkspaceID:    run.WorkspaceID,
		RunID:          run.ID,
		StepID:         step.ID,
		Type:           EventStepActivated,
		IdempotencyKey: stepEventKey(step, "activated"),
		ActorType:      defaultActorType(in.ActorType),
		ActorID:        in.ActorID,
		Payload:        mustJSON(map[string]any{"node_key": in.Node.Key, "node_type": string(in.Node.Type), "attempt": in.Attempt}),
	}); err != nil {
		return db.WorkflowStepInstance{}, err
	}

	switch in.Node.Type {
	case NodeTypeAgent:
		return e.dispatchAgentStep(ctx, q, in, step, agentBrief{
			RunInput:        runInput,
			ImageAttachment: imageAttachment,
			Upstream:        upstream,
		})
	case NodeTypeAcceptance:
		return e.requestAcceptance(ctx, q, in, step)
	case NodeTypeCondition:
		return e.executeCondition(ctx, q, in, step, upstream)
	case NodeTypeFanOut:
		return e.executeFanOut(ctx, q, in, step, upstream)
	case NodeTypeJoin:
		return e.executeJoin(ctx, q, in, step)
	case NodeTypeEnd:
		return e.completeAtEnd(ctx, q, in, step)
	case NodeTypeInput:
		// An intake node is a DELIBERATE passthrough, not a fallthrough.
		//
		// This case is written out rather than left to the default branch below.
		// An input node is finished: its work was done by a human before StartRun, whose
		// ValidateRunInput already checked the declared fields, and the values are
		// on run.Input. There is nothing to dispatch, now or ever.
		//
		// Sharing the default branch would mean a future change to it silently
		// starts sending intake nodes to agents — asking an agent to fill in the
		// form the human already filled in. The step row is still written and still
		// passes, so the Run's trace records that intake happened and when.
		passed, err := q.MarkWorkflowStepPassed(ctx, db.MarkWorkflowStepPassedParams{
			ID:          step.ID,
			WorkspaceID: run.WorkspaceID,
		})
		if err != nil {
			return db.WorkflowStepInstance{}, fmt.Errorf("pass input step %q: %w", in.Node.Key, err)
		}
		// Unlike the default branch, intake ADVANCES. It is the entry node, so
		// stopping here would leave a running Run whose only step is already
		// terminal and whose first agent never dispatched — a silent stall on the
		// happy path.
		// Validate guarantees exactly one outgoing edge, so advanceToNext's
		// Next[0] is the whole successor set.
		if err := e.advanceFromStep(ctx, q, run, in.Def, passed, in.Node, in.Effects, in.ActorType, in.ActorID); err != nil {
			return db.WorkflowStepInstance{}, err
		}
		return passed, nil
	default:
		return db.WorkflowStepInstance{}, newEngineError(ErrCodeInvariantViolation,
			fmt.Sprintf("node %q has unsupported type %q", in.Node.Key, in.Node.Type))
	}
}

// activationTimeout bounds how long a Step may sit ready without being queued
// before the reconciler treats it as stuck (plan section 7).
const activationTimeout = 5 * time.Minute

// agentBrief carries the resolved work context activateNode already computed for
// the Step's durable input, so dispatchAgentStep can build the Agent Task's
// prompt from the SAME values rather than re-querying (and possibly disagreeing
// with) the row it just wrote.
type agentBrief struct {
	RunInput        RunInput
	ImageAttachment *ImageAttachmentRef
	Upstream        *upstreamSubmission
}

// dispatchAgentStep routes the node to an Agent, creates the Agent Task carrying
// the Step's brief, and binds it to the Step — all inside the caller's
// transaction.
//
// This is the single most important transaction boundary in the design. If the
// task were created after commit, a crash in between would leave a queued Step
// with no Task (silent stall); if the Step were bound after the task committed,
// a crash would leave a Task no Step owns (duplicate work on retry). Both rows
// move together or neither does. The brief is written by the same INSERT for the
// same reason: a task with no prompt is worse than no task, so there must be no
// moment at which a claimable workflow task exists without its work description.
func (e *Engine) dispatchAgentStep(ctx context.Context, q *db.Queries, in activateInput, step db.WorkflowStepInstance, brief agentBrief) (db.WorkflowStepInstance, error) {
	run := in.Run
	if e.Router == nil {
		return db.WorkflowStepInstance{}, newEngineError(ErrCodeRoutingFailed, "engine has no router configured")
	}

	prior, err := e.priorAgentsByNode(ctx, q, run)
	if err != nil {
		return db.WorkflowStepInstance{}, err
	}

	route, err := e.Router.Route(ctx, q, RouteRequest{
		WorkspaceID:       run.WorkspaceID,
		Run:               run,
		Node:              in.Node,
		AccountableUserID: run.AccountableUserID,
		PriorAgentByNode:  prior,
		RequiresVision:    brief.ImageAttachment != nil,
	})
	if err != nil {
		// No eligible Agent is a blocked Step, not a crash: a human can grant
		// permission or bring a runtime online and the Run resumes.
		blocked, berr := q.MarkWorkflowStepBlocked(ctx, db.MarkWorkflowStepBlockedParams{
			ID:            step.ID,
			WorkspaceID:   run.WorkspaceID,
			FailureReason: ReasonRoutingNoCandidate,
			FailureDetail: textOrNull(err.Error()),
		})
		if berr != nil {
			return db.WorkflowStepInstance{}, fmt.Errorf("block step after routing failure: %w (original: %v)", berr, err)
		}
		if err := e.blockRun(ctx, q, run, step, ReasonRoutingNoCandidate, err.Error(), in.ActorType, in.ActorID); err != nil {
			return db.WorkflowStepInstance{}, err
		}
		return blocked, nil
	}

	taskCtx, err := json.Marshal(e.buildTaskContext(in, step, brief))
	if err != nil {
		// The value is a struct of strings built by this package, so a failure is
		// a programming error. Refuse to dispatch rather than fall back to an
		// empty context: an agent claiming a promptless task cannot do the work,
		// and the step would then block on the submission contract - a confusing
		// symptom several hops from this cause.
		return db.WorkflowStepInstance{}, fmt.Errorf("marshal workflow task context for step %s: %w", step.NodeKey, err)
	}

	// CreateWorkflowAgentTask (not CreateAgentTask) because the generic insert
	// derives `context` from a head_sha argument and cannot take a blob; it also
	// stamps workflow_step_instance_id inline, so the task is never briefly
	// visible as an unlinked non-workflow row.
	task, err := q.CreateWorkflowAgentTask(ctx, db.CreateWorkflowAgentTaskParams{
		AgentID:                route.AgentID,
		RuntimeID:              route.RuntimeID,
		IssueID:                run.IssueID,
		Priority:               workflowTaskPriority,
		Context:                taskCtx,
		WorkflowStepInstanceID: step.ID,
		AccountableUserID:      run.AccountableUserID,
		OriginatorUserID:       run.AccountableUserID,
	})
	if err != nil {
		return db.WorkflowStepInstance{}, fmt.Errorf("create agent task for step %s: %w", step.NodeKey, err)
	}

	bound, err := q.BindWorkflowStepTask(ctx, db.BindWorkflowStepTaskParams{
		ID:            step.ID,
		WorkspaceID:   run.WorkspaceID,
		TaskID:        task.ID,
		AgentID:       route.AgentID,
		RoutingReason: textOrNull(route.Reason),
	})
	if err != nil {
		if isUniqueViolation(err) {
			return db.WorkflowStepInstance{}, newEngineError(ErrCodeInvariantViolation,
				"task is already bound to another step attempt")
		}
		if errors.Is(err, pgx.ErrNoRows) {
			// The status guard (ready + task_id IS NULL) did not match, meaning
			// something else already bound this attempt.
			return db.WorkflowStepInstance{}, newEngineError(ErrCodeInvalidTransition,
				"step is not in ready state or already has a task")
		}
		return db.WorkflowStepInstance{}, fmt.Errorf("bind step task: %w", err)
	}

	queuedVerb := "queued"
	if in.Reconcile {
		queuedVerb = "requeued"
	}
	if err := e.recordEvent(ctx, q, eventSpec{
		WorkspaceID:    run.WorkspaceID,
		RunID:          run.ID,
		StepID:         step.ID,
		Type:           EventStepQueued,
		IdempotencyKey: stepEventKey(step, queuedVerb),
		ActorType:      "system",
		Payload:        mustJSON(map[string]any{"routing_reason": route.Reason}),
	}); err != nil {
		return db.WorkflowStepInstance{}, err
	}

	// Deferred until after commit so the daemon cannot claim a task the
	// transaction later rolls back.
	if in.Effects != nil {
		in.Effects.taskEnqueued(task)
	}
	return bound, nil
}

// workflowTaskPriority matches the priority chat tasks use, so workflow work is
// not starved by, nor starves, interactive requests.
const workflowTaskPriority = 2

// upstreamSubmission is the immediately-preceding Step's deliverable, reduced to
// what a downstream Agent needs: which node produced it, its verdict, and its
// artifact. Carried as a value rather than the raw db row so callers cannot
// accidentally leak workspace-scoped columns into a prompt.
type upstreamSubmission struct {
	NodeKey  string
	Verdict  string
	Artifact Artifact
}

// latestSubmissionForRun returns the most recent submission on the Run, or nil
// when none exists yet.
//
// "Most recent on the Run" rather than "the submission of the node this Step's
// incoming edge came from" is a deliberate simplification for the linear graphs
// the first release executes (Agent -> Agent -> Agent -> Acceptance -> End): in a
// linear chain the latest submission IS the upstream one, and it is also the
// right answer on a rework attempt, where the reason the work came back is the
// most recent thing that happened. Join instead resolves its full durable child
// set through parent_step_id, because selecting only the latest child would be
// arbitrary.
func (e *Engine) latestSubmissionForRun(ctx context.Context, q *db.Queries, run db.WorkflowRun) (*upstreamSubmission, error) {
	subs, err := q.ListWorkflowSubmissionsForRun(ctx, db.ListWorkflowSubmissionsForRunParams{
		RunID:       run.ID,
		WorkspaceID: run.WorkspaceID,
	})
	if err != nil {
		return nil, fmt.Errorf("list submissions for step activation: %w", err)
	}
	if len(subs) == 0 {
		return nil, nil
	}
	// The query orders by submitted_at ASC, so the last row is the newest.
	latest := subs[len(subs)-1]

	// Map the submission back to the node that produced it. A submission whose
	// step has since vanished is not a reason to refuse dispatch - the artifact
	// is still the work product - so an unresolvable node key degrades to empty
	// rather than erroring.
	nodeKey := ""
	if step, err := q.GetWorkflowStepInstance(ctx, db.GetWorkflowStepInstanceParams{
		ID:          latest.StepID,
		WorkspaceID: run.WorkspaceID,
	}); err == nil {
		nodeKey = step.NodeKey
	}

	out := &upstreamSubmission{NodeKey: nodeKey, Verdict: latest.Verdict}
	if len(latest.Artifact) > 0 {
		// A malformed artifact is tolerated: verdict + node key alone is still
		// useful context, and blocking a dispatch over an unreadable artifact
		// would let one bad row stall the Run.
		_ = json.Unmarshal(latest.Artifact, &out.Artifact)
	}
	return out, nil
}

// buildTaskContext assembles the Agent Task brief for a Step.
//
// Everything here is derived from values activateNode already resolved, so the
// task's prompt and the Step's durable `input` column describe the same work. The
// submission contract is included unconditionally: the engine accepts only a
// parseable Submission, so an agent that was not told the format produces
// correct work with a rejected result.
func (e *Engine) buildTaskContext(in activateInput, step db.WorkflowStepInstance, brief agentBrief) TaskContext {
	stepID := uuidString(step.ID)
	tc := TaskContext{
		Type:               TaskContextType,
		RunID:              uuidString(in.Run.ID),
		StepInstanceID:     stepID,
		NodeKey:            in.Node.Key,
		NodeName:           in.Node.Name,
		Attempt:            step.Attempt,
		WorkspaceID:        uuidString(in.Run.WorkspaceID),
		Instruction:        truncateContextField(in.Node.Instruction),
		RunTitle:           truncateContextField(brief.RunInput.Title),
		RunDescription:     truncateContextField(brief.RunInput.Description),
		RunFields:          truncateRunFields(brief.RunInput.Fields),
		ImageAttachment:    brief.ImageAttachment,
		AcceptanceCriteria: in.Node.AcceptanceCriteria,
		SubmissionSchema:   in.Node.SubmissionSchema,
		SubmissionContract: SubmissionContractInstructions(stepID),
		ExpansionKey:       in.ExpansionKey,
		ExpansionValue:     truncateContextField(in.ExpansionValue),
	}
	if brief.Upstream != nil {
		tc.UpstreamNodeKey = brief.Upstream.NodeKey
		tc.UpstreamVerdict = brief.Upstream.Verdict
		tc.UpstreamSummary = truncateContextField(brief.Upstream.Artifact.Summary)
		tc.UpstreamReferences = brief.Upstream.Artifact.References
	}
	if in.ReworkContext != nil {
		// ReworkContext is built by applyFailurePolicy (from_node/reason/detail)
		// and by DecideAcceptance (from_node/reason/rejected_by/rejection_round)
		// as map[string]any. Read defensively rather than asserting: a missing key
		// means the agent loses one line of context, whereas a panic here would
		// take down the whole activation.
		//
		// rejected_by / rejection_round are deliberately NOT forwarded to the
		// agent. Who rejected and on which round is audit metadata that lives on
		// the workflow_acceptance row; naming the reviewer in the prompt invites
		// the agent to optimise for a person instead of the stated reason.
		if v, ok := in.ReworkContext["from_node"].(string); ok {
			tc.ReworkFromNode = v
		}
		if v, ok := in.ReworkContext["reason"].(string); ok {
			tc.ReworkReason = truncateContextField(v)
		}
		if v, ok := in.ReworkContext["detail"].(string); ok {
			tc.ReworkDetail = truncateContextField(v)
		}
	}
	return tc
}

// priorAgentsByNode maps node key -> the Agent that ran it, for
// RoutingPreviousStep. Later attempts overwrite earlier ones, so a rework
// re-routes to whoever most recently held the node.
func (e *Engine) priorAgentsByNode(ctx context.Context, q *db.Queries, run db.WorkflowRun) (map[string]pgtype.UUID, error) {
	steps, err := q.ListWorkflowStepInstances(ctx, db.ListWorkflowStepInstancesParams{
		RunID:       run.ID,
		WorkspaceID: run.WorkspaceID,
	})
	if err != nil {
		return nil, fmt.Errorf("list steps for routing: %w", err)
	}
	out := make(map[string]pgtype.UUID, len(steps))
	for _, s := range steps {
		if s.AgentID.Valid {
			out[s.NodeKey] = s.AgentID
		}
	}
	return out, nil
}

func (e *Engine) limitsFor(run db.WorkflowRun, def *Definition) Limits {
	// Prefer the snapshot taken at StartRun; fall back to the definition for
	// rows written before the snapshot existed.
	var l Limits
	if len(run.Policy) > 0 {
		if err := json.Unmarshal(run.Policy, &l); err == nil && l.MaxAttemptsPerNode > 0 {
			return l
		}
	}
	return def.EffectiveLimits()
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		// The inputs here are maps of strings and ints built by this package; a
		// failure would be a programming error, not a runtime condition.
		return []byte("{}")
	}
	return b
}

func textOrNull(s string) pgtype.Text {
	if s == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: s, Valid: true}
}

func defaultActorType(s string) string {
	if s == "" {
		return "system"
	}
	return s
}

// stepEventKey derives a deterministic idempotency key for a step event. Built
// from (step id, verb) so replaying the same command produces the same key and
// collides, while a genuinely new attempt gets a different step id and proceeds.
func stepEventKey(step db.WorkflowStepInstance, verb string) string {
	return fmt.Sprintf("step:%s:%s", uuidString(step.ID), verb)
}

func uuidString(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	b := u.Bytes
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
