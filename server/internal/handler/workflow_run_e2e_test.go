package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/workflow"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// The whole feature, end to end, against a real Postgres: press Run on the
// built-in Bug Fix template and walk every hop until the Run is completed.
//
// This file exists to answer two questions that no unit test can answer
// together, because each one is about a SEAM rather than a component:
//
//	1. can a template actually be run?
//	2. does the input the human typed actually reach the agent?
//
// Every earlier phase of this feature could pass its own tests while the answer
// to both was no. The engine's commands were unit-tested against a fake router;
// the router was unit-tested without the engine; the run endpoint existed but the
// engine was never constructed; and the first cut of dispatchAgentStep created an
// agent task carrying nothing but ids - so an agent would claim a task with no
// work description, answer in prose, and every step of every run would block on
// the submission contract. A test that stops at "the Run row says running" would
// have been green for all of it.
//
// So the assertions here are deliberately about delivery, not about state:
//
//   - the queued task's context carries the step instruction, the description the
//     human typed, and the submission contract, and
//   - the DAEMON CLAIM RESPONSE carries them too. A context blob the daemon never
//     forwards is still an agent with no prompt, so the claim path is exercised
//     through the real HTTP handler rather than by reading the row back.
//
// Everything runs in a FRESH workspace with its own runtime and two labelled
// specialists, because capability routing has to actually choose: a workspace
// with one agent would pass with a router that ignored the capability entirely.

// ---------------------------------------------------------------------------
// Fixture
// ---------------------------------------------------------------------------

// workflowE2EEnv is one isolated workspace with everything a Bug Fix run needs.
type workflowE2EEnv struct {
	workspaceID string
	runtimeID   string
	// analyst carries "bug_analysis", implementer carries "code_change". The
	// built-in graph's validate node routes previous_step from implement, so the
	// implementer must run twice and the analyst exactly once - which is what makes
	// a mis-routed run visible rather than coincidentally correct.
	analystID     string
	implementerID string
	templateID    string
}

// newWorkflowE2EEnv builds the workspace, runtime, agents, labels, and seeds the
// built-in templates.
//
// A dedicated workspace rather than the shared handler fixture: the shared one
// already has an agent with no capability labels, and the assertions below are
// about which of several candidates routing picked. It also means this test's
// cleanup cannot disturb another test's rows.
func newWorkflowE2EEnv(t *testing.T, slug string) *workflowE2EEnv {
	t.Helper()
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()

	var workspaceID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO workspace (name, slug, description, issue_prefix)
		VALUES ($1, $2, $3, $4)
		RETURNING id
	`, "Workflow E2E "+slug, "workflow-e2e-"+slug, "workflow end-to-end test", "WFE").Scan(&workspaceID); err != nil {
		t.Fatalf("create e2e workspace: %v", err)
	}
	// Cleanup registered first so a failure part-way through still tears down. The
	// workflow tables carry no foreign keys by design, so dropping the workspace
	// does NOT remove them - they must be deleted explicitly, child-first.
	t.Cleanup(func() {
		bg := context.Background()
		for _, stmt := range []string{
			`DELETE FROM workflow_event WHERE workspace_id = $1`,
			`DELETE FROM workflow_acceptance WHERE workspace_id = $1`,
			`DELETE FROM workflow_submission WHERE workspace_id = $1`,
			`DELETE FROM workflow_step_instance WHERE workspace_id = $1`,
			`DELETE FROM workflow_run WHERE workspace_id = $1`,
			`DELETE FROM workflow_template_version WHERE workspace_id = $1`,
			`DELETE FROM workflow_template WHERE workspace_id = $1`,
			// The workspace cascade removes members, runtimes, agents, issues, and
			// (through agent) the agent_task_queue rows plus their task tokens.
			`DELETE FROM workspace WHERE id = $1`,
		} {
			if _, err := testPool.Exec(bg, stmt, workspaceID); err != nil {
				t.Logf("cleanup %q: %v", stmt, err)
			}
		}
	})

	// The user who will press Run. Owner, because starting a Run is an ordinary use
	// of a published process and the accountable human is who routing checks
	// invocation permission against.
	if _, err := testPool.Exec(ctx, `
		INSERT INTO member (workspace_id, user_id, role) VALUES ($1, $2, 'owner')
	`, workspaceID, testUserID); err != nil {
		t.Fatalf("add e2e member: %v", err)
	}

	// owner_id is required: the claim path refuses to mint a task token for a
	// runtime with no owner and cancels the task instead, which would look exactly
	// like a routing failure.
	var runtimeID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_runtime (
			workspace_id, daemon_id, name, runtime_mode, provider, status,
			device_info, metadata, owner_id, last_seen_at
		)
		VALUES ($1, NULL, $2, 'cloud', 'workflow_e2e_runtime', 'online', $3, '{}'::jsonb, $4, now())
		RETURNING id
	`, workspaceID, "Workflow E2E Runtime", "workflow e2e runtime", testUserID).Scan(&runtimeID); err != nil {
		t.Fatalf("create e2e runtime: %v", err)
	}

	env := &workflowE2EEnv{workspaceID: workspaceID, runtimeID: runtimeID}
	env.analystID = env.createLabelledAgent(t, "e2e-analyst", "bug_analysis")
	env.implementerID = env.createLabelledAgent(t, "e2e-implementer", "code_change")

	// Seed the built-ins the way the workflows page does. Called directly rather
	// than through the list endpoint so this test does not depend on that endpoint
	// to establish its own preconditions.
	if err := service.EnsureBuiltinWorkflowTemplates(ctx, testHandler.Queries, parseUUID(workspaceID)); err != nil {
		t.Fatalf("EnsureBuiltinWorkflowTemplates: %v", err)
	}
	tpl, err := testHandler.Queries.GetWorkflowTemplateByKey(ctx, db.GetWorkflowTemplateByKeyParams{
		WorkspaceID: parseUUID(workspaceID),
		Key:         "bug_fix",
	})
	if err != nil {
		t.Fatalf("the bug_fix built-in was not seeded: %v", err)
	}
	env.templateID = uuidToString(tpl.ID)

	// PRECONDITIONS, asserted rather than assumed. Each of these, if false, makes
	// every later assertion fail for a reason that has nothing to do with the thing
	// under test - a run blocked with routing_no_candidate reads identically whether
	// the router is broken or the fixture never brought a runtime online.
	if !tpl.CurrentVersion.Valid {
		t.Fatalf("the seeded bug_fix template has no published version to run")
	}
	version, err := testHandler.Queries.GetPublishedWorkflowTemplateVersion(ctx, db.GetPublishedWorkflowTemplateVersionParams{
		TemplateID:  tpl.ID,
		WorkspaceID: parseUUID(workspaceID),
	})
	if err != nil {
		t.Fatalf("the seeded bug_fix template has no published version row: %v", err)
	}
	def, err := workflow.ParseDefinition(version.Definition)
	if err != nil {
		t.Fatalf("the seeded definition does not parse: %v", err)
	}
	// "intake" leads the list: the shipped graph now declares an input node as its
	// entry, and a run that never materialized an intake step would mean the human's
	// report reached the first agent through some path the graph does not describe.
	// Added, not substituted - the five agent/gate nodes below are still every hop
	// this test drives.
	for _, want := range []string{"intake", "analyze", "implement", "validate", "acceptance", "end"} {
		if _, ok := def.NodeByKey(want); !ok {
			t.Fatalf("the pinned graph has no %q node; this test asserts the full Bug Fix path", want)
		}
	}
	if _, ok := def.EntryInputNode(); !ok {
		t.Fatalf("the pinned graph's entry node %q is not an input node", def.EntryNode)
	}
	env.assertAgentRoutable(t, env.analystID, "bug_analysis")
	env.assertAgentRoutable(t, env.implementerID, "code_change")

	return env
}

// createLabelledAgent creates an agent bound to the env's online runtime and
// advertising one capability as an agent-scoped label.
//
// resource_type='agent' is load-bearing: ListLabelsForAgents filters on it, so an
// issue-scoped label with the same name is invisible to routing and the node would
// block with routing_no_candidate.
func (env *workflowE2EEnv) createLabelledAgent(t *testing.T, name, capability string) string {
	t.Helper()
	ctx := context.Background()

	// public_to + a workspace invocation target, not owner-only: this exercises the
	// allow-list branch of AgentInvokePermitted rather than the owner short-circuit,
	// which is the branch a real shared specialist agent takes.
	var agentID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent (
			workspace_id, name, description, runtime_mode, runtime_config,
			runtime_id, visibility, permission_mode, max_concurrent_tasks, owner_id,
			instructions, custom_env, custom_args, mcp_config
		)
		VALUES ($1, $2, '', 'cloud', '{}'::jsonb, $3, 'workspace', 'public_to', 1, $4,
				'', '{}'::jsonb, '[]'::jsonb, '{}'::jsonb)
		RETURNING id
	`, env.workspaceID, name, env.runtimeID, testUserID).Scan(&agentID); err != nil {
		t.Fatalf("create agent %q: %v", name, err)
	}
	if _, err := testPool.Exec(ctx, `
		INSERT INTO agent_invocation_target (agent_id, target_type, target_id)
		VALUES ($1, 'workspace', $2)
		ON CONFLICT (agent_id, target_type, target_id) DO NOTHING
	`, agentID, env.workspaceID); err != nil {
		t.Fatalf("seed invocation target for %q: %v", name, err)
	}

	var labelID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO issue_label (workspace_id, resource_type, name, color)
		VALUES ($1, 'agent', $2, '#336699')
		RETURNING id
	`, env.workspaceID, capability).Scan(&labelID); err != nil {
		t.Fatalf("create capability label %q: %v", capability, err)
	}
	if _, err := testPool.Exec(ctx,
		`INSERT INTO agent_to_label (agent_id, label_id) VALUES ($1, $2)`,
		agentID, labelID,
	); err != nil {
		t.Fatalf("attach capability label %q: %v", capability, err)
	}
	return agentID
}

// assertAgentRoutable proves the two candidate gates the router applies would
// admit this agent, and that its capability label is visible to the batch lookup
// routing uses.
func (env *workflowE2EEnv) assertAgentRoutable(t *testing.T, agentID, capability string) {
	t.Helper()
	ctx := context.Background()

	agent, err := testHandler.Queries.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{
		ID:          parseUUID(agentID),
		WorkspaceID: parseUUID(env.workspaceID),
	})
	if err != nil {
		t.Fatalf("load agent %s: %v", agentID, err)
	}
	// Upstream reshaped AgentReadiness to take a RuntimeLookup and to return an
	// AgentVerdict (it was `(ready bool, reason string, err error)`). Mirrors
	// what WorkflowRouter.eligible actually calls, so this still asserts the
	// real gate routing applies: Ready() is the admission test, and Detail is
	// the daemon's own description, which is what the old `reason` carried.
	verdict, err := service.AgentReadiness(ctx, service.RuntimeLookup{Queries: testHandler.Queries}, agent)
	if err != nil {
		t.Fatalf("AgentReadiness(%s): %v", agent.Name, err)
	}
	if !verdict.Ready() {
		t.Fatalf("agent %q is not ready (%s); routing would block the run", agent.Name, verdict.Detail)
	}
	if !testHandler.canInvokeAgent(ctx, agent, "member", testUserID, testUserID, env.workspaceID) {
		t.Fatalf("the accountable user may not invoke agent %q; routing would block the run", agent.Name)
	}

	labels, err := testHandler.Queries.ListLabelsForAgents(ctx, db.ListLabelsForAgentsParams{
		AgentIds:    []pgtype.UUID{parseUUID(agentID)},
		WorkspaceID: parseUUID(env.workspaceID),
	})
	if err != nil {
		t.Fatalf("ListLabelsForAgents(%s): %v", agent.Name, err)
	}
	found := false
	for _, l := range labels {
		if strings.EqualFold(l.Name, capability) {
			found = true
		}
	}
	if !found {
		t.Fatalf("agent %q does not advertise the %q capability label routing matches on: %+v",
			agent.Name, capability, labels)
	}
}

// request builds a member-authenticated request scoped to the env's workspace.
// newRequest pins the SHARED fixture workspace, which is not the one under test.
func (env *workflowE2EEnv) request(method, path string, body any) *http.Request {
	req := newRequest(method, path, body)
	req.Header.Set("X-Workspace-ID", env.workspaceID)
	return req
}

// startRun posts the run endpoint the Run dialog posts to.
func (env *workflowE2EEnv) startRun(t *testing.T, title, description string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	req := withURLParam(
		env.request("POST", "/api/workflow-templates/"+env.templateID+"/run", map[string]any{
			"title":       title,
			"description": description,
		}),
		"id", env.templateID,
	)
	testHandler.RunWorkflowTemplate(w, req)
	return w
}

func (env *workflowE2EEnv) getRun(t *testing.T, runID string) WorkflowRunDetailResponse {
	t.Helper()
	w := httptest.NewRecorder()
	req := withURLParam(env.request("GET", "/api/workflow-runs/"+runID, nil), "id", runID)
	testHandler.GetWorkflowRun(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GetWorkflowRun: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	return decodeWorkflowRunDetail(t, w, "GetWorkflowRun")
}

// e2eClaimedTask is the part of the daemon claim response this test cares about.
// Decoded from the wire rather than from the response struct so a field that stops
// being serialized (an accidental `json:"-"`, a renamed tag) fails here: the
// daemon reads JSON, not Go types.
type e2eClaimedTask struct {
	ID                     string `json:"id"`
	AgentID                string `json:"agent_id"`
	Status                 string `json:"status"`
	WorkspaceID            string `json:"workspace_id"`
	ThreadName             string `json:"thread_name"`
	WorkflowPrompt         string `json:"workflow_prompt"`
	WorkflowRunID          string `json:"workflow_run_id"`
	WorkflowStepInstanceID string `json:"workflow_step_instance_id"`
	WorkflowNodeKey        string `json:"workflow_node_key"`
}

// claimNextTask claims through the REAL daemon endpoint.
//
// This is the assertion that the brief is DELIVERED rather than merely stored.
// Reading agent_task_queue.context back would prove the engine wrote a prompt;
// only the claim response proves the agent receives one, and the two were
// independently capable of being wrong (the context is written by the engine, the
// forwarding is a separate branch in buildClaimedTaskResponse keyed on
// workflow_step_instance_id).
func (env *workflowE2EEnv) claimNextTask(t *testing.T) e2eClaimedTask {
	t.Helper()
	w := httptest.NewRecorder()
	req := newDaemonTokenRequest("POST", "/api/daemon/runtimes/"+env.runtimeID+"/tasks/claim", nil,
		env.workspaceID, "workflow-e2e-daemon")
	req = withURLParam(req, "runtimeId", env.runtimeID)
	testHandler.ClaimTaskByRuntime(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("ClaimTaskByRuntime: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var body struct {
		Task *e2eClaimedTask `json:"task"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode claim response: %v (%s)", err, w.Body.String())
	}
	if body.Task == nil {
		t.Fatalf("no task was claimable on the runtime; the step queued no work: %s", w.Body.String())
	}
	return *body.Task
}

// assertBriefIsComplete checks the three things without which the step cannot
// work, on both the stored context and the delivered prompt.
func (env *workflowE2EEnv) assertBriefIsComplete(
	t *testing.T,
	nodeKey, taskID, stepID, wantInstructionFragment, wantDescription string,
	claimed e2eClaimedTask,
) workflow.TaskContext {
	t.Helper()

	var rawContext []byte
	if err := testPool.QueryRow(context.Background(),
		`SELECT context FROM agent_task_queue WHERE id = $1`, taskID,
	).Scan(&rawContext); err != nil {
		t.Fatalf("%s: load task context: %v", nodeKey, err)
	}
	brief, ok := workflow.ParseTaskContext(rawContext)
	if !ok {
		t.Fatalf("%s: the queued task carries no workflow brief; the agent would receive nothing to work on: %s",
			nodeKey, string(rawContext))
	}
	if brief.StepInstanceID != stepID {
		t.Fatalf("%s: brief step_instance_id = %q, want the step it answers (%s); a mismatch is rejected as cross-talk",
			nodeKey, brief.StepInstanceID, stepID)
	}
	if !strings.Contains(brief.Instruction, wantInstructionFragment) {
		t.Fatalf("%s: brief instruction = %q, want the node instruction containing %q",
			nodeKey, brief.Instruction, wantInstructionFragment)
	}
	if !strings.Contains(brief.RunDescription, wantDescription) {
		t.Fatalf("%s: brief run_description = %q, want the description the human typed (%q)",
			nodeKey, brief.RunDescription, wantDescription)
	}
	if brief.SubmissionContract == "" {
		t.Fatalf("%s: brief carries no submission contract; the agent would do correct work and have it rejected", nodeKey)
	}

	// Delivered form. Assert on CONTENT, not on the presence of the field: a
	// non-empty prompt that omitted the contract would still block every step.
	if claimed.WorkflowPrompt == "" {
		t.Fatalf("%s: the claim response carries no workflow_prompt; the brief never reaches the agent", nodeKey)
	}
	if claimed.WorkflowNodeKey != nodeKey {
		t.Fatalf("%s: claim workflow_node_key = %q", nodeKey, claimed.WorkflowNodeKey)
	}
	if claimed.WorkflowStepInstanceID != stepID {
		t.Fatalf("%s: claim workflow_step_instance_id = %q, want %s", nodeKey, claimed.WorkflowStepInstanceID, stepID)
	}
	for _, want := range []string{
		wantInstructionFragment, // this step's task
		wantDescription,         // the actual defect, not the generic instruction
		"<<<MULTICA_SUBMISSION>>>",
		"<<<END_MULTICA_SUBMISSION>>>",
		stepID, // the step id the agent must echo back
	} {
		if !strings.Contains(claimed.WorkflowPrompt, want) {
			t.Fatalf("%s: the delivered prompt is missing %q.\n--- prompt ---\n%s",
				nodeKey, want, claimed.WorkflowPrompt)
		}
	}
	return brief
}

// finishStep drives one Agent step exactly as production does: claim through the
// daemon endpoint, start it, then complete it with a delimited submission through
// TaskService.CompleteTask.
//
// CompleteTask, never engine.SubmitResult: the terminal observer that translates a
// finished task into workflow state IS the wiring under test, and calling the
// engine directly would assert the engine works while leaving the question "does
// a finishing agent advance the run" unanswered - which is precisely the link that
// was missing.
func (env *workflowE2EEnv) finishStep(
	t *testing.T,
	runID, nodeKey, wantAgentID, wantInstructionFragment, wantDescription, artifactType, summary string,
) {
	t.Helper()
	ctx := context.Background()

	run := env.getRun(t, runID)
	step, ok := findWorkflowStep(run.Steps, nodeKey)
	if !ok {
		t.Fatalf("%s: no such step in the trace: %+v", nodeKey, run.Steps)
	}
	if step.Status != "queued" {
		t.Fatalf("%s: step status = %q, want queued (failure_reason=%v routing_reason=%v)",
			nodeKey, step.Status, step.FailureReason, step.RoutingReason)
	}
	if step.TaskID == nil {
		t.Fatalf("%s: step has no task; no agent was asked to do anything", nodeKey)
	}
	if wantAgentID != "" && (step.AgentID == nil || *step.AgentID != wantAgentID) {
		t.Fatalf("%s: routed to %v, want %s", nodeKey, step.AgentID, wantAgentID)
	}

	claimed := env.claimNextTask(t)
	if claimed.ID != *step.TaskID {
		t.Fatalf("%s: claimed task %s, want the step's task %s", nodeKey, claimed.ID, *step.TaskID)
	}
	if claimed.WorkflowRunID != runID {
		t.Fatalf("%s: claim workflow_run_id = %q, want %s", nodeKey, claimed.WorkflowRunID, runID)
	}
	brief := env.assertBriefIsComplete(t, nodeKey, *step.TaskID, step.ID,
		wantInstructionFragment, wantDescription, claimed)

	if _, err := testHandler.TaskService.StartTask(ctx, parseUUID(claimed.ID)); err != nil {
		t.Fatalf("%s: StartTask: %v", nodeKey, err)
	}
	if _, err := testHandler.TaskService.CompleteTask(ctx, parseUUID(claimed.ID),
		e2eTaskResult(t, claimed.ID, brief.StepInstanceID, artifactType, summary), "", "", "", false, "", ""); err != nil {
		t.Fatalf("%s: CompleteTask: %v", nodeKey, err)
	}
}

// e2eTaskResult builds the daemon's completion body for an agent that followed
// the contract.
//
// Wrapped in the same protocol envelope a real completion carries, because that
// envelope's JSON escaping of `<` is exactly what the submission extractor has to
// survive - an earlier version of the hook read the escaped form and blocked every
// step. The delimiters are read back out of the instructions the engine hands the
// agent, so the test cannot drift from the real contract.
func e2eTaskResult(t *testing.T, taskID, stepID, artifactType, summary string) []byte {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"schema_version":   workflow.SchemaVersion,
		"step_instance_id": stepID,
		"verdict":          "pass",
		"artifact": map[string]any{
			"type":    artifactType,
			"summary": summary,
		},
		"rationale":  "verified against the reproduction",
		"confidence": 0.9,
	})
	if err != nil {
		t.Fatalf("marshal submission: %v", err)
	}
	open, closing := workflowSubmissionMarkersForTest(t, stepID)
	result, err := json.Marshal(map[string]any{
		"task_id": taskID,
		// Prose around the block, the way a real agent answers: the extractor must
		// find the payload inside a normal message rather than requiring the output
		// to be nothing but JSON.
		"output": "I looked into this and here is what I found.\n\n" +
			open + "\n" + string(payload) + "\n" + closing + "\n",
	})
	if err != nil {
		t.Fatalf("marshal task result: %v", err)
	}
	return result
}

// ---------------------------------------------------------------------------
// The test
// ---------------------------------------------------------------------------

// TestWorkflowRunExecutesBugFixEndToEnd walks the built-in Bug Fix template from
// "press Run" to "Run completed", asserting each hop.
func TestWorkflowRunExecutesBugFixEndToEnd(t *testing.T) {
	withWorkflowEngineForTest(t)
	env := newWorkflowE2EEnv(t, "happy")

	const (
		title       = "Save button silently discards comment edits"
		description = "Editing a comment and pressing Save closes the editor with no request sent; the edit is lost on reload."
	)

	// --- 2. POST the run endpoint.
	w := env.startRun(t, title, description)
	if w.Code != http.StatusCreated {
		t.Fatalf("RunWorkflowTemplate: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	run := decodeWorkflowRunDetail(t, w, "RunWorkflowTemplate")
	if run.Status != "running" {
		t.Fatalf("run status = %q, want running", run.Status)
	}
	if run.TemplateKey != "bug_fix" {
		t.Fatalf("run template_key = %q, want bug_fix", run.TemplateKey)
	}
	if run.AccountableUserID == nil || *run.AccountableUserID != testUserID {
		t.Fatalf("accountable_user_id = %v, want the member who pressed Run", run.AccountableUserID)
	}
	if run.IssueID == nil {
		t.Fatalf("the run created no issue; a human has nothing to open")
	}
	var issueTitle, issueDescription string
	if err := testPool.QueryRow(context.Background(),
		`SELECT title, COALESCE(description, '') FROM issue WHERE id = $1 AND workspace_id = $2`,
		*run.IssueID, env.workspaceID,
	).Scan(&issueTitle, &issueDescription); err != nil {
		t.Fatalf("load the run's issue: %v", err)
	}
	if issueTitle != title || !strings.Contains(issueDescription, "no request sent") {
		t.Fatalf("issue = %q / %q, want the submitted title and description", issueTitle, issueDescription)
	}

	// --- 3 + 4. THE CENTRAL ASSERTIONS, on the entry step: it is queued to the
	// bug_analysis specialist with a readable routing reason, and its task carries
	// AND delivers a brief containing the instruction, the human's description, and
	// the submission contract.
	analyze, ok := findWorkflowStep(run.Steps, "analyze")
	if !ok {
		t.Fatalf("no analyze step in the trace: %+v", run.Steps)
	}
	if analyze.Status != "queued" {
		t.Fatalf("analyze status = %q, want queued (failure_reason=%v routing_reason=%v)",
			analyze.Status, analyze.FailureReason, analyze.RoutingReason)
	}
	if analyze.TaskID == nil {
		t.Fatalf("analyze queued no task; no agent was asked to do anything")
	}
	if analyze.AgentID == nil || *analyze.AgentID != env.analystID {
		t.Fatalf("analyze routed to %v, want the bug_analysis specialist %s", analyze.AgentID, env.analystID)
	}
	if analyze.AgentID != nil && *analyze.AgentID == env.implementerID {
		t.Fatalf("analyze routed to the code_change specialist; capability routing did not discriminate")
	}
	if analyze.RoutingReason == nil || !strings.Contains(*analyze.RoutingReason, "capability:bug_analysis") {
		t.Fatalf("analyze routing_reason = %v, want the capability that selected the agent", analyze.RoutingReason)
	}
	if analyze.AgentName == nil || *analyze.AgentName != "e2e-analyst" {
		t.Fatalf("analyze agent_name = %v, want e2e-analyst; the trace must name who did the work", analyze.AgentName)
	}

	claimedAnalyze := env.claimNextTask(t)
	if claimedAnalyze.ID != *analyze.TaskID {
		t.Fatalf("claimed %s, want the analyze task %s", claimedAnalyze.ID, *analyze.TaskID)
	}
	if claimedAnalyze.AgentID != env.analystID {
		t.Fatalf("the analyze task was claimed for agent %s, want the analyst %s", claimedAnalyze.AgentID, env.analystID)
	}
	if claimedAnalyze.WorkspaceID != env.workspaceID {
		t.Fatalf("claim workspace_id = %q, want %s", claimedAnalyze.WorkspaceID, env.workspaceID)
	}
	analyzeBrief := env.assertBriefIsComplete(t, "analyze", *analyze.TaskID, analyze.ID,
		"identify the root cause", description, claimedAnalyze)
	if analyzeBrief.UpstreamSummary != "" {
		t.Fatalf("the entry step has an upstream summary (%q); there is no prior step", analyzeBrief.UpstreamSummary)
	}
	// Recorded so the prompt an agent actually receives is inspectable from the
	// test log rather than only assertable in fragments.
	t.Logf("=== delivered prompt for analyze ===\n%s\n=== end prompt ===", claimedAnalyze.WorkflowPrompt)

	// --- 5. The agent finishes. Through TaskService, so the real terminal hook
	// fires and the run advances on its own.
	ctx := context.Background()
	if _, err := testHandler.TaskService.StartTask(ctx, parseUUID(claimedAnalyze.ID)); err != nil {
		t.Fatalf("StartTask(analyze): %v", err)
	}
	const analysisSummary = "the Save handler is bound to a detached DOM node after the editor re-renders, so the click never fires"
	if _, err := testHandler.TaskService.CompleteTask(ctx, parseUUID(claimedAnalyze.ID),
		e2eTaskResult(t, claimedAnalyze.ID, analyzeBrief.StepInstanceID, "analysis", analysisSummary),
		"", "", "", false, "", ""); err != nil {
		t.Fatalf("CompleteTask(analyze): %v", err)
	}

	afterAnalyze := env.getRun(t, run.ID)
	analyzeDone, _ := findWorkflowStep(afterAnalyze.Steps, "analyze")
	if analyzeDone.Status != "passed" {
		t.Fatalf("analyze status = %q after completion, want passed; the terminal hook did not advance the run (failure_reason=%v)",
			analyzeDone.Status, analyzeDone.FailureReason)
	}
	if analyzeDone.Submission == nil || analyzeDone.Submission.Verdict != "pass" {
		t.Fatalf("analyze submission = %+v, want a recorded pass", analyzeDone.Submission)
	}
	implement, ok := findWorkflowStep(afterAnalyze.Steps, "implement")
	if !ok {
		t.Fatalf("the run did not advance to implement: %+v", afterAnalyze.Steps)
	}
	if implement.Status != "queued" || implement.TaskID == nil {
		t.Fatalf("implement status = %q task=%v, want a queued step with its own task",
			implement.Status, implement.TaskID)
	}
	if implement.AgentID == nil || *implement.AgentID != env.implementerID {
		t.Fatalf("implement routed to %v, want the code_change specialist %s", implement.AgentID, env.implementerID)
	}

	// --- 6. Drive implement and validate the same way. The handoff is the point:
	// implement must be told what analyze found, or it re-derives it.
	claimedImplement := env.claimNextTask(t)
	if claimedImplement.ID != *implement.TaskID {
		t.Fatalf("claimed %s, want the implement task %s", claimedImplement.ID, *implement.TaskID)
	}
	implementBrief := env.assertBriefIsComplete(t, "implement", *implement.TaskID, implement.ID,
		"Apply the minimal change", description, claimedImplement)
	if implementBrief.UpstreamNodeKey != "analyze" {
		t.Fatalf("implement brief upstream_node_key = %q, want analyze", implementBrief.UpstreamNodeKey)
	}
	if !strings.Contains(implementBrief.UpstreamSummary, "detached DOM node") {
		t.Fatalf("implement brief upstream_summary = %q; the implementer was not given the analysis it must act on",
			implementBrief.UpstreamSummary)
	}
	if !strings.Contains(claimedImplement.WorkflowPrompt, "detached DOM node") {
		t.Fatalf("the delivered implement prompt does not carry the analysis:\n%s", claimedImplement.WorkflowPrompt)
	}
	if _, err := testHandler.TaskService.StartTask(ctx, parseUUID(claimedImplement.ID)); err != nil {
		t.Fatalf("StartTask(implement): %v", err)
	}
	if _, err := testHandler.TaskService.CompleteTask(ctx, parseUUID(claimedImplement.ID),
		e2eTaskResult(t, claimedImplement.ID, implementBrief.StepInstanceID, "code_change",
			"rebound the Save handler after re-render and added a regression test"),
		"", "", "", false, "", ""); err != nil {
		t.Fatalf("CompleteTask(implement): %v", err)
	}

	// validate routes previous_step from implement, so it must land on the SAME
	// agent that wrote the code. That is a different code path in the router than
	// capability matching, and it is the reason the fixture has two agents.
	env.finishStep(t, run.ID, "validate", env.implementerID,
		"build and test suite", description, "test_report",
		"ran build and the full suite: 1 new regression test, all green")

	beforeAcceptance := env.getRun(t, run.ID)
	if beforeAcceptance.Status != "waiting_acceptance" {
		t.Fatalf("run status = %q after validate, want waiting_acceptance; agent success is not business acceptance",
			beforeAcceptance.Status)
	}
	validateStep, _ := findWorkflowStep(beforeAcceptance.Steps, "validate")
	if validateStep.RoutingReason == nil || !strings.Contains(*validateStep.RoutingReason, "previous_step:implement") {
		t.Fatalf("validate routing_reason = %v, want the previous_step reason", validateStep.RoutingReason)
	}
	if beforeAcceptance.Acceptance == nil {
		t.Fatalf("no acceptance gate on a run waiting for acceptance")
	}
	gate := beforeAcceptance.Acceptance
	if gate.Status != "pending" {
		t.Fatalf("acceptance status = %q, want pending", gate.Status)
	}
	if len(gate.Criteria) != 2 {
		t.Fatalf("acceptance criteria = %+v, want the two the pinned graph declares", gate.Criteria)
	}
	if len(gate.ReworkTargets) != 3 {
		t.Fatalf("acceptance rework_targets = %+v, want analyze/implement/validate", gate.ReworkTargets)
	}
	acceptanceStep, ok := findWorkflowStep(beforeAcceptance.Steps, "acceptance")
	if !ok || acceptanceStep.Status != "waiting_acceptance" {
		t.Fatalf("acceptance step = %+v, want a step waiting on the human", acceptanceStep)
	}
	// No agent task for a human gate: an acceptance node that dispatched work would
	// mean the "a human decides" seam does not exist.
	if acceptanceStep.TaskID != nil {
		t.Fatalf("the acceptance gate queued an agent task (%v); acceptance is a human decision", acceptanceStep.TaskID)
	}

	// --- 7. Accept through the endpoint the reviewer's button hits.
	aw := httptest.NewRecorder()
	testHandler.DecideWorkflowAcceptance(aw, withURLParam(
		env.request("POST", "/api/workflow-runs/"+run.ID+"/acceptance", map[string]any{"accept": true}),
		"id", run.ID,
	))
	if aw.Code != http.StatusOK {
		t.Fatalf("accept: expected 200, got %d: %s", aw.Code, aw.Body.String())
	}
	accepted := decodeWorkflowRunDetail(t, aw, "accept")
	if accepted.Status != "completed" {
		t.Fatalf("run status = %q after acceptance, want completed", accepted.Status)
	}
	if accepted.CompletedAt == nil {
		t.Fatalf("a completed run must record completed_at")
	}
	if accepted.Acceptance == nil || accepted.Acceptance.Status != "accepted" {
		t.Fatalf("acceptance = %+v, want accepted", accepted.Acceptance)
	}
	endStep, ok := findWorkflowStep(accepted.Steps, "end")
	if !ok {
		t.Fatalf("the run completed without traversing the End node: %+v", accepted.Steps)
	}
	if endStep.Status != "passed" {
		t.Fatalf("end step status = %q, want passed", endStep.Status)
	}
	// completeAtEnd is the ONLY writer of run.completed, and it records this event
	// naming the End node it passed through. Asserting the event (not just the
	// status) is what distinguishes "completed through End" from "something else set
	// the status".
	events, err := testHandler.Queries.ListWorkflowEventsForRun(ctx, db.ListWorkflowEventsForRunParams{
		RunID:       parseUUID(run.ID),
		WorkspaceID: parseUUID(env.workspaceID),
	})
	if err != nil {
		t.Fatalf("list workflow events: %v", err)
	}
	completedViaEnd := false
	for _, ev := range events {
		if ev.EventType != workflow.EventRunCompleted {
			continue
		}
		var payload struct {
			EndNode string `json:"end_node"`
		}
		if json.Unmarshal(ev.Payload, &payload) == nil && payload.EndNode == "end" {
			completedViaEnd = true
		}
	}
	if !completedViaEnd {
		t.Fatalf("no run.completed event naming the End node; the run did not complete through End")
	}

	// Every Agent step ran, each with a submission a reviewer can read.
	for _, nodeKey := range []string{"analyze", "implement", "validate"} {
		step, ok := findWorkflowStep(accepted.Steps, nodeKey)
		if !ok {
			t.Fatalf("%s is missing from the finished trace", nodeKey)
		}
		if step.Status != "passed" {
			t.Fatalf("%s status = %q in the finished trace, want passed", nodeKey, step.Status)
		}
		if step.Submission == nil || step.Submission.Verdict != "pass" {
			t.Fatalf("%s carries no pass submission: %+v", nodeKey, step.Submission)
		}
	}
	// Nothing may still be claimable: a completed run that left a queued task would
	// send an agent to work on an accepted defect.
	leftover := httptest.NewRecorder()
	leftoverReq := withURLParam(
		newDaemonTokenRequest("POST", "/api/daemon/runtimes/"+env.runtimeID+"/tasks/claim", nil,
			env.workspaceID, "workflow-e2e-daemon"),
		"runtimeId", env.runtimeID)
	testHandler.ClaimTaskByRuntime(leftover, leftoverReq)
	if leftover.Code == http.StatusOK && !strings.Contains(leftover.Body.String(), `"task":null`) {
		t.Fatalf("a completed run left a claimable task behind: %s", leftover.Body.String())
	}
}

// TestWorkflowRunProseReplyBlocksTheStep is the negative that protects the
// contract, and it is not a corner case: it is what EVERY step did before the
// brief carried the submission instructions.
//
// An agent that answers in prose has its step BLOCKED with
// submission_contract_invalid and the run does NOT advance. Not failed (the work
// may well have been correct) and emphatically not passed - inferring success from
// "I fixed it" is the one thing the engine must never do, because a false pass is
// trusted by every downstream step and by the human reviewer.
func TestWorkflowRunProseReplyBlocksTheStep(t *testing.T) {
	withWorkflowEngineForTest(t)
	env := newWorkflowE2EEnv(t, "prose")
	ctx := context.Background()

	w := env.startRun(t, "Prose reply must not pass",
		"The agent will answer in prose with no submission block; the step must block rather than advance.")
	if w.Code != http.StatusCreated {
		t.Fatalf("RunWorkflowTemplate: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	run := decodeWorkflowRunDetail(t, w, "RunWorkflowTemplate")
	analyze, ok := findWorkflowStep(run.Steps, "analyze")
	if !ok || analyze.TaskID == nil {
		t.Fatalf("no queued analyze task: %+v", run.Steps)
	}

	claimed := env.claimNextTask(t)
	if _, err := testHandler.TaskService.StartTask(ctx, parseUUID(claimed.ID)); err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	// Confident, plausible, and entirely unparseable. The wording matters: if any
	// part of the system inferred a verdict from prose, this is the text that would
	// fool it.
	result, err := json.Marshal(map[string]any{
		"task_id": claimed.ID,
		"output": "I reproduced the issue and fixed it. The root cause was a stale event binding. " +
			"Everything passes now, so this step is complete and successful.",
	})
	if err != nil {
		t.Fatalf("marshal task result: %v", err)
	}
	if _, err := testHandler.TaskService.CompleteTask(ctx, parseUUID(claimed.ID), result, "", "", "", false, "", ""); err != nil {
		t.Fatalf("CompleteTask: %v", err)
	}

	after := env.getRun(t, run.ID)
	blocked, _ := findWorkflowStep(after.Steps, "analyze")
	if blocked.Status != "blocked" {
		t.Fatalf("analyze status = %q after a prose reply, want blocked", blocked.Status)
	}
	if blocked.FailureReason == nil || *blocked.FailureReason != workflow.ReasonSubmissionContractInvalid {
		t.Fatalf("analyze failure_reason = %v, want %s", blocked.FailureReason, workflow.ReasonSubmissionContractInvalid)
	}
	if after.Status != "blocked" {
		t.Fatalf("run status = %q, want blocked", after.Status)
	}
	if after.BlockedReason == nil || *after.BlockedReason != workflow.ReasonSubmissionContractInvalid {
		t.Fatalf("run blocked_reason = %v, want %s", after.BlockedReason, workflow.ReasonSubmissionContractInvalid)
	}
	// The run must NOT have advanced. This is the assertion that matters: a step
	// that blocked but still activated its successor would have handed the next
	// agent an artifact that does not exist.
	if _, ok := findWorkflowStep(after.Steps, "implement"); ok {
		t.Fatalf("a blocked step still advanced the run to implement: %+v", after.Steps)
	}
	// The refused output is retained as evidence, with the reasons, or a human
	// cannot tell a formatting mistake from a broken agent.
	if blocked.Submission == nil {
		t.Fatalf("the refused reply was not recorded; a human has nothing to inspect")
	}
	if blocked.Submission.RawResult == nil || !strings.Contains(*blocked.Submission.RawResult, "I reproduced the issue") {
		t.Fatal("rejected agent reply is missing from the run detail API")
	}
	if blocked.Submission.Verdict != "blocked" {
		t.Fatalf("recorded verdict = %q, want blocked", blocked.Submission.Verdict)
	}
	if len(blocked.Submission.ValidationErrors) == 0 {
		t.Fatalf("the blocked submission carries no validation errors: %+v", blocked.Submission)
	}
	var problems []string
	if err := json.Unmarshal(blocked.Submission.ValidationErrors, &problems); err != nil || len(problems) == 0 {
		t.Fatalf("validation_errors = %s, want the reasons the payload was refused", string(blocked.Submission.ValidationErrors))
	}
	// And no second run of the same node was opened: blocking is terminal until a
	// human acts, not an automatic retry.
	attempts := 0
	for _, s := range after.Steps {
		if s.NodeKey == "analyze" {
			attempts++
		}
	}
	if attempts != 1 {
		t.Fatalf("analyze has %d attempts after a blocked step, want 1", attempts)
	}
}

func TestWorkflowStructuredClarificationRecordsBlockedVerdict(t *testing.T) {
	withWorkflowEngineForTest(t)
	env := newWorkflowE2EEnv(t, "structured_clarification")
	ctx := context.Background()
	run := decodeWorkflowRunDetail(t, env.startRun(t, "Change workflow connections", "Analyze the requested visual changes"), "start")
	step, _ := findWorkflowStep(run.Steps, "analyze")
	claimed := env.claimNextTask(t)
	if _, err := testHandler.TaskService.StartTask(ctx, parseUUID(claimed.ID)); err != nil {
		t.Fatal(err)
	}
	reply, _ := json.Marshal(map[string]any{"schema_version": 1, "step_instance_id": step.ID, "verdict": "blocked", "artifact": map[string]any{"type": "analysis", "summary": "Need clarification", "references": []string{}}, "rationale": "Should connection changes affect execution order?"})
	envelope, _ := json.Marshal(map[string]any{"task_id": claimed.ID, "output": string(reply)})
	if _, err := testHandler.TaskService.CompleteTask(ctx, parseUUID(claimed.ID), envelope, "", "", "", false, "", ""); err != nil {
		t.Fatal(err)
	}
	after := env.getRun(t, run.ID)
	blocked, _ := findWorkflowStep(after.Steps, "analyze")
	if after.BlockedReason == nil || *after.BlockedReason != workflow.ReasonAgentVerdictBlocked {
		t.Fatalf("wrong reason: %+v", after.BlockedReason)
	}
	if blocked.Submission == nil || blocked.Submission.Rationale != "Should connection changes affect execution order?" || blocked.Submission.RawResult == nil {
		t.Fatalf("clarification lost: %+v", blocked.Submission)
	}
	if _, ok := findWorkflowStep(after.Steps, "implement"); ok {
		t.Fatal("clarification must not advance the workflow")
	}
}
