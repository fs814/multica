package workflow

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const ReasonDebugDeadlineExceeded = "debug_deadline_exceeded"

// expireDebugRun is called with the run lock, before graph parsing. It also
// covers blocked runs and acceptance waits. Returning true commits the timeout;
// callers must not return an error that would roll that decision back.
func (e *Engine) expireDebugRun(ctx context.Context, q *db.Queries, run db.WorkflowRun, effects *txEffects) (bool, error) {
	if run.ExecutionMode != ExecutionDraftTest || IsTerminalRunStatus(RunStatus(run.Status)) {
		return false, nil
	}
	now, err := q.WorkflowDebugDatabaseTime(ctx)
	if err != nil {
		return false, err
	}
	if !run.DebugDeadlineAt.Valid || run.DebugDeadlineAt.Time.After(now.Time) {
		return false, nil
	}
	failed, err := q.FailWorkflowRun(ctx, db.FailWorkflowRunParams{ID: run.ID, WorkspaceID: run.WorkspaceID, FailureReason: ReasonDebugDeadlineExceeded})
	if err != nil {
		return false, err
	}
	if _, err = q.CancelWorkflowStepInstancesForRun(ctx, db.CancelWorkflowStepInstancesForRunParams{RunID: run.ID, WorkspaceID: run.WorkspaceID}); err != nil {
		return false, err
	}
	if _, err = q.CancelPendingWorkflowAcceptancesForRun(ctx, db.CancelPendingWorkflowAcceptancesForRunParams{RunID: run.ID, WorkspaceID: run.WorkspaceID}); err != nil {
		return false, err
	}
	effects.runChanged(failed)
	err = e.recordEvent(ctx, q, eventSpec{WorkspaceID: run.WorkspaceID, RunID: run.ID, Type: EventRunFailed, IdempotencyKey: "debug:deadline:" + uuidString(run.ID), ActorType: "system", Payload: mustJSON(map[string]string{"reason": ReasonDebugDeadlineExceeded})})
	return true, err
}

// DebugStopSender reuses TaskService cancellation after the engine transaction.
// It must send to the original claim runtime even when task.status is cancelled.
type DebugStopSender func(context.Context, db.AgentTaskQueue, db.WorkflowDebugTaskExecution) error

func (e *Engine) MaintainDebugRun(ctx context.Context, ws, runID pgtype.UUID) error {
	effects := &txEffects{}
	err := e.runInTx(ctx, effects, func(ctx context.Context, q *db.Queries) error {
		run, err := q.GetWorkflowRunForUpdate(ctx, db.GetWorkflowRunForUpdateParams{ID: runID, WorkspaceID: ws})
		if err != nil {
			return err
		}
		if run.ExecutionMode != ExecutionDraftTest || run.DetailsPurgedAt.Valid {
			return nil
		}
		expired, err := e.expireDebugRun(ctx, q, run, effects)
		if err != nil {
			return err
		}
		if expired {
			run, err = q.GetWorkflowRun(ctx, db.GetWorkflowRunParams{ID: runID, WorkspaceID: ws})
			if err != nil {
				return err
			}
		}
		if !IsTerminalRunStatus(RunStatus(run.Status)) {
			return nil
		}
		if err = q.MarkWorkflowDebugTerminal(ctx, db.MarkWorkflowDebugTerminalParams{ID: run.ID, WorkspaceID: ws}); err != nil {
			return err
		}
		if _, err = q.ListWorkflowStepsForUpdate(ctx, db.ListWorkflowStepsForUpdateParams{RunID: run.ID, WorkspaceID: ws}); err != nil {
			return err
		}
		tasks, err := q.LockWorkflowDebugTasksForRun(ctx, db.LockWorkflowDebugTasksForRunParams{RunID: run.ID, WorkspaceID: ws})
		if err != nil {
			return err
		}
		executions, err := q.ListWorkflowDebugExecutions(ctx, db.ListWorkflowDebugExecutionsParams{RunID: run.ID, WorkspaceID: ws})
		if err != nil {
			return err
		}
		claimed := map[pgtype.UUID]bool{}
		for _, x := range executions {
			claimed[x.TaskID] = true
			if x.DeliveryDrainedAt.Valid {
				continue
			}
			if err = q.MarkWorkflowDebugExecutionStop(ctx, db.MarkWorkflowDebugExecutionStopParams{ID: x.ID, WorkspaceID: ws}); err != nil {
				return err
			}
			if err = q.EnsureWorkflowDebugStopRequest(ctx, db.EnsureWorkflowDebugStopRequestParams{WorkspaceID: ws, RunID: run.ID, TaskID: x.TaskID, ClaimID: x.ID}); err != nil {
				return err
			}
		}
		waiting := false
		for _, task := range tasks {
			if !claimed[task.ID] && !task.DebugNeverDispatchedAt.Valid {
				n, err := q.MarkWorkflowDebugNeverDispatched(ctx, db.MarkWorkflowDebugNeverDispatchedParams{ID: task.ID, WorkspaceID: ws})
				if err != nil {
					return err
				}
				waiting = waiting || n == 0
			}
		}
		for _, x := range executions {
			waiting = waiting || !x.DeliveryDrainedAt.Valid
		}
		if waiting {
			return q.SetWorkflowDebugWaitingStop(ctx, db.SetWorkflowDebugWaitingStopParams{ID: run.ID, WorkspaceID: ws})
		}
		return q.SetWorkflowDebugStopConfirmed(ctx, db.SetWorkflowDebugStopConfirmedParams{ID: run.ID, WorkspaceID: ws})
	})
	if err != nil {
		return err
	}
	effects.flush(ctx, e.Notifier)
	if e.SendDebugStop == nil {
		return nil
	}
	pending, err := e.Queries.ListWorkflowDebugStopRequests(ctx, db.ListWorkflowDebugStopRequestsParams{RunID: runID, WorkspaceID: ws})
	if err != nil {
		return err
	}
	for _, todo := range pending {
		x, err := e.Queries.GetWorkflowDebugExecution(ctx, todo.ClaimID)
		if err != nil {
			return err
		}
		if x.DeliveryDrainedAt.Valid {
			if err = e.Queries.ResolveWorkflowDebugStopRequest(ctx, db.ResolveWorkflowDebugStopRequestParams{ClaimID: x.ID, WorkspaceID: ws}); err != nil {
				return err
			}
			continue
		}
		task, err := e.Queries.GetAgentTask(ctx, todo.TaskID)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		code := "retry_pending"
		if err = e.SendDebugStop(ctx, task, x); err != nil {
			code = "delivery_failed"
		}
		if err = e.Queries.RetryWorkflowDebugStopRequest(ctx, db.RetryWorkflowDebugStopRequestParams{ID: todo.ID, WorkspaceID: ws, ErrorCode: pgtype.Text{String: code, Valid: true}}); err != nil {
			return err
		}
	}
	return nil
}
