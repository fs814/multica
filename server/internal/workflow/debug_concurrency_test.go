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
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type debugInterceptStarter struct {
	TxStarter
	wrap func(pgx.Tx) pgx.Tx
}

func (s debugInterceptStarter) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := s.TxStarter.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return s.wrap(tx), nil
}

type debugInterceptTx struct {
	pgx.Tx
	beforeRow   func(string)
	commitError *atomic.Bool
}

func (tx debugInterceptTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	if tx.beforeRow != nil {
		tx.beforeRow(sql)
	}
	return tx.Tx.QueryRow(ctx, sql, args...)
}
func (tx debugInterceptTx) Commit(ctx context.Context) error {
	if tx.commitError != nil && tx.commitError.CompareAndSwap(true, false) {
		_ = tx.Tx.Rollback(ctx)
		return &pgconn.PgError{Code: "40001", Message: "controlled serialization failure"}
	}
	return tx.Tx.Commit(ctx)
}

func TestDebugLockedReplayAfterBothFastMisses(t *testing.T) {
	for _, different := range []bool{false, true} {
		t.Run(map[bool]string{false: "same", true: "different"}[different], func(t *testing.T) {
			env, in := setupDebugTest(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			reached := make(chan struct{}, 2)
			release := make(chan struct{})
			var calls atomic.Int32
			env.engine.TxStarter = debugInterceptStarter{env.pool, func(tx pgx.Tx) pgx.Tx {
				return debugInterceptTx{Tx: tx, beforeRow: func(sql string) {
					if strings.Contains(sql, "-- name: LockWorkflowDebugQuota") && calls.Add(1) <= 2 {
						reached <- struct{}{}
						<-release
					}
				}}
			}}
			inputs := []DraftTestRequest{in, in}
			if different {
				inputs[1].Input = []byte(`{"title":"different","description":"controlled"}`)
			}
			results := make([]*StartRunResult, 2)
			errs := make([]error, 2)
			var wg sync.WaitGroup
			for i := range inputs {
				wg.Add(1)
				go func() {
					defer wg.Done()
					results[i], errs[i] = env.engine.StartDraftTest(ctx, env.workspaceID, env.userID, env.templateID, inputs[i])
				}()
			}
			for i := 0; i < 2; i++ {
				select {
				case <-reached:
				case <-ctx.Done():
					close(release)
					wg.Wait()
					t.Fatal("both requests did not reach quota lock after fast miss")
				}
			}
			close(release)
			wg.Wait()
			if different {
				if !((errs[0] == nil && IsIdempotencyConflict(errs[1])) || (errs[1] == nil && IsIdempotencyConflict(errs[0]))) {
					t.Fatalf("want create plus conflict before quota: %v", errs)
				}
			} else if errs[0] != nil || errs[1] != nil || results[0].Run.ID != results[1].Run.ID || results[0].AlreadyExisted == results[1].AlreadyExisted {
				t.Fatalf("create plus replay: %v %+v", errs, results)
			}
			var count int
			if err := env.pool.QueryRow(ctx, "SELECT count(*) FROM workflow_execution_snapshot WHERE workspace_id=$1", env.workspaceID).Scan(&count); err != nil || count != 1 {
				t.Fatalf("snapshots=%d %v", count, err)
			}
		})
	}
}
func TestDebugSerializationRetryRollsBackMaterialization(t *testing.T) {
	env, in := setupDebugTest(t)
	var fail atomic.Bool
	fail.Store(true)
	var begins atomic.Int32
	env.engine.TxStarter = debugInterceptStarter{env.pool, func(tx pgx.Tx) pgx.Tx { begins.Add(1); return debugInterceptTx{Tx: tx, commitError: &fail} }}
	result, err := env.engine.StartDraftTest(context.Background(), env.workspaceID, env.userID, env.templateID, in)
	if err != nil || result == nil || begins.Load() != 2 {
		t.Fatalf("retry: %v begins=%d", err, begins.Load())
	}
	var count int
	ctx := context.Background()
	for _, table := range []string{"workflow_run", "workflow_execution_snapshot", "workflow_step_instance", "workflow_event"} {
		if err = env.pool.QueryRow(ctx, "SELECT count(*) FROM "+table+" WHERE workspace_id=$1", env.workspaceID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if table != "workflow_event" && count != 1 {
			t.Fatalf("%s duplicated: %d", table, count)
		}
	}
	quota, err := env.q.GetWorkflowDebugQuota(ctx, env.workspaceID)
	if err != nil || quota.PayloadBytes != result.Run.DebugPayloadBytes.Int64 {
		t.Fatalf("quota duplicated: %v", err)
	}
}
func TestDebugClaimCancelSerializationAndRecovery(t *testing.T) {
	for _, claimFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel-before-claim", true: "claim-before-cancel"}[claimFirst], func(t *testing.T) {
			env, in := setupDebugTest(t)
			ctx := context.Background()
			result, err := env.engine.StartDraftTest(ctx, env.workspaceID, env.userID, env.templateID, in)
			if err != nil {
				t.Fatal(err)
			}
			tasks, err := env.q.ListWorkflowDebugTasksForRun(ctx, db.ListWorkflowDebugTasksForRunParams{RunID: result.Run.ID, WorkspaceID: env.workspaceID})
			if err != nil || len(tasks) != 1 {
				t.Fatalf("tasks %v", err)
			}
			task := tasks[0]
			var incarnation pgtype.UUID
			_ = incarnation.Scan(uuid.NewString())
			var granted atomic.Int32
			grant := func(context.Context, *db.Queries, db.AgentTaskQueue) error { granted.Add(1); return nil }
			var claim db.WorkflowDebugTaskExecution
			if claimFirst {
				claim, err = env.engine.ClaimDebugTask(ctx, task.ID, env.runtimeID, incarnation, grant)
				if err != nil {
					t.Fatal(err)
				}
			}
			if _, err = env.engine.CancelRun(ctx, env.workspaceID, result.Run.ID, env.userID); err != nil {
				t.Fatal(err)
			}
			if !claimFirst {
				_, err = env.engine.ClaimDebugTask(ctx, task.ID, env.runtimeID, incarnation, grant)
				if err == nil || granted.Load() != 0 {
					t.Fatalf("claim after cancel granted credentials: %v", err)
				}
			}
			var sends atomic.Int32
			env.engine.SendDebugStop = func(context.Context, db.AgentTaskQueue, db.WorkflowDebugTaskExecution) error {
				sends.Add(1)
				return errors.New("controlled offline runtime")
			}
			fx := testutil.New(env.pool, uuidString(env.workspaceID), uuidString(env.userID))
			for i := 0; i < 2; i++ {
				if err = env.engine.MaintainDebugRun(ctx, env.workspaceID, result.Run.ID); err != nil {
					t.Fatal(err)
				}
				fx.Exec(t, "UPDATE workflow_debug_stop_request SET next_attempt_at=now() WHERE run_id=$1", result.Run.ID)
			}
			reloaded := env.reloadRun(t, result.Run.ID)
			if claimFirst {
				if sends.Load() != 2 || reloaded.DebugCleanupState.String != "waiting_stop" {
					t.Fatalf("stop retry lost: %d %s", sends.Load(), reloaded.DebugCleanupState.String)
				}
				fx.Exec(t, "UPDATE agent_task_queue SET status='cancelled',completed_at=now() WHERE id=$1", task.ID)
				receipt := DebugExecutionReceipt{SchemaVersion: "1", ReceiptID: uuid.NewString(), Kind: "cancel_ack", ProcessStopped: true, DeliveryDrained: true, ProcessStoppedAt: time.Now()}
				if err = env.engine.AcceptDebugExecutionReceipt(ctx, task.ID, claim.ID, testDebugAuth(env.runtimeID), receipt); err != nil {
					t.Fatal(err)
				}
			} else {
				reloadedTask, err := env.q.GetAgentTask(ctx, task.ID)
				if err != nil || !reloadedTask.DebugNeverDispatchedAt.Valid || sends.Load() != 0 {
					t.Fatalf("never-dispatched proof missing: %v", err)
				}
			}
		})
	}
}
