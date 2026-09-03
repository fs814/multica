package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/workflow"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// Tests for the run + acceptance REST surface.
//
// These drive the real engine against a real Postgres rather than a fake,
// because every property worth asserting here is a property of the seam between
// the HTTP layer and the engine's transactions: that a Run and its Issue commit
// together, that a double-clicked button does not start two Runs, that cancelling
// a Run also stops the agent it started. A fake engine would let the tests assert
// the handler's beliefs about those things instead of the things.

// ---------------------------------------------------------------------------
// Fixture
// ---------------------------------------------------------------------------

// withWorkflowEngineForTest wires a real engine onto the shared test handler for
// the duration of one test.
//
// testHandler is built by handler.New, which does NOT construct the engine -
// cmd/server/router.go assigns it afterwards, because the engine needs the
// TaskService that New produces. Reproducing exactly that wiring here (router,
// notifier, and the WorkflowTerminal back-edge) is the point: a test against a
// differently-wired engine would pass while production stalled.
//
// Restored to nil on cleanup so unrelated tests still exercise the nil-engine
// path, which is what the 503 guard exists for.
func withWorkflowEngineForTest(t *testing.T) *workflow.Engine {
	t.Helper()
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	engine := &workflow.Engine{
		Queries:   testHandler.Queries,
		TxStarter: testPool,
		Router:    service.NewWorkflowRouter(testHandler.Queries),
		Notifier:  service.NewWorkflowNotifier(testHandler.Bus, testHandler.TaskService),
		Schemas:   workflow.DefaultSchemaRegistry,
	}
	previousEngine := testHandler.WorkflowEngine
	previousTerminal := testHandler.TaskService.WorkflowTerminal
	testHandler.WorkflowEngine = engine
	// Without this a finished workflow task never advances its Run. The
	// acceptance tests below reach the gate through node activation rather than
	// task completion, but wiring it keeps the fixture honest about production.
	testHandler.TaskService.WorkflowTerminal = engine
	t.Cleanup(func() {
		testHandler.WorkflowEngine = previousEngine
		testHandler.TaskService.WorkflowTerminal = previousTerminal
	})
	return engine
}

// cleanupWorkflowRuns removes every workflow row plus the issues and agent tasks
// a Run created.
//
// The workflow tables carry no foreign keys by design (plan section 4), so
// dropping a workspace does not remove them and neither does deleting an issue.
// Ordered child-first so a partially-failed cleanup still leaves no row whose
// parent is gone.
func cleanupWorkflowRuns(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		ctx := context.Background()
		for _, stmt := range []string{
			`DELETE FROM agent_task_queue WHERE workflow_step_instance_id IN
				(SELECT id FROM workflow_step_instance WHERE workspace_id = $1)`,
			`DELETE FROM workflow_callback_delivery WHERE workspace_id = $1`,
			`DELETE FROM workflow_callback_destination WHERE workspace_id = $1`,
			`DELETE FROM workflow_event WHERE workspace_id = $1`,
			`DELETE FROM workflow_acceptance WHERE workspace_id = $1`,
			`DELETE FROM workflow_submission WHERE workspace_id = $1`,
			`DELETE FROM workflow_step_instance WHERE workspace_id = $1`,
			`DELETE FROM issue WHERE id IN (SELECT issue_id FROM workflow_run WHERE workspace_id = $1 AND issue_id IS NOT NULL)`,
			`DELETE FROM workflow_run WHERE workspace_id = $1`,
			`DELETE FROM workflow_template_version WHERE workspace_id = $1`,
			`DELETE FROM workflow_template WHERE workspace_id = $1`,
		} {
			if _, err := testPool.Exec(ctx, stmt, testWorkspaceID); err != nil {
				t.Logf("cleanup %q: %v", stmt, err)
			}
		}
	})
}

// labelTestAgentWithCapabilities makes the fixture agent routable for the
// built-in graph's capability nodes.
//
// The router matches an agent's LABELS against the node's capability (the product
// decision: labels are the capability vocabulary), so without these rows every
// Agent step of the built-in blocks with routing_no_candidate and the run tests
// would be asserting the failure path. resource_type='agent' is required -
// ListLabelsForAgents filters on it, so an issue-scoped label with the same name
// would be invisible to routing.
func labelTestAgentWithCapabilities(t *testing.T, capabilities ...string) string {
	t.Helper()
	ctx := context.Background()

	var agentID string
	if err := testPool.QueryRow(ctx,
		`SELECT id FROM agent WHERE workspace_id = $1 AND archived_at IS NULL AND kind = 'user'
		 ORDER BY created_at ASC LIMIT 1`,
		testWorkspaceID,
	).Scan(&agentID); err != nil {
		t.Fatalf("load fixture agent: %v", err)
	}

	for _, capability := range capabilities {
		var labelID string
		if err := testPool.QueryRow(ctx, `
			INSERT INTO issue_label (workspace_id, resource_type, name, color)
			VALUES ($1, 'agent', $2, '#336699')
			RETURNING id
		`, testWorkspaceID, capability).Scan(&labelID); err != nil {
			t.Fatalf("insert capability label %q: %v", capability, err)
		}
		if _, err := testPool.Exec(ctx,
			`INSERT INTO agent_to_label (agent_id, label_id) VALUES ($1, $2)`,
			agentID, labelID,
		); err != nil {
			t.Fatalf("attach capability label %q: %v", capability, err)
		}
		t.Cleanup(func() {
			bg := context.Background()
			testPool.Exec(bg, `DELETE FROM agent_to_label WHERE label_id = $1`, labelID)
			testPool.Exec(bg, `DELETE FROM issue_label WHERE id = $1`, labelID)
		})
	}
	return agentID
}

// seededBugFixTemplate returns the workspace's built-in Bug Fix template, seeding
// it through the same GET the workflows page uses.
func seededBugFixTemplate(t *testing.T) WorkflowTemplateResponse {
	t.Helper()
	tpl, ok := findWorkflowTemplateByKey(listWorkflowTemplatesForTest(t).Templates, "bug_fix")
	if !ok {
		t.Fatalf("bug_fix was not seeded")
	}
	return tpl
}

// runWorkflowTemplateForTest posts the run request and returns the recorder.
func runWorkflowTemplateForTest(t *testing.T, templateID string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	req := withURLParam(newRequest("POST", "/api/workflow-templates/"+templateID+"/run", body), "id", templateID)
	testHandler.RunWorkflowTemplate(w, req)
	return w
}

func decodeWorkflowRunDetail(t *testing.T, w *httptest.ResponseRecorder, what string) WorkflowRunDetailResponse {
	t.Helper()
	var detail WorkflowRunDetailResponse
	if err := json.NewDecoder(w.Body).Decode(&detail); err != nil {
		t.Fatalf("%s: decode body: %v", what, err)
	}
	return detail
}

func getWorkflowRunForTest(t *testing.T, runID string) WorkflowRunDetailResponse {
	t.Helper()
	w := httptest.NewRecorder()
	req := withURLParam(newRequest("GET", "/api/workflow-runs/"+runID, nil), "id", runID)
	testHandler.GetWorkflowRun(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GetWorkflowRun: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	return decodeWorkflowRunDetail(t, w, "GetWorkflowRun")
}

func findWorkflowStep(steps []WorkflowStepResponse, nodeKey string) (WorkflowStepResponse, bool) {
	for _, s := range steps {
		if s.NodeKey == nodeKey {
			return s, true
		}
	}
	return WorkflowStepResponse{}, false
}

func findLatestWorkflowStep(steps []WorkflowStepResponse, nodeKey string) (WorkflowStepResponse, bool) {
	for i := len(steps) - 1; i >= 0; i-- {
		if steps[i].NodeKey == nodeKey {
			return steps[i], true
		}
	}
	return WorkflowStepResponse{}, false
}

// ---------------------------------------------------------------------------
// POST /api/workflow-templates/{id}/run
// ---------------------------------------------------------------------------

// TestWorkflowRunCreatesIssueAndQueuesFirstStep is the whole feature in one
// assertion set: pressing Run must produce an Issue, a Run, and a QUEUED first
// Step bound to an Agent Task carrying a prompt.
//
// The queued step with a task is the part that matters. Every earlier phase of
// this feature could produce a Run row that looked correct while no agent was
// ever asked to do anything, and that failure is invisible from the Run's status
// alone - it stays "running" forever.
func TestWorkflowRunCreatesIssueAndQueuesFirstStep(t *testing.T) {
	cleanupWorkflowTemplates(t)
	cleanupWorkflowRuns(t)
	withWorkflowEngineForTest(t)
	agentID := labelTestAgentWithCapabilities(t, "bug_analysis", "code_change")

	tpl := seededBugFixTemplate(t)
	w := runWorkflowTemplateForTest(t, tpl.ID, map[string]any{
		"title":       "Save button silently discards edits",
		"description": "Editing a comment and pressing Save closes the editor with no request sent.",
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("RunWorkflowTemplate: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	detail := decodeWorkflowRunDetail(t, w, "RunWorkflowTemplate")

	if detail.Status != "running" {
		t.Fatalf("run status = %q, want running", detail.Status)
	}
	if detail.Source != "manual" {
		t.Fatalf("run source = %q, want manual", detail.Source)
	}
	// The list-shaped fields must be populated on the create response too: the
	// client drops this body into its cache instead of refetching.
	if detail.TemplateKey != "bug_fix" || detail.TemplateName != "Bug Fix" {
		t.Fatalf("template labels = %q/%q, want bug_fix/Bug Fix", detail.TemplateKey, detail.TemplateName)
	}
	if detail.AccountableUserID == nil || *detail.AccountableUserID != testUserID {
		t.Fatalf("accountable_user_id = %v, want the member who pressed Run (%s)", detail.AccountableUserID, testUserID)
	}

	// The Issue must exist and carry the submitted title/description: it is what
	// a human opens to see what was asked for.
	if detail.IssueID == nil {
		t.Fatalf("run has no issue_id; the run endpoint must create one")
	}
	var issueTitle, issueDescription, issueStatus string
	var assigneeType *string
	var issueNumber int
	if err := testPool.QueryRow(context.Background(),
		`SELECT title, COALESCE(description, ''), status, assignee_type, number FROM issue WHERE id = $1`,
		*detail.IssueID,
	).Scan(&issueTitle, &issueDescription, &issueStatus, &assigneeType, &issueNumber); err != nil {
		t.Fatalf("load the run's issue: %v", err)
	}
	if issueTitle != "Save button silently discards edits" {
		t.Fatalf("issue title = %q", issueTitle)
	}
	if !strings.Contains(issueDescription, "no request sent") {
		t.Fatalf("issue description = %q, want the submitted description", issueDescription)
	}
	if issueStatus != "in_progress" {
		t.Fatalf("issue status = %q, want in_progress while the workflow is running", issueStatus)
	}
	// No assignee, deliberately: an assigned issue would make the ordinary issue
	// listener enqueue a SECOND, non-workflow task for the same work.
	if assigneeType != nil {
		t.Fatalf("issue assignee_type = %q; a workflow issue must be unassigned so the run owns routing", *assigneeType)
	}
	if issueNumber <= 0 {
		t.Fatalf("issue number = %d; the run's issue must get a real workspace number", issueNumber)
	}
	var ownerSubscriptions int
	if err := testPool.QueryRow(context.Background(), `
		SELECT count(*) FROM issue_subscriber
		WHERE issue_id = $1 AND user_type = 'member' AND user_id = $2
		  AND reason = 'manual' AND unsubscribed_at IS NULL`, *detail.IssueID, testUserID).Scan(&ownerSubscriptions); err != nil {
		t.Fatalf("count workflow owner subscriptions: %v", err)
	}
	if ownerSubscriptions != 1 {
		t.Fatalf("workflow owner subscriptions = %d, want one durable issue subscription", ownerSubscriptions)
	}

	// The run's input is the agent's brief. An empty bag here means every step's
	// prompt names a generic instruction with no defect attached.
	var runInput workflow.RunInput
	if err := json.Unmarshal(detail.Input, &runInput); err != nil {
		t.Fatalf("run input is not an object: %v (%s)", err, string(detail.Input))
	}
	if runInput.Title == "" || !strings.Contains(runInput.Description, "no request sent") {
		t.Fatalf("run input = %+v, want the submitted title and description", runInput)
	}

	// The entry step must be queued to the labelled agent, with a bound task.
	analyze, ok := findWorkflowStep(detail.Steps, "analyze")
	if !ok {
		t.Fatalf("no analyze step in the trace: %+v", detail.Steps)
	}
	if analyze.Status != "queued" {
		t.Fatalf("analyze step status = %q, want queued (failure_reason=%v routing_reason=%v)",
			analyze.Status, analyze.FailureReason, analyze.RoutingReason)
	}
	if analyze.AgentID == nil || *analyze.AgentID != agentID {
		t.Fatalf("analyze agent = %v, want the bug_analysis-labelled agent %s", analyze.AgentID, agentID)
	}
	if analyze.AgentName == nil || *analyze.AgentName == "" {
		t.Fatalf("analyze step has no agent_name; the trace must name who did the work")
	}
	if analyze.RoutingReason == nil || !strings.Contains(*analyze.RoutingReason, "capability:bug_analysis") {
		t.Fatalf("routing reason = %v, want the capability that selected the agent", analyze.RoutingReason)
	}
	if analyze.TaskID == nil {
		t.Fatalf("analyze step has no task_id; no agent was asked to do anything")
	}
	if detail.CurrentNodeKey == nil || *detail.CurrentNodeKey != "analyze" {
		t.Fatalf("current_node_key = %v, want analyze", detail.CurrentNodeKey)
	}
	if detail.StepCount != len(detail.Steps) {
		t.Fatalf("step_count = %d but trace has %d steps; the list and detail views disagree",
			detail.StepCount, len(detail.Steps))
	}

	// The queued task must carry the brief. A task with no prompt is worse than
	// no task: the agent claims it and has nothing to work on.
	var taskContext []byte
	var taskStatus string
	if err := testPool.QueryRow(context.Background(),
		`SELECT context, status FROM agent_task_queue WHERE id = $1`, *analyze.TaskID,
	).Scan(&taskContext, &taskStatus); err != nil {
		t.Fatalf("load the analyze task: %v", err)
	}
	if taskStatus != "queued" {
		t.Fatalf("agent task status = %q, want queued", taskStatus)
	}
	brief, ok := workflow.ParseTaskContext(taskContext)
	if !ok {
		t.Fatalf("the queued task carries no workflow brief: %s", string(taskContext))
	}
	if brief.Instruction == "" {
		t.Fatalf("brief has no instruction")
	}
	if !strings.Contains(brief.RunDescription, "no request sent") {
		t.Fatalf("brief run_description = %q, want the submitted description", brief.RunDescription)
	}
	if brief.SubmissionContract == "" {
		t.Fatalf("brief carries no submission contract; the agent would produce correct work with a rejected result")
	}

	// GET must agree with the create response.
	reread := getWorkflowRunForTest(t, detail.ID)
	if reread.ID != detail.ID || reread.Status != detail.Status || len(reread.Steps) != len(detail.Steps) {
		t.Fatalf("GET disagrees with the create response: %+v vs %+v", reread.WorkflowRunResponse, detail.WorkflowRunResponse)
	}

	// And the list must contain it, with a real total.
	lw := httptest.NewRecorder()
	testHandler.ListWorkflowRuns(lw, newRequest("GET", "/api/workflow-runs", nil))
	if lw.Code != http.StatusOK {
		t.Fatalf("ListWorkflowRuns: expected 200, got %d: %s", lw.Code, lw.Body.String())
	}
	var list struct {
		Runs  []WorkflowRunResponse `json:"runs"`
		Total int64                 `json:"total"`
	}
	if err := json.NewDecoder(lw.Body).Decode(&list); err != nil {
		t.Fatalf("ListWorkflowRuns: decode body: %v", err)
	}
	if list.Total < 1 {
		t.Fatalf("list total = %d, want at least 1", list.Total)
	}
	found := false
	for _, run := range list.Runs {
		if run.ID == detail.ID {
			found = true
			if run.TemplateName != "Bug Fix" || run.StepCount == 0 {
				t.Fatalf("list row is missing its summary fields: %+v", run)
			}
			if run.CurrentNodeKey == nil || *run.CurrentNodeKey != "analyze" {
				t.Fatalf("list current_node_key = %v, want analyze; the list and detail must agree", run.CurrentNodeKey)
			}
		}
	}
	if !found {
		t.Fatalf("the new run is missing from the list: %+v", list.Runs)
	}

	// A template_id filter that matches nothing must return an empty page rather
	// than the unfiltered list.
	fw := httptest.NewRecorder()
	const stranger = "00000000-0000-0000-0000-0000000000ff"
	testHandler.ListWorkflowRuns(fw, newRequest("GET", "/api/workflow-runs?template_id="+stranger, nil))
	if fw.Code != http.StatusOK {
		t.Fatalf("ListWorkflowRuns(filtered): expected 200, got %d: %s", fw.Code, fw.Body.String())
	}
	var filtered struct {
		Runs  []WorkflowRunResponse `json:"runs"`
		Total int64                 `json:"total"`
	}
	if err := json.NewDecoder(fw.Body).Decode(&filtered); err != nil {
		t.Fatalf("decode filtered list: %v", err)
	}
	if len(filtered.Runs) != 0 || filtered.Total != 0 {
		t.Fatalf("template_id filter did not apply: %d runs, total %d", len(filtered.Runs), filtered.Total)
	}
}

// TestWorkflowRunIsIdempotent pins the double-clicked-button case, which is the
// reason the idempotency key is derived server-side.
//
// Two runs would mean two sets of agent tasks doing the same work, two issues,
// and two competing sets of commits - and the user has no way to tell which Run
// to cancel. The second POST must return the FIRST Run.
func TestWorkflowOwnedIssueStatusRequiresRunCancellation(t *testing.T) {
	cleanupWorkflowTemplates(t)
	cleanupWorkflowRuns(t)
	withWorkflowEngineForTest(t)
	labelTestAgentWithCapabilities(t, "bug_analysis", "code_change")

	tpl := seededBugFixTemplate(t)
	started := runWorkflowTemplateForTest(t, tpl.ID, map[string]any{
		"title":       "Guard the workflow issue lifecycle",
		"description": "An active run must remain the authority for its issue status.",
	})
	if started.Code != http.StatusCreated {
		t.Fatalf("RunWorkflowTemplate: expected 201, got %d: %s", started.Code, started.Body.String())
	}
	detail := decodeWorkflowRunDetail(t, started, "RunWorkflowTemplate")
	if detail.IssueID == nil {
		t.Fatal("workflow run has no issue")
	}

	done := httptest.NewRecorder()
	doneReq := withURLParam(newRequest("PUT", "/api/issues/"+*detail.IssueID, map[string]any{"status": "done"}), "id", *detail.IssueID)
	testHandler.UpdateIssue(done, doneReq)
	if done.Code != http.StatusConflict || !strings.Contains(done.Body.String(), "workflow_run_active") {
		t.Fatalf("direct done update = %d %s, want workflow_run_active conflict", done.Code, done.Body.String())
	}

	batch := httptest.NewRecorder()
	batchReq := newRequest("PATCH", "/api/issues/batch?workspace_id="+testWorkspaceID, map[string]any{
		"issue_ids": []string{*detail.IssueID},
		"updates":   map[string]any{"status": "done"},
	})
	testHandler.BatchUpdateIssues(batch, batchReq)
	if batch.Code != http.StatusConflict || !strings.Contains(batch.Body.String(), "workflow_run_active") {
		t.Fatalf("batch done update = %d %s, want workflow_run_active conflict", batch.Code, batch.Body.String())
	}

	cancelled := httptest.NewRecorder()
	cancelReq := withURLParam(newRequest("PUT", "/api/issues/"+*detail.IssueID, map[string]any{"status": "cancelled"}), "id", *detail.IssueID)
	testHandler.UpdateIssue(cancelled, cancelReq)
	if cancelled.Code != http.StatusOK {
		t.Fatalf("issue cancellation = %d %s, want 200", cancelled.Code, cancelled.Body.String())
	}
	got := getWorkflowRunForTest(t, detail.ID)
	if got.Status != "cancelled" {
		t.Fatalf("workflow run status = %q after issue cancellation, want cancelled", got.Status)
	}
}

func TestWorkflowIntakeReturnsStableReceiptAndRejectsConflictingReplay(t *testing.T) {
	cleanupWorkflowTemplates(t)
	cleanupWorkflowRuns(t)
	withWorkflowEngineForTest(t)
	labelTestAgentWithCapabilities(t, "bug_analysis", "code_change")
	tpl := seededBugFixTemplate(t)

	var destinationID string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO workflow_callback_destination (
			workspace_id, name, url, signing_secret_encrypted, created_by_user_id
		) VALUES ($1, $2, 'https://callbacks.example.test/workflows', $3, $4)
		RETURNING id`, testWorkspaceID, "intake callback "+time.Now().Format(time.RFC3339Nano), []byte("encrypted-test-secret"), testUserID).Scan(&destinationID); err != nil {
		t.Fatalf("create intake callback destination: %v", err)
	}
	body := map[string]any{
		"source": "ticketing", "event_id": "ticket-123", "template_key": tpl.Key,
		"title": "External ticket 123", "description": "The same event must always resolve to the same workflow receipt.",
		"source_url": "https://tickets.example.test/123", "payload": map[string]any{"severity": "high"},
		"callback_destination_id": destinationID,
	}
	post := func(payload map[string]any) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := withURLParam(newRequest("POST", "/api/workspaces/"+testWorkspaceID+"/workflow-intake", payload), "id", testWorkspaceID)
		testHandler.WorkflowIntake(w, req)
		return w
	}

	first := post(body)
	if first.Code != http.StatusCreated {
		t.Fatalf("first intake = %d %s, want 201", first.Code, first.Body.String())
	}
	var receipt WorkflowIntakeResponse
	if err := json.Unmarshal(first.Body.Bytes(), &receipt); err != nil {
		t.Fatalf("decode intake receipt: %v", err)
	}
	if receipt.ReceiptID == "" || receipt.WorkflowRunID != receipt.ReceiptID || receipt.IssueID == "" || receipt.TemplateKey != tpl.Key {
		t.Fatalf("incomplete intake receipt: %+v", receipt)
	}

	replay := post(body)
	if replay.Code != http.StatusOK {
		t.Fatalf("identical intake replay = %d %s, want 200", replay.Code, replay.Body.String())
	}
	var replayReceipt WorkflowIntakeResponse
	_ = json.Unmarshal(replay.Body.Bytes(), &replayReceipt)
	if replayReceipt.WorkflowRunID != receipt.WorkflowRunID || replayReceipt.IssueID != receipt.IssueID {
		t.Fatalf("intake replay changed identity: first=%+v replay=%+v", receipt, replayReceipt)
	}

	conflictingBody := make(map[string]any, len(body))
	for key, value := range body {
		conflictingBody[key] = value
	}
	conflictingBody["title"] = "A different request reusing ticket-123"
	conflict := post(conflictingBody)
	if conflict.Code != http.StatusConflict || !strings.Contains(conflict.Body.String(), "idempotency_conflict") {
		t.Fatalf("conflicting intake replay = %d %s, want idempotency conflict", conflict.Code, conflict.Body.String())
	}

	var source, sourceEventID string
	if err := testPool.QueryRow(context.Background(), `
		SELECT source, source_event_id FROM workflow_run WHERE id = $1`, receipt.WorkflowRunID).Scan(&source, &sourceEventID); err != nil {
		t.Fatalf("load intake workflow provenance: %v", err)
	}
	if source != "external" || sourceEventID != "ticket-123" {
		t.Fatalf("workflow provenance = %q/%q, want external/ticket-123", source, sourceEventID)
	}
	var callbackCount int
	if err := testPool.QueryRow(context.Background(), `
		SELECT count(*) FROM workflow_callback_delivery
		WHERE workflow_run_id = $1 AND event_type = 'run.started'`, receipt.WorkflowRunID).Scan(&callbackCount); err != nil {
		t.Fatalf("count intake callbacks: %v", err)
	}
	if callbackCount != 1 {
		t.Fatalf("intake run.started callbacks = %d, want one after replay", callbackCount)
	}
}

func TestWorkflowRunIsIdempotent(t *testing.T) {
	cleanupWorkflowTemplates(t)
	cleanupWorkflowRuns(t)
	withWorkflowEngineForTest(t)
	labelTestAgentWithCapabilities(t, "bug_analysis", "code_change")

	tpl := seededBugFixTemplate(t)
	body := map[string]any{
		"title":       "Duplicate submit starts two runs",
		"description": "Pressing the Run button twice in quick succession.",
	}

	first := runWorkflowTemplateForTest(t, tpl.ID, body)
	if first.Code != http.StatusCreated {
		t.Fatalf("first run: expected 201, got %d: %s", first.Code, first.Body.String())
	}
	firstDetail := decodeWorkflowRunDetail(t, first, "first run")

	second := runWorkflowTemplateForTest(t, tpl.ID, body)
	// 200, not 201: nothing was created the second time.
	if second.Code != http.StatusOK {
		t.Fatalf("second run: expected 200 (replay), got %d: %s", second.Code, second.Body.String())
	}
	secondDetail := decodeWorkflowRunDetail(t, second, "second run")
	if secondDetail.ID != firstDetail.ID {
		t.Fatalf("a replayed run started a second run: %s then %s", firstDetail.ID, secondDetail.ID)
	}

	var runCount int
	if err := testPool.QueryRow(context.Background(),
		`SELECT count(*) FROM workflow_run WHERE workspace_id = $1 AND template_id = $2`,
		testWorkspaceID, tpl.ID,
	).Scan(&runCount); err != nil {
		t.Fatalf("count runs: %v", err)
	}
	if runCount != 1 {
		t.Fatalf("workflow_run rows = %d, want exactly 1 after a double submit", runCount)
	}

	// The replay must not have left an orphan issue either. This is the reason the
	// handler reads the idempotency key BEFORE creating the issue rather than
	// relying on StartRun's own dedup.
	var issueCount int
	if err := testPool.QueryRow(context.Background(),
		`SELECT count(*) FROM issue WHERE workspace_id = $1 AND title = $2`,
		testWorkspaceID, body["title"],
	).Scan(&issueCount); err != nil {
		t.Fatalf("count issues: %v", err)
	}
	if issueCount != 1 {
		t.Fatalf("issue rows = %d, want exactly 1; the replay created an issue with no run", issueCount)
	}

	// A DIFFERENT description is different work and must start its own run: the
	// key is derived from content, so it must discriminate on content.
	third := runWorkflowTemplateForTest(t, tpl.ID, map[string]any{
		"title":       "Duplicate submit starts two runs",
		"description": "A genuinely different report about the same button.",
	})
	if third.Code != http.StatusCreated {
		t.Fatalf("third run (new description): expected 201, got %d: %s", third.Code, third.Body.String())
	}
	thirdDetail := decodeWorkflowRunDetail(t, third, "third run")
	if thirdDetail.ID == firstDetail.ID {
		t.Fatalf("a different description was deduped onto the first run")
	}
}

func TestWorkflowRunExplicitIdempotencyKeyRejectsChangedPayload(t *testing.T) {
	cleanupWorkflowTemplates(t)
	cleanupWorkflowRuns(t)
	withWorkflowEngineForTest(t)
	labelTestAgentWithCapabilities(t, "bug_analysis", "code_change")
	tpl := seededBugFixTemplate(t)
	body := map[string]any{
		"idempotency_key": "cli-retry-42",
		"title":           "Stable automation start",
		"description":     "The same CLI or MCP retry must converge.",
	}
	first := runWorkflowTemplateForTest(t, tpl.ID, body)
	if first.Code != http.StatusCreated {
		t.Fatalf("first explicit-key run = %d %s", first.Code, first.Body.String())
	}
	firstRun := decodeWorkflowRunDetail(t, first, "first explicit-key run")
	replay := runWorkflowTemplateForTest(t, tpl.ID, body)
	if replay.Code != http.StatusOK || decodeWorkflowRunDetail(t, replay, "explicit replay").ID != firstRun.ID {
		t.Fatalf("explicit replay = %d %s, want same Run %s", replay.Code, replay.Body.String(), firstRun.ID)
	}
	conflict := runWorkflowTemplateForTest(t, tpl.ID, map[string]any{
		"idempotency_key": "cli-retry-42",
		"title":           "Stable automation start",
		"description":     "Changed payload under a reused key.",
	})
	if conflict.Code != http.StatusConflict || !strings.Contains(conflict.Body.String(), workflow.ErrCodeIdempotencyConflict) {
		t.Fatalf("changed explicit replay = %d %s, want idempotency conflict", conflict.Code, conflict.Body.String())
	}
}

func TestWorkflowRunExplicitKeyConvergesAcrossManualAndIntake(t *testing.T) {
	cleanupWorkflowTemplates(t)
	cleanupWorkflowRuns(t)
	withWorkflowEngineForTest(t)
	labelTestAgentWithCapabilities(t, "bug_analysis", "code_change")
	tpl := seededBugFixTemplate(t)
	key := "cross-entry-42"
	title := "One operation through two adapters"
	description := "A retry must return the existing durable Run."
	manual := runWorkflowTemplateForTest(t, tpl.ID, map[string]any{
		"idempotency_key": key, "title": title, "description": description,
	})
	if manual.Code != http.StatusCreated {
		t.Fatalf("manual start = %d %s", manual.Code, manual.Body.String())
	}
	manualRun := decodeWorkflowRunDetail(t, manual, "manual start")

	intake := httptest.NewRecorder()
	request := withURLParam(newRequest("POST", "/api/workspaces/"+testWorkspaceID+"/workflow-intake", map[string]any{
		"source": "contract-test", "event_id": "transport-event-42", "template_key": tpl.Key,
		"idempotency_key": key, "title": title, "description": description,
	}), "id", testWorkspaceID)
	testHandler.WorkflowIntake(intake, request)
	if intake.Code != http.StatusOK {
		t.Fatalf("intake replay = %d %s", intake.Code, intake.Body.String())
	}
	var receipt WorkflowIntakeResponse
	if err := json.Unmarshal(intake.Body.Bytes(), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.WorkflowRunID != manualRun.ID || receipt.ReceiptID != manualRun.ID {
		t.Fatalf("cross-entry identities differ: manual=%s intake=%+v", manualRun.ID, receipt)
	}
}

// TestWorkflowRunRejectsBadRequests covers the input contract, including the
// deliberate decision that description is REQUIRED.
//
// A run with no description gives its first agent a generic instruction and no
// defect, so it burns an attempt producing a guess. Refusing at the door is the
// only place the submitter can still fix it.
func TestWorkflowRunRejectsBadRequests(t *testing.T) {
	cleanupWorkflowTemplates(t)
	cleanupWorkflowRuns(t)
	withWorkflowEngineForTest(t)
	labelTestAgentWithCapabilities(t, "bug_analysis", "code_change")

	tpl := seededBugFixTemplate(t)

	cases := []struct {
		name string
		body map[string]any
		want int
	}{
		{"no title", map[string]any{"description": "something is broken"}, http.StatusBadRequest},
		{"blank title", map[string]any{"title": "   ", "description": "something is broken"}, http.StatusBadRequest},
		{"no description", map[string]any{"title": "Something is broken"}, http.StatusBadRequest},
		{"blank description", map[string]any{"title": "Something is broken", "description": "  "}, http.StatusBadRequest},
		{"overlong title", map[string]any{"title": strings.Repeat("x", 201), "description": "broken"}, http.StatusBadRequest},
		{
			"foreign project",
			map[string]any{"title": "Broken", "description": "broken", "project_id": "00000000-0000-0000-0000-0000000000ff"},
			http.StatusBadRequest,
		},
		{
			"malformed project",
			map[string]any{"title": "Broken", "description": "broken", "project_id": "not-a-uuid"},
			http.StatusBadRequest,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := runWorkflowTemplateForTest(t, tpl.ID, tc.body)
			if w.Code != tc.want {
				t.Fatalf("expected %d, got %d: %s", tc.want, w.Code, w.Body.String())
			}
		})
	}

	// Nothing was created by any refusal.
	var runCount int
	if err := testPool.QueryRow(context.Background(),
		`SELECT count(*) FROM workflow_run WHERE workspace_id = $1`, testWorkspaceID,
	).Scan(&runCount); err != nil {
		t.Fatalf("count runs: %v", err)
	}
	if runCount != 0 {
		t.Fatalf("a rejected run request created %d run rows", runCount)
	}

	// An unpublished template cannot be run: there is no version to pin. 409
	// rather than 422 - the request is fine, publishing fixes it.
	draft := createWorkflowTemplateForTest(t, "run_unpublished")
	w := runWorkflowTemplateForTest(t, draft.ID, map[string]any{
		"title":       "Cannot run a draft",
		"description": "The template has never been published.",
	})
	if w.Code != http.StatusConflict {
		t.Fatalf("running an unpublished template: expected 409, got %d: %s", w.Code, w.Body.String())
	}
	// And it must not have consumed an issue number's worth of state.
	if err := testPool.QueryRow(context.Background(),
		`SELECT count(*) FROM issue WHERE workspace_id = $1 AND title = $2`,
		testWorkspaceID, "Cannot run a draft",
	).Scan(&runCount); err != nil {
		t.Fatalf("count issues: %v", err)
	}
	if runCount != 0 {
		t.Fatalf("running an unpublished template created an orphan issue")
	}
}

// TestWorkflowRunCancelStopsTheAgentToo is the bug this endpoint exists to
// prevent: a "cancelled" Run whose agent keeps working.
//
// engine.CancelRun cascades to workflow rows only, deliberately - TaskService is
// canonical for task state. If the handler did not also cancel the tasks, the
// user would see a cancelled Run while the agent kept burning tokens and pushing
// commits, and would have no UI anywhere that offered to stop it.
func TestWorkflowRunCancelStopsTheAgentToo(t *testing.T) {
	cleanupWorkflowTemplates(t)
	cleanupWorkflowRuns(t)
	withWorkflowEngineForTest(t)
	labelTestAgentWithCapabilities(t, "bug_analysis", "code_change")

	tpl := seededBugFixTemplate(t)
	w := runWorkflowTemplateForTest(t, tpl.ID, map[string]any{
		"title":       "Cancel must stop the agent",
		"description": "Start a run and cancel it while the first step is queued.",
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("RunWorkflowTemplate: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	started := decodeWorkflowRunDetail(t, w, "RunWorkflowTemplate")
	analyze, ok := findWorkflowStep(started.Steps, "analyze")
	if !ok || analyze.TaskID == nil {
		t.Fatalf("the run did not queue a task to cancel: %+v", started.Steps)
	}
	taskID := *analyze.TaskID

	cw := httptest.NewRecorder()
	creq := withURLParam(newRequest("POST", "/api/workflow-runs/"+started.ID+"/cancel", nil), "id", started.ID)
	testHandler.CancelWorkflowRun(cw, creq)
	if cw.Code != http.StatusOK {
		t.Fatalf("CancelWorkflowRun: expected 200, got %d: %s", cw.Code, cw.Body.String())
	}
	cancelled := decodeWorkflowRunDetail(t, cw, "CancelWorkflowRun")
	if cancelled.Status != "cancelled" {
		t.Fatalf("run status = %q, want cancelled", cancelled.Status)
	}
	if cancelled.CompletedAt == nil {
		t.Fatalf("a cancelled run must record completed_at")
	}
	// Every Step must be terminal: a non-terminal Step on a terminal Run is
	// exactly the state the reconciler exists to repair, so producing it here
	// would be manufacturing work for it on the happy path.
	for _, step := range cancelled.Steps {
		if !workflow.IsTerminalStepStatus(workflow.StepStatus(step.Status)) {
			t.Fatalf("step %s/%d is %q after cancel; every step must be terminal", step.NodeKey, step.Attempt, step.Status)
		}
	}

	// THE POINT: the agent task must be cancelled too.
	var taskStatus string
	if err := testPool.QueryRow(context.Background(),
		`SELECT status FROM agent_task_queue WHERE id = $1`, taskID,
	).Scan(&taskStatus); err != nil {
		t.Fatalf("load the cancelled run's task: %v", err)
	}
	if taskStatus != "cancelled" {
		t.Fatalf("agent task status = %q after cancelling its run; the agent is still working on a cancelled run", taskStatus)
	}

	// Cancelling twice is a no-op, not an error: a user clicking twice must not
	// see a failure.
	again := httptest.NewRecorder()
	testHandler.CancelWorkflowRun(again, withURLParam(newRequest("POST", "/api/workflow-runs/"+started.ID+"/cancel", nil), "id", started.ID))
	if again.Code != http.StatusOK {
		t.Fatalf("re-cancel: expected 200 (idempotent), got %d: %s", again.Code, again.Body.String())
	}
}

// acceptanceOnlyWorkflowDefinition returns the smallest legal graph that can
// reach an acceptance node and route a rejection back to an upstream agent.
//
// This is a test scaffold, not a shape any product template would use: reaching
// the built-in's acceptance node requires several agents to submit passing work,
// which would make these tests about the submission path rather than about the
// acceptance endpoint. One completed agent isolates the decision while keeping
// the rework target on a real entry -> target -> acceptance forward path.
func acceptanceOnlyWorkflowDefinition() map[string]any {
	return map[string]any{
		"schema_version": 1,
		"entry_node":     "rework",
		"nodes": []map[string]any{
			{
				"key":                 "acceptance",
				"type":                "acceptance",
				"name":                "Acceptance",
				"instruction":         "Confirm the reported defect is gone.",
				"next":                []string{"end"},
				"acceptance_criteria": []string{"happy path verified", "edge case covered"},
				"rework_targets":      []string{"rework"},
			},
			{
				"key":         "rework",
				"type":        "agent",
				"name":        "Rework",
				"instruction": "Address the reviewer's stated gap.",
				"next":        []string{"acceptance"},
				"routing": map[string]any{
					"strategy":   "capability",
					"capability": "bug_analysis",
				},
				"submission_schema": "analysis",
				"on_failure":        "block",
			},
			{"key": "end", "type": "end", "name": "Done"},
		},
	}
}

// startAcceptanceGatedRun publishes the acceptance-entry graph and starts a Run
// on it, returning the Run detail with a pending acceptance.
func startAcceptanceGatedRun(t *testing.T, key string) WorkflowRunDetailResponse {
	t.Helper()

	cw := httptest.NewRecorder()
	testHandler.CreateWorkflowTemplate(cw, newRequest("POST", "/api/workflow-templates", map[string]any{
		"key":        key,
		"name":       "Acceptance Gate",
		"definition": acceptanceOnlyWorkflowDefinition(),
	}))
	if cw.Code != http.StatusCreated {
		t.Fatalf("CreateWorkflowTemplate(%s): expected 201, got %d: %s", key, cw.Code, cw.Body.String())
	}
	var created WorkflowTemplateDetailResponse
	if err := json.NewDecoder(cw.Body).Decode(&created); err != nil {
		t.Fatalf("decode created template: %v", err)
	}

	pw := httptest.NewRecorder()
	testHandler.PublishWorkflowTemplate(pw, withURLParam(newRequest("POST", "/api/workflow-templates/"+created.ID+"/publish", nil), "id", created.ID))
	if pw.Code != http.StatusOK {
		t.Fatalf("PublishWorkflowTemplate: expected 200, got %d: %s", pw.Code, pw.Body.String())
	}

	rw := runWorkflowTemplateForTest(t, created.ID, map[string]any{
		"title":       "Acceptance gate " + key,
		"description": "A run that stops at the human review gate.",
	})
	if rw.Code != http.StatusCreated {
		t.Fatalf("RunWorkflowTemplate: expected 201, got %d: %s", rw.Code, rw.Body.String())
	}
	detail := decodeWorkflowRunDetail(t, rw, "RunWorkflowTemplate")
	reworkStep, ok := findWorkflowStep(detail.Steps, "rework")
	if !ok || reworkStep.TaskID == nil {
		t.Fatalf("initial rework step did not queue a task: %+v", detail.Steps)
	}
	completeWorkflowTaskForTest(t, *reworkStep.TaskID, reworkStep.ID, "ready for acceptance")
	detail = getWorkflowRunForTest(t, detail.ID)

	if detail.Status != "waiting_acceptance" {
		t.Fatalf("run status = %q, want waiting_acceptance", detail.Status)
	}
	if detail.Acceptance == nil {
		t.Fatalf("run has no acceptance; the gate did not open")
	}
	return detail
}

func decideWorkflowAcceptanceForTest(t *testing.T, runID string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	req := withURLParam(newRequest("POST", "/api/workflow-runs/"+runID+"/acceptance", body), "id", runID)
	testHandler.DecideWorkflowAcceptance(w, req)
	return w
}

// TestWorkflowRunAcceptanceRejectionRequiresReasonAndPermittedTarget pins the
// rejection contract.
//
// Both refusals are 409 and both come from the engine, not from a second copy of
// the rule in the handler. That matters: the permitted targets are a property of
// the Run's PINNED graph, and a handler-side check would eventually disagree with
// the engine - presenting to a reviewer as a rejection the API accepted and the
// engine then refused.
func TestWorkflowRunAcceptanceRejectionRequiresReasonAndPermittedTarget(t *testing.T) {
	cleanupWorkflowTemplates(t)
	cleanupWorkflowRuns(t)
	withWorkflowEngineForTest(t)
	labelTestAgentWithCapabilities(t, "bug_analysis")

	run := startAcceptanceGatedRun(t, "accept_reject_guard")

	// The reviewer must be shown the criteria and the permitted targets from the
	// pinned version, or the UI cannot offer a legal rejection at all.
	if len(run.Acceptance.Criteria) != 2 {
		t.Fatalf("acceptance criteria = %+v, want the two from the pinned graph", run.Acceptance.Criteria)
	}
	if len(run.Acceptance.ReworkTargets) != 1 || run.Acceptance.ReworkTargets[0] != "rework" {
		t.Fatalf("acceptance rework_targets = %+v, want [rework]", run.Acceptance.ReworkTargets)
	}
	if run.Acceptance.Status != "pending" {
		t.Fatalf("acceptance status = %q, want pending", run.Acceptance.Status)
	}

	// A rejection with no reason: the rework attempt would carry nothing telling
	// the agent what to change.
	w := decideWorkflowAcceptanceForTest(t, run.ID, map[string]any{"accept": false, "rework_target": "rework"})
	if w.Code != http.StatusConflict {
		t.Fatalf("reject with no reason: expected 409, got %d: %s", w.Code, w.Body.String())
	}
	assertWorkflowErrorCode(t, w, workflow.ErrCodeAcceptanceConflict)

	// A rejection with a reason but no target.
	w = decideWorkflowAcceptanceForTest(t, run.ID, map[string]any{"accept": false, "reason": "the edge case is still broken"})
	if w.Code != http.StatusConflict {
		t.Fatalf("reject with no target: expected 409, got %d: %s", w.Code, w.Body.String())
	}

	// A target the pinned graph never declared. Allowing it would let a reviewer
	// reroute work through a path the template author never validated.
	w = decideWorkflowAcceptanceForTest(t, run.ID, map[string]any{
		"accept":        false,
		"reason":        "the edge case is still broken",
		"rework_target": "not_a_node",
	})
	if w.Code != http.StatusConflict {
		t.Fatalf("reject with an unpermitted target: expected 409, got %d: %s", w.Code, w.Body.String())
	}
	assertWorkflowErrorCode(t, w, workflow.ErrCodeAcceptanceConflict)

	// None of the refusals may have consumed the gate.
	still := getWorkflowRunForTest(t, run.ID)
	if still.Status != "waiting_acceptance" {
		t.Fatalf("run status = %q after refused decisions, want waiting_acceptance", still.Status)
	}
	if still.Acceptance == nil || still.Acceptance.Status != "pending" {
		t.Fatalf("a refused decision consumed the acceptance: %+v", still.Acceptance)
	}

	// A legal rejection opens a NEW attempt at the permitted target and carries
	// the reason to it.
	w = decideWorkflowAcceptanceForTest(t, run.ID, map[string]any{
		"accept":        false,
		"reason":        "the edge case is still broken",
		"rework_target": "rework",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("legal rejection: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	rejected := decodeWorkflowRunDetail(t, w, "legal rejection")
	if rejected.Status != "running" {
		t.Fatalf("run status = %q after a rejection, want running (the rework attempt is live)", rejected.Status)
	}
	reworkStep, ok := findLatestWorkflowStep(rejected.Steps, "rework")
	if !ok {
		t.Fatalf("a rejection did not open the rework step: %+v", rejected.Steps)
	}
	if reworkStep.TaskID == nil {
		t.Fatalf("the rework step queued no task (status=%q failure_reason=%v)", reworkStep.Status, reworkStep.FailureReason)
	}
	// The reviewer's reason must reach the agent, or the rework attempt is the
	// same work again.
	var reworkContext []byte
	if err := testPool.QueryRow(context.Background(),
		`SELECT context FROM agent_task_queue WHERE id = $1`, *reworkStep.TaskID,
	).Scan(&reworkContext); err != nil {
		t.Fatalf("load the rework task: %v", err)
	}
	brief, ok := workflow.ParseTaskContext(reworkContext)
	if !ok {
		t.Fatalf("rework task carries no brief: %s", string(reworkContext))
	}
	if !strings.Contains(brief.ReworkReason, "edge case is still broken") {
		t.Fatalf("rework brief reason = %q, want the reviewer's reason", brief.ReworkReason)
	}
	if brief.ReworkFromNode != "acceptance" {
		t.Fatalf("rework brief from_node = %q, want acceptance", brief.ReworkFromNode)
	}

	// The decided acceptance stays on the response: after a rejection the
	// reviewer's reason IS the explanation for the new attempt.
	if rejected.Acceptance == nil || rejected.Acceptance.Status != "rejected" {
		t.Fatalf("the decided acceptance was dropped from the trace: %+v", rejected.Acceptance)
	}
	if rejected.Acceptance.Reason == nil || !strings.Contains(*rejected.Acceptance.Reason, "edge case") {
		t.Fatalf("decided acceptance reason = %v, want the reviewer's text", rejected.Acceptance.Reason)
	}

	// Deciding again is a conflict: the gate is consumed, and a stale UI must not
	// overwrite the first reviewer's verdict.
	w = decideWorkflowAcceptanceForTest(t, run.ID, map[string]any{"accept": true})
	if w.Code != http.StatusConflict {
		t.Fatalf("second decision: expected 409, got %d: %s", w.Code, w.Body.String())
	}
}

// TestWorkflowRunAcceptanceAcceptCompletesTheRun pins the other half: a Run
// completes ONLY through End, and acceptance is what lets it get there.
func TestWorkflowRunAcceptanceAcceptCompletesTheRun(t *testing.T) {
	cleanupWorkflowTemplates(t)
	cleanupWorkflowRuns(t)
	withWorkflowEngineForTest(t)
	labelTestAgentWithCapabilities(t, "bug_analysis")

	run := startAcceptanceGatedRun(t, "accept_completes")

	w := decideWorkflowAcceptanceForTest(t, run.ID, map[string]any{"accept": true})
	if w.Code != http.StatusOK {
		t.Fatalf("accept: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	accepted := decodeWorkflowRunDetail(t, w, "accept")
	if accepted.Status != "completed" {
		t.Fatalf("run status = %q after acceptance, want completed", accepted.Status)
	}
	if accepted.CompletedAt == nil {
		t.Fatalf("a completed run must record completed_at")
	}
	if _, ok := findWorkflowStep(accepted.Steps, "end"); !ok {
		t.Fatalf("the run completed without traversing the End node: %+v", accepted.Steps)
	}
	if accepted.Acceptance == nil || accepted.Acceptance.Status != "accepted" {
		t.Fatalf("acceptance = %+v, want accepted", accepted.Acceptance)
	}
}

// TestWorkflowRunAcceptanceWithNoPendingGate covers the state every Run is in
// most of its life.
//
// 409, not 404: the Run exists and is visible, it is just not waiting for a
// decision. A 404 would read as "this run is gone" and send the user looking for
// a deleted row.
func TestWorkflowRunAcceptanceWithNoPendingGate(t *testing.T) {
	cleanupWorkflowTemplates(t)
	cleanupWorkflowRuns(t)
	withWorkflowEngineForTest(t)
	labelTestAgentWithCapabilities(t, "bug_analysis", "code_change")

	tpl := seededBugFixTemplate(t)
	w := runWorkflowTemplateForTest(t, tpl.ID, map[string]any{
		"title":       "No gate yet",
		"description": "The run is still on its first agent step.",
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("RunWorkflowTemplate: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	run := decodeWorkflowRunDetail(t, w, "RunWorkflowTemplate")
	if run.Acceptance != nil {
		t.Fatalf("a run on its first agent step must have no acceptance: %+v", run.Acceptance)
	}

	dw := decideWorkflowAcceptanceForTest(t, run.ID, map[string]any{"accept": true})
	if dw.Code != http.StatusConflict {
		t.Fatalf("deciding with no pending gate: expected 409, got %d: %s", dw.Code, dw.Body.String())
	}
}

// TestWorkflowRunCrossWorkspaceIsNotFound is the tenant posture: the workspace is
// part of every WHERE clause, so a valid id from another workspace is a 404
// rather than a leak - and 404 rather than 403, because distinguishing them would
// confirm the id exists.
func TestWorkflowRunCrossWorkspaceIsNotFound(t *testing.T) {
	cleanupWorkflowTemplates(t)
	cleanupWorkflowRuns(t)
	withWorkflowEngineForTest(t)
	labelTestAgentWithCapabilities(t, "bug_analysis", "code_change")

	tpl := seededBugFixTemplate(t)
	w := runWorkflowTemplateForTest(t, tpl.ID, map[string]any{
		"title":       "Visible only in its own workspace",
		"description": "A run another workspace must not be able to read or cancel.",
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("RunWorkflowTemplate: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	run := decodeWorkflowRunDetail(t, w, "RunWorkflowTemplate")

	// A second workspace the SAME user owns. Membership is therefore not the gate
	// under test - only the workspace scoping of the query is, which is the
	// interesting case: a permission check that passed would still have to fail on
	// scoping.
	otherWorkspaceID := createOtherTestWorkspace(t)
	asOther := func(method, path string, body any) *http.Request {
		req := newRequest(method, path, body)
		req.Header.Set("X-Workspace-ID", otherWorkspaceID)
		return req
	}

	for _, tc := range []struct {
		name   string
		invoke func(w http.ResponseWriter, r *http.Request)
		req    *http.Request
	}{
		{"get", testHandler.GetWorkflowRun, withURLParam(asOther("GET", "/api/workflow-runs/"+run.ID, nil), "id", run.ID)},
		{"cancel", testHandler.CancelWorkflowRun, withURLParam(asOther("POST", "/api/workflow-runs/"+run.ID+"/cancel", nil), "id", run.ID)},
		{
			"acceptance",
			testHandler.DecideWorkflowAcceptance,
			withURLParam(asOther("POST", "/api/workflow-runs/"+run.ID+"/acceptance", map[string]any{"accept": true}), "id", run.ID),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			tc.invoke(rec, tc.req)
			if rec.Code != http.StatusNotFound {
				t.Fatalf("%s from another workspace: expected 404, got %d: %s", tc.name, rec.Code, rec.Body.String())
			}
		})
	}

	// The run must be untouched by the refused cancel.
	after := getWorkflowRunForTest(t, run.ID)
	if after.Status == "cancelled" {
		t.Fatalf("a cross-workspace cancel took effect")
	}

	// The other workspace's list must not contain it either.
	lw := httptest.NewRecorder()
	testHandler.ListWorkflowRuns(lw, asOther("GET", "/api/workflow-runs", nil))
	if lw.Code != http.StatusOK {
		t.Fatalf("ListWorkflowRuns(other workspace): expected 200, got %d: %s", lw.Code, lw.Body.String())
	}
	var list struct {
		Runs  []WorkflowRunResponse `json:"runs"`
		Total int64                 `json:"total"`
	}
	if err := json.NewDecoder(lw.Body).Decode(&list); err != nil {
		t.Fatalf("decode other-workspace list: %v", err)
	}
	for _, r := range list.Runs {
		if r.ID == run.ID {
			t.Fatalf("a run leaked into another workspace's list")
		}
	}
	if list.Total != 0 {
		t.Fatalf("other workspace total = %d, want 0", list.Total)
	}

	// A malformed id is a 400 (client bug), an unknown one a 404.
	rec := httptest.NewRecorder()
	testHandler.GetWorkflowRun(rec, withURLParam(newRequest("GET", "/api/workflow-runs/not-a-uuid", nil), "id", "not-a-uuid"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed run id: expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	const stranger = "00000000-0000-0000-0000-0000000000ff"
	rec = httptest.NewRecorder()
	testHandler.GetWorkflowRun(rec, withURLParam(newRequest("GET", "/api/workflow-runs/"+stranger, nil), "id", stranger))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown run id: expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestWorkflowRunWithoutEngineIs503 pins the nil-engine guard.
//
// h.WorkflowEngine is assigned after handler.New, so any construction path that
// skips that wiring leaves it nil. 503 says "this deployment cannot run
// workflows"; a panic would return a 500 whose stack trace says nothing about the
// cause, and the operator would go looking at the database.
func TestWorkflowRunWithoutEngineIs503(t *testing.T) {
	cleanupWorkflowTemplates(t)
	cleanupWorkflowRuns(t)
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	// Deliberately does NOT call withWorkflowEngineForTest. Defend against an
	// earlier test leaking a wired engine onto the shared handler.
	previous := testHandler.WorkflowEngine
	testHandler.WorkflowEngine = nil
	t.Cleanup(func() { testHandler.WorkflowEngine = previous })

	tpl := seededBugFixTemplate(t)
	w := runWorkflowTemplateForTest(t, tpl.ID, map[string]any{
		"title":       "No engine wired",
		"description": "The server was built without a workflow engine.",
	})
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("run with no engine: expected 503, got %d: %s", w.Code, w.Body.String())
	}
	// And it must not have created an issue on the way to refusing.
	var issueCount int
	if err := testPool.QueryRow(context.Background(),
		`SELECT count(*) FROM issue WHERE workspace_id = $1 AND title = $2`,
		testWorkspaceID, "No engine wired",
	).Scan(&issueCount); err != nil {
		t.Fatalf("count issues: %v", err)
	}
	if issueCount != 0 {
		t.Fatalf("a 503 left %d orphan issues behind", issueCount)
	}
}

// TestWorkflowRunBlocksWhenNoAgentCarriesTheCapability pins the designed failure
// path, which is a product decision rather than an accident: a Run with no
// eligible specialist STOPS and says why.
//
// The alternative - substituting some other agent - would produce
// plausible-looking wrong work with no signal that anything was substituted. A
// blocked Run naming the missing capability is recoverable; a quietly-misrouted
// one is not.
func TestWorkflowRunBlocksWhenNoAgentCarriesTheCapability(t *testing.T) {
	cleanupWorkflowTemplates(t)
	cleanupWorkflowRuns(t)
	withWorkflowEngineForTest(t)
	// Deliberately NO capability labels.

	tpl := seededBugFixTemplate(t)
	w := runWorkflowTemplateForTest(t, tpl.ID, map[string]any{
		"title":       "Nobody can analyze this",
		"description": "No agent in this workspace carries the bug_analysis label.",
	})
	// Still 201: the Run was created, and its blocked state is the answer. A 4xx
	// here would discard the Run and with it the explanation.
	if w.Code != http.StatusCreated {
		t.Fatalf("RunWorkflowTemplate: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	detail := decodeWorkflowRunDetail(t, w, "RunWorkflowTemplate")
	if detail.Status != "blocked" {
		t.Fatalf("run status = %q, want blocked", detail.Status)
	}
	if detail.BlockedReason == nil || *detail.BlockedReason != workflow.ReasonRoutingNoCandidate {
		t.Fatalf("blocked_reason = %v, want %q", detail.BlockedReason, workflow.ReasonRoutingNoCandidate)
	}
	analyze, ok := findWorkflowStep(detail.Steps, "analyze")
	if !ok {
		t.Fatalf("no analyze step: %+v", detail.Steps)
	}
	if analyze.Status != "blocked" {
		t.Fatalf("analyze step status = %q, want blocked", analyze.Status)
	}
	if analyze.TaskID != nil {
		t.Fatalf("a blocked step must not have dispatched a task")
	}
	// The reason must name the capability, or an operator has nothing to fix.
	if analyze.FailureReason == nil || *analyze.FailureReason != workflow.ReasonRoutingNoCandidate {
		t.Fatalf("step failure_reason = %v, want %q", analyze.FailureReason, workflow.ReasonRoutingNoCandidate)
	}
}

// TestWorkflowRunTraceCarriesSubmissions asserts the detail endpoint surfaces the
// Agent deliverable, not just the Step status.
//
// A trace of statuses answers "where is it"; the submission answers "what did it
// produce", which is the only thing a reviewer can actually judge. The submission
// is created by driving a real task completion through TaskService, so this also
// re-proves the terminal hook end to end through the HTTP shape.
func TestWorkflowRunTraceCarriesSubmissions(t *testing.T) {
	cleanupWorkflowTemplates(t)
	cleanupWorkflowRuns(t)
	withWorkflowEngineForTest(t)
	labelTestAgentWithCapabilities(t, "bug_analysis", "code_change")

	tpl := seededBugFixTemplate(t)
	w := runWorkflowTemplateForTest(t, tpl.ID, map[string]any{
		"title":       "Trace carries submissions",
		"description": "Complete the first step and read the artifact back off the trace.",
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("RunWorkflowTemplate: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	run := decodeWorkflowRunDetail(t, w, "RunWorkflowTemplate")
	analyze, ok := findWorkflowStep(run.Steps, "analyze")
	if !ok || analyze.TaskID == nil {
		t.Fatalf("no queued analyze task: %+v", run.Steps)
	}
	if analyze.Submission != nil {
		t.Fatalf("a queued step must have no submission yet: %+v", analyze.Submission)
	}

	completeWorkflowTaskForTest(t, *analyze.TaskID, analyze.ID,
		"the Save handler is bound to a detached node after re-render")

	after := getWorkflowRunForTest(t, run.ID)
	analyzeAfter, ok := findWorkflowStep(after.Steps, "analyze")
	if !ok {
		t.Fatalf("analyze step vanished from the trace")
	}
	if analyzeAfter.Status != "passed" {
		t.Fatalf("analyze status = %q after completion, want passed (failure_reason=%v)",
			analyzeAfter.Status, analyzeAfter.FailureReason)
	}
	if analyzeAfter.Submission == nil {
		t.Fatalf("the trace carries no submission for a completed step; a reviewer has nothing to judge")
	}
	sub := analyzeAfter.Submission
	if sub.Verdict != "pass" {
		t.Fatalf("submission verdict = %q, want pass", sub.Verdict)
	}
	var artifact struct {
		Type    string `json:"type"`
		Summary string `json:"summary"`
	}
	if err := json.Unmarshal(sub.Artifact, &artifact); err != nil {
		t.Fatalf("submission artifact is not an object: %v (%s)", err, string(sub.Artifact))
	}
	if !strings.Contains(artifact.Summary, "detached node") {
		t.Fatalf("artifact summary = %q, want the agent's own summary", artifact.Summary)
	}
	if sub.SubmittedAt == "" {
		t.Fatalf("submission has no submitted_at")
	}

	// The run must have advanced to the next node, and the current_node_key on the
	// list shape must move with it.
	if _, ok := findWorkflowStep(after.Steps, "implement"); !ok {
		t.Fatalf("the run did not advance to implement: %+v", after.Steps)
	}
	if after.CurrentNodeKey == nil || *after.CurrentNodeKey != "implement" {
		t.Fatalf("current_node_key = %v after the handoff, want implement", after.CurrentNodeKey)
	}
}

// completeWorkflowTaskForTest drives a queued workflow task to completion through
// TaskService, the way a daemon would.
//
// Deliberately routed through TaskService.CompleteTask rather than calling
// engine.SubmitResult: the production path is a task completion, and the
// terminal-observer hook that translates it into workflow state is exactly the
// link a test should not bypass. The payload is wrapped in the same
// protocol.TaskCompletedPayload envelope a real completion carries, because that
// envelope's JSON escaping of `<` is what made an earlier version of the hook
// block every step.
func completeWorkflowTaskForTest(t *testing.T, taskID, stepID, summary string) {
	t.Helper()
	ctx := context.Background()

	// The daemon claim path walks the task to running; CompleteTask's status CAS
	// needs it there. Driving it directly keeps this about the workflow chain
	// rather than the claim protocol.
	if _, err := testPool.Exec(ctx,
		`UPDATE agent_task_queue SET status = 'running', dispatched_at = now(), started_at = now() WHERE id = $1`,
		taskID,
	); err != nil {
		t.Fatalf("start the task: %v", err)
	}

	payload, err := json.Marshal(map[string]any{
		"schema_version":   workflow.SchemaVersion,
		"step_instance_id": stepID,
		"verdict":          "pass",
		"artifact":         map[string]any{"type": "analysis", "summary": summary},
		"rationale":        "verified against the reproduction",
		"confidence":       0.9,
	})
	if err != nil {
		t.Fatalf("marshal submission payload: %v", err)
	}
	// The delimiters are unexported in package workflow; take them from the
	// instructions the engine itself hands the agent so this cannot drift from the
	// real contract.
	open, close := workflowSubmissionMarkersForTest(t, stepID)
	result, err := json.Marshal(map[string]any{
		"task_id": taskID,
		"output":  open + "\n" + string(payload) + "\n" + close,
	})
	if err != nil {
		t.Fatalf("marshal task result: %v", err)
	}

	if _, err := testHandler.TaskService.CompleteTask(ctx, parseUUID(taskID), result, "", "", false, ""); err != nil {
		t.Fatalf("CompleteTask: %v", err)
	}
}

func workflowSubmissionMarkersForTest(t *testing.T, stepID string) (string, string) {
	t.Helper()
	var open, closing string
	for _, line := range strings.Split(workflow.SubmissionContractInstructions(stepID), "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "<<<") {
			continue
		}
		if strings.Contains(trimmed, "END") {
			closing = trimmed
		} else if open == "" {
			open = trimmed
		}
	}
	if open == "" || closing == "" {
		t.Fatalf("submission contract instructions no longer contain the delimiters")
	}
	return open, closing
}

// assertWorkflowErrorCode checks the machine-readable code travels alongside the
// message. Plan section 9: a client must be able to distinguish "this transition
// is illegal right now" from "you hit a budget ceiling" without string-matching.
func assertWorkflowErrorCode(t *testing.T, w *httptest.ResponseRecorder, want string) {
	t.Helper()
	var body struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body: %v (%s)", err, w.Body.String())
	}
	if body.Code != want {
		t.Fatalf("error code = %q, want %q (body: %s)", body.Code, want, w.Body.String())
	}
	if body.Error == "" {
		t.Fatalf("error body has no message: %s", w.Body.String())
	}
}

// TestWorkflowRunEmitsRunChangedEvent pins the realtime signal the run trace page
// refetches on. Without it the page shows a queued step until the user reloads,
// which for a multi-minute agent run reads as "nothing happened".
func TestWorkflowRunEmitsRunChangedEvent(t *testing.T) {
	cleanupWorkflowTemplates(t)
	cleanupWorkflowRuns(t)
	withWorkflowEngineForTest(t)
	labelTestAgentWithCapabilities(t, "bug_analysis", "code_change")

	got := make(chan events.Event, 4)
	testHandler.Bus.Subscribe(protocol.EventWorkflowRunChanged, func(e events.Event) {
		select {
		case got <- e:
		default:
		}
	})

	tpl := seededBugFixTemplate(t)
	w := runWorkflowTemplateForTest(t, tpl.ID, map[string]any{
		"title":       "Run emits a change event",
		"description": "The trace page needs a signal to refetch on.",
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("RunWorkflowTemplate: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	run := decodeWorkflowRunDetail(t, w, "RunWorkflowTemplate")

	select {
	case ev := <-got:
		payload, ok := ev.Payload.(map[string]any)
		if !ok {
			t.Fatalf("event payload is not an object: %#v", ev.Payload)
		}
		if payload["run_id"] != run.ID {
			t.Fatalf("event run_id = %v, want %s", payload["run_id"], run.ID)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("no workflow run_changed event within timeout")
	}
}
