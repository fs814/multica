package workflow

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type debugCleanupFaultTx struct {
	pgx.Tx
	fail *atomic.Bool
}

func (tx debugCleanupFaultTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if strings.Contains(sql, "-- name: AddWorkflowDebugPayloadBytes") && tx.fail.CompareAndSwap(true, false) {
		return pgconn.CommandTag{}, errors.New("controlled cleanup transaction failure")
	}
	return tx.Tx.Exec(ctx, sql, args...)
}
func TestDebugCleanupRollbackAndObjectDeleteRecovery(t *testing.T) {
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
	fx.Exec(t, "UPDATE agent_task_queue SET status='cancelled',completed_at=now(),context=$2,result=$2,error='payload-marker',session_id='payload-marker',work_dir='payload-marker',durable_work_dir='payload-marker',branch_name='payload-marker',retired_session_id='payload-marker',trigger_summary='payload-marker',wait_reason='payload-marker',cancelled_by_name='payload-marker',handoff_note='payload-marker',runtime_mcp_overlay=$2,runtime_connected_apps=$2 WHERE id=$1", task.ID, []byte(`{"payload-marker":"value"}`))
	fx.Exec(t, "UPDATE workflow_step_instance SET input=$2,output=$2,routing_reason='payload-marker',failure_detail='payload-marker',expansion_key='payload-marker' WHERE id=$1", task.WorkflowStepInstanceID, []byte(`{"payload-marker":"value"}`))
	fx.Exec(t, "UPDATE workflow_run SET debug_purge_after=now()-interval '1 second',context=$2,input=$2,failure_detail='payload-marker' WHERE id=$1", run.ID, []byte(`{"payload-marker":"value"}`))
	attachment := fx.Insert(t, "attachment", testutil.Cols{"workspace_id": env.workspaceID, "task_id": task.ID, "uploader_type": "member", "uploader_id": env.userID, "filename": "payload-marker.txt", "url": "https://controlled.invalid/payload-marker", "content_type": "text/plain", "size_bytes": 1})
	var attachmentID pgtype.UUID
	_ = attachmentID.Scan(attachment)
	pending := fx.Insert(t, "workflow_debug_upload", testutil.Cols{"workspace_id": env.workspaceID, "run_id": run.ID, "claim_id": claim, "task_id": task.ID})
	receipt := DebugExecutionReceipt{SchemaVersion: "1", ReceiptID: uuid.NewString(), Kind: "cancel_ack", ProcessStopped: true, DeliveryDrained: true, ProcessStoppedAt: time.Now()}
	if err = env.engine.AcceptDebugExecutionReceipt(ctx, task.ID, claim, testDebugAuth(env.runtimeID), receipt); err == nil {
		t.Fatal("receipt ignored pending upload")
	}
	fx.Exec(t, "UPDATE workflow_debug_upload SET state='finalized',settled_at=now(),attachment_id=$2 WHERE id=$1", pending, attachment)
	if err = env.engine.AcceptDebugExecutionReceipt(ctx, task.ID, claim, testDebugAuth(env.runtimeID), receipt); err != nil {
		t.Fatal(err)
	}
	var fail atomic.Bool
	fail.Store(true)
	env.engine.TxStarter = debugInterceptStarter{env.pool, func(tx pgx.Tx) pgx.Tx { return debugCleanupFaultTx{tx, &fail} }}
	if err = env.engine.PurgeDebugRun(ctx, env.workspaceID, run.ID); err == nil {
		t.Fatal("injected transaction failure did not fire")
	}
	retained := env.reloadRun(t, run.ID)
	quota, err := env.q.GetWorkflowDebugQuota(ctx, env.workspaceID)
	if err != nil || retained.DetailsPurgedAt.Valid || quota.PayloadBytes != run.DebugPayloadBytes.Int64 || !strings.Contains(string(retained.Input), "payload-marker") {
		t.Fatalf("cleanup did not roll back atomically: %v", err)
	}
	var deletes int
	env.engine.DeleteDebugObject = func(ctx context.Context, object db.WorkflowDebugCleanupObject) error {
		deletes++
		if deletes == 1 {
			return errors.New("controlled object store unavailable")
		}
		_, err := env.q.DeleteAttachment(ctx, db.DeleteAttachmentParams{ID: object.ObjectID, WorkspaceID: object.WorkspaceID})
		return err
	}
	if err = env.engine.PurgeDebugRun(ctx, env.workspaceID, run.ID); err != nil {
		t.Fatal(err)
	}
	purging := env.reloadRun(t, run.ID)
	if !purging.DetailsPurgedAt.Valid || purging.PurgeCompletedAt.Valid {
		t.Fatal("object failure was reported as complete")
	}
	if _, err = env.q.GetAttachment(ctx, db.GetAttachmentParams{ID: attachmentID, WorkspaceID: env.workspaceID}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("public attachment query exposed purged payload: %v", err)
	}
	taskAfter, err := env.q.GetAgentTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	raw := mustJSON(taskAfter)
	if strings.Contains(string(raw), "payload-marker") {
		t.Fatal("task retained a payload field")
	}
	fx.Exec(t, "UPDATE workflow_debug_cleanup_object SET next_attempt_at=now() WHERE run_id=$1", run.ID)
	for i := 0; i < 2; i++ {
		if err = env.engine.PurgeDebugRun(ctx, env.workspaceID, run.ID); err != nil {
			t.Fatal(err)
		}
	}
	final := env.reloadRun(t, run.ID)
	quota, err = env.q.GetWorkflowDebugQuota(ctx, env.workspaceID)
	if err != nil || !final.PurgeCompletedAt.Valid || quota.PayloadBytes != 0 || deletes != 2 {
		t.Fatalf("recovery double counted or forgot object: %v deletes=%d", err, deletes)
	}
	if err = env.engine.WithDebugTaskExecution(ctx, task.ID, claim, testDebugAuth(env.runtimeID), func(_ context.Context, _ *db.Queries, id DebugTaskIdentity) error {
		if DebugPayloadDisposition(id, false) != "ignored_expired" {
			t.Fatal("late payload was not ignored")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
