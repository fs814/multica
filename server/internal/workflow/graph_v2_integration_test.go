package workflow

import (
	"context"
	"encoding/json"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"sync"
	"testing"
)

func submitGraphV2(t *testing.T, env *testEnv, step db.WorkflowStepInstance, verdict Verdict, summary string) {
	t.Helper()
	_, err := env.engine.SubmitResult(context.Background(), SubmitResultInput{WorkspaceID: env.workspaceID, StepID: step.ID, RawOutput: submissionPayload(verdict, summary), ActorType: "agent"})
	if err != nil {
		t.Fatal(err)
	}
}
func TestGraphV2EngineParallelJoinReplayAndVersionIsolation(t *testing.T) {
	env := setupTestEnv(t)
	d := graphV2Fixture()
	env.publishTemplate(t, d)
	ctx := context.Background()
	run := env.startRun(t, "v2-parallel")
	a, b := env.stepByNode(t, run.ID, "a"), env.stepByNode(t, run.ID, "b")
	if a.Status != "queued" || b.Status != "queued" {
		t.Fatal("fanout did not queue both branches")
	}
	// Publish a different graph while the original run is active.
	next := &Definition{SchemaVersion: 2, EntryNode: "end", Nodes: []Node{{Key: "end", Type: NodeTypeEnd}}}
	raw, _ := MarshalDefinition(next)
	version, err := env.q.CreateWorkflowTemplateVersion(ctx, db.CreateWorkflowTemplateVersionParams{WorkspaceID: env.workspaceID, TemplateID: env.templateID, Definition: raw, SchemaVersion: 2})
	if err != nil {
		t.Fatal(err)
	}
	pub, err := env.q.PublishWorkflowTemplateVersion(ctx, db.PublishWorkflowTemplateVersionParams{ID: version.ID, WorkspaceID: env.workspaceID, PublishedByType: "member", PublishedByID: env.userID})
	if err != nil {
		t.Fatal(err)
	}
	_, err = env.q.SetWorkflowTemplateCurrentVersion(ctx, db.SetWorkflowTemplateCurrentVersionParams{ID: env.templateID, WorkspaceID: env.workspaceID, CurrentVersion: pub.Version})
	if err != nil {
		t.Fatal(err)
	}
	submitGraphV2(t, env, a, VerdictPass, "A")
	if env.reloadRun(t, run.ID).Status != "running" {
		t.Fatal("completed before second branch")
	}
	submitGraphV2(t, env, b, VerdictPass, "B")
	joined := env.stepByNode(t, run.ID, "join")
	output := graphOutput(joined.Output)
	if output["first"] != "A" || output["second"] != "B" {
		t.Fatalf("wrong bound output: %s", joined.Output)
	}
	if env.reloadRun(t, run.ID).TemplateVersionID != run.TemplateVersionID {
		t.Fatal("active run changed version")
	}
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- env.engine.RecordTaskTerminal(ctx, RecordTaskTerminalInput{TaskID: b.TaskID, TaskStatus: "completed", Result: submissionPayload(VerdictPass, "B")})
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	rows, err := env.q.ListWorkflowStepInstances(ctx, db.ListWorkflowStepInstancesParams{RunID: run.ID, WorkspaceID: env.workspaceID})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 5 {
		t.Fatalf("duplicate activation: %d", len(rows))
	}
	if env.reloadRun(t, run.ID).Status != "completed" {
		t.Fatal("join did not finish run")
	}
	later := env.startRun(t, "v2-later")
	if later.TemplateVersionID != pub.ID {
		t.Fatal("new run did not pin current version")
	}
}
func TestGraphV2EngineFailureKeepsIndependentBranch(t *testing.T) {
	env := setupTestEnv(t)
	env.publishTemplate(t, graphV2Fixture())
	run := env.startRun(t, "v2-fail")
	a, b := env.stepByNode(t, run.ID, "a"), env.stepByNode(t, run.ID, "b")
	submitGraphV2(t, env, a, VerdictFail, "failed")
	if env.reloadRun(t, run.ID).Status != "running" {
		t.Fatal("independent branch stopped")
	}
	submitGraphV2(t, env, b, VerdictPass, "B")
	if env.reloadRun(t, run.ID).Status != "failed" {
		t.Fatal("run did not fail after branch completion")
	}
	if env.stepByNode(t, run.ID, "join").Status != "skipped" {
		t.Fatal("failed dependency executed")
	}
}
func TestGraphV2EngineRetryThenReleaseOnce(t *testing.T) {
	env := setupTestEnv(t)
	d := graphV2Fixture()
	d.Nodes[1].MaxAttempts = 2
	env.publishTemplate(t, d)
	run := env.startRun(t, "v2-retry")
	a := env.stepByNode(t, run.ID, "a")
	submitGraphV2(t, env, a, VerdictFail, "try again")
	attempts, err := env.q.ListWorkflowStepInstances(context.Background(), db.ListWorkflowStepInstancesParams{RunID: run.ID, WorkspaceID: env.workspaceID})
	if err != nil {
		t.Fatal(err)
	}
	var retry db.WorkflowStepInstance
	for _, step := range attempts {
		if step.NodeKey == "a" && step.Attempt == 2 {
			retry = step
		}
	}
	if retry.Status != "queued" {
		t.Fatal("explicit retry was not queued")
	}
	submitGraphV2(t, env, retry, VerdictPass, "A")
	submitGraphV2(t, env, env.stepByNode(t, run.ID, "b"), VerdictPass, "B")
	if env.reloadRun(t, run.ID).Status != "completed" {
		t.Fatal("successful retry did not release join")
	}
}
func TestGraphV2EngineConditionalMergeAndBoundPrompt(t *testing.T) {
	env := setupTestEnv(t)
	d := graphV2Fixture()
	d.Nodes[0].Next = []string{"route"}
	d.Nodes[0].NextIDs = []string{"sr"}
	d.Nodes[0].OutputPorts = []Port{{ID: "title", Type: "string"}}
	d.Nodes = append(d.Nodes, Node{Key: "route", Type: NodeTypeCondition, InputPorts: []Port{{ID: "choice", Type: "string", Required: true}}, Branches: []Branch{{ID: "ra", Predicate: &Predicate{InputPort: "choice", Equals: "select-a"}, Target: "a"}, {ID: "rb", Predicate: &Predicate{InputPort: "choice", Equals: "select-a"}, Target: "b"}, {ID: "default", Target: "b"}}})
	for i := range d.Nodes[3].InputPorts {
		d.Nodes[3].InputPorts[i].Required = false
	}
	d.DataEdges = append(d.DataEdges, DataEdge{ID: "choice", Source: "start", SourcePort: "title", Target: "route", TargetPort: "choice"})
	env.publishTemplate(t, d)
	res, err := env.engine.StartRun(context.Background(), StartRunInput{WorkspaceID: env.workspaceID, TemplateID: env.templateID, Source: "manual", IdempotencyKey: "v2-condition", AccountableUserID: env.userID, Input: json.RawMessage(`{"title":"select-a"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if env.stepByNode(t, res.Run.ID, "b").Status != "skipped" {
		t.Fatal("second matching condition activated")
	}
	submitGraphV2(t, env, env.stepByNode(t, res.Run.ID, "a"), VerdictPass, "A")
	if env.reloadRun(t, res.Run.ID).Status != "completed" {
		t.Fatal("exclusive join hung")
	}
}

func TestGraphV2EngineAcceptanceWithoutRework(t *testing.T) {
	for _, accept := range []bool{true, false} {
		t.Run(map[bool]string{true: "accept", false: "reject"}[accept], func(t *testing.T) {
			env := setupTestEnv(t)
			d := &Definition{SchemaVersion: 2, EntryNode: "gate", Nodes: []Node{{Key: "gate", Type: NodeTypeAcceptance, Next: []string{"end"}, NextIDs: []string{"gate-end"}, AcceptanceCriteria: []string{"Meets requirements"}}, {Key: "end", Type: NodeTypeEnd}}}
			env.publishTemplate(t, d)
			run := env.startRun(t, "v2-gate")
			step := env.stepByNode(t, run.ID, "gate")
			ctx := context.Background()
			pending, err := env.q.GetPendingWorkflowAcceptanceForStep(ctx, db.GetPendingWorkflowAcceptanceForStepParams{StepID: step.ID, WorkspaceID: env.workspaceID})
			if err != nil {
				t.Fatal(err)
			}
			if graphOutput(pending.Context)["can_reject_without_rework"] != true {
				t.Fatal("missing explicit v2 rejection contract")
			}
			_, err = env.engine.DecideAcceptance(ctx, DecideAcceptanceInput{WorkspaceID: env.workspaceID, AcceptanceID: pending.ID, Accept: accept, Reason: "Reviewed criteria", ReviewerUserID: env.userID})
			if err != nil {
				t.Fatal(err)
			}
			want := "failed"
			if accept {
				want = "completed"
			}
			if got := env.reloadRun(t, run.ID).Status; got != want {
				t.Fatalf("status %s, want %s", got, want)
			}
		})
	}
}
func TestGraphV2EngineMissingInputTerminatesWithoutTask(t *testing.T) {
	env := setupTestEnv(t)
	d := graphV2Fixture()
	d.Nodes[0].OutputPorts = []Port{{ID: "absent", Type: "string"}}
	d.Nodes[1].InputPorts = []Port{{ID: "required", Type: "string", Required: true}}
	d.Nodes[1].MaxAttempts = 2
	d.DataEdges = append(d.DataEdges, DataEdge{ID: "missing", Source: "start", SourcePort: "absent", Target: "a", TargetPort: "required"})
	env.publishTemplate(t, d)
	run := env.startRun(t, "v2-missing")
	a := env.stepByNode(t, run.ID, "a")
	if a.Status != "failed" || a.TaskID.Valid {
		t.Fatalf("missing input created work: %+v", a)
	}
	submitGraphV2(t, env, env.stepByNode(t, run.ID, "b"), VerdictPass, "B")
	if got := env.reloadRun(t, run.ID).Status; got != "failed" {
		t.Fatalf("run stuck: %s", got)
	}
}
