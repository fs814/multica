package handler

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/workflow"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type issuePoolDispatchRow struct {
	ItemID, IssueID, WorkspaceID, ProjectID pgtype.UUID
	CycleID, AutopilotID                    pgtype.UUID
	TemplateID, TemplateVersionID           pgtype.UUID
	ReviewerID                              pgtype.UUID
	Snapshot, Mapping, PolicySnapshot       []byte
}

func (h *Handler) dispatchApprovedIssuePoolItems(ctx context.Context, ap db.Autopilot, cycleID, fallbackAccountable pgtype.UUID) {
	if h.WorkflowEngine == nil {
		return
	}
	rows, err := h.DB.Query(ctx, `SELECT item.id,item.issue_id,item.workspace_id,item.project_id,
		item.cycle_id,item.autopilot_id,cycle.workflow_template_id,cycle.workflow_template_version_id,
		item.reviewer_id,item.issue_snapshot,cycle.workflow_input_mapping_snapshot,cycle.policy_snapshot
		FROM issue_pool_item item JOIN issue_pool_cycle cycle ON cycle.id=item.cycle_id
		WHERE item.cycle_id=$1 AND item.workspace_id=$2 AND item.status IN ('approved','dispatching')
		ORDER BY item.created_at,item.id LIMIT 100`, cycleID, ap.WorkspaceID)
	if err != nil {
		return
	}
	var pending []issuePoolDispatchRow
	for rows.Next() {
		var row issuePoolDispatchRow
		if rows.Scan(&row.ItemID, &row.IssueID, &row.WorkspaceID, &row.ProjectID,
			&row.CycleID, &row.AutopilotID, &row.TemplateID, &row.TemplateVersionID,
			&row.ReviewerID, &row.Snapshot, &row.Mapping, &row.PolicySnapshot) == nil {
			if !row.ReviewerID.Valid {
				row.ReviewerID = fallbackAccountable
			}
			pending = append(pending, row)
		}
	}
	rows.Close()
	for _, row := range pending {
		h.dispatchIssuePoolItem(ctx, ap, row)
	}
	h.reconcileIssuePoolCycle(ctx, ap, cycleID)
}

func (h *Handler) dispatchIssuePoolItem(ctx context.Context, ap db.Autopilot, row issuePoolDispatchRow) {
	claimed, err := h.DB.Exec(ctx, `UPDATE issue_pool_item
		SET status='dispatching',dispatch_attempts=dispatch_attempts+1,updated_at=now()
		WHERE id=$1 AND (status='approved' OR (status='dispatching' AND updated_at < now()-interval '1 minute'))`, row.ItemID)
	if err != nil || claimed.RowsAffected() != 1 {
		return
	}
	input, updatedAt, projectID, err := h.resolveIssuePoolWorkflowInput(ctx, row)
	if err != nil {
		h.deferIssuePoolItem(ctx, row.ItemID, "input_incompatible", err)
		return
	}
	hashBytes, _ := json.Marshal(map[string]any{
		"issue_snapshot": json.RawMessage(row.Snapshot), "template_version_id": uuidToString(row.TemplateVersionID),
		"resolved_input": json.RawMessage(input), "policy_snapshot": json.RawMessage(row.PolicySnapshot),
	})
	requestHash := fmt.Sprintf("%x", sha256.Sum256(hashBytes))
	if _, err := h.DB.Exec(ctx, `UPDATE issue_pool_item SET resolved_input=$2::jsonb,
		request_hash=$3, updated_at=now() WHERE id=$1 AND status='dispatching'`, row.ItemID, string(input), requestHash); err != nil {
		return
	}
	started, err := h.WorkflowEngine.StartRun(ctx, workflow.StartRunInput{
		WorkspaceID: row.WorkspaceID, TemplateID: row.TemplateID, TemplateVersionID: row.TemplateVersionID,
		IssueID: row.IssueID, Source: "autopilot", SourceEventID: uuidToString(row.ItemID),
		IdempotencyKey: "issue-pool:" + uuidToString(row.ItemID), RequestHash: requestHash,
		AccountableUserID: row.ReviewerID, Input: input, ActorType: "member", ActorID: row.ReviewerID,
		EnforceIssueSnapshot: true, ExpectedIssueProjectID: projectID, ExpectedIssueUpdatedAt: updatedAt,
	})
	if err != nil {
		var engineErr *workflow.EngineError
		if errors.As(err, &engineErr) && !engineErr.Retryable {
			h.deferIssuePoolItem(ctx, row.ItemID, engineErr.Code, err)
		} else {
			_, _ = h.DB.Exec(ctx, `UPDATE issue_pool_item SET failure_code='dispatch_retryable',
				failure_detail=jsonb_build_object('message',$2::text),updated_at=now() WHERE id=$1`, row.ItemID, err.Error())
		}
		return
	}
	status := started.Run.Status
	if status == "pending" {
		status = "running"
	}
	_, _ = h.DB.Exec(ctx, `UPDATE issue_pool_item SET workflow_run_id=$2,status=$3,
		dispatched_at=COALESCE(dispatched_at,now()),waiting_acceptance_at=CASE WHEN $3='waiting_acceptance' THEN now() ELSE waiting_acceptance_at END,
		failure_code=NULL,failure_detail=NULL,updated_at=now() WHERE id=$1`, row.ItemID, started.Run.ID, status)
}

func (h *Handler) resolveIssuePoolWorkflowInput(ctx context.Context, row issuePoolDispatchRow) (json.RawMessage, pgtype.Timestamptz, pgtype.UUID, error) {
	version, err := h.Queries.GetWorkflowTemplateVersion(ctx, db.GetWorkflowTemplateVersionParams{ID: row.TemplateVersionID, WorkspaceID: row.WorkspaceID})
	if err != nil {
		return nil, pgtype.Timestamptz{}, pgtype.UUID{}, fmt.Errorf("pinned workflow version is unavailable: %w", err)
	}
	def, err := workflow.ParseDefinition(version.Definition)
	if err != nil {
		return nil, pgtype.Timestamptz{}, pgtype.UUID{}, err
	}
	entry, _ := def.NodeByKey(def.EntryNode)
	if entry != nil && entry.EffectiveInputMode() == workflow.InputModeImage {
		return nil, pgtype.Timestamptz{}, pgtype.UUID{}, fmt.Errorf("image input is not supported by issue pools")
	}
	var snapshot map[string]any
	if err := json.Unmarshal(row.Snapshot, &snapshot); err != nil {
		return nil, pgtype.Timestamptz{}, pgtype.UUID{}, fmt.Errorf("invalid issue snapshot: %w", err)
	}
	var updated time.Time
	if micros, ok := snapshot["updated_at_unix_micro"].(float64); ok {
		updated = time.UnixMicro(int64(micros))
	} else {
		updatedText, _ := snapshot["updated_at"].(string)
		var err error
		updated, err = time.Parse(time.RFC3339Nano, updatedText)
		if err != nil {
			return nil, pgtype.Timestamptz{}, pgtype.UUID{}, fmt.Errorf("issue snapshot has no valid updated_at")
		}
	}
	var mapping map[string]string
	_ = json.Unmarshal(row.Mapping, &mapping)
	if mapping == nil {
		mapping = map[string]string{}
	}
	if len(mapping) == 0 {
		mapping = map[string]string{"title": "title", "description": "description"}
		if entry != nil {
			for _, field := range entry.InputFields {
				if _, ok := snapshot[field.Key]; ok {
					mapping[field.Key] = field.Key
				}
			}
		}
	}
	input := map[string]any{}
	for target, source := range mapping {
		var value string
		switch source {
		case "issue_id":
			value = uuidToString(row.IssueID)
		case "issue_number":
			value = fmt.Sprint(snapshot["number"])
		case "acceptance_criteria":
			if values, ok := snapshot[source].([]any); ok {
				parts := make([]string, 0, len(values))
				for _, v := range values {
					parts = append(parts, fmt.Sprint(v))
				}
				value = strings.Join(parts, "\n")
			}
		default:
			value = fmt.Sprint(snapshot[source])
		}
		input[target] = value
	}
	raw, _ := json.Marshal(input)
	if err := workflow.ValidateRunInput(raw, entry); err != nil {
		return nil, pgtype.Timestamptz{}, pgtype.UUID{}, err
	}
	return raw, pgtype.Timestamptz{Time: updated, Valid: true}, row.ProjectID, nil
}

func (h *Handler) deferIssuePoolItem(ctx context.Context, itemID pgtype.UUID, code string, err error) {
	_, _ = h.DB.Exec(ctx, `UPDATE issue_pool_item SET status='deferred',failure_code=$2,
		failure_detail=jsonb_build_object('message',$3::text),completed_at=now(),updated_at=now() WHERE id=$1`, itemID, code, err.Error())
}

// reconcileIssuePoolCycle projects canonical WorkflowRun state to pool items,
// then derives the cycle and AutopilotRun status. It is safe to replay.
func (h *Handler) reconcileIssuePoolCycle(ctx context.Context, ap db.Autopilot, cycleID pgtype.UUID) {
	_, _ = h.DB.Exec(ctx, `UPDATE issue_pool_item item SET
		status=CASE run.status WHEN 'completed' THEN 'completed' WHEN 'failed' THEN 'failed'
			WHEN 'cancelled' THEN 'cancelled' WHEN 'blocked' THEN 'blocked'
			WHEN 'waiting_acceptance' THEN 'waiting_acceptance' ELSE 'running' END,
		waiting_acceptance_at=CASE WHEN run.status='waiting_acceptance' THEN COALESCE(item.waiting_acceptance_at,now()) ELSE item.waiting_acceptance_at END,
		completed_at=CASE WHEN run.status IN ('completed','failed','cancelled') THEN COALESCE(item.completed_at,run.completed_at,now()) ELSE item.completed_at END,
		failure_code=CASE WHEN run.status='failed' THEN COALESCE(run.failure_reason,'workflow_failed') WHEN run.status='blocked' THEN COALESCE(run.blocked_reason,'workflow_blocked') ELSE item.failure_code END,
		updated_at=now()
		FROM workflow_run run WHERE item.cycle_id=$1 AND item.workflow_run_id=run.id
		AND item.status IS DISTINCT FROM CASE run.status WHEN 'completed' THEN 'completed' WHEN 'failed' THEN 'failed'
			WHEN 'cancelled' THEN 'cancelled' WHEN 'blocked' THEN 'blocked' WHEN 'waiting_acceptance' THEN 'waiting_acceptance' ELSE 'running' END`, cycleID)

	var total, claimed, active, waiting, blocked, completed, failed, cancelled, deferred, rejected int32
	err := h.DB.QueryRow(ctx, `SELECT count(*)::int,
		count(*) FILTER (WHERE status='claimed')::int,
		count(*) FILTER (WHERE status IN ('approved','dispatching','running'))::int,
		count(*) FILTER (WHERE status='waiting_acceptance')::int,
		count(*) FILTER (WHERE status='blocked')::int,
		count(*) FILTER (WHERE status='completed')::int,
		count(*) FILTER (WHERE status='failed')::int,
		count(*) FILTER (WHERE status='cancelled')::int,
		count(*) FILTER (WHERE status='deferred')::int,
		count(*) FILTER (WHERE status='rejected')::int FROM issue_pool_item WHERE cycle_id=$1`, cycleID).
		Scan(&total, &claimed, &active, &waiting, &blocked, &completed, &failed, &cancelled, &deferred, &rejected)
	if err != nil {
		return
	}
	status := "running"
	terminal := completed + failed + cancelled + deferred + rejected
	if claimed > 0 {
		status = "awaiting_review"
	} else if blocked > 0 {
		status = "blocked"
	} else if waiting > 0 {
		status = "waiting_acceptance"
	} else if active > 0 {
		status = "running"
	} else if terminal == total {
		if failed > 0 && completed == 0 && deferred == 0 {
			status = "failed"
		} else if failed > 0 || deferred > 0 {
			status = "partial"
		} else if cancelled > 0 && completed == 0 {
			status = "cancelled"
		} else {
			status = "completed"
		}
	}
	var autopilotRunID pgtype.UUID
	_ = h.DB.QueryRow(ctx, `UPDATE issue_pool_cycle SET status=$2,completed_count=$3,blocked_count=$4,
		failed_count=$5,deferred_count=$6,rejected_count=$7,updated_at=now(),
		completed_at=CASE WHEN $2 IN ('completed','partial','cancelled','failed') THEN COALESCE(completed_at,now()) ELSE NULL END
		WHERE id=$1 RETURNING autopilot_run_id`, cycleID, status, completed, blocked, failed, deferred, rejected).Scan(&autopilotRunID)
	if autopilotRunID.Valid && (status == "completed" || status == "partial" || status == "cancelled") {
		result, _ := json.Marshal(map[string]any{"cycle_id": uuidToString(cycleID), "status": status, "completed": completed, "failed": failed, "deferred": deferred})
		_, _ = h.DB.Exec(ctx, `UPDATE autopilot_run SET status='completed',completed_at=COALESCE(completed_at,now()),result=$2::jsonb
			WHERE id=$1 AND status='running'`, autopilotRunID, string(result))
	}
	if autopilotRunID.Valid && status == "failed" {
		result, _ := json.Marshal(map[string]any{"cycle_id": uuidToString(cycleID), "status": status, "failed": failed})
		_, _ = h.DB.Exec(ctx, `UPDATE autopilot_run SET status='failed',completed_at=COALESCE(completed_at,now()),
			failure_reason=COALESCE(failure_reason,'issue_pool_failed'),result=$2::jsonb
			WHERE id=$1 AND status='running'`, autopilotRunID, string(result))
	}
	// Durable, replay-safe notification intents. The unique outbox key suppresses
	// duplicates across every reconciler replay and crash-recovery pass.
	_, _ = h.DB.Exec(ctx, `WITH events AS (
		SELECT id AS item_id,issue_id,CASE status WHEN 'waiting_acceptance' THEN 'waiting_acceptance'
			WHEN 'blocked' THEN 'item_blocked' ELSE 'item_failed' END AS event_type
		FROM issue_pool_item WHERE cycle_id=$1 AND status IN ('waiting_acceptance','blocked','failed')
	)
	INSERT INTO issue_pool_notification_outbox (workspace_id,autopilot_id,cycle_id,item_id,recipient_id,event_type,payload)
	SELECT $3,$2,$1,e.item_id,r.recipient_id,e.event_type,jsonb_build_object('item_id',e.item_id)
	FROM events e CROSS JOIN LATERAL (
		SELECT created_by_id AS recipient_id FROM issue_pool_cycle WHERE id=$1
		UNION SELECT user_id FROM autopilot_subscriber WHERE autopilot_id=$2 AND user_type='member'
		UNION SELECT user_id FROM issue_subscriber WHERE issue_id=e.issue_id AND user_type='member'
	) r ON CONFLICT DO NOTHING`, cycleID, ap.ID, ap.WorkspaceID)
	if status == "completed" || status == "partial" || status == "cancelled" || status == "failed" {
		_, _ = h.DB.Exec(ctx, `WITH recipients AS (
			SELECT created_by_id AS recipient_id FROM issue_pool_cycle WHERE id=$1
			UNION SELECT user_id FROM autopilot_subscriber WHERE autopilot_id=$2 AND user_type='member'
			UNION SELECT subscriber.user_id FROM issue_subscriber subscriber
			JOIN issue_pool_item item ON item.issue_id=subscriber.issue_id
			WHERE item.cycle_id=$1 AND subscriber.user_type='member')
		INSERT INTO issue_pool_notification_outbox (workspace_id,autopilot_id,cycle_id,recipient_id,event_type,payload)
		SELECT $3,$2,$1,recipient_id,'cycle_terminal',jsonb_build_object('status',$4::text)
		FROM recipients ON CONFLICT DO NOTHING`, cycleID, ap.ID, ap.WorkspaceID, status)
	}
}

// RunIssuePoolReconciler repairs review-claim expiry, the StartRun/link crash
// window, Workflow terminal projection, cycle aggregation, and outbox delivery.
func (h *Handler) RunIssuePoolReconciler(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		h.reconcileIssuePools(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (h *Handler) reconcileIssuePools(ctx context.Context) {
	_, _ = h.DB.Exec(ctx, `UPDATE issue_pool_item SET status='deferred',failure_code='claim_expired',
		failure_detail='{"message":"manual review claim expired"}'::jsonb,completed_at=now(),updated_at=now()
		WHERE status='claimed' AND claim_expires_at IS NOT NULL AND claim_expires_at < now()`)
	rows, err := h.DB.Query(ctx, `SELECT DISTINCT cycle.id,cycle.autopilot_id,cycle.workspace_id
		FROM issue_pool_cycle cycle LEFT JOIN issue_pool_item item ON item.cycle_id=cycle.id
		WHERE cycle.status NOT IN ('completed','partial','failed','cancelled')
		ORDER BY cycle.updated_at LIMIT 100`)
	if err == nil {
		for rows.Next() {
			var cycleID, autopilotID, workspaceID pgtype.UUID
			if rows.Scan(&cycleID, &autopilotID, &workspaceID) != nil {
				continue
			}
			ap, e := h.Queries.GetAutopilot(ctx, autopilotID)
			if e != nil || ap.WorkspaceID != workspaceID {
				continue
			}
			h.dispatchApprovedIssuePoolItems(ctx, ap, cycleID, pgtype.UUID{})
		}
		rows.Close()
	}
	// Claim notification intents with bounded exponential backoff. Delivery
	// persists the user-visible inbox row before any realtime broadcast.
	outboxRows, err := h.DB.Query(ctx, `WITH claimed AS (
		SELECT id FROM issue_pool_notification_outbox WHERE delivered_at IS NULL AND available_at<=now()
		ORDER BY created_at LIMIT 100 FOR UPDATE SKIP LOCKED)
		UPDATE issue_pool_notification_outbox outbox SET attempts=attempts+1,
		available_at=now()+make_interval(secs => LEAST(3600,30*power(2,LEAST(attempts,6))::int)),
		updated_at=now() FROM claimed
		WHERE outbox.id=claimed.id RETURNING outbox.id,outbox.workspace_id,outbox.recipient_id,outbox.event_type,outbox.payload`)
	if err != nil {
		return
	}
	var notices []issuePoolOutboxNotice
	for outboxRows.Next() {
		var n issuePoolOutboxNotice
		if outboxRows.Scan(&n.id, &n.workspace, &n.recipient, &n.event, &n.payload) == nil {
			notices = append(notices, n)
		}
	}
	outboxRows.Close()
	for _, n := range notices {
		if err := h.persistIssuePoolInbox(ctx, n); err != nil {
			_, _ = h.DB.Exec(ctx, `UPDATE issue_pool_notification_outbox
				SET last_error=left($2,2000),updated_at=now() WHERE id=$1 AND delivered_at IS NULL`, n.id, err.Error())
			continue
		}
		var payload map[string]any
		_ = json.Unmarshal(n.payload, &payload)
		payload["notification_id"] = uuidToString(n.id)
		payload["recipient_id"] = uuidToString(n.recipient)
		h.publish("issue_pool."+n.event, uuidToString(n.workspace), "system", "", payload)
		_, _ = h.DB.Exec(ctx, `UPDATE issue_pool_notification_outbox
			SET delivered_at=now(),last_error=NULL,updated_at=now() WHERE id=$1 AND delivered_at IS NULL`, n.id)
	}
}

type issuePoolOutboxNotice struct {
	id, workspace, recipient pgtype.UUID
	event                    string
	payload                  []byte
}

// persistIssuePoolInbox is independently replay-safe. The partial unique
// index on details.notification_id arbitrates concurrent workers and the
// crash window after this commit but before the outbox is marked delivered.
func (h *Handler) persistIssuePoolInbox(ctx context.Context, n issuePoolOutboxNotice) error {
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin issue-pool inbox delivery: %w", err)
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `
		INSERT INTO inbox_item (
			workspace_id,recipient_type,recipient_id,type,severity,issue_id,title,body,actor_type,details
		)
		SELECT outbox.workspace_id,'member',outbox.recipient_id,'issue_pool',
			CASE outbox.event_type
				WHEN 'candidate_review' THEN 'action_required'
				WHEN 'waiting_acceptance' THEN 'action_required'
				WHEN 'item_blocked' THEN 'attention'
				WHEN 'item_failed' THEN 'attention'
				ELSE 'info'
			END,
			item.issue_id,
			CASE outbox.event_type
				WHEN 'candidate_review' THEN 'Issue pool review requested'
				WHEN 'waiting_acceptance' THEN 'Workflow acceptance required'
				WHEN 'item_blocked' THEN 'Issue pool workflow blocked'
				WHEN 'item_failed' THEN 'Issue pool workflow failed'
				ELSE 'Issue pool cycle completed'
			END,
			NULL,'system',
			outbox.payload || jsonb_build_object(
				'notification_id',outbox.id,'autopilot_id',outbox.autopilot_id,
				'cycle_id',outbox.cycle_id,'event_type',outbox.event_type
			)
		FROM issue_pool_notification_outbox outbox
		LEFT JOIN issue_pool_item item ON item.id=outbox.item_id
		WHERE outbox.id=$1
		ON CONFLICT DO NOTHING`, n.id)
	if err != nil {
		return fmt.Errorf("persist issue-pool inbox delivery: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit issue-pool inbox delivery: %w", err)
	}
	return nil
}
