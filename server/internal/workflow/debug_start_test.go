package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"sync"
	"testing"
)

func setupDebugTest(t *testing.T) (*testEnv, DraftTestRequest) {
	t.Helper()
	env := setupTestEnv(t)
	env.publishTemplate(t, linearDefinition())
	fx := testutil.New(env.pool, uuidString(env.workspaceID), uuidString(env.userID))
	fx.Insert(t, "member", testutil.Cols{"workspace_id": env.workspaceID, "user_id": env.userID, "role": "owner"})
	fx.InsertNoID(t, "workflow_debug_quota", testutil.Cols{"workspace_id": env.workspaceID}, "workspace_id=$1", env.workspaceID)
	fx.InsertNoID(t, "workflow_debug_policy", testutil.Cols{"workspace_id": env.workspaceID, "enabled": true, "user_active_runs": 1, "workspace_active_runs": 1}, "workspace_id=$1", env.workspaceID)
	for _, table := range []string{"workflow_execution_snapshot", "workflow_debug_task_execution", "workflow_debug_stop_request", "workflow_debug_cleanup_object"} {
		fx.Cleanup(t, "DELETE FROM "+table+" WHERE workspace_id=$1", env.workspaceID)
	}
	env.engine.DebugReady = true
	env.engine.RevalidateDebugEnvironment = func(context.Context, *db.Queries, db.WorkflowRun) error { return nil }
	env.engine.ResolveDraftEnvironment = func(context.Context, *db.Queries, pgtype.UUID, pgtype.UUID, *string) (json.RawMessage, error) {
		return json.RawMessage(`{"resources":[],"project_id":null}`), nil
	}
	tpl, err := env.q.GetWorkflowTemplate(context.Background(), db.GetWorkflowTemplateParams{ID: env.templateID, WorkspaceID: env.workspaceID})
	if err != nil {
		t.Fatal(err)
	}
	return env, DraftTestRequest{SchemaVersion: "1", ExpectedRevision: tpl.Revision, ExpectedDebugPolicyRevision: 1, Definition: mustJSON(linearDefinition()), Input: json.RawMessage(`{"title":"trial","description":"controlled execution"}`), IdempotencyKey: "draft-one", ExecutionAcknowledged: true}
}
func TestDebugStartReplayPrecedesAdmission(t *testing.T) {
	env, in := setupDebugTest(t)
	ctx := context.Background()
	first, err := env.engine.StartDraftTest(ctx, env.workspaceID, env.userID, env.templateID, in)
	if err != nil {
		t.Fatal(err)
	}
	if first.Run.TemplateVersionID.Valid || first.Run.ExecutionMode != ExecutionDraftTest || first.Run.IssueID.Valid || first.AlreadyExisted {
		t.Fatalf("wrong source: %+v", first)
	}
	if !first.Run.DebugDeadlineAt.Valid || first.Run.DebugDeadlineAt.Time.Sub(first.Run.CreatedAt.Time).Seconds() != 1800 {
		t.Fatal("unset duration must use trial default")
	}
	fx := testutil.New(env.pool, uuidString(env.workspaceID), uuidString(env.userID))
	fx.Exec(t, "UPDATE workflow_template SET status='archived',revision=revision+1 WHERE id=$1", env.templateID)
	fx.Exec(t, "UPDATE workflow_debug_policy SET enabled=false,revision=revision+1 WHERE workspace_id=$1", env.workspaceID)
	env.engine.DebugReady = false
	replay, err := env.engine.StartDraftTest(ctx, env.workspaceID, env.userID, env.templateID, in)
	if err != nil || !replay.AlreadyExisted || replay.Run.ID != first.Run.ID {
		t.Fatalf("replay after disable/archive: %v %v", replay, err)
	}
	changed := in
	changed.Input = json.RawMessage(`{"title":"different","description":"request"}`)
	if _, err = env.engine.StartDraftTest(ctx, env.workspaceID, env.userID, env.templateID, changed); !IsIdempotencyConflict(err) {
		t.Fatalf("conflict: %v", err)
	}
	published, err := env.q.ListWorkflowRuns(ctx, db.ListWorkflowRunsParams{WorkspaceID: env.workspaceID, LimitCount: 20})
	if err != nil || len(published) != 0 {
		t.Fatalf("trial leaked into published list: %v %v", published, err)
	}
	def, err := ResolveRunDefinition(ctx, env.q, env.workspaceID, first.Run)
	if err != nil || def.EntryNode != "analyze" {
		t.Fatalf("snapshot graph: %v %v", def, err)
	}
}
func TestDebugConcurrentReplayUsesLastQuotaSlotOnce(t *testing.T) {
	env, in := setupDebugTest(t)
	ctx := context.Background()
	// Both calls begin against an empty store. The first materialization consumes
	// the only slot; the second must re-read identity before checking that slot.
	start := make(chan struct{})
	var wg sync.WaitGroup
	results := make([]*StartRunResult, 2)
	errs := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results[i], errs[i] = env.engine.StartDraftTest(ctx, env.workspaceID, env.userID, env.templateID, in)
		}()
	}
	close(start)
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if results[0].Run.ID != results[1].Run.ID || results[0].AlreadyExisted == results[1].AlreadyExisted {
		t.Fatalf("duplicate materialization: %+v", results)
	}
	runs, err := env.q.ListWorkflowDraftTestRuns(ctx, db.ListWorkflowDraftTestRunsParams{WorkspaceID: env.workspaceID, LimitCount: 20})
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs %v %v", runs, err)
	}
	tasks, err := env.q.ListWorkflowDebugTasksForRun(ctx, db.ListWorkflowDebugTasksForRunParams{RunID: runs[0].ID, WorkspaceID: env.workspaceID})
	if err != nil || len(tasks) != 1 {
		t.Fatalf("tasks %v %v", tasks, err)
	}
	in.IdempotencyKey = "another-run"
	_, err = env.engine.StartDraftTest(ctx, env.workspaceID, env.userID, env.templateID, in)
	var quota *DebugQuotaError
	if !errors.As(err, &quota) || len(quota.Dimensions) != 2 {
		t.Fatalf("full quota: %v", err)
	}
}
func TestDebugDurationAndPolicyBoundaries(t *testing.T) {
	p := DefaultDebugPolicy()
	if p.Enabled {
		t.Fatal("default must be off")
	}
	for _, tc := range []struct {
		explicit, want int
		fail           bool
	}{{0, 1800, false}, {900, 900, false}, {3600, 0, true}, {-1, 0, true}} {
		got, err := debugDuration(tc.explicit, p, DefaultWorkspacePolicy)
		if got != tc.want || (err != nil) != tc.fail {
			t.Fatalf("duration %d: %d %v", tc.explicit, got, err)
		}
	}
	for _, change := range []func(*DebugPolicy){func(p *DebugPolicy) { p.UserActiveRuns = 0 }, func(p *DebugPolicy) { p.RetentionSeconds = 7776001 }, func(p *DebugPolicy) { p.UserActiveRuns = 6 }, func(p *DebugPolicy) { p.PayloadCapacityBytes = 1073741825 }} {
		v := p
		change(&v)
		if v.Validate() == nil {
			t.Fatalf("bad policy accepted: %+v", v)
		}
	}
	row := db.WorkflowDebugPolicy{Revision: 1, UserActiveRuns: 2, WorkspaceActiveRuns: 5, UserStartsPerHour: 20, PayloadCapacityBytes: 1048576}
	if err := checkDebugQuota(row, db.GetWorkflowDebugUsageRow{}, 100, 1048476); err != nil {
		t.Fatal("exact capacity must fit", err)
	}
	if err := checkDebugQuota(row, db.GetWorkflowDebugUsageRow{}, 100, 1048477); err == nil {
		t.Fatal("one byte overflow accepted")
	}
}
