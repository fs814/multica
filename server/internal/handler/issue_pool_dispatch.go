package handler

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// DispatchIssuePool implements service.IssuePoolDispatcher. It materializes
// exactly one durable review cycle for an AutopilotRun. Selection and claims
// commit before this method returns; Workflow execution begins only after a
// reviewer approves an item.
func (h *Handler) DispatchIssuePool(ctx context.Context, ap db.Autopilot, run *db.AutopilotRun, actorUserID pgtype.UUID) error {
	if ap.ExecutionMode != "issue_pool" || !ap.WorkflowTemplateID.Valid || !ap.WorkflowTemplateVersionID.Valid {
		return fmt.Errorf("issue_pool requires a fixed workflow template version")
	}
	accountable := actorUserID
	if !accountable.Valid && ap.CreatedByType == "member" {
		accountable = ap.CreatedByID
	}
	if !accountable.Valid {
		return fmt.Errorf("workspace fail-closed: no accountable human for issue pool review")
	}
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin issue pool cycle: %w", err)
	}
	defer tx.Rollback(ctx)

	p, err := loadIssuePoolPolicy(ctx, tx, ap.ID, ap.WorkspaceID)
	if err != nil {
		return fmt.Errorf("load issue pool policy: %w", err)
	}
	if !issuePoolPolicyScopeCurrent(p, ap) {
		return fmt.Errorf("issue pool policy project scope is stale")
	}

	idempotencyKey := "autopilot:" + uuidToString(run.ID)
	policySnapshot, _ := json.Marshal(issuePoolPolicyToResponse(p))
	hashMaterial, _ := json.Marshal(map[string]any{
		"autopilot_run_id": uuidToString(run.ID), "policy": json.RawMessage(policySnapshot),
		"template_version_id": uuidToString(ap.WorkflowTemplateVersionID),
	})
	requestHash := fmt.Sprintf("%x", sha256.Sum256(hashMaterial))
	var cycleID pgtype.UUID
	err = tx.QueryRow(ctx, `INSERT INTO issue_pool_cycle (
		policy_id, autopilot_id, workspace_id, project_id, autopilot_run_id,
		workflow_template_id, workflow_template_version_id, idempotency_key,
		status, policy_snapshot, workflow_input_mapping_snapshot, request_hash, created_by_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'scanning',$9::jsonb,$10::jsonb,$11,$12)
		ON CONFLICT DO NOTHING RETURNING id`,
		p.ID, ap.ID, ap.WorkspaceID, nullableUUID(ap.ProjectID), run.ID,
		ap.WorkflowTemplateID, ap.WorkflowTemplateVersionID, idempotencyKey,
		string(policySnapshot), string(p.WorkflowInputMapping), requestHash, accountable).Scan(&cycleID)
	if errors.Is(err, pgx.ErrNoRows) {
		// A retry after commit reuses the cycle anchored by autopilot_run_id or
		// (autopilot_id,idempotency_key). No state is rewritten.
		return tx.Commit(ctx)
	}
	if err != nil {
		return fmt.Errorf("create issue pool cycle: %w", err)
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1::uuid::text || ':issue_pool_claim', 0))`, ap.ID); err != nil {
		return fmt.Errorf("lock issue pool capacity: %w", err)
	}
	reference := time.Now().UTC()
	scanned, eligible, _, err := queryIssuePoolSummary(ctx, tx, p, reference)
	if err != nil {
		return fmt.Errorf("summarize issue pool: %w", err)
	}
	var active int32
	if err := tx.QueryRow(ctx, `SELECT count(*)::int FROM issue_pool_item WHERE autopilot_id=$1
		AND status IN ('claimed','approved','dispatching','running','waiting_acceptance','blocked')`, ap.ID).Scan(&active); err != nil {
		return fmt.Errorf("count issue pool capacity: %w", err)
	}
	capacity := p.MaxInFlight - active
	if capacity > p.BatchLimit {
		capacity = p.BatchLimit
	}
	if capacity < 0 {
		capacity = 0
	}
	var candidates []issuePoolCandidate
	if capacity > 0 {
		candidates, err = lockIssuePoolCandidates(ctx, tx, p, reference, capacity)
		if err != nil {
			return fmt.Errorf("lock issue pool candidates: %w", err)
		}
	}
	claimed := int32(0)
	for _, candidate := range candidates {
		response := candidateToResponse(candidate)
		breakdown, _ := json.Marshal(response.Breakdown)
		reasons, _ := json.Marshal(response.Reasons)
		snapshot, _ := json.Marshal(map[string]any{
			"issue_id": response.IssueID, "number": response.Number, "title": response.Title,
			"description": candidate.Description.String, "status": response.Status,
			"priority": response.Priority, "project_id": response.ProjectID,
			"updated_at":            timestampToString(candidate.UpdatedAt),
			"updated_at_unix_micro": candidate.UpdatedAt.Time.UnixMicro(),
			"last_activity_at":      response.LastActivity,
			"acceptance_criteria":   json.RawMessage(candidate.Acceptance),
		})
		var itemID pgtype.UUID
		err := tx.QueryRow(ctx, `INSERT INTO issue_pool_item (
			cycle_id, policy_id, autopilot_id, workspace_id, project_id, issue_id,
			status, score, score_breakdown, selection_reasons, issue_snapshot, claim_expires_at)
			VALUES ($1,$2,$3,$4,$5,$6,'claimed',$7,$8::jsonb,$9::jsonb,$10::jsonb,$11)
			ON CONFLICT DO NOTHING RETURNING id`, cycleID, p.ID, ap.ID, ap.WorkspaceID,
			nullableUUID(candidate.ProjectID), candidate.IssueID, candidate.Score,
			string(breakdown), string(reasons), string(snapshot), reference.Add(24*time.Hour)).Scan(&itemID)
		if err == nil {
			claimed++
			continue
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("claim issue pool candidate: %w", err)
		}
	}
	cycleStatus := "awaiting_review"
	if claimed == 0 {
		cycleStatus = "completed"
	}
	if _, err := tx.Exec(ctx, `UPDATE issue_pool_cycle SET status=$2, scanned_count=$3,
		eligible_count=$4, claimed_count=$5, updated_at=now(),
		completed_at=CASE WHEN $2='completed' THEN now() ELSE NULL END WHERE id=$1`,
		cycleID, cycleStatus, scanned, eligible, claimed); err != nil {
		return fmt.Errorf("finalize issue pool scan: %w", err)
	}
	if claimed == 0 {
		result, _ := json.Marshal(map[string]any{"cycle_id": uuidToString(cycleID), "status": cycleStatus, "claimed_count": 0})
		if _, err := tx.Exec(ctx, `UPDATE autopilot_run SET status='completed', completed_at=now(), result=$2::jsonb WHERE id=$1`, run.ID, string(result)); err != nil {
			return fmt.Errorf("complete empty issue pool run: %w", err)
		}
		run.Status = "completed"
		run.Result = result
	} else {
		if err := h.enqueueIssuePoolOutbox(ctx, tx, ap, cycleID, pgtype.UUID{}, accountable, "candidate_review", map[string]any{"claimed_count": claimed}); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit issue pool cycle: %w", err)
	}
	return nil
}

func (h *Handler) enqueueIssuePoolOutbox(ctx context.Context, tx pgx.Tx, ap db.Autopilot, cycleID, itemID, accountable pgtype.UUID, eventType string, payload map[string]any) error {
	recipients := map[string]pgtype.UUID{}
	if accountable.Valid {
		recipients[uuidToString(accountable)] = accountable
	}
	rows, err := tx.Query(ctx, `SELECT user_id FROM autopilot_subscriber WHERE autopilot_id=$1 AND user_type='member'`, ap.ID)
	if err != nil {
		return fmt.Errorf("list issue pool notification recipients: %w", err)
	}
	for rows.Next() {
		var id pgtype.UUID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		recipients[uuidToString(id)] = id
	}
	rows.Close()
	issueRows, err := tx.Query(ctx, `SELECT DISTINCT subscriber.user_id FROM issue_subscriber subscriber
		JOIN issue_pool_item item ON item.issue_id=subscriber.issue_id
		WHERE item.cycle_id=$1 AND subscriber.user_type='member'`, cycleID)
	if err != nil {
		return fmt.Errorf("list issue pool issue subscribers: %w", err)
	}
	for issueRows.Next() {
		var id pgtype.UUID
		if err := issueRows.Scan(&id); err != nil {
			issueRows.Close()
			return err
		}
		recipients[uuidToString(id)] = id
	}
	issueRows.Close()
	raw, _ := json.Marshal(payload)
	for _, recipient := range recipients {
		if _, err := tx.Exec(ctx, `INSERT INTO issue_pool_notification_outbox
			(workspace_id,autopilot_id,cycle_id,item_id,recipient_id,event_type,payload)
			VALUES ($1,$2,$3,$4,$5,$6,$7::jsonb) ON CONFLICT DO NOTHING`,
			ap.WorkspaceID, ap.ID, cycleID, nullableUUID(itemID), recipient, eventType, string(raw)); err != nil {
			return fmt.Errorf("enqueue issue pool notification: %w", err)
		}
	}
	return nil
}
