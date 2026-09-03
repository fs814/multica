package workflow

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	obsmetrics "github.com/multica-ai/multica/server/internal/metrics"
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
	first := env.assertWorkflowIdentityCounts(t, run.ID, string(RunRunning), 2, 7)
	if err := reconciler.Sweep(context.Background()); err != nil {
		t.Fatalf("second Sweep after restart: %v", err)
	}
	if second := env.assertWorkflowIdentityCounts(t, run.ID, string(RunRunning), 2, 7); second != first {
		t.Fatalf("replayed terminal recovery changed identities: first=%+v second=%+v", first, second)
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
	first := env.assertWorkflowIdentityCounts(t, run.ID, string(RunRunning), 1, 4)
	if err := env.engine.ReconcileRun(context.Background(), env.workspaceID, run.ID); err != nil {
		t.Fatalf("second ReconcileRun: %v", err)
	}
	if second := env.assertWorkflowIdentityCounts(t, run.ID, string(RunRunning), 1, 4); second != first {
		t.Fatalf("ready-step recovery changed identities on replay: first=%+v second=%+v", first, second)
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
	first := env.assertWorkflowIdentityCounts(t, run.ID, string(RunRunning), 2, 5)
	if err := env.engine.ReconcileRun(context.Background(), env.workspaceID, run.ID); err != nil {
		t.Fatalf("second ReconcileRun: %v", err)
	}
	if second := env.assertWorkflowIdentityCounts(t, run.ID, string(RunRunning), 2, 5); second != first {
		t.Fatalf("lost-advance recovery changed identities on replay: first=%+v second=%+v", first, second)
	}
}

func TestWorkflowReconcilerSQLMetricsReachEndpoint(t *testing.T) {
	env := setupTestEnv(t)
	registry := obsmetrics.NewRegistry(obsmetrics.RegistryOptions{})
	env.engine.Metrics = registry.Workflow
	env.publishTemplate(t, linearDefinition())
	run := env.startRun(t, "reconcile-metrics")
	if _, err := env.pool.Exec(context.Background(),
		"UPDATE workflow_run SET updated_at = now() - interval '2 minutes' WHERE id = $1", run.ID,
	); err != nil {
		t.Fatalf("age active Run: %v", err)
	}

	reconciler := NewReconciler(env.engine, env.q)
	reconciler.StaleAfter = 0
	if err := reconciler.Sweep(context.Background()); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	rec := httptest.NewRecorder()
	obsmetrics.NewHandler(registry.Gatherer).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/metrics status = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`multica_workflow_transitions_total{event="run.started"}`,
		`multica_workflow_reconciliations_total{outcome="repaired_or_healthy"}`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("/metrics missing %q", want)
		}
	}
	var stalled float64
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "multica_workflow_oldest_stalled_active_run_seconds ") {
			if _, err := fmt.Sscanf(line, "multica_workflow_oldest_stalled_active_run_seconds %f", &stalled); err != nil {
				t.Fatalf("parse stalled Run metric %q: %v", line, err)
			}
		}
	}
	if stalled < 100 {
		t.Fatalf("oldest stalled active Run = %.3fs, want SQL-sampled age >= 100s", stalled)
	}
}
