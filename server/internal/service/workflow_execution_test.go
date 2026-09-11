package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/internal/workflow"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// End-to-end test of the whole executable path, with the real router, the real
// notifier, the real TaskService, and the real engine wired to each other exactly
// as cmd/server/router.go wires them.
//
// The unit tests each prove one link. This proves the chain, which is the only
// thing a user experiences:
//
//	StartRun -> router picks a labelled agent -> a task is queued carrying the
//	step's brief -> TaskService.CompleteTask on that task -> the observer fires ->
//	the engine records a submission and activates the NEXT node -> that node's task
//	also carries a brief
//
// Every one of those links was either missing or unwired before this change, and
// a break in any of them presents identically to the user: the workflow silently
// does nothing.

// TestWorkflowRunExecutesEndToEnd is the acceptance test for "a run is physically
// executable".
func TestWorkflowRunExecutesEndToEnd(t *testing.T) {
	env := newWorkflowRouterEnv(t)
	ctx := context.Background()

	// Two specialists, each advertising one capability, so routing has to actually
	// choose rather than fall through to "the only agent".
	analyst := env.createAgent(t, agentSpec{name: "analyst", labels: []string{"bug_analysis"}})
	implementer := env.createAgent(t, agentSpec{name: "implementer", labels: []string{"code_change"}})

	bus := events.New()
	var runChanged int
	bus.Subscribe(protocol.EventWorkflowRunChanged, func(events.Event) { runChanged++ })

	tasks := &TaskService{Queries: env.q, TxStarter: env.pool, Bus: bus}
	engine := &workflow.Engine{
		Queries:   env.q,
		TxStarter: env.pool,
		Router:    NewWorkflowRouter(env.q),
		Notifier:  NewWorkflowNotifier(bus, tasks),
		Schemas:   workflow.DefaultSchemaRegistry,
	}
	// The wiring under test: without this line a finished task never advances the
	// run.
	tasks.WorkflowTerminal = engine

	templateID := env.publishWorkflow(t, &workflow.Definition{
		SchemaVersion: workflow.SchemaVersion,
		EntryNode:     "analyze",
		Nodes: []workflow.Node{
			{
				Key: "analyze", Type: workflow.NodeTypeAgent, Name: "Analyze",
				Instruction: "Find the root cause.",
				Next:        []string{"implement"},
				Routing: &workflow.Routing{
					Strategy: workflow.RoutingCapability, Capability: "bug_analysis",
				},
				SubmissionSchema: "analysis",
			},
			{
				Key: "implement", Type: workflow.NodeTypeAgent, Name: "Implement",
				Instruction: "Apply the minimal fix for the named root cause.",
				Next:        []string{"end"},
				Routing: &workflow.Routing{
					Strategy: workflow.RoutingCapability, Capability: "code_change",
				},
				SubmissionSchema: "code_change",
			},
			{Key: "end", Type: workflow.NodeTypeEnd, Name: "Done"},
		},
	})

	input, err := json.Marshal(map[string]any{
		"title":       "Save button silently discards edits",
		"description": "Editing a comment and pressing Save closes the editor with no request sent.",
	})
	if err != nil {
		t.Fatalf("marshal run input: %v", err)
	}

	started, err := engine.StartRun(ctx, workflow.StartRunInput{
		WorkspaceID:       env.workspaceID,
		TemplateID:        templateID,
		Source:            "manual",
		IdempotencyKey:    fmt.Sprintf("e2e-%d", time.Now().UnixNano()),
		AccountableUserID: env.userID,
		Input:             input,
		ActorType:         "member",
		ActorID:           env.userID,
	})
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	run := started.Run
	t.Cleanup(func() {
		bg := context.Background()
		for _, stmt := range []string{
			`DELETE FROM workflow_event WHERE workspace_id = $1`,
			`DELETE FROM workflow_submission WHERE workspace_id = $1`,
			`DELETE FROM workflow_step_instance WHERE workspace_id = $1`,
			`DELETE FROM workflow_run WHERE workspace_id = $1`,
			`DELETE FROM workflow_template_version WHERE workspace_id = $1`,
			`DELETE FROM workflow_template WHERE workspace_id = $1`,
			`DELETE FROM agent_task_queue WHERE agent_id IN (SELECT id FROM agent WHERE workspace_id = $1)`,
		} {
			if _, err := env.pool.Exec(bg, stmt, env.workspaceID); err != nil {
				t.Logf("cleanup %q: %v", stmt, err)
			}
		}
	})

	// --- The entry step must be dispatched to the ANALYST with a real brief.
	analyzeStep := env.stepForNode(t, run.ID, "analyze")
	if util.UUIDToString(analyzeStep.AgentID) != util.UUIDToString(analyst) {
		t.Fatalf("analyze routed to %s, want the bug_analysis specialist %s",
			util.UUIDToString(analyzeStep.AgentID), util.UUIDToString(analyst))
	}
	if !strings.Contains(analyzeStep.RoutingReason.String, "capability:bug_analysis") {
		t.Errorf("routing reason = %q; the persisted reason must explain the choice",
			analyzeStep.RoutingReason.String)
	}
	analyzeTask, err := env.q.GetAgentTask(ctx, analyzeStep.TaskID)
	if err != nil {
		t.Fatalf("load analyze task: %v", err)
	}
	if analyzeTask.Status != "queued" {
		t.Errorf("task status = %q, want queued", analyzeTask.Status)
	}
	brief, ok := workflow.ParseTaskContext(analyzeTask.Context)
	if !ok {
		t.Fatalf("the queued task carries no workflow brief; the agent would receive nothing to work on. context=%s",
			string(analyzeTask.Context))
	}
	if brief.Instruction == "" || brief.SubmissionContract == "" {
		t.Fatalf("brief is incomplete: instruction=%q contract_len=%d",
			brief.Instruction, len(brief.SubmissionContract))
	}
	if !strings.Contains(brief.RunDescription, "no request sent") {
		t.Errorf("brief run_description = %q, want the human's description", brief.RunDescription)
	}

	// --- The agent finishes. This goes through TaskService, NOT the engine
	// directly: the point is that the ordinary completion path advances the run.
	dispatchAndStart(t, env, analyzeTask.ID)
	result, err := json.Marshal(protocol.TaskCompletedPayload{
		TaskID: util.UUIDToString(analyzeTask.ID),
		Output: workflowSubmissionBlock(brief.StepInstanceID, "analysis",
			"the Save handler is bound to a detached node after re-render"),
	})
	if err != nil {
		t.Fatalf("marshal task result: %v", err)
	}
	if _, err := tasks.CompleteTask(ctx, analyzeTask.ID, result, "", "", "", false, "", ""); err != nil {
		t.Fatalf("CompleteTask: %v", err)
	}

	// --- The submission must exist and the analyze step must have passed.
	analyzeStep = env.stepForNode(t, run.ID, "analyze")
	if analyzeStep.Status != "passed" {
		t.Fatalf("analyze step status = %q, want passed; the terminal hook did not advance the run", analyzeStep.Status)
	}
	subs, err := env.q.ListWorkflowSubmissionsForRun(ctx, db.ListWorkflowSubmissionsForRunParams{
		RunID: run.ID, WorkspaceID: env.workspaceID,
	})
	if err != nil {
		t.Fatalf("list submissions: %v", err)
	}
	if len(subs) != 1 || subs[0].Verdict != "pass" {
		t.Fatalf("submissions = %d with verdict %v, want one pass", len(subs), subs)
	}

	// --- The NEXT node must be dispatched, to the OTHER specialist, carrying the
	// first step's artifact. This is the handoff the whole feature exists for.
	implementStep := env.stepForNode(t, run.ID, "implement")
	if util.UUIDToString(implementStep.AgentID) != util.UUIDToString(implementer) {
		t.Fatalf("implement routed to %s, want the code_change specialist %s",
			util.UUIDToString(implementStep.AgentID), util.UUIDToString(implementer))
	}
	implementTask, err := env.q.GetAgentTask(ctx, implementStep.TaskID)
	if err != nil {
		t.Fatalf("load implement task: %v", err)
	}
	implementBrief, ok := workflow.ParseTaskContext(implementTask.Context)
	if !ok {
		t.Fatalf("the second step's task carries no brief: %s", string(implementTask.Context))
	}
	if implementBrief.UpstreamNodeKey != "analyze" {
		t.Errorf("upstream_node_key = %q, want analyze", implementBrief.UpstreamNodeKey)
	}
	if !strings.Contains(implementBrief.UpstreamSummary, "detached node") {
		t.Fatalf("upstream_summary = %q; the implementer was not given the analysis it must act on",
			implementBrief.UpstreamSummary)
	}
	// The run input travels the whole way, not just to the entry node.
	if !strings.Contains(implementBrief.RunDescription, "no request sent") {
		t.Errorf("the second step lost the run input: %q", implementBrief.RunDescription)
	}

	// --- Finish the second step; the run must complete, and only via End.
	dispatchAndStart(t, env, implementTask.ID)
	result2, err := json.Marshal(protocol.TaskCompletedPayload{
		TaskID: util.UUIDToString(implementTask.ID),
		Output: workflowSubmissionBlock(implementBrief.StepInstanceID, "code_change",
			"rebound the handler after re-render and added a regression test"),
	})
	if err != nil {
		t.Fatalf("marshal task result: %v", err)
	}
	if _, err := tasks.CompleteTask(ctx, implementTask.ID, result2, "", "", "", false, "", ""); err != nil {
		t.Fatalf("CompleteTask (implement): %v", err)
	}

	finished, err := env.q.GetWorkflowRun(ctx, db.GetWorkflowRunParams{
		ID: run.ID, WorkspaceID: env.workspaceID,
	})
	if err != nil {
		t.Fatalf("reload run: %v", err)
	}
	if finished.Status != "completed" {
		t.Fatalf("run status = %q, want completed", finished.Status)
	}

	// The realtime signal must have fired, or the UI shows a stale run until the
	// user reloads.
	if runChanged == 0 {
		t.Error("no workflow:run_changed event was published; the UI would never update")
	}
}

// TestNonWorkflowTaskCompletionIsUntouched: the observer is wired into a path
// every task in the system flows through. A legacy task must complete exactly as
// it did before - no workflow rows, no error, no interference.
func TestNonWorkflowTaskCompletionIsUntouched(t *testing.T) {
	env := newWorkflowRouterEnv(t)
	ctx := context.Background()
	agentID := env.createAgent(t, agentSpec{name: "legacy-agent"})

	tasks := &TaskService{Queries: env.q, TxStarter: env.pool, Bus: events.New()}
	tasks.WorkflowTerminal = &workflow.Engine{Queries: env.q, TxStarter: env.pool}

	var taskID pgtype.UUID
	mustQueryRow(t, env.pool, &taskID,
		`INSERT INTO agent_task_queue (agent_id, runtime_id, status, priority)
		 VALUES ($1, $2, 'queued', 1) RETURNING id`,
		agentID, env.onlineRT)
	t.Cleanup(func() {
		env.pool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id = $1`, taskID)
	})

	dispatchAndStart(t, env, taskID)
	done, err := tasks.CompleteTask(ctx, taskID, []byte(`{"output":"did the thing"}`), "", "", "", false, "", "")
	if err != nil {
		t.Fatalf("a non-workflow task must complete normally with the observer wired: %v", err)
	}
	if done.Status != "completed" {
		t.Errorf("task status = %q, want completed", done.Status)
	}
}

// publishWorkflow stores and publishes a definition, returning the template id.
// Validates first so a malformed test graph fails here rather than surfacing as a
// confusing engine error later.
func (env *workflowRouterEnv) publishWorkflow(t *testing.T, def *workflow.Definition) pgtype.UUID {
	t.Helper()
	ctx := context.Background()
	if err := workflow.Validate(def, workflow.DefaultWorkspacePolicy, workflow.DefaultSchemaRegistry); err != nil {
		t.Fatalf("test definition is invalid: %v", err)
	}
	raw, err := workflow.MarshalDefinition(def)
	if err != nil {
		t.Fatalf("marshal definition: %v", err)
	}
	tpl, err := env.q.CreateWorkflowTemplate(ctx, db.CreateWorkflowTemplateParams{
		WorkspaceID:   env.workspaceID,
		Key:           fmt.Sprintf("e2e-tpl-%d", time.Now().UnixNano()),
		Name:          "E2E Template",
		CreatedByType: "member",
		CreatedByID:   env.userID,
	})
	if err != nil {
		t.Fatalf("create template: %v", err)
	}
	version, err := env.q.CreateWorkflowTemplateVersion(ctx, db.CreateWorkflowTemplateVersionParams{
		WorkspaceID:   env.workspaceID,
		TemplateID:    tpl.ID,
		Definition:    raw,
		SchemaVersion: int32(workflow.SchemaVersion),
	})
	if err != nil {
		t.Fatalf("create version: %v", err)
	}
	published, err := env.q.PublishWorkflowTemplateVersion(ctx, db.PublishWorkflowTemplateVersionParams{
		ID:              version.ID,
		WorkspaceID:     env.workspaceID,
		PublishedByType: "member",
		PublishedByID:   env.userID,
	})
	if err != nil {
		t.Fatalf("publish version: %v", err)
	}
	if _, err := env.q.SetWorkflowTemplateCurrentVersion(ctx, db.SetWorkflowTemplateCurrentVersionParams{
		ID:             tpl.ID,
		WorkspaceID:    env.workspaceID,
		CurrentVersion: published.Version,
	}); err != nil {
		t.Fatalf("set current version: %v", err)
	}
	return tpl.ID
}

func (env *workflowRouterEnv) stepForNode(t *testing.T, runID pgtype.UUID, nodeKey string) db.WorkflowStepInstance {
	t.Helper()
	steps, err := env.q.ListWorkflowStepInstances(context.Background(), db.ListWorkflowStepInstancesParams{
		RunID:       runID,
		WorkspaceID: env.workspaceID,
	})
	if err != nil {
		t.Fatalf("list steps: %v", err)
	}
	var found db.WorkflowStepInstance
	for _, s := range steps {
		if s.NodeKey == nodeKey && s.Attempt >= found.Attempt {
			found = s
		}
	}
	if !found.ID.Valid {
		t.Fatalf("no step found for node %q", nodeKey)
	}
	return found
}

// dispatchAndStart walks a queued task to 'running' so CompleteTask's status CAS
// matches. The daemon claim path does this in production; driving it directly
// keeps the test about the workflow chain rather than the claim protocol.
func dispatchAndStart(t *testing.T, env *workflowRouterEnv, taskID pgtype.UUID) {
	t.Helper()
	mustExec(t, env.pool,
		`UPDATE agent_task_queue SET status = 'running', dispatched_at = now(), started_at = now()
		 WHERE id = $1`, taskID)
}

// workflowSubmissionBlock builds the delimited payload an agent following the
// brief's contract would emit. Built from workflow.SubmissionContractInstructions'
// own shape, so if the contract and the parser ever diverge this test breaks
// alongside the round-trip test in package workflow.
func workflowSubmissionBlock(stepID, artifactType, summary string) string {
	payload, err := json.Marshal(map[string]any{
		"schema_version":   workflow.SchemaVersion,
		"step_instance_id": stepID,
		"verdict":          "pass",
		"artifact":         map[string]any{"type": artifactType, "summary": summary},
		"rationale":        "verified",
		"confidence":       0.9,
	})
	if err != nil {
		panic(err)
	}
	// The markers are not exported; take them from the instructions the engine
	// itself hands the agent, so the test cannot drift from the real contract.
	instructions := workflow.SubmissionContractInstructions(stepID)
	open, close := submissionMarkersFrom(instructions)
	return open + "\n" + string(payload) + "\n" + close
}

// submissionMarkersFrom pulls the open/close markers out of the contract text.
// The constants are unexported in package workflow, and exporting them purely for
// a test would widen the API for no product reason; reading them back out of the
// instructions the agent receives is both sufficient and self-checking.
func submissionMarkersFrom(instructions string) (string, string) {
	var open, close string
	for _, line := range strings.Split(instructions, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "<<<") {
			continue
		}
		if strings.Contains(trimmed, "END") {
			close = trimmed
		} else if open == "" {
			open = trimmed
		}
	}
	if open == "" || close == "" {
		panic("submission contract instructions no longer contain the delimiters")
	}
	return open, close
}
