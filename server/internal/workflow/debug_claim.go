package workflow

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// ClaimDebugTask grants execution only after locking the run, step and task and
// revalidating routing/resources. finalize persists the task-scoped credentials
// in the same transaction; a response build failure must never call this entry.
func (e *Engine) ClaimDebugTask(ctx context.Context, taskID, runtimeID, incarnationID pgtype.UUID, finalize func(context.Context, *db.Queries, db.AgentTaskQueue) error) (db.WorkflowDebugTaskExecution, error) {
	var out db.WorkflowDebugTaskExecution
	if !incarnationID.Valid || finalize == nil || e.RevalidateDebugEnvironment == nil {
		return out, newEngineError("debug_unavailable", "debug claim protocol unavailable")
	}
	probe, err := e.Queries.GetWorkflowDebugTaskRun(ctx, taskID)
	if err != nil {
		return out, err
	}
	effects := &txEffects{}
	err = e.runInTx(ctx, effects, func(ctx context.Context, q *db.Queries) error {
		run, err := q.GetWorkflowRunForUpdate(ctx, db.GetWorkflowRunForUpdateParams{ID: probe.ID, WorkspaceID: probe.WorkspaceID})
		if err != nil {
			return err
		}
		if expired, err := e.expireDebugRun(ctx, q, run, effects); expired || err != nil {
			return err
		}
		if run.DetailsPurgedAt.Valid || run.DebugStopRequestedAt.Valid || IsTerminalRunStatus(RunStatus(run.Status)) {
			return newEngineError(ErrCodeInvalidTransition, "draft trial is terminal")
		}
		tp, err := q.GetAgentTask(ctx, taskID)
		if err != nil {
			return err
		}
		step, err := q.GetWorkflowStepInstanceForUpdate(ctx, db.GetWorkflowStepInstanceForUpdateParams{ID: tp.WorkflowStepInstanceID, WorkspaceID: run.WorkspaceID})
		if err != nil {
			return err
		}
		task, err := q.GetAgentTaskForDelegatedFailureUpdate(ctx, taskID)
		if err != nil {
			return err
		}
		if task.RuntimeID != runtimeID || task.WorkflowStepInstanceID != step.ID || step.RunID != run.ID || task.Status != "queued" || task.DebugNeverDispatchedAt.Valid {
			return newEngineError(ErrCodeInvalidTransition, "task cannot be claimed")
		}
		claims, err := q.ListWorkflowDebugExecutions(ctx, db.ListWorkflowDebugExecutionsParams{RunID: run.ID, WorkspaceID: run.WorkspaceID})
		if err != nil {
			return err
		}
		for _, x := range claims {
			if x.TaskID == task.ID {
				return newEngineError(ErrCodeInvalidTransition, "task execution was already granted")
			}
		}
		if err = debugMember(ctx, q, run.WorkspaceID, run.AccountableUserID, false); err != nil {
			return err
		}
		def, err := ResolveRunDefinition(ctx, q, run.WorkspaceID, run)
		if err != nil {
			return err
		}
		node, ok := def.NodeByKey(step.NodeKey)
		if !ok {
			return newEngineError(ErrCodeInvariantViolation, "claim node missing")
		}
		prior, err := e.priorAgentsByNode(ctx, q, run)
		if err != nil {
			return err
		}
		if e.Router == nil {
			return newEngineError(ErrCodeRoutingFailed, "router unavailable")
		}
		route, err := e.Router.Route(ctx, q, RouteRequest{WorkspaceID: run.WorkspaceID, Run: run, Node: node, AccountableUserID: run.AccountableUserID, PriorAgentByNode: prior, RequiresVision: imageAttachmentFromRunContext(run.Context) != nil})
		if err != nil {
			return err
		}
		if route.AgentID != task.AgentID || route.RuntimeID != runtimeID {
			return newEngineError(ErrCodeRoutingFailed, "task routing permission changed")
		}
		if err = e.validateDebugEnvironment(ctx, q, run); err != nil {
			return err
		}
		dispatched, err := q.DispatchWorkflowDebugTask(ctx, db.DispatchWorkflowDebugTaskParams{ID: taskID, RuntimeID: runtimeID})
		if errors.Is(err, pgx.ErrNoRows) {
			return newEngineError(ErrCodeInvalidTransition, "task is no longer claimable")
		}
		if err != nil {
			return err
		}
		out, err = q.CreateWorkflowDebugExecution(ctx, db.CreateWorkflowDebugExecutionParams{WorkspaceID: run.WorkspaceID, RunID: run.ID, StepID: step.ID, TaskID: task.ID, TaskAttempt: task.Attempt, RuntimeID: runtimeID, DaemonIncarnationID: incarnationID, ClaimGeneration: 1})
		if err != nil {
			return err
		}
		return finalize(ctx, q, dispatched)
	})
	if err == nil {
		effects.flush(ctx, e.Notifier)
	}
	return out, err
}

func (e *Engine) validateDebugEnvironment(ctx context.Context, q *db.Queries, run db.WorkflowRun) error {
	if err := debugMember(ctx, q, run.WorkspaceID, run.AccountableUserID, false); err != nil {
		return err
	}
	if image := imageAttachmentFromRunContext(run.Context); image != nil {
		if _, err := resolveWorkflowImageAttachment(ctx, q, run.WorkspaceID, image.ID); err != nil {
			return err
		}
	}
	if e.RevalidateDebugEnvironment == nil {
		return newEngineError("debug_unavailable", "fixed environment validation unavailable")
	}
	return e.RevalidateDebugEnvironment(ctx, q, run)
}
