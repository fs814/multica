package workflow

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"strings"
	"testing"
	"time"
)

func debugClaimFixture(t *testing.T, env *testEnv, run db.WorkflowRun) (db.AgentTaskQueue, pgtype.UUID) {
	t.Helper()
	ctx := context.Background()
	tasks, err := env.q.ListWorkflowDebugTasksForRun(ctx, db.ListWorkflowDebugTasksForRunParams{RunID: run.ID, WorkspaceID: env.workspaceID})
	if err != nil || len(tasks) != 1 {
		t.Fatalf("tasks: %v %v", tasks, err)
	}
	fx := testutil.New(env.pool, uuidString(env.workspaceID), uuidString(env.userID))
	task := tasks[0]
	claim := fx.Insert(t, "workflow_debug_task_execution", testutil.Cols{"workspace_id": env.workspaceID, "run_id": run.ID, "step_id": task.WorkflowStepInstanceID, "task_id": task.ID, "task_attempt": task.Attempt, "runtime_id": env.runtimeID, "daemon_incarnation_id": uuid.NewString(), "claim_generation": 1})
	var id pgtype.UUID
	if err = id.Scan(claim); err != nil {
		t.Fatal(err)
	}
	fx.Exec(t, "UPDATE agent_task_queue SET status='dispatched',dispatched_at=now() WHERE id=$1", task.ID)
	return task, id
}
func testDebugAuth(runtime pgtype.UUID) func(context.Context, db.WorkflowDebugTaskExecution) error {
	return func(_ context.Context, x db.WorkflowDebugTaskExecution) error {
		if x.RuntimeID != runtime {
			return newEngineError("debug_forbidden", "wrong runtime")
		}
		return nil
	}
}
func TestDebugStopReceiptGatesCleanupAndSealsDelivery(t *testing.T) {
	env, in := setupDebugTest(t)
	ctx := context.Background()
	started, err := env.engine.StartDraftTest(ctx, env.workspaceID, env.userID, env.templateID, in)
	if err != nil {
		t.Fatal(err)
	}
	run := started.Run
	task, claim := debugClaimFixture(t, env, run)
	fx := testutil.New(env.pool, uuidString(env.workspaceID), uuidString(env.userID))
	if _, err = env.engine.CancelRun(ctx, env.workspaceID, run.ID, env.userID); err != nil {
		t.Fatal(err)
	}
	// A server-side terminal status and a null started_at cannot prove no delivery.
	fx.Exec(t, "UPDATE agent_task_queue SET status='cancelled',completed_at=now(),context=$2,result=$2,error='marker-task-error',handoff_note='marker-handoff' WHERE id=$1", task.ID, []byte(`{"marker":"private-marker"}`))
	fx.Exec(t, "UPDATE workflow_run SET debug_purge_after=now()-interval '1 second',context=$2,failure_detail='private-marker' WHERE id=$1", run.ID, []byte(`{"marker":"private-marker"}`))
	fx.Insert(t, "workflow_submission", testutil.Cols{"workspace_id": env.workspaceID, "run_id": run.ID, "step_id": task.WorkflowStepInstanceID, "task_id": task.ID, "verdict": "fail", "artifact": []byte(`{"marker":"private-marker"}`), "rationale": "private-marker", "raw_result": "private-marker", "validation_errors": []byte(`["private-marker"]`)})
	fx.Insert(t, "workflow_acceptance", testutil.Cols{"workspace_id": env.workspaceID, "run_id": run.ID, "step_id": task.WorkflowStepInstanceID, "status": "rejected", "reviewer_user_id": env.userID, "decided_at": time.Now(), "reason": "private-marker", "context": []byte(`{"marker":"private-marker"}`)})
	if err = env.engine.PurgeDebugRun(ctx, env.workspaceID, run.ID); err != nil {
		t.Fatal(err)
	}
	waiting := env.reloadRun(t, run.ID)
	if waiting.DebugCleanupState.String != "waiting_stop" || waiting.DetailsPurgedAt.Valid {
		t.Fatal("cancelled task was treated as physical stop proof")
	}
	receipt := DebugExecutionReceipt{SchemaVersion: "1", ReceiptID: uuid.NewString(), Kind: "cancel_ack", ProcessStopped: true, DeliveryDrained: true, ProcessStoppedAt: time.Now().UTC(), FinalMessageSeq: 1}
	err = env.engine.AcceptDebugExecutionReceipt(ctx, task.ID, claim, testDebugAuth(env.runtimeID), receipt)
	var eng *EngineError
	if !errors.As(err, &eng) || eng.Code != "debug_delivery_incomplete" {
		t.Fatalf("missing final log: %v", err)
	}
	fx.Insert(t, "task_message", testutil.Cols{"task_id": task.ID, "seq": 1, "type": "text", "content": "private-marker"})
	if err = env.engine.AcceptDebugExecutionReceipt(ctx, task.ID, claim, testDebugAuth(env.runtimeID), receipt); err != nil {
		t.Fatal(err)
	}
	if err = env.engine.AcceptDebugExecutionReceipt(ctx, task.ID, claim, testDebugAuth(env.runtimeID), receipt); err != nil {
		t.Fatalf("lost response replay: %v", err)
	}
	receipt.Kind = "fail"
	if err = env.engine.AcceptDebugExecutionReceipt(ctx, task.ID, claim, testDebugAuth(env.runtimeID), receipt); err == nil {
		t.Fatal("receipt overwrite accepted")
	}
	if err = env.engine.WithDebugTaskExecution(ctx, task.ID, claim, testDebugAuth(env.runtimeID), func(_ context.Context, _ *db.Queries, id DebugTaskIdentity) error {
		if DebugPayloadDisposition(id, false) != "ignored_delivery_closed" {
			t.Fatal("receipt did not seal payload")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err = env.engine.PurgeDebugRun(ctx, env.workspaceID, run.ID); err != nil {
			t.Fatal(err)
		}
	}
	purged := env.reloadRun(t, run.ID)
	if !purged.DetailsPurgedAt.Valid || !purged.PurgeCompletedAt.Valid {
		t.Fatal("cleanup failed")
	}
	quota, err := env.q.LockWorkflowDebugQuota(ctx, env.workspaceID)
	if err != nil || quota.PayloadBytes != 0 {
		t.Fatalf("byte release: %v %v", quota, err)
	}
	for _, table := range []string{"workflow_run", "workflow_execution_snapshot", "workflow_step_instance", "workflow_submission", "workflow_acceptance", "workflow_event"} {
		var rows string
		column := "run_id"
		value := run.ID
		if table == "workflow_run" {
			column = "id"
		}
		if table == "workflow_execution_snapshot" {
			column = "id"
			value = run.ExecutionSnapshotID
		}
		err = env.pool.QueryRow(ctx, "SELECT COALESCE(string_agg(row_to_json(x)::text,''),'') FROM "+table+" x WHERE "+column+"=$1", value).Scan(&rows)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(rows, "private-marker") {
			t.Fatalf("payload leaked from %s", table)
		}
	}
	finalTask, err := env.q.GetAgentTask(ctx, task.ID)
	if err != nil || len(finalTask.Result) != 0 || finalTask.Error.Valid || finalTask.HandoffNote.Valid || string(finalTask.Context) != "{}" {
		t.Fatalf("task payload remains: %v", err)
	}
	if _, err = ResolveRunDefinition(ctx, nil, env.workspaceID, purged); err == nil {
		t.Fatal("purged graph accepted")
	}
	if _, err = env.engine.StartDraftTest(ctx, env.workspaceID, env.userID, env.templateID, in); !errors.As(err, &eng) || eng.Code != "debug_details_expired" {
		t.Fatalf("expired key restarted: %v", err)
	}
}
func TestDebugDeadlineCoversBlockedAndNeverDispatched(t *testing.T) {
	env, in := setupDebugTest(t)
	ctx := context.Background()
	started, err := env.engine.StartDraftTest(ctx, env.workspaceID, env.userID, env.templateID, in)
	if err != nil {
		t.Fatal(err)
	}
	fx := testutil.New(env.pool, uuidString(env.workspaceID), uuidString(env.userID))
	fx.Exec(t, "UPDATE workflow_run SET status='blocked',blocked_reason='offline',debug_deadline_at=now()-interval '1 second',updated_at=now() WHERE id=$1", started.Run.ID)
	if err = env.engine.MaintainDebugRun(ctx, env.workspaceID, started.Run.ID); err != nil {
		t.Fatal(err)
	}
	run := env.reloadRun(t, started.Run.ID)
	if run.Status != "failed" || run.FailureReason.String != ReasonDebugDeadlineExceeded || !run.DebugStopRequestedAt.Valid {
		t.Fatalf("deadline: %+v", run)
	}
	tasks, err := env.q.ListWorkflowDebugTasksForRun(ctx, db.ListWorkflowDebugTasksForRunParams{RunID: run.ID, WorkspaceID: env.workspaceID})
	if err != nil || len(tasks) != 1 || !tasks[0].DebugNeverDispatchedAt.Valid {
		t.Fatalf("never-dispatched proof: %v %v", tasks, err)
	}
}
