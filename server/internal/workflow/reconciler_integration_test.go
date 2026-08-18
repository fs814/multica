package workflow

import (
	"context"
	"testing"
)

func TestReconcilerReplaysLostTerminalTaskCallback(t *testing.T) {
	env := setupTestEnv(t)
	env.publishTemplate(t, linearDefinition())
	run := env.startRun(t, "reconcile-lost-callback")
	step := env.stepByNode(t, run.ID, "analyze")
	if _, err := env.pool.Exec(context.Background(), `UPDATE agent_task_queue SET status='completed', result=jsonb_build_object('output', $2::text), completed_at=now()-interval '1 minute' WHERE id=$1`, step.TaskID, passPayload("replayed completion")); err != nil {
		t.Fatalf("complete task without callback: %v", err)
	}
	reconciler := NewReconciler(env.engine, env.q)
	reconciler.StaleAfter = 0
	if err := reconciler.Sweep(context.Background()); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if got := env.stepByNode(t, run.ID, "analyze").Status; got != string(StepPassed) {
		t.Fatalf("analyze status = %q, want passed", got)
	}
	if got := env.stepByNode(t, run.ID, "implement").Status; got != string(StepQueued) {
		t.Fatalf("implement status = %q, want queued", got)
	}
}

func TestReconcileRunRepairsReadyAgentWithoutTask(t *testing.T) {
	env := setupTestEnv(t)
	env.publishTemplate(t, linearDefinition())
	run := env.startRun(t, "reconcile-ready-no-task")
	step := env.stepByNode(t, run.ID, "analyze")
	if _, err := env.pool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id=$1`, step.TaskID); err != nil {
		t.Fatalf("delete task: %v", err)
	}
	if _, err := env.pool.Exec(context.Background(), `UPDATE workflow_step_instance SET status='ready', task_id=NULL, activation_timeout_at=now()-interval '1 minute' WHERE id=$1`, step.ID); err != nil {
		t.Fatalf("orphan ready step: %v", err)
	}
	if err := env.engine.ReconcileRun(context.Background(), env.workspaceID, run.ID); err != nil {
		t.Fatalf("ReconcileRun: %v", err)
	}
	repaired := env.stepByNode(t, run.ID, "analyze")
	if repaired.Status != string(StepQueued) || !repaired.TaskID.Valid {
		t.Fatalf("repaired step = status %q task %v, want queued with task", repaired.Status, repaired.TaskID.Valid)
	}
}

func TestReconcileRunRepairsRunningRunWithNoActiveStep(t *testing.T) {
	env := setupTestEnv(t)
	env.publishTemplate(t, linearDefinition())
	run := env.startRun(t, "reconcile-no-active")
	step := env.stepByNode(t, run.ID, "analyze")
	if _, err := env.pool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id=$1`, step.TaskID); err != nil {
		t.Fatalf("delete task: %v", err)
	}
	if _, err := env.pool.Exec(context.Background(), `UPDATE workflow_step_instance SET status='passed', task_id=NULL, completed_at=now() WHERE id=$1`, step.ID); err != nil {
		t.Fatalf("create lost-advance state: %v", err)
	}
	if err := env.engine.ReconcileRun(context.Background(), env.workspaceID, run.ID); err != nil {
		t.Fatalf("ReconcileRun: %v", err)
	}
	if got := env.stepByNode(t, run.ID, "implement").Status; got != string(StepQueued) {
		t.Fatalf("implement status = %q, want queued", got)
	}
}
