package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Each schema executes all seven node kinds against a draft snapshot while the
// template is replaced. New Engine instances read persisted state between steps;
// the fixture router is controlled, not a model or a production runtime.
func TestDebugSevenNodeSnapshotRecovery(t *testing.T) {
	for _, schema := range []int{1, 2} {
		for _, accept := range []bool{true, false} {
			t.Run(fmt.Sprintf("v%d/accept=%v", schema, accept), func(t *testing.T) {
				env, in := setupDebugTest(t)
				d := &Definition{SchemaVersion: schema, EntryNode: "input", Nodes: []Node{
					{Key: "input", Type: NodeTypeInput, Next: []string{"plan"}},
					{Key: "plan", Type: NodeTypeAgent, Next: []string{"route"}, Routing: &Routing{Strategy: RoutingCapability, Capability: "work"}, SubmissionSchema: "code_change"},
					{Key: "route", Type: NodeTypeCondition, Branches: []Branch{{WhenVerdict: "pass", Target: "spread"}, {Target: "end"}}},
					{Key: "spread", Type: NodeTypeFanOut, Next: []string{"worker"}, FanOutMax: 2},
					{Key: "worker", Type: NodeTypeAgent, Next: []string{"gather"}, Routing: &Routing{Strategy: RoutingCapability, Capability: "work"}, SubmissionSchema: "code_change"},
					{Key: "gather", Type: NodeTypeJoin, Next: []string{"review"}, JoinSources: []string{"worker"}, JoinPolicy: JoinPolicyFailFast},
					{Key: "review", Type: NodeTypeAcceptance, Next: []string{"end"}, AcceptanceCriteria: []string{"snapshot B verified"}, ReworkTargets: []string{"plan"}},
					{Key: "end", Type: NodeTypeEnd},
				}}
				if schema == 2 {
					for i := range d.Nodes {
						n := &d.Nodes[i]
						n.ReworkTargets = nil
						for j, target := range n.Next {
							n.NextIDs = append(n.NextIDs, fmt.Sprintf("%s-%s-%d", n.Key, target, j))
						}
						for j := range n.Branches {
							n.Branches[j].ID = fmt.Sprintf("branch-%d", j)
						}
					}
				}
				if err := Validate(d, DefaultWorkspacePolicy, DefaultSchemaRegistry); err != nil {
					t.Fatal(err)
				}
				in.Definition = mustJSON(d)
				ctx := context.Background()
				result, err := env.engine.StartDraftTest(ctx, env.workspaceID, env.userID, env.templateID, in)
				if err != nil {
					t.Fatal(err)
				}
				run := result.Run
				fx := testutil.New(env.pool, uuidString(env.workspaceID), uuidString(env.userID))
				fx.Exec(t, "UPDATE workflow_template_version SET definition=$1 WHERE template_id=$2", mustJSON(&Definition{SchemaVersion: schema, EntryNode: "end", Nodes: []Node{{Key: "end", Type: NodeTypeEnd}}}), env.templateID)
				restart := func() {
					old := env.engine
					env.engine = &Engine{Queries: db.New(env.pool), TxStarter: env.pool, Router: old.Router, Schemas: DefaultSchemaRegistry, DebugReady: false, RevalidateDebugEnvironment: old.RevalidateDebugEnvironment}
				}
				submit := func(step db.WorkflowStepInstance, refs ...string) {
					t.Helper()
					restart()
					if _, err := env.engine.SubmitResult(ctx, SubmitResultInput{WorkspaceID: env.workspaceID, StepID: step.ID, RawOutput: submissionPayload(VerdictPass, "snapshot B", refs...), ActorType: "agent"}); err != nil {
						t.Fatal(err)
					}
				}
				for round := 0; round < 2; round++ {
					submit(env.stepByNode(t, run.ID, "plan"), "a", "b")
					rows, err := env.q.ListWorkflowStepInstances(ctx, db.ListWorkflowStepInstancesParams{RunID: run.ID, WorkspaceID: env.workspaceID})
					if err != nil {
						t.Fatal(err)
					}
					count := 0
					for _, step := range rows {
						if step.NodeKey == "worker" && step.Status == "queued" {
							submit(step)
							count++
						}
					}
					expected := 2
					if schema == 2 {
						expected = 1
					}
					if count != expected {
						t.Fatalf("workers %d want %d", count, expected)
					}
					if env.reloadRun(t, run.ID).Status != "waiting_acceptance" {
						t.Fatal("snapshot did not reach acceptance")
					}
					gate := env.stepByNode(t, run.ID, "review")
					pending, err := env.q.GetPendingWorkflowAcceptanceForStep(ctx, db.GetPendingWorkflowAcceptanceForStepParams{StepID: gate.ID, WorkspaceID: env.workspaceID})
					if err != nil {
						t.Fatal(err)
					}
					restart()
					decision := DecideAcceptanceInput{WorkspaceID: env.workspaceID, AcceptanceID: pending.ID, Accept: accept || round == 1, Reason: "review snapshot B", ReviewerUserID: env.userID}
					if schema == 1 && !decision.Accept {
						decision.ReworkTarget = "plan"
					}
					if _, err = env.engine.DecideAcceptance(ctx, decision); err != nil {
						t.Fatal(err)
					}
					if accept || schema == 2 || round == 1 {
						break
					}
				}
				final := env.reloadRun(t, run.ID)
				want := "completed"
				if schema == 2 && !accept {
					want = "failed"
				}
				if final.Status != want {
					t.Fatalf("status %s want %s", final.Status, want)
				}
				snapshot, err := ResolveRunDefinition(ctx, env.q, env.workspaceID, final)
				if err != nil || snapshot.EntryNode != "input" {
					t.Fatalf("snapshot changed %v", err)
				}
				if final.TemplateVersionID.Valid || final.IssueID.Valid || final.InputInstanceID.Valid {
					t.Fatal("trial acquired published/business identity")
				}
				for _, table := range []string{"issue", "workflow_callback_delivery", "workflow_input_instance"} {
					var n int
					if err := env.pool.QueryRow(ctx, "SELECT count(*) FROM "+table+" WHERE workspace_id=$1", env.workspaceID).Scan(&n); err != nil || n != 0 {
						t.Fatalf("%s side effects %d: %v", table, n, err)
					}
				}
				var kinds int
				if err := env.pool.QueryRow(ctx, "SELECT count(DISTINCT node_type) FROM workflow_step_instance WHERE run_id=$1", run.ID).Scan(&kinds); err != nil || kinds != 7 {
					t.Fatalf("executed kinds %d: %v", kinds, err)
				}
			})
		}
	}
}

// Hold the winner's row lock and start the competing real transaction before
// releasing it. Both orderings are deterministic and do not depend on sleeps.
func TestDebugClaimCancelOverlappingTransactions(t *testing.T) {
	for _, claimFirst := range []bool{true, false} {
		t.Run(fmt.Sprintf("claimFirst=%v", claimFirst), func(t *testing.T) {
			env, in := setupDebugTest(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			result, err := env.engine.StartDraftTest(ctx, env.workspaceID, env.userID, env.templateID, in)
			if err != nil {
				t.Fatal(err)
			}
			task := env.stepByNode(t, result.Run.ID, "analyze").TaskID
			var incarnation pgtype.UUID
			_ = incarnation.Scan(uuid.NewString())
			acquired := make(chan struct{})
			release := make(chan struct{})
			second := make(chan struct{})
			var calls atomic.Int32
			env.engine.TxStarter = debugInterceptStarter{env.pool, func(tx pgx.Tx) pgx.Tx {
				return debugLockBarrierTx{Tx: tx, ctx: ctx, calls: &calls, acquired: acquired, release: release, second: second}
			}}
			var grants atomic.Int32
			claim := func() error {
				_, e := env.engine.ClaimDebugTask(ctx, task, env.runtimeID, incarnation, func(context.Context, *db.Queries, db.AgentTaskQueue) error { grants.Add(1); return nil })
				return e
			}
			stop := func() error { _, e := env.engine.CancelRun(ctx, env.workspaceID, result.Run.ID, env.userID); return e }
			first, last := claim, stop
			if !claimFirst {
				first, last = stop, claim
			}
			firstResult := make(chan error, 1)
			lastResult := make(chan error, 1)
			go func() { firstResult <- first() }()
			select {
			case <-acquired:
			case <-ctx.Done():
				close(release)
				t.Fatal("first lock not acquired")
			}
			go func() { lastResult <- last() }()
			select {
			case <-second:
			case <-ctx.Done():
				close(release)
				t.Fatal("competitor never attempted lock")
			}
			close(release)
			a, b := <-firstResult, <-lastResult
			if a != nil || (claimFirst && b != nil) || (!claimFirst && b == nil) {
				t.Fatalf("outcomes %v %v", a, b)
			}
			expected := int32(0)
			if claimFirst {
				expected = 1
			}
			if grants.Load() != expected {
				t.Fatal("credential grant escaped cancellation")
			}
			if env.reloadRun(t, result.Run.ID).Status != "cancelled" {
				t.Fatal("cancel lost")
			}
			if err = env.engine.MaintainDebugRun(ctx, env.workspaceID, result.Run.ID); err != nil {
				t.Fatal(err)
			}
			var n int
			_ = env.pool.QueryRow(ctx, "SELECT count(*) FROM workflow_debug_task_execution WHERE run_id=$1", result.Run.ID).Scan(&n)
			if n != int(expected) {
				t.Fatal("incorrect durable claims")
			}
		})
	}
}

type debugLockBarrierTx struct {
	pgx.Tx
	ctx                       context.Context
	calls                     *atomic.Int32
	acquired, release, second chan struct{}
}

func (tx debugLockBarrierTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	if !strings.Contains(sql, "-- name: GetWorkflowRunForUpdate") {
		return tx.Tx.QueryRow(ctx, sql, args...)
	}
	n := tx.calls.Add(1)
	if n == 2 {
		close(tx.second)
	}
	row := tx.Tx.QueryRow(ctx, sql, args...)
	if n != 1 {
		return row
	}
	return debugBarrierRow{Row: row, ctx: tx.ctx, acquired: tx.acquired, release: tx.release}
}

type debugBarrierRow struct {
	pgx.Row
	ctx               context.Context
	acquired, release chan struct{}
}

func (r debugBarrierRow) Scan(dest ...any) error {
	err := r.Row.Scan(dest...)
	if err == nil {
		close(r.acquired)
		select {
		case <-r.release:
		case <-r.ctx.Done():
			return r.ctx.Err()
		}
	}
	return err
}

func TestDebugInvalidAdmissionAndRevisionAreAtomic(t *testing.T) {
	cases := []string{"cycle", "dangling", "missing-input", "image", "template-revision", "policy-revision", "draft-identity"}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			env, in := setupDebugTest(t)
			switch name {
			case "cycle":
				d := linearDefinition()
				d.Nodes[1].Next = []string{"analyze"}
				in.Definition = mustJSON(d)
			case "dangling":
				d := linearDefinition()
				d.Nodes[0].Next = []string{"missing"}
				in.Definition = mustJSON(d)
			case "missing-input":
				in.Input = json.RawMessage(`{"title":"empty description"}`)
			case "image":
				id := uuid.NewString()
				in.ImageAttachmentID = &id
			case "template-revision":
				in.ExpectedRevision++
			case "policy-revision":
				in.ExpectedDebugPolicyRevision++
			case "draft-identity":
				id := uuid.NewString()
				in.BaseDraftVersionID = &id
			}
			if _, err := env.engine.StartDraftTest(context.Background(), env.workspaceID, env.userID, env.templateID, in); err == nil {
				t.Fatal("invalid request admitted")
			}
			for _, table := range []string{"workflow_run", "workflow_execution_snapshot", "workflow_event", "workflow_step_instance"} {
				var n int
				if err := env.pool.QueryRow(context.Background(), "SELECT count(*) FROM "+table+" WHERE workspace_id=$1", env.workspaceID).Scan(&n); err != nil || n != 0 {
					t.Fatalf("%s writes %d: %v", table, n, err)
				}
			}
			quota, err := env.q.GetWorkflowDebugQuota(context.Background(), env.workspaceID)
			if err != nil || quota.PayloadBytes != 0 {
				t.Fatal("rejected request consumed bytes", err)
			}
		})
	}
}

func TestDebugDeadlineConcurrentAdvance(t *testing.T) {
	for _, acceptance := range []bool{true, false} {
		t.Run(fmt.Sprintf("acceptance=%v", acceptance), func(t *testing.T) {
			env, in := setupDebugTest(t)
			if acceptance {
				in.Definition = mustJSON(&Definition{SchemaVersion: 2, EntryNode: "gate", Nodes: []Node{{Key: "gate", Type: NodeTypeAcceptance, Next: []string{"end"}, NextIDs: []string{"ge"}, AcceptanceCriteria: []string{"ok"}}, {Key: "end", Type: NodeTypeEnd}}})
			}
			ctx := context.Background()
			result, err := env.engine.StartDraftTest(ctx, env.workspaceID, env.userID, env.templateID, in)
			if err != nil {
				t.Fatal(err)
			}
			advance := func() error {
				s := env.stepByNode(t, result.Run.ID, "analyze")
				_, e := env.engine.SubmitResult(ctx, SubmitResultInput{WorkspaceID: env.workspaceID, StepID: s.ID, RawOutput: submissionPayload(VerdictPass, "too late"), ActorType: "agent"})
				return e
			}
			if acceptance {
				s := env.stepByNode(t, result.Run.ID, "gate")
				p, e := env.q.GetPendingWorkflowAcceptanceForStep(ctx, db.GetPendingWorkflowAcceptanceForStepParams{StepID: s.ID, WorkspaceID: env.workspaceID})
				if e != nil {
					t.Fatal(e)
				}
				advance = func() error {
					_, e := env.engine.DecideAcceptance(ctx, DecideAcceptanceInput{WorkspaceID: env.workspaceID, AcceptanceID: p.ID, Accept: true, ReviewerUserID: env.userID})
					return e
				}
			}
			fx := testutil.New(env.pool, uuidString(env.workspaceID), uuidString(env.userID))
			fx.Exec(t, "UPDATE workflow_run SET debug_deadline_at=now()-interval '1 second' WHERE id=$1", result.Run.ID)
			start := make(chan struct{})
			var wg sync.WaitGroup
			errs := make([]error, 3)
			commands := []func() error{advance, func() error { _, e := env.engine.CancelRun(ctx, env.workspaceID, result.Run.ID, env.userID); return e }, func() error { return env.engine.MaintainDebugRun(ctx, env.workspaceID, result.Run.ID) }}
			for i, f := range commands {
				wg.Add(1)
				go func() { defer wg.Done(); <-start; errs[i] = f() }()
			}
			close(start)
			wg.Wait()
			final := env.reloadRun(t, result.Run.ID)
			if !IsTerminalRunStatus(RunStatus(final.Status)) || final.Status == "completed" {
				t.Fatalf("late advance won: %s %v", final.Status, errs)
			}
			var n int
			_ = env.pool.QueryRow(ctx, "SELECT count(*) FROM workflow_step_instance WHERE run_id=$1 AND node_key IN ('end','implement')", result.Run.ID).Scan(&n)
			if n != 0 {
				t.Fatal("late downstream activation")
			}
		})
	}
}

func TestDebugDistinctStartsCompeteForEachQuota(t *testing.T) {
	for _, dimension := range []string{"user", "workspace", "hourly", "storage"} {
		t.Run(dimension, func(t *testing.T) {
			env, in := setupDebugTest(t)
			ctx := context.Background()
			fx := testutil.New(env.pool, uuidString(env.workspaceID), uuidString(env.userID))
			fx.Exec(t, "UPDATE workflow_debug_policy SET user_active_runs=20,workspace_active_runs=20,user_starts_per_hour=20 WHERE workspace_id=$1", env.workspaceID)
			users := []pgtype.UUID{env.userID, env.userID}
			code := ""
			switch dimension {
			case "user":
				fx.Exec(t, "UPDATE workflow_debug_policy SET user_active_runs=1 WHERE workspace_id=$1", env.workspaceID)
				code = "debug_user_active_limit"
			case "workspace":
				fx.Exec(t, "UPDATE workflow_debug_policy SET user_active_runs=1,workspace_active_runs=1 WHERE workspace_id=$1", env.workspaceID)
				user := fx.Insert(t, "user", testutil.Cols{"name": "Other owner", "email": uuid.NewString() + "@example.test"})
				_ = users[1].Scan(user)
				fx.Insert(t, "member", testutil.Cols{"workspace_id": env.workspaceID, "user_id": users[1], "role": "admin"})
				code = "debug_workspace_active_limit"
			case "hourly":
				fx.Exec(t, "UPDATE workflow_debug_policy SET user_starts_per_hour=1 WHERE workspace_id=$1", env.workspaceID)
				in.Definition = mustJSON(&Definition{SchemaVersion: 2, EntryNode: "end", Nodes: []Node{{Key: "end", Type: NodeTypeEnd}}})
				code = "debug_hourly_limit"
			case "storage":
				fx.Exec(t, "UPDATE workflow_debug_policy SET payload_capacity_bytes=1048576 WHERE workspace_id=$1", env.workspaceID)
				code = "debug_storage_limit"
			}
			def, _ := canonicalJSON(in.Definition)
			input, _ := canonicalJSON(in.Input)
			environment, _ := canonicalJSON([]byte(`{"resources":[],"project_id":null}`))
			size := int64(len(def) + len(input) + len(environment))
			initial := int64(0)
			if dimension == "storage" {
				initial = 1048576 - size
				fx.Exec(t, "UPDATE workflow_debug_quota SET payload_bytes=$2 WHERE workspace_id=$1", env.workspaceID, initial)
			}
			var wg sync.WaitGroup
			start := make(chan struct{})
			errs := make([]error, 2)
			results := make([]*StartRunResult, 2)
			for i := range 2 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					req := in
					req.IdempotencyKey = fmt.Sprintf("different-%d", i)
					results[i], errs[i] = env.engine.StartDraftTest(ctx, env.workspaceID, users[i], env.templateID, req)
				}()
			}
			close(start)
			wg.Wait()
			success := 0
			denied := 0
			for i, err := range errs {
				if err == nil {
					success++
					if results[i].Run.DebugPayloadBytes.Int64 != size {
						t.Fatal("stored byte count wrong")
					}
					continue
				}
				var quota *DebugQuotaError
				if !errors.As(err, &quota) || len(quota.Dimensions) != 1 || quota.Dimensions[0].Code != code {
					t.Fatalf("wrong denial: %v %+v", err, quota)
				}
				if dimension == "hourly" && quota.RetryAfterSeconds <= 0 {
					t.Fatal("missing retry-after")
				}
				denied++
			}
			if success != 1 || denied != 1 {
				t.Fatalf("success %d denied %d", success, denied)
			}
			quota, err := env.q.GetWorkflowDebugQuota(ctx, env.workspaceID)
			if err != nil || quota.PayloadBytes != initial+size {
				t.Fatal("losing transaction consumed capacity", err)
			}
		})
	}
}

func TestDebugReconcilerRecoversLostTerminalDelivery(t *testing.T) {
	env, in := setupDebugTest(t)
	in.Definition = mustJSON(acceptanceDefinition())
	ctx := context.Background()
	result, err := env.engine.StartDraftTest(ctx, env.workspaceID, env.userID, env.templateID, in)
	if err != nil {
		t.Fatal(err)
	}
	task := env.stepByNode(t, result.Run.ID, "implement").TaskID
	fx := testutil.New(env.pool, uuidString(env.workspaceID), uuidString(env.userID))
	// Model the durable task commit with its notification lost before engine delivery.
	fx.Exec(t, "UPDATE agent_task_queue SET status='completed',completed_at=now(),result=$2 WHERE id=$1", task, mustJSON(map[string]string{"output": submissionPayload(VerdictPass, "durable result")}))
	env.engine.DebugReady = false
	r := NewReconciler(env.engine, env.q)
	r.StaleAfter = 0
	for i := 0; i < 2; i++ {
		if err = r.Sweep(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if env.reloadRun(t, result.Run.ID).Status != "waiting_acceptance" {
		t.Fatal("lost terminal delivery not recovered")
	}
	var n int
	if err = env.pool.QueryRow(ctx, "SELECT count(*) FROM workflow_acceptance WHERE run_id=$1", result.Run.ID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("replayed acceptance %d %v", n, err)
	}
}
