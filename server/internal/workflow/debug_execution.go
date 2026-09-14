package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"time"
)

const DebugStopReceiptCapability = "workflow_debug_stop_receipt_v1"
const DebugFixedEnvironmentCapability = "workflow_debug_fixed_environment_v1"
const DebugExecutionHeader = "X-Workflow-Execution-Id"

type DebugExecutionReceipt struct {
	SchemaVersion    string    `json:"schema_version"`
	ReceiptID        string    `json:"receipt_id"`
	Kind             string    `json:"kind"`
	ProcessStopped   bool      `json:"process_stopped"`
	DeliveryDrained  bool      `json:"delivery_drained"`
	ProcessStoppedAt time.Time `json:"process_stopped_at"`
	FinalMessageSeq  int32     `json:"final_message_seq"`
}
type DebugTaskIdentity struct {
	Run       db.WorkflowRun
	Step      db.WorkflowStepInstance
	Task      db.AgentTaskQueue
	Execution db.WorkflowDebugTaskExecution
}

// WithDebugTaskExecution serializes every payload writer and receipt with
// cleanup, using durable bindings even after context has been erased. The
// supplied callback must use q for every database write, never pool queries.
// authenticate verifies the original runtime owner before terminal/tombstone
// handling; a claim id is an identity, not a bearer credential.
func (e *Engine) WithDebugTaskExecution(ctx context.Context, taskID, claimID pgtype.UUID, authenticate func(context.Context, db.WorkflowDebugTaskExecution) error, fn func(context.Context, *db.Queries, DebugTaskIdentity) error) error {
	if authenticate == nil || !claimID.Valid {
		return newEngineError("debug_forbidden", "execution identity required")
	}
	probe, err := e.Queries.GetWorkflowDebugTaskRun(ctx, taskID)
	if errors.Is(err, pgx.ErrNoRows) {
		return newEngineError(ErrCodeNotFound, "draft trial task not found")
	}
	if err != nil {
		return err
	}
	effects := &txEffects{}
	err = e.runInTx(ctx, effects, func(ctx context.Context, q *db.Queries) error {
		run, err := q.GetWorkflowRunForUpdate(ctx, db.GetWorkflowRunForUpdateParams{ID: probe.ID, WorkspaceID: probe.WorkspaceID})
		if err != nil {
			return err
		}
		taskProbe, err := q.GetAgentTask(ctx, taskID)
		if err != nil {
			return err
		}
		step, err := q.GetWorkflowStepInstanceForUpdate(ctx, db.GetWorkflowStepInstanceForUpdateParams{ID: taskProbe.WorkflowStepInstanceID, WorkspaceID: run.WorkspaceID})
		if err != nil {
			return err
		}
		task, err := q.GetAgentTaskForDelegatedFailureUpdate(ctx, taskID)
		if err != nil {
			return err
		}
		claim, err := q.GetWorkflowDebugExecutionForUpdate(ctx, db.GetWorkflowDebugExecutionForUpdateParams{ID: claimID, WorkspaceID: run.WorkspaceID})
		if errors.Is(err, pgx.ErrNoRows) {
			return newEngineError("debug_forbidden", "execution identity does not match")
		}
		if err != nil {
			return err
		}
		if claim.RunID != run.ID || claim.StepID != step.ID || claim.TaskID != task.ID || claim.TaskAttempt != task.Attempt || step.RunID != run.ID || task.WorkflowStepInstanceID != step.ID {
			return newEngineError("debug_forbidden", "execution identity does not match")
		}
		if err = authenticate(ctx, claim); err != nil {
			return err
		}
		if expired, expiryErr := e.expireDebugRun(ctx, q, run, effects); expiryErr != nil {
			return expiryErr
		} else if expired {
			run, err = q.GetWorkflowRun(ctx, db.GetWorkflowRunParams{ID: run.ID, WorkspaceID: run.WorkspaceID})
			if err != nil {
				return err
			}
			step, err = q.GetWorkflowStepInstance(ctx, db.GetWorkflowStepInstanceParams{ID: step.ID, WorkspaceID: run.WorkspaceID})
			if err != nil {
				return err
			}
		}
		return fn(ctx, q, DebugTaskIdentity{run, step, task, claim})
	})
	if err == nil {
		effects.flush(ctx, e.Notifier)
	}
	return err
}
func (e *Engine) AcceptDebugExecutionReceipt(ctx context.Context, taskID, claimID pgtype.UUID, authenticate func(context.Context, db.WorkflowDebugTaskExecution) error, receipt DebugExecutionReceipt) error {
	return e.WithDebugTaskExecution(ctx, taskID, claimID, authenticate, func(ctx context.Context, q *db.Queries, id DebugTaskIdentity) error {
		receiptID, err := debugUUID(&receipt.ReceiptID)
		if err != nil {
			return err
		}
		if receipt.SchemaVersion != "1" || !receipt.ProcessStopped || !receipt.DeliveryDrained || receipt.ProcessStoppedAt.IsZero() || receipt.FinalMessageSeq < 0 || (receipt.Kind != "complete" && receipt.Kind != "fail" && receipt.Kind != "cancel_ack") {
			return newEngineError("debug_bad_request", "invalid execution receipt")
		}
		raw, err := json.Marshal(receipt)
		if err != nil {
			return err
		}
		hash := debugHash(raw)
		if id.Execution.ReceiptID.Valid {
			if id.Execution.ReceiptID != receiptID || id.Execution.ReceiptHash.String != hash {
				return newEngineError("debug_receipt_conflict", "execution receipt already sealed")
			}
			return nil
		}
		if id.Run.DetailsPurgedAt.Valid {
			return newEngineError("debug_details_expired", "draft trial details have expired")
		}
		if id.Task.Status != "completed" && id.Task.Status != "failed" && id.Task.Status != "cancelled" {
			return newEngineError("debug_delivery_incomplete", "terminal report has not been accepted")
		}
		// Final delivery may complete after a run timeout or cancellation. The task
		// status therefore cannot dictate the daemon's receipt kind.
		delivery, err := q.GetWorkflowDebugMessageDelivery(ctx, taskID)
		if err != nil {
			return err
		}
		if delivery.LastSeq != receipt.FinalMessageSeq || delivery.MessageCount != int64(receipt.FinalMessageSeq) || (receipt.FinalMessageSeq > 0 && delivery.FirstSeq != 1) {
			return newEngineError("debug_delivery_incomplete", "final messages have gaps")
		}
		pending, err := q.CountWorkflowDebugPendingUploads(ctx, claimID)
		if err != nil {
			return err
		}
		if pending > 0 {
			return newEngineError("debug_delivery_incomplete", "uploads have not settled")
		}
		_, err = q.AcceptWorkflowDebugReceipt(ctx, db.AcceptWorkflowDebugReceiptParams{ID: claimID, WorkspaceID: id.Run.WorkspaceID, ReceiptKind: pgtype.Text{String: receipt.Kind, Valid: true}, ReceiptID: receiptID, ReceiptHash: pgtype.Text{String: hash, Valid: true}, ProcessStoppedAt: pgtype.Timestamptz{Time: receipt.ProcessStoppedAt, Valid: true}, FinalMessageSeq: pgtype.Int4{Int32: receipt.FinalMessageSeq, Valid: true}})
		if err != nil {
			return err
		}
		return q.ResolveWorkflowDebugStopRequest(ctx, db.ResolveWorkflowDebugStopRequestParams{ClaimID: claimID, WorkspaceID: id.Run.WorkspaceID})
	})
}

// DebugPayloadDisposition is evaluated under the execution lock before decoding
// a request's payload. Terminal tasks may finish logs/uploads until their receipt
// seals delivery; no terminal report may advance the graph twice.
func DebugPayloadDisposition(id DebugTaskIdentity, terminalReport bool) string {
	if id.Run.DetailsPurgedAt.Valid {
		return "ignored_expired"
	}
	if id.Execution.DeliveryDrainedAt.Valid {
		return "ignored_delivery_closed"
	}
	if terminalReport && (IsTerminalRunStatus(RunStatus(id.Run.Status)) || IsTerminalStepStatus(StepStatus(id.Step.Status))) {
		return "ignored_terminal"
	}
	return ""
}
