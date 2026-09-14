package workflow

import (
	"context"
	"errors"
	"fmt"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"testing"
)

func TestReviewDebugMaintenanceDoesNotStarveLaterStops(t *testing.T) {
	env, in := setupDebugTest(t)
	ctx := context.Background()
	first, err := env.engine.StartDraftTest(ctx, env.workspaceID, env.userID, env.templateID, in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = env.engine.CancelRun(ctx, env.workspaceID, first.Run.ID, env.userID); err != nil {
		t.Fatal(err)
	}
	if err = env.engine.MaintainDebugRun(ctx, env.workspaceID, first.Run.ID); err != nil {
		t.Fatal(err)
	}
	in.IdempotencyKey = "later-claimed-cancel"
	second, err := env.engine.StartDraftTest(ctx, env.workspaceID, env.userID, env.templateID, in)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = debugClaimFixture(t, env, second.Run)
	if _, err = env.engine.CancelRun(ctx, env.workspaceID, second.Run.ID, env.userID); err != nil {
		t.Fatal(err)
	}
	sent := 0
	env.engine.SendDebugStop = func(context.Context, db.AgentTaskQueue, db.WorkflowDebugTaskExecution) error { sent++; return nil }
	r := NewReconciler(env.engine, env.q)
	r.BatchSize = 1 // Same head-of-queue condition as 100 retained rows at the default batch size.
	r.StaleAfter = 0
	for i := 0; i < 3; i++ {
		if err = r.Sweep(ctx); err != nil {
			t.Fatal(err)
		}
	}
	// A delivered stop is now in backoff, so inspect the durable row rather
	// than the due-only retry query from the original reproduction.
	var pending int
	if err = env.pool.QueryRow(ctx, "SELECT count(*) FROM workflow_debug_stop_request WHERE run_id=$1 AND resolved_at IS NULL", second.Run.ID).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if sent == 0 || pending != 1 {
		t.Fatalf("later claim starved after 3 sweeps: stop sends=%d durable pending=%d", sent, pending)
	}
}

func TestDebugMaintenanceFairAcrossBatchesBackoffAndPurge(t *testing.T) {
	env, in := setupDebugTest(t)
	ctx := context.Background()
	fx := testutil.New(env.pool, uuidString(env.workspaceID), uuidString(env.userID))
	start := func(key string) db.WorkflowRun {
		t.Helper()
		in.IdempotencyKey = key
		x, err := env.engine.StartDraftTest(ctx, env.workspaceID, env.userID, env.templateID, in)
		if err != nil {
			t.Fatal(err)
		}
		return x.Run
	}
	cancel := func(run db.WorkflowRun) {
		t.Helper()
		if _, err := env.engine.CancelRun(ctx, env.workspaceID, run.ID, env.userID); err != nil {
			t.Fatal(err)
		}
	}
	maintain := func(run db.WorkflowRun) {
		t.Helper()
		if err := env.engine.MaintainDebugRun(ctx, env.workspaceID, run.ID); err != nil {
			t.Fatal(err)
		}
	}
	var retained []db.WorkflowRun
	for i := 0; i < 3; i++ {
		run := start(fmt.Sprintf("retained-%d", i))
		cancel(run)
		maintain(run)
		retained = append(retained, run)
	}
	// Unprovable legacy dispatches remain due indefinitely: advancing after
	// failures/no-progress is essential even with a due-time filter.
	for i := 0; i < 3; i++ {
		run := start(fmt.Sprintf("unprovable-%d", i))
		fx.Exec(t, "UPDATE agent_task_queue SET status='dispatched',dispatched_at=now() WHERE workflow_step_instance_id IN (SELECT id FROM workflow_step_instance WHERE run_id=$1)", run.ID)
		cancel(run)
		maintain(run)
	}
	offline := start("offline-backoff")
	debugClaimFixture(t, env, offline)
	cancel(offline)
	env.engine.SendDebugStop = func(context.Context, db.AgentTaskQueue, db.WorkflowDebugTaskExecution) error {
		return errors.New("controlled offline runtime")
	}
	maintain(offline)
	fx.Exec(t, "UPDATE workflow_debug_stop_request SET next_attempt_at=now()+interval '1 hour' WHERE run_id=$1", offline.ID)
	later := start("later-stop")
	debugClaimFixture(t, env, later)
	cancel(later)
	purge := start("later-purge")
	cancel(purge)
	maintain(purge)
	fx.Exec(t, "UPDATE workflow_run SET debug_purge_after=now()-interval '1 second' WHERE id=$1", purge.ID)
	sent := map[string]int{}
	env.engine.SendDebugStop = func(_ context.Context, _ db.AgentTaskQueue, x db.WorkflowDebugTaskExecution) error {
		sent[uuidString(x.RunID)]++
		return nil
	}
	r := NewReconciler(env.engine, env.q)
	r.BatchSize = 2
	r.StaleAfter = 0
	for i := 0; i < 4; i++ {
		if err := r.Sweep(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if sent[uuidString(later.ID)] == 0 || sent[uuidString(offline.ID)] != 0 {
		t.Fatalf("stop fairness/backoff violated: %v", sent)
	}
	if !env.reloadRun(t, purge.ID).PurgeCompletedAt.Valid {
		t.Fatal("later cleanup starved behind unprovable runs")
	}
	for _, run := range retained {
		if env.reloadRun(t, run.ID).DetailsPurgedAt.Valid {
			t.Fatal("retained payload purged early")
		}
	}
	// Once backoff expires, wrapping the cursor must revisit the earlier run.
	fx.Exec(t, "UPDATE workflow_debug_stop_request SET next_attempt_at=now()-interval '1 second' WHERE run_id=$1", offline.ID)
	for i := 0; i < 4; i++ {
		if err := r.Sweep(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if sent[uuidString(offline.ID)] == 0 {
		t.Fatal("expired retry was never revisited after cursor wrap")
	}
}

func TestDebugMaintenanceObjectBackoffDoesNotHideLaterCleanup(t *testing.T) {
	env, in := setupDebugTest(t)
	ctx := context.Background()
	fx := testutil.New(env.pool, uuidString(env.workspaceID), uuidString(env.userID))
	var runs []db.WorkflowRun
	deletes := map[string]int{}
	env.engine.DeleteDebugObject = func(_ context.Context, o db.WorkflowDebugCleanupObject) error {
		deletes[uuidString(o.RunID)]++
		return errors.New("controlled deletion outage")
	}
	for i := 0; i < 5; i++ {
		in.IdempotencyKey = fmt.Sprintf("object-backoff-%d", i)
		result, err := env.engine.StartDraftTest(ctx, env.workspaceID, env.userID, env.templateID, in)
		if err != nil {
			t.Fatal(err)
		}
		run := result.Run
		runs = append(runs, run)
		tasks, err := env.q.ListWorkflowDebugTasksForRun(ctx, db.ListWorkflowDebugTasksForRunParams{RunID: run.ID, WorkspaceID: env.workspaceID})
		if err != nil || len(tasks) != 1 {
			t.Fatalf("tasks: %v", err)
		}
		fx.Insert(t, "attachment", testutil.Cols{"workspace_id": env.workspaceID, "task_id": tasks[0].ID, "uploader_type": "member", "uploader_id": env.userID, "filename": "artifact.txt", "url": "https://controlled.invalid/artifact", "content_type": "text/plain", "size_bytes": 1})
		if _, err = env.engine.CancelRun(ctx, env.workspaceID, run.ID, env.userID); err != nil {
			t.Fatal(err)
		}
		fx.Exec(t, "UPDATE workflow_run SET debug_purge_after=now()-interval '1 second' WHERE id=$1", run.ID)
		if i < 3 {
			if err = env.engine.PurgeDebugRun(ctx, env.workspaceID, run.ID); err != nil {
				t.Fatal(err)
			}
			fx.Exec(t, "UPDATE workflow_debug_cleanup_object SET next_attempt_at=now()+interval '1 hour' WHERE run_id=$1", run.ID)
		}
	}
	env.engine.DeleteDebugObject = func(ctx context.Context, o db.WorkflowDebugCleanupObject) error {
		deletes[uuidString(o.RunID)]++
		_, err := env.q.DeleteAttachment(ctx, db.DeleteAttachmentParams{ID: o.ObjectID, WorkspaceID: o.WorkspaceID})
		return err
	}
	r := NewReconciler(env.engine, env.q)
	r.BatchSize = 1
	r.StaleAfter = 0
	for i := 0; i < 3; i++ {
		if err := r.Sweep(ctx); err != nil {
			t.Fatal(err)
		}
	}
	for i, run := range runs {
		final := env.reloadRun(t, run.ID)
		if final.PurgeCompletedAt.Valid != (i >= 3) || deletes[uuidString(run.ID)] != 1 {
			t.Fatalf("run %d backoff/fairness: purged=%v deletes=%d", i, final.PurgeCompletedAt.Valid, deletes[uuidString(run.ID)])
		}
		if i < 3 {
			fx.Exec(t, "UPDATE workflow_debug_cleanup_object SET next_attempt_at=now()-interval '1 second' WHERE run_id=$1", run.ID)
		}
	}
	for i := 0; i < 4; i++ {
		if err := r.Sweep(ctx); err != nil {
			t.Fatal(err)
		}
	}
	for i, run := range runs {
		if !env.reloadRun(t, run.ID).PurgeCompletedAt.Valid {
			t.Fatalf("run %d never recovered after backoff expired", i)
		}
	}
}
