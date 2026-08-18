package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func submissionPayload(verdict Verdict, summary string, references ...string) string {
	refs, _ := json.Marshal(references)
	return fmt.Sprintf(`%s
{"verdict":%q,"artifact":{"type":"code_change","summary":%q,"references":%s},"rationale":"control-flow test","confidence":0.9}
%s`, submissionOpen, verdict, summary, refs, submissionClose)
}

func TestConditionRoutesStructuredFailure(t *testing.T) {
	env := setupTestEnv(t)
	env.publishTemplate(t, &Definition{
		SchemaVersion: SchemaVersion,
		EntryNode:     "inspect",
		Nodes: []Node{
			{Key: "inspect", Type: NodeTypeAgent, Next: []string{"route"}, Routing: &Routing{Strategy: RoutingCapability, Capability: "inspect"}, SubmissionSchema: "code_change"},
			{Key: "route", Type: NodeTypeCondition, Branches: []Branch{{WhenVerdict: string(VerdictPass), Target: "end"}, {WhenVerdict: string(VerdictFail), Target: "repair"}, {Target: "repair"}}},
			{Key: "repair", Type: NodeTypeAgent, Next: []string{"end"}, Routing: &Routing{Strategy: RoutingCapability, Capability: "repair"}, SubmissionSchema: "code_change"},
			{Key: "end", Type: NodeTypeEnd},
		},
	})
	ctx := context.Background()
	run := env.startRun(t, "condition-failure-route")
	inspect := env.stepByNode(t, run.ID, "inspect")
	if _, err := env.engine.SubmitResult(ctx, SubmitResultInput{WorkspaceID: env.workspaceID, StepID: inspect.ID, RawOutput: submissionPayload(VerdictFail, "needs repair"), ActorType: "agent"}); err != nil {
		t.Fatalf("submit failing inspection: %v", err)
	}
	condition := env.stepByNode(t, run.ID, "route")
	if condition.Status != string(StepPassed) {
		t.Fatalf("condition status = %q, want passed", condition.Status)
	}
	repair := env.stepByNode(t, run.ID, "repair")
	if repair.Status != string(StepQueued) {
		t.Fatalf("repair status = %q, want queued", repair.Status)
	}
	if _, err := env.engine.SubmitResult(ctx, SubmitResultInput{WorkspaceID: env.workspaceID, StepID: repair.ID, RawOutput: submissionPayload(VerdictPass, "repaired"), ActorType: "agent"}); err != nil {
		t.Fatalf("submit repair: %v", err)
	}
	if got := env.reloadRun(t, run.ID).Status; got != string(RunCompleted) {
		t.Fatalf("run status = %q, want completed", got)
	}
}

func TestFanOutJoinIsBoundedDurableAndReplaySafe(t *testing.T) {
	env := setupTestEnv(t)
	env.publishTemplate(t, &Definition{
		SchemaVersion: SchemaVersion,
		EntryNode:     "plan",
		Nodes: []Node{
			{Key: "plan", Type: NodeTypeAgent, Next: []string{"spread"}, Routing: &Routing{Strategy: RoutingCapability, Capability: "plan"}, SubmissionSchema: "code_change"},
			{Key: "spread", Type: NodeTypeFanOut, Next: []string{"worker"}, FanOutMax: 3},
			{Key: "worker", Type: NodeTypeAgent, Next: []string{"gather"}, Routing: &Routing{Strategy: RoutingCapability, Capability: "work"}, SubmissionSchema: "code_change"},
			{Key: "gather", Type: NodeTypeJoin, Next: []string{"end"}, JoinSources: []string{"worker"}, JoinPolicy: JoinPolicyFailFast},
			{Key: "end", Type: NodeTypeEnd},
		},
	})
	ctx := context.Background()
	run := env.startRun(t, "fanout-join-replay")
	plan := env.stepByNode(t, run.ID, "plan")
	if _, err := env.engine.SubmitResult(ctx, SubmitResultInput{WorkspaceID: env.workspaceID, StepID: plan.ID, RawOutput: submissionPayload(VerdictPass, "two work items", "src/a.go", "src/b.go"), ActorType: "agent"}); err != nil {
		t.Fatalf("submit plan: %v", err)
	}
	steps, err := env.q.ListWorkflowStepInstances(ctx, db.ListWorkflowStepInstancesParams{RunID: run.ID, WorkspaceID: env.workspaceID})
	if err != nil {
		t.Fatalf("list expanded steps: %v", err)
	}
	workers := make([]db.WorkflowStepInstance, 0, 2)
	for _, step := range steps {
		if step.NodeKey == "worker" {
			workers = append(workers, step)
			if !step.ParentStepID.Valid || !step.ExpansionKey.Valid || step.ExpansionKey.String == "" {
				t.Fatalf("fan-out child missing durable parent/expansion key: %+v", step)
			}
		}
	}
	if len(workers) != 2 {
		t.Fatalf("worker count = %d, want 2", len(workers))
	}
	for i, worker := range workers {
		if _, err := env.engine.SubmitResult(ctx, SubmitResultInput{WorkspaceID: env.workspaceID, StepID: worker.ID, RawOutput: submissionPayload(VerdictPass, fmt.Sprintf("worker %d done", i)), ActorType: "agent"}); err != nil {
			t.Fatalf("submit worker %d: %v", i, err)
		}
	}
	if got := env.reloadRun(t, run.ID).Status; got != string(RunCompleted) {
		t.Fatalf("run status = %q, want completed after join", got)
	}
	// A terminal callback replay is benign and cannot materialize another Join.
	last := workers[len(workers)-1]
	if err := env.engine.RecordTaskTerminal(ctx, RecordTaskTerminalInput{TaskID: last.TaskID, TaskStatus: "completed", Result: submissionPayload(VerdictPass, "replay")}); err != nil {
		t.Fatalf("replay terminal submission: %v", err)
	}
	steps, err = env.q.ListWorkflowStepInstances(ctx, db.ListWorkflowStepInstancesParams{RunID: run.ID, WorkspaceID: env.workspaceID})
	if err != nil {
		t.Fatalf("list replayed steps: %v", err)
	}
	joins := 0
	for _, step := range steps {
		if step.NodeKey == "gather" {
			joins++
		}
	}
	if joins != 1 {
		t.Fatalf("join count after replay = %d, want 1", joins)
	}
}

func TestFanOutJoinFailFastFailsAfterAllChildrenAreTerminal(t *testing.T) {
	env := setupTestEnv(t)
	env.publishTemplate(t, &Definition{SchemaVersion: SchemaVersion, EntryNode: "plan", Nodes: []Node{
		{Key: "plan", Type: NodeTypeAgent, Next: []string{"spread"}, Routing: &Routing{Strategy: RoutingCapability, Capability: "plan"}, SubmissionSchema: "code_change"},
		{Key: "spread", Type: NodeTypeFanOut, Next: []string{"worker"}, FanOutMax: 2},
		{Key: "worker", Type: NodeTypeAgent, Next: []string{"gather"}, Routing: &Routing{Strategy: RoutingCapability, Capability: "work"}, SubmissionSchema: "code_change"},
		{Key: "gather", Type: NodeTypeJoin, Next: []string{"end"}, JoinSources: []string{"worker"}, JoinPolicy: JoinPolicyFailFast},
		{Key: "end", Type: NodeTypeEnd},
	}})
	ctx := context.Background()
	run := env.startRun(t, "fanout-join-fail-fast")
	plan := env.stepByNode(t, run.ID, "plan")
	if _, err := env.engine.SubmitResult(ctx, SubmitResultInput{WorkspaceID: env.workspaceID, StepID: plan.ID, RawOutput: submissionPayload(VerdictPass, "two", "a", "b"), ActorType: "agent"}); err != nil {
		t.Fatalf("submit plan: %v", err)
	}
	steps, err := env.q.ListWorkflowStepInstances(ctx, db.ListWorkflowStepInstancesParams{RunID: run.ID, WorkspaceID: env.workspaceID})
	if err != nil {
		t.Fatal(err)
	}
	workers := make([]db.WorkflowStepInstance, 0, 2)
	for _, step := range steps {
		if step.NodeKey == "worker" {
			workers = append(workers, step)
		}
	}
	if len(workers) != 2 {
		t.Fatalf("workers = %d, want 2", len(workers))
	}
	if _, err := env.engine.SubmitResult(ctx, SubmitResultInput{WorkspaceID: env.workspaceID, StepID: workers[0].ID, RawOutput: submissionPayload(VerdictFail, "failed"), ActorType: "agent"}); err != nil {
		t.Fatalf("submit failed child: %v", err)
	}
	if got := env.reloadRun(t, run.ID).Status; got != string(RunRunning) {
		t.Fatalf("run failed before sibling completed: %q", got)
	}
	if _, err := env.engine.SubmitResult(ctx, SubmitResultInput{WorkspaceID: env.workspaceID, StepID: workers[1].ID, RawOutput: submissionPayload(VerdictPass, "passed"), ActorType: "agent"}); err != nil {
		t.Fatalf("submit passed child: %v", err)
	}
	got := env.reloadRun(t, run.ID)
	if got.Status != string(RunFailed) || got.FailureReason.String != ReasonJoinChildFailed {
		t.Fatalf("run = status %q reason %q", got.Status, got.FailureReason.String)
	}
}
