package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/workflow"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func newIssuePoolAutopilot(t *testing.T, projectID any) string {
	return newIssuePoolAutopilotWithDefinition(t, projectID, func(string) *workflow.Definition {
		return &workflow.Definition{SchemaVersion: workflow.SchemaVersion, EntryNode: "end", Nodes: []workflow.Node{{Key: "end", Type: workflow.NodeTypeEnd}}}
	})
}

func newIssuePoolAutopilotWithDefinition(t *testing.T, projectID any, definition func(agentID string) *workflow.Definition) string {
	t.Helper()
	ctx := context.Background()
	workspaceID, userID := parseUUID(testWorkspaceID), parseUUID(testUserID)
	var agentID string
	dbfx.QueryRow(t, `SELECT id::text FROM agent WHERE workspace_id=$1 ORDER BY created_at LIMIT 1`, testWorkspaceID).Scan(&agentID)
	template, err := testHandler.Queries.CreateWorkflowTemplate(ctx, db.CreateWorkflowTemplateParams{
		WorkspaceID: workspaceID, Key: fmt.Sprintf("issue-pool-%d", time.Now().UnixNano()),
		Name: "Issue pool test", CreatedByType: "member", CreatedByID: userID,
	})
	if err != nil {
		t.Fatalf("create issue-pool workflow template: %v", err)
	}
	raw, err := workflow.MarshalDefinition(definition(agentID))
	if err != nil {
		t.Fatalf("marshal issue-pool workflow definition: %v", err)
	}
	version, err := testHandler.Queries.CreateWorkflowTemplateVersion(ctx, db.CreateWorkflowTemplateVersionParams{
		WorkspaceID: workspaceID, TemplateID: template.ID, Definition: raw, SchemaVersion: workflow.SchemaVersion,
	})
	if err != nil {
		t.Fatalf("create issue-pool workflow version: %v", err)
	}
	version, err = testHandler.Queries.PublishWorkflowTemplateVersion(ctx, db.PublishWorkflowTemplateVersionParams{
		ID: version.ID, WorkspaceID: workspaceID, PublishedByType: "member", PublishedByID: userID,
	})
	if err != nil {
		t.Fatalf("publish issue-pool workflow version: %v", err)
	}
	if _, err := testHandler.Queries.SetWorkflowTemplateCurrentVersion(ctx, db.SetWorkflowTemplateCurrentVersionParams{
		ID: template.ID, WorkspaceID: workspaceID, CurrentVersion: version.Version,
	}); err != nil {
		t.Fatalf("pin issue-pool workflow version: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM workflow_template_version WHERE template_id=$1`, template.ID)
		testPool.Exec(context.Background(), `DELETE FROM workflow_template WHERE id=$1`, template.ID)
	})
	return dbfx.Insert(t, "autopilot", testutil.Cols{
		"workspace_id":                 testWorkspaceID,
		"title":                        "Historical issue pool",
		"assignee_type":                "agent",
		"assignee_id":                  agentID,
		"status":                       "active",
		"execution_mode":               "issue_pool",
		"workflow_template_id":         uuidToString(template.ID),
		"workflow_template_version_id": uuidToString(version.ID),
		"created_by_type":              "member",
		"created_by_id":                testUserID,
		"project_id":                   projectID,
	})
}

func cleanIssuePoolRows(t *testing.T, autopilotID string) {
	t.Helper()
	dbfx.Cleanup(t, `DELETE FROM issue_pool_policy WHERE autopilot_id=$1`, autopilotID)
	dbfx.Cleanup(t, `DELETE FROM issue_pool_cycle WHERE autopilot_id=$1`, autopilotID)
	dbfx.Cleanup(t, `DELETE FROM issue_pool_item WHERE autopilot_id=$1`, autopilotID)
	dbfx.Cleanup(t, `DELETE FROM issue_pool_notification_outbox WHERE autopilot_id=$1`, autopilotID)
}

func putIssuePoolPolicy(t *testing.T, autopilotID string, body map[string]any) IssuePoolPolicyResponse {
	t.Helper()
	path := "/api/autopilots/" + autopilotID + "/issue-pool/policy?workspace_id=" + testWorkspaceID
	req := withURLParam(newRequest(http.MethodPut, path, body), "id", autopilotID)
	var out IssuePoolPolicyResponse
	testutil.Call(t, testHandler.PutIssuePoolPolicy, req).WantOneOf(http.StatusCreated, http.StatusOK).JSON(&out)
	return out
}

func previewIssuePool(t *testing.T, autopilotID string, wantStatus int) IssuePoolPreviewResponse {
	t.Helper()
	path := "/api/autopilots/" + autopilotID + "/issue-pool/preview?workspace_id=" + testWorkspaceID
	req := withURLParam(newRequest(http.MethodPost, path, nil), "id", autopilotID)
	var out IssuePoolPreviewResponse
	testutil.Call(t, testHandler.PreviewIssuePool, req).Want(wantStatus).JSON(&out)
	return out
}

func createIssuePoolCycle(t *testing.T, autopilotID, key string, wantStatus int) IssuePoolCycleResponse {
	t.Helper()
	path := "/api/autopilots/" + autopilotID + "/issue-pool/cycles?workspace_id=" + testWorkspaceID
	req := withURLParam(newRequest(http.MethodPost, path, map[string]any{"idempotency_key": key}), "id", autopilotID)
	var out IssuePoolCycleResponse
	testutil.Call(t, testHandler.CreateIssuePoolCycle, req).Want(wantStatus).JSON(&out)
	return out
}

func TestListIssuePoolCyclesPaginatesNewestFirst(t *testing.T) {
	autopilotID := newIssuePoolAutopilot(t, nil)
	cleanIssuePoolRows(t, autopilotID)
	putIssuePoolPolicy(t, autopilotID, map[string]any{
		"eligible_statuses": []string{"backlog"}, "batch_limit": 1, "max_in_flight": 1,
	})
	first := createIssuePoolCycle(t, autopilotID, "list-first", http.StatusCreated)
	second := createIssuePoolCycle(t, autopilotID, "list-second", http.StatusCreated)

	path := "/api/autopilots/" + autopilotID + "/issue-pool/cycles?workspace_id=" + testWorkspaceID + "&limit=1&offset=1"
	req := withURLParam(newRequest(http.MethodGet, path, nil), "id", autopilotID)
	var out struct {
		Cycles []IssuePoolCycleResponse `json:"cycles"`
		Total  int                      `json:"total"`
	}
	testutil.Call(t, testHandler.ListIssuePoolCycles, req).Want(http.StatusOK).JSON(&out)
	if out.Total != 2 || len(out.Cycles) != 1 || out.Cycles[0].ID != first.ID || second.ID == first.ID {
		t.Fatalf("unexpected paginated cycles: total=%d cycles=%v", out.Total, out.Cycles)
	}
}

func withIssuePoolParams(req *http.Request, autopilotID, cycleID string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", autopilotID)
	if cycleID != "" {
		rctx.URLParams.Add("cycleId", cycleID)
	}
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

func reviewIssuePoolItems(t *testing.T, autopilotID, cycleID string, decisions []map[string]any, wantStatus int) IssuePoolCycleResponse {
	t.Helper()
	path := "/api/autopilots/" + autopilotID + "/issue-pool/cycles/" + cycleID + "/review?workspace_id=" + testWorkspaceID
	req := withURLParam(newRequest(http.MethodPost, path, map[string]any{"decisions": decisions}), "id", autopilotID)
	req = withIssuePoolParams(req, autopilotID, cycleID)
	var out IssuePoolCycleResponse
	testutil.Call(t, testHandler.ReviewIssuePoolItems, req).Want(wantStatus).JSON(&out)
	return out
}

func TestIssuePoolPreviewDeterministicExplainableAndProjectScoped(t *testing.T) {
	projectA := dbfx.Project(t, "Pool project A")
	projectB := dbfx.Project(t, "Pool project B")
	autopilotID := newIssuePoolAutopilot(t, projectA)
	cleanIssuePoolRows(t, autopilotID)

	high := dbfx.Issue(t, "High priority old issue", testutil.Cols{
		"project_id": projectA, "status": "backlog", "priority": "high",
		"updated_at": testutil.Raw("'2020-01-02T00:00:00Z'::timestamptz"),
	})
	low := dbfx.Issue(t, "Low priority older issue", testutil.Cols{
		"project_id": projectA, "status": "backlog", "priority": "low",
		"updated_at": testutil.Raw("'2020-01-01T00:00:00Z'::timestamptz"),
	})
	dbfx.Issue(t, "Human-owned issue", testutil.Cols{
		"project_id": projectA, "status": "backlog", "priority": "urgent",
		"assignee_type": "member", "assignee_id": testUserID,
		"updated_at": testutil.Raw("'2020-01-01T00:00:00Z'::timestamptz"),
	})
	activeIssue := dbfx.Issue(t, "Issue with active task", testutil.Cols{
		"project_id": projectA, "status": "backlog", "priority": "urgent",
		"updated_at": testutil.Raw("'2020-01-01T00:00:00Z'::timestamptz"),
	})
	var agentID string
	dbfx.QueryRow(t, `SELECT id::text FROM agent WHERE workspace_id=$1 ORDER BY created_at LIMIT 1`, testWorkspaceID).Scan(&agentID)
	dbfx.Task(t, agentID, testutil.Cols{"issue_id": activeIssue, "status": "queued", "runtime_id": testRuntimeID})
	activeWorkflowIssue := dbfx.Issue(t, "Issue with active Workflow", testutil.Cols{
		"project_id": projectA, "status": "backlog", "priority": "urgent",
		"updated_at": testutil.Raw("'2020-01-01T00:00:00Z'::timestamptz"),
	})
	ap, err := testHandler.Queries.GetAutopilot(context.Background(), parseUUID(autopilotID))
	if err != nil {
		t.Fatal(err)
	}
	activeRun := dbfx.Insert(t, "workflow_run", testutil.Cols{
		"workspace_id": testWorkspaceID, "issue_id": activeWorkflowIssue,
		"template_id": uuidToString(ap.WorkflowTemplateID), "template_version_id": uuidToString(ap.WorkflowTemplateVersionID),
		"status": "running", "source": "manual", "idempotency_key": "active-workflow-" + activeWorkflowIssue,
		"accountable_user_id": testUserID, "input": testutil.Raw("'{}'::jsonb"),
		"context": testutil.Raw("'{}'::jsonb"), "policy": testutil.Raw("'{}'::jsonb"),
	})
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM workflow_run WHERE id=$1`, activeRun) })
	dbfx.Issue(t, "Other project must not be scanned", testutil.Cols{
		"project_id": projectB, "status": "backlog", "priority": "urgent",
		"updated_at": testutil.Raw("'2019-01-01T00:00:00Z'::timestamptz"),
	})

	putIssuePoolPolicy(t, autopilotID, map[string]any{
		"eligible_statuses":           []string{"backlog"},
		"inactive_for_days":           30,
		"batch_limit":                 10,
		"max_in_flight":               10,
		"require_description":         false,
		"require_acceptance_criteria": false,
	})
	first := previewIssuePool(t, autopilotID, http.StatusOK)
	second := previewIssuePool(t, autopilotID, http.StatusOK)

	if first.ScannedCount != 5 || first.EligibleCount != 2 || first.SelectedCount != 2 {
		t.Fatalf("preview counts = scanned:%d eligible:%d selected:%d, want 5/2/2", first.ScannedCount, first.EligibleCount, first.SelectedCount)
	}
	if got := first.ExcludedByRule["human_assignee"]; got != 1 {
		t.Fatalf("human_assignee exclusions = %d, want 1", got)
	}
	if got := first.ExcludedByRule["active_task"]; got != 1 {
		t.Fatalf("active_task exclusions = %d, want 1", got)
	}
	if got := first.ExcludedByRule["active_workflow"]; got != 1 {
		t.Fatalf("active_workflow exclusions = %d, want 1", got)
	}
	if first.Candidates[0].IssueID != high || first.Candidates[1].IssueID != low {
		t.Fatalf("stable score order = [%s %s], want [%s %s]", first.Candidates[0].IssueID, first.Candidates[1].IssueID, high, low)
	}
	for i := range first.Candidates {
		a, b := first.Candidates[i], second.Candidates[i]
		if a.IssueID != b.IssueID || a.Score != b.Score || len(a.Reasons) != 3 || a.Breakdown["total"] != int(a.Score) {
			t.Fatalf("candidate %d is not reproducible/explainable: first=%+v second=%+v", i, a, b)
		}
	}

	// A later Autopilot retarget cannot silently broaden or move the persisted
	// policy. It must be re-saved to acknowledge the new project boundary.
	dbfx.Exec(t, `UPDATE autopilot SET project_id=$1 WHERE id=$2`, projectB, autopilotID)
	path := "/api/autopilots/" + autopilotID + "/issue-pool/preview?workspace_id=" + testWorkspaceID
	req := withURLParam(newRequest(http.MethodPost, path, nil), "id", autopilotID)
	testutil.Call(t, testHandler.PreviewIssuePool, req).Want(http.StatusConflict)
}

func TestIssuePoolClaimConcurrentAndIdempotent(t *testing.T) {
	autopilotID := newIssuePoolAutopilot(t, nil)
	cleanIssuePoolRows(t, autopilotID)
	dbfx.Issue(t, "Only claimable issue", testutil.Cols{
		"status": "backlog", "updated_at": testutil.Raw("'2020-01-01T00:00:00Z'::timestamptz"),
	})
	dbfx.Issue(t, "Second claimable issue", testutil.Cols{
		"status": "backlog", "updated_at": testutil.Raw("'2020-01-02T00:00:00Z'::timestamptz"),
	})
	putIssuePoolPolicy(t, autopilotID, map[string]any{
		"inactive_for_days": 0, "batch_limit": 1, "max_in_flight": 1,
		"require_description": false, "require_acceptance_criteria": false,
	})

	var taskCountBefore int
	dbfx.QueryRow(t, `SELECT count(*) FROM agent_task_queue`).Scan(&taskCountBefore)
	type result struct {
		status int
		body   []byte
	}
	results := make(chan result, 2)
	var wg sync.WaitGroup
	for _, key := range []string{"concurrent-a", "concurrent-b"} {
		wg.Add(1)
		go func(key string) {
			defer wg.Done()
			path := "/api/autopilots/" + autopilotID + "/issue-pool/cycles?workspace_id=" + testWorkspaceID
			req := withURLParam(newRequest(http.MethodPost, path, map[string]any{"idempotency_key": key}), "id", autopilotID)
			w := httptest.NewRecorder()
			testHandler.CreateIssuePoolCycle(w, req)
			results <- result{status: w.Code, body: append([]byte(nil), w.Body.Bytes()...)}
		}(key)
	}
	wg.Wait()
	close(results)
	claimedResponses := 0
	var firstCycleID string
	for got := range results {
		if got.status != http.StatusCreated {
			t.Fatalf("concurrent cycle status = %d, body=%s", got.status, got.body)
		}
		var cycle IssuePoolCycleResponse
		if err := json.Unmarshal(got.body, &cycle); err != nil {
			t.Fatalf("decode concurrent cycle: %v", err)
		}
		if firstCycleID == "" {
			firstCycleID = cycle.ID
		}
		if len(cycle.Items) == 1 {
			claimedResponses++
		}
	}
	if claimedResponses != 1 {
		t.Fatalf("responses with a claim = %d, want exactly 1", claimedResponses)
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM issue_pool_item WHERE autopilot_id=$1 AND status='claimed'`, autopilotID); got != 1 {
		t.Fatalf("active claims = %d, want 1", got)
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM issue_pool_cycle WHERE autopilot_id=$1`, autopilotID); got != 2 {
		t.Fatalf("cycles = %d, want 2", got)
	}
	var taskCountAfter int
	dbfx.QueryRow(t, `SELECT count(*) FROM agent_task_queue`).Scan(&taskCountAfter)
	if taskCountAfter != taskCountBefore {
		t.Fatalf("manual review claim created agent tasks: before=%d after=%d", taskCountBefore, taskCountAfter)
	}

	replay := createIssuePoolCycle(t, autopilotID, "concurrent-a", http.StatusOK)
	if !replay.Idempotent {
		t.Fatal("replayed idempotency key was not marked as an idempotent replay")
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM issue_pool_cycle WHERE autopilot_id=$1`, autopilotID); got != 2 {
		t.Fatalf("idempotent replay inserted a cycle: count=%d", got)
	}
}

func TestIssuePoolManualReviewRejectReleasesApproveRetainsClaim(t *testing.T) {
	autopilotID := newIssuePoolAutopilot(t, nil)
	cleanIssuePoolRows(t, autopilotID)
	dbfx.Issue(t, "Reviewable issue", testutil.Cols{
		"status": "backlog", "updated_at": testutil.Raw("'2020-01-01T00:00:00Z'::timestamptz"),
	})
	putIssuePoolPolicy(t, autopilotID, map[string]any{
		"inactive_for_days": 0, "batch_limit": 1, "max_in_flight": 1,
		"require_description": false, "require_acceptance_criteria": false,
	})
	first := createIssuePoolCycle(t, autopilotID, "review-1", http.StatusCreated)
	if len(first.Items) != 1 || first.Status != "awaiting_review" {
		t.Fatalf("first cycle = %+v, want one awaiting item", first)
	}

	// Rejection reasons are part of the audit contract and cannot be omitted.
	reviewIssuePoolItems(t, autopilotID, first.ID, []map[string]any{{
		"item_id": first.Items[0].ID, "decision": "reject",
	}}, http.StatusBadRequest)
	rejected := reviewIssuePoolItems(t, autopilotID, first.ID, []map[string]any{{
		"item_id": first.Items[0].ID, "decision": "reject", "reason": "Needs clearer impact",
	}}, http.StatusOK)
	if rejected.Status != "completed" || rejected.Items[0].Status != "rejected" || rejected.Items[0].ReviewReason == nil {
		t.Fatalf("rejected cycle did not preserve review audit: %+v", rejected)
	}
	// Same decision is idempotent.
	reviewIssuePoolItems(t, autopilotID, first.ID, []map[string]any{{
		"item_id": first.Items[0].ID, "decision": "reject", "reason": "Needs clearer impact",
	}}, http.StatusOK)

	second := createIssuePoolCycle(t, autopilotID, "review-2", http.StatusCreated)
	if len(second.Items) != 1 {
		t.Fatalf("rejected claim was not released: second items=%d", len(second.Items))
	}
	approved := reviewIssuePoolItems(t, autopilotID, second.ID, []map[string]any{{
		"item_id": second.Items[0].ID, "decision": "approve",
	}}, http.StatusOK)
	if approved.Status != "running" || approved.Items[0].Status != "approved" {
		t.Fatalf("approval did not persist: %+v", approved)
	}
	// Approval is an active claim reserved for phase 4 dispatch.
	third := createIssuePoolCycle(t, autopilotID, "review-3", http.StatusCreated)
	if len(third.Items) != 0 {
		t.Fatalf("approved claim was released unexpectedly: third items=%d", len(third.Items))
	}
	reviewIssuePoolItems(t, autopilotID, second.ID, []map[string]any{{
		"item_id": second.Items[0].ID, "decision": "reject", "reason": "Changed mind",
	}}, http.StatusConflict)

	if got := dbfx.Count(t, `SELECT count(*) FROM agent_task_queue task JOIN issue_pool_item item ON item.issue_id=task.issue_id WHERE item.autopilot_id=$1`, autopilotID); got != 0 {
		t.Fatalf("phase-1 review dispatched %d tasks, want 0", got)
	}
}

func TestIssuePoolCycleWorkspaceBoundary(t *testing.T) {
	autopilotID := newIssuePoolAutopilot(t, nil)
	cleanIssuePoolRows(t, autopilotID)
	foreignWorkspace := dbfx.Workspace(t, "Foreign issue pool workspace", "foreign-issue-pool")
	foreignAutopilot := dbfx.Insert(t, "autopilot", testutil.Cols{
		"workspace_id": foreignWorkspace, "title": "Foreign pool", "assignee_type": "agent",
		"assignee_id": dbfx.Agent(t, "Foreign agent", "", testutil.Cols{"workspace_id": foreignWorkspace}),
		"status":      "active", "execution_mode": "run_only", "created_by_type": "member", "created_by_id": testUserID,
	})
	foreignPolicy := dbfx.Insert(t, "issue_pool_policy", testutil.Cols{
		"autopilot_id": foreignAutopilot, "workspace_id": foreignWorkspace, "created_by_id": testUserID,
	})
	foreignCycle := dbfx.Insert(t, "issue_pool_cycle", testutil.Cols{
		"policy_id": foreignPolicy, "autopilot_id": foreignAutopilot, "workspace_id": foreignWorkspace,
		"idempotency_key": "foreign", "status": "completed", "policy_snapshot": testutil.Raw("'{}'::jsonb"), "created_by_id": testUserID,
	})

	path := "/api/autopilots/" + autopilotID + "/issue-pool/cycles/" + foreignCycle + "?workspace_id=" + testWorkspaceID
	req := withURLParam(newRequest(http.MethodGet, path, nil), "id", autopilotID)
	req = withIssuePoolParams(req, autopilotID, foreignCycle)
	testutil.Call(t, testHandler.GetIssuePoolCycle, req).Want(http.StatusNotFound)

	// The handler's query itself carries workspace+autopilot scope; direct row
	// knowledge cannot be used to cross that boundary.
	var count int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM issue_pool_cycle WHERE id=$1 AND workspace_id=$2 AND autopilot_id=$3`, foreignCycle, testWorkspaceID, autopilotID).Scan(&count); err != nil {
		t.Fatalf("verify workspace boundary: %v", err)
	}
	if count != 0 {
		t.Fatalf("foreign cycle matched local scope: count=%d", count)
	}
}

func TestIssuePoolApprovalRunsOriginalIssueAndRecoversLostLink(t *testing.T) {
	autopilotID := newIssuePoolAutopilot(t, nil)
	cleanIssuePoolRows(t, autopilotID)
	issueID := dbfx.Issue(t, "Execute this historical issue", testutil.Cols{
		"status": "backlog", "description": "deterministic input",
		"updated_at": testutil.Raw("'2020-01-01T00:00:00Z'::timestamptz"),
	})
	putIssuePoolPolicy(t, autopilotID, map[string]any{
		"inactive_for_days": 0, "batch_limit": 1, "max_in_flight": 1,
		"require_description": false, "require_acceptance_criteria": false,
		"workflow_input_mapping": map[string]string{"title": "title", "description": "description"},
	})
	t.Cleanup(func() {
		ctx := context.Background()
		testPool.Exec(ctx, `DELETE FROM workflow_event WHERE run_id IN (SELECT id FROM workflow_run WHERE idempotency_key LIKE 'issue-pool:%')`)
		testPool.Exec(ctx, `DELETE FROM workflow_step_instance WHERE run_id IN (SELECT id FROM workflow_run WHERE idempotency_key LIKE 'issue-pool:%')`)
		testPool.Exec(ctx, `DELETE FROM workflow_run WHERE idempotency_key LIKE 'issue-pool:%'`)
	})
	previous := testHandler.WorkflowEngine
	testHandler.WorkflowEngine = &workflow.Engine{Queries: testHandler.Queries, TxStarter: testPool, Schemas: workflow.DefaultSchemaRegistry}
	t.Cleanup(func() { testHandler.WorkflowEngine = previous })

	var issueCountBefore int
	dbfx.QueryRow(t, `SELECT count(*) FROM issue WHERE workspace_id=$1`, testWorkspaceID).Scan(&issueCountBefore)
	cycle := createIssuePoolCycle(t, autopilotID, "execute-original", http.StatusCreated)
	if len(cycle.Items) != 1 || cycle.Items[0].IssueID != issueID {
		t.Fatalf("cycle items = %+v", cycle.Items)
	}
	result := reviewIssuePoolItems(t, autopilotID, cycle.ID, []map[string]any{{"item_id": cycle.Items[0].ID, "decision": "approve"}}, http.StatusOK)
	if result.Status != "completed" || result.Items[0].Status != "completed" {
		t.Fatalf("execution did not aggregate to completed: %+v", result)
	}
	var runID, runIssueID, key string
	dbfx.QueryRow(t, `SELECT workflow_run_id::text FROM issue_pool_item WHERE id=$1`, cycle.Items[0].ID).Scan(&runID)
	dbfx.QueryRow(t, `SELECT issue_id::text,idempotency_key FROM workflow_run WHERE id=$1`, runID).Scan(&runIssueID, &key)
	if runIssueID != issueID || key != "issue-pool:"+cycle.Items[0].ID {
		t.Fatalf("run issue/key = %s/%s", runIssueID, key)
	}
	var issueCountAfter int
	dbfx.QueryRow(t, `SELECT count(*) FROM issue WHERE workspace_id=$1`, testWorkspaceID).Scan(&issueCountAfter)
	if issueCountAfter != issueCountBefore {
		t.Fatalf("issue-pool created a new issue: before=%d after=%d", issueCountBefore, issueCountAfter)
	}

	// Simulate the precise crash window: StartRun committed, but the item link
	// was not observed. Replaying the item finds the idempotent Run and backfills
	// the same id without creating a second run.
	dbfx.Exec(t, `UPDATE issue_pool_item SET workflow_run_id=NULL,status='dispatching',updated_at=now()-interval '2 minutes' WHERE id=$1`, cycle.Items[0].ID)
	// Exercise periodic discovery as well as replay; direct dispatch hid SQL errors.
	dbfx.Exec(t, `UPDATE issue_pool_cycle SET status='running' WHERE id=$1`, cycle.ID)
	testHandler.reconcileIssuePools(context.Background())
	var repairedRunID string
	dbfx.QueryRow(t, `SELECT workflow_run_id::text FROM issue_pool_item WHERE id=$1`, cycle.Items[0].ID).Scan(&repairedRunID)
	if repairedRunID != runID {
		t.Fatalf("repaired run = %s, want %s", repairedRunID, runID)
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM workflow_run WHERE idempotency_key=$1`, key); got != 1 {
		t.Fatalf("runs after replay = %d", got)
	}
}

func TestIssuePoolReconcilerReclaimsExpiredClaimAndDeduplicatesOutbox(t *testing.T) {
	autopilotID := newIssuePoolAutopilot(t, nil)
	cleanIssuePoolRows(t, autopilotID)
	issueID := dbfx.Issue(t, "Expired pool claim", testutil.Cols{
		"status": "backlog", "updated_at": testutil.Raw("'2020-01-01T00:00:00Z'::timestamptz"),
	})
	putIssuePoolPolicy(t, autopilotID, map[string]any{
		"inactive_for_days": 0, "batch_limit": 1, "max_in_flight": 1,
		"require_description": false, "require_acceptance_criteria": false,
	})
	cycle := createIssuePoolCycle(t, autopilotID, "expire-claim", http.StatusCreated)
	dbfx.Exec(t, `UPDATE issue_pool_item SET claim_expires_at=now()-interval '1 second' WHERE id=$1`, cycle.Items[0].ID)
	testHandler.reconcileIssuePools(context.Background())
	var state string
	var code *string
	dbfx.QueryRow(t, `SELECT status,failure_code FROM issue_pool_item WHERE id=$1`, cycle.Items[0].ID).Scan(&state, &code)
	if state != "deferred" || code == nil || *code != "claim_expired" {
		t.Fatalf("expired claim = %s/%v", state, code)
	}
	reclaimed := createIssuePoolCycle(t, autopilotID, "reclaim-claim", http.StatusCreated)
	if len(reclaimed.Items) != 1 || reclaimed.Items[0].IssueID != issueID {
		t.Fatalf("claim was not reclaimable: %+v", reclaimed.Items)
	}

	ap, _ := testHandler.Queries.GetAutopilot(context.Background(), parseUUID(autopilotID))
	dbfx.Exec(t, `UPDATE issue_pool_item SET status='rejected',reviewer_id=$2,review_reason='no',reviewed_at=now() WHERE id=$1`, reclaimed.Items[0].ID, testUserID)
	testHandler.reconcileIssuePoolCycle(context.Background(), ap, parseUUID(reclaimed.ID))
	first := dbfx.Count(t, `SELECT count(*) FROM issue_pool_notification_outbox WHERE cycle_id=$1`, reclaimed.ID)
	testHandler.reconcileIssuePoolCycle(context.Background(), ap, parseUUID(reclaimed.ID))
	second := dbfx.Count(t, `SELECT count(*) FROM issue_pool_notification_outbox WHERE cycle_id=$1`, reclaimed.ID)
	if first == 0 || second != first {
		t.Fatalf("outbox dedupe counts = %d then %d", first, second)
	}
}

func TestDispatchIssuePoolAutopilotRunIsIdempotent(t *testing.T) {
	autopilotID := newIssuePoolAutopilot(t, nil)
	cleanIssuePoolRows(t, autopilotID)
	dbfx.Issue(t, "Autopilot pool candidate", testutil.Cols{
		"status": "backlog", "updated_at": testutil.Raw("'2020-01-01T00:00:00Z'::timestamptz"),
	})
	putIssuePoolPolicy(t, autopilotID, map[string]any{
		"inactive_for_days": 0, "batch_limit": 1, "max_in_flight": 1,
		"require_description": false, "require_acceptance_criteria": false,
	})
	runID := dbfx.Insert(t, "autopilot_run", testutil.Cols{
		"autopilot_id": autopilotID, "source": "manual", "status": "running",
	})
	ap, err := testHandler.Queries.GetAutopilot(context.Background(), parseUUID(autopilotID))
	if err != nil {
		t.Fatal(err)
	}
	run, err := testHandler.Queries.GetAutopilotRun(context.Background(), parseUUID(runID))
	if err != nil {
		t.Fatal(err)
	}
	if err := testHandler.DispatchIssuePool(context.Background(), ap, &run, parseUUID(testUserID)); err != nil {
		t.Fatal(err)
	}
	firstNotices := dbfx.Count(t, `SELECT count(*) FROM issue_pool_notification_outbox WHERE autopilot_id=$1 AND event_type='candidate_review'`, autopilotID)
	if err := testHandler.DispatchIssuePool(context.Background(), ap, &run, parseUUID(testUserID)); err != nil {
		t.Fatal(err)
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM issue_pool_cycle WHERE autopilot_run_id=$1`, runID); got != 1 {
		t.Fatalf("cycles for replayed run = %d", got)
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM issue_pool_item WHERE autopilot_id=$1 AND status='claimed'`, autopilotID); got != 1 {
		t.Fatalf("claims for replayed run = %d", got)
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM issue_pool_notification_outbox WHERE autopilot_id=$1 AND event_type='candidate_review'`, autopilotID); firstNotices == 0 || got != firstNotices {
		t.Fatalf("review notices for replayed run = %d then %d", firstNotices, got)
	}
}

func TestIssuePoolConcurrentDispatchCreatesExactlyOneWorkflowRunAndTask(t *testing.T) {
	autopilotID := newIssuePoolAutopilotWithDefinition(t, nil, func(agentID string) *workflow.Definition {
		return &workflow.Definition{
			SchemaVersion: workflow.SchemaVersion, EntryNode: "implement",
			Nodes: []workflow.Node{
				{Key: "implement", Type: workflow.NodeTypeAgent, Next: []string{"end"}, Routing: &workflow.Routing{Strategy: workflow.RoutingExplicit, AgentID: agentID}, SubmissionSchema: "code_change"},
				{Key: "end", Type: workflow.NodeTypeEnd},
			},
		}
	})
	cleanIssuePoolRows(t, autopilotID)
	dbfx.Issue(t, "Concurrent dispatch candidate", testutil.Cols{
		"status": "backlog", "description": "dispatch exactly once",
		"updated_at": testutil.Raw("'2020-01-01T00:00:00Z'::timestamptz"),
	})
	putIssuePoolPolicy(t, autopilotID, map[string]any{
		"inactive_for_days": 0, "batch_limit": 1, "max_in_flight": 1,
		"require_description": false, "require_acceptance_criteria": false,
	})
	previous := testHandler.WorkflowEngine
	testHandler.WorkflowEngine = nil
	t.Cleanup(func() { testHandler.WorkflowEngine = previous })
	cycle := createIssuePoolCycle(t, autopilotID, "concurrent-dispatch", http.StatusCreated)
	reviewIssuePoolItems(t, autopilotID, cycle.ID, []map[string]any{{"item_id": cycle.Items[0].ID, "decision": "approve"}}, http.StatusOK)
	testHandler.WorkflowEngine = &workflow.Engine{
		Queries: testHandler.Queries, TxStarter: testPool, Router: service.NewWorkflowRouter(testHandler.Queries), Schemas: workflow.DefaultSchemaRegistry,
	}
	ap, _ := testHandler.Queries.GetAutopilot(context.Background(), parseUUID(autopilotID))
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			testHandler.dispatchApprovedIssuePoolItems(context.Background(), ap, parseUUID(cycle.ID), parseUUID(testUserID))
		}()
	}
	wg.Wait()
	key := "issue-pool:" + cycle.Items[0].ID
	if got := dbfx.Count(t, `SELECT count(*) FROM workflow_run WHERE idempotency_key=$1`, key); got != 1 {
		t.Fatalf("concurrent dispatch WorkflowRuns = %d, want 1", got)
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM agent_task_queue task JOIN workflow_step_instance step ON step.task_id=task.id JOIN workflow_run run ON run.id=step.run_id WHERE run.idempotency_key=$1`, key); got != 1 {
		t.Fatalf("concurrent dispatch tasks = %d, want 1", got)
	}
	var attempts int
	dbfx.QueryRow(t, `SELECT dispatch_attempts FROM issue_pool_item WHERE id=$1`, cycle.Items[0].ID).Scan(&attempts)
	if attempts != 1 {
		t.Fatalf("dispatch attempts = %d, want 1 claimed worker", attempts)
	}
}

func TestIssuePoolWorkflowAcceptanceAndBoundedReworkRemainCanonical(t *testing.T) {
	definition := func(agentID string) *workflow.Definition {
		return &workflow.Definition{
			SchemaVersion: workflow.SchemaVersion, EntryNode: "implement",
			Nodes: []workflow.Node{
				{Key: "implement", Type: workflow.NodeTypeAgent, Next: []string{"acceptance"}, Routing: &workflow.Routing{Strategy: workflow.RoutingExplicit, AgentID: agentID}, SubmissionSchema: "code_change"},
				{Key: "acceptance", Type: workflow.NodeTypeAcceptance, Next: []string{"end"}, AcceptanceCriteria: []string{"result verified"}, ReworkTargets: []string{"implement"}},
				{Key: "end", Type: workflow.NodeTypeEnd},
			},
			Limits: workflow.Limits{MaxAttemptsPerNode: 3, MaxReworkRounds: 1},
		}
	}
	previous := testHandler.WorkflowEngine
	engine := &workflow.Engine{Queries: testHandler.Queries, TxStarter: testPool, Router: service.NewWorkflowRouter(testHandler.Queries), Schemas: workflow.DefaultSchemaRegistry}
	testHandler.WorkflowEngine = engine
	t.Cleanup(func() { testHandler.WorkflowEngine = previous })

	start := func(t *testing.T, key string) (string, IssuePoolCycleResponse) {
		autopilotID := newIssuePoolAutopilotWithDefinition(t, nil, definition)
		cleanIssuePoolRows(t, autopilotID)
		dbfx.Issue(t, "Acceptance "+key, testutil.Cols{
			"status": "backlog", "description": "workflow acceptance input",
			"updated_at": testutil.Raw("'2020-01-01T00:00:00Z'::timestamptz"),
		})
		putIssuePoolPolicy(t, autopilotID, map[string]any{
			"inactive_for_days": 0, "batch_limit": 1, "max_in_flight": 1,
			"require_description": false, "require_acceptance_criteria": false,
		})
		cycle := createIssuePoolCycle(t, autopilotID, key, http.StatusCreated)
		cycle = reviewIssuePoolItems(t, autopilotID, cycle.ID, []map[string]any{{"item_id": cycle.Items[0].ID, "decision": "approve"}}, http.StatusOK)
		if cycle.Items[0].WorkflowRunID == nil {
			t.Fatal("approved item has no canonical WorkflowRun")
		}
		return autopilotID, cycle
	}
	latestStep := func(t *testing.T, runID, node string) db.WorkflowStepInstance {
		steps, err := testHandler.Queries.ListWorkflowStepInstances(context.Background(), db.ListWorkflowStepInstancesParams{RunID: parseUUID(runID), WorkspaceID: parseUUID(testWorkspaceID)})
		if err != nil {
			t.Fatal(err)
		}
		var latest db.WorkflowStepInstance
		for _, step := range steps {
			if step.NodeKey == node && step.Attempt >= latest.Attempt {
				latest = step
			}
		}
		return latest
	}
	submit := func(t *testing.T, runID, summary string) {
		step := latestStep(t, runID, "implement")
		dbfx.Exec(t, `UPDATE agent_task_queue SET status='completed',completed_at=now() WHERE id=$1`, step.TaskID)
		raw := fmt.Sprintf(`{"verdict":"pass","artifact":{"type":"code_change","summary":%q},"rationale":"done","confidence":0.9}`, summary)
		if _, err := engine.SubmitResult(context.Background(), workflow.SubmitResultInput{
			WorkspaceID: parseUUID(testWorkspaceID), StepID: step.ID, RawOutput: raw, Payload: []byte(raw), ActorType: "agent", ActorID: step.AgentID,
		}); err != nil {
			t.Fatalf("submit Workflow result: %v", err)
		}
	}
	decide := func(t *testing.T, runID string, accept bool, reason string) {
		step := latestStep(t, runID, "acceptance")
		pending, err := testHandler.Queries.GetPendingWorkflowAcceptanceForStep(context.Background(), db.GetPendingWorkflowAcceptanceForStepParams{StepID: step.ID, WorkspaceID: parseUUID(testWorkspaceID)})
		if err != nil {
			t.Fatal(err)
		}
		input := workflow.DecideAcceptanceInput{WorkspaceID: parseUUID(testWorkspaceID), AcceptanceID: pending.ID, Accept: accept, ReviewerUserID: parseUUID(testUserID)}
		if !accept {
			input.Reason, input.ReworkTarget = reason, "implement"
		}
		if _, err := engine.DecideAcceptance(context.Background(), input); err != nil {
			t.Fatalf("decide acceptance: %v", err)
		}
	}

	t.Run("accept completes only after human decision", func(t *testing.T) {
		autopilotID, cycle := start(t, "accept")
		runID := *cycle.Items[0].WorkflowRunID
		submit(t, runID, "accepted result")
		ap, _ := testHandler.Queries.GetAutopilot(context.Background(), parseUUID(autopilotID))
		testHandler.reconcileIssuePoolCycle(context.Background(), ap, parseUUID(cycle.ID))
		var itemStatus, cycleStatus string
		dbfx.QueryRow(t, `SELECT status FROM issue_pool_item WHERE id=$1`, cycle.Items[0].ID).Scan(&itemStatus)
		dbfx.QueryRow(t, `SELECT status FROM issue_pool_cycle WHERE id=$1`, cycle.ID).Scan(&cycleStatus)
		if itemStatus != "waiting_acceptance" || cycleStatus != "waiting_acceptance" {
			t.Fatalf("before acceptance item/cycle = %s/%s", itemStatus, cycleStatus)
		}
		decide(t, runID, true, "")
		testHandler.reconcileIssuePoolCycle(context.Background(), ap, parseUUID(cycle.ID))
		dbfx.QueryRow(t, `SELECT status FROM issue_pool_item WHERE id=$1`, cycle.Items[0].ID).Scan(&itemStatus)
		if itemStatus != "completed" {
			t.Fatalf("accepted item status = %s", itemStatus)
		}
	})

	t.Run("rejection reworks same run and blocks at limit", func(t *testing.T) {
		autopilotID, cycle := start(t, "bounded-rework")
		runID := *cycle.Items[0].WorkflowRunID
		submit(t, runID, "attempt one")
		decide(t, runID, false, "retry once")
		submit(t, runID, "attempt two")
		decide(t, runID, false, "budget exhausted")
		ap, _ := testHandler.Queries.GetAutopilot(context.Background(), parseUUID(autopilotID))
		testHandler.reconcileIssuePoolCycle(context.Background(), ap, parseUUID(cycle.ID))
		var itemStatus, cycleStatus string
		dbfx.QueryRow(t, `SELECT status FROM issue_pool_item WHERE id=$1`, cycle.Items[0].ID).Scan(&itemStatus)
		dbfx.QueryRow(t, `SELECT status FROM issue_pool_cycle WHERE id=$1`, cycle.ID).Scan(&cycleStatus)
		if itemStatus != "blocked" || cycleStatus != "blocked" {
			t.Fatalf("exhausted rework item/cycle = %s/%s", itemStatus, cycleStatus)
		}
		if got := dbfx.Count(t, `SELECT count(*) FROM workflow_run WHERE id=$1`, runID); got != 1 {
			t.Fatalf("bounded rework WorkflowRuns = %d, want same one", got)
		}
	})
}

func TestIssuePoolOutboxCrashReplayPersistsOneInboxItem(t *testing.T) {
	autopilotID := newIssuePoolAutopilot(t, nil)
	cleanIssuePoolRows(t, autopilotID)
	dbfx.Issue(t, "Outbox replay candidate", testutil.Cols{"status": "backlog", "updated_at": testutil.Raw("'2020-01-01T00:00:00Z'::timestamptz")})
	putIssuePoolPolicy(t, autopilotID, map[string]any{"inactive_for_days": 0, "batch_limit": 1, "max_in_flight": 1, "require_description": false, "require_acceptance_criteria": false})
	cycle := createIssuePoolCycle(t, autopilotID, "outbox-crash", http.StatusCreated)
	var notificationID string
	dbfx.QueryRow(t, `INSERT INTO issue_pool_notification_outbox(workspace_id,autopilot_id,cycle_id,item_id,recipient_id,event_type,payload)
		VALUES($1,$2,$3,$4,$5,'candidate_review','{}') RETURNING id::text`, testWorkspaceID, autopilotID, cycle.ID, cycle.Items[0].ID, testUserID).Scan(&notificationID)
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM inbox_item WHERE details->>'notification_id'=$1`, notificationID)
	})
	n := issuePoolOutboxNotice{id: parseUUID(notificationID), workspace: parseUUID(testWorkspaceID), recipient: parseUUID(testUserID), event: "candidate_review", payload: []byte(`{}`)}
	if err := testHandler.persistIssuePoolInbox(context.Background(), n); err != nil {
		t.Fatal(err)
	}
	// Crash now: the durable inbox commit exists but delivered_at was never set.
	if got := dbfx.Count(t, `SELECT count(*) FROM inbox_item WHERE details->>'notification_id'=$1`, notificationID); got != 1 {
		t.Fatalf("inbox rows before replay = %d, want 1", got)
	}
	testHandler.reconcileIssuePools(context.Background())
	if got := dbfx.Count(t, `SELECT count(*) FROM inbox_item WHERE details->>'notification_id'=$1`, notificationID); got != 1 {
		t.Fatalf("inbox rows after crash replay = %d, want 1", got)
	}
	var delivered bool
	dbfx.QueryRow(t, `SELECT delivered_at IS NOT NULL FROM issue_pool_notification_outbox WHERE id=$1`, notificationID).Scan(&delivered)
	if !delivered {
		t.Fatal("replayed outbox was not marked delivered")
	}
}

func TestIssuePoolInfrastructureFailureProjectsAutopilotRunFailed(t *testing.T) {
	autopilotID := newIssuePoolAutopilot(t, nil)
	cleanIssuePoolRows(t, autopilotID)
	dbfx.Issue(t, "Failed pool candidate", testutil.Cols{"status": "backlog", "updated_at": testutil.Raw("'2020-01-01T00:00:00Z'::timestamptz")})
	putIssuePoolPolicy(t, autopilotID, map[string]any{"inactive_for_days": 0, "batch_limit": 1, "max_in_flight": 1, "require_description": false, "require_acceptance_criteria": false})
	runID := dbfx.Insert(t, "autopilot_run", testutil.Cols{"autopilot_id": autopilotID, "source": "manual", "status": "running"})
	ap, _ := testHandler.Queries.GetAutopilot(context.Background(), parseUUID(autopilotID))
	run, _ := testHandler.Queries.GetAutopilotRun(context.Background(), parseUUID(runID))
	if err := testHandler.DispatchIssuePool(context.Background(), ap, &run, parseUUID(testUserID)); err != nil {
		t.Fatal(err)
	}
	var cycleID, itemID string
	dbfx.QueryRow(t, `SELECT cycle.id::text,item.id::text FROM issue_pool_cycle cycle JOIN issue_pool_item item ON item.cycle_id=cycle.id WHERE cycle.autopilot_run_id=$1`, runID).Scan(&cycleID, &itemID)
	dbfx.Exec(t, `UPDATE issue_pool_item SET status='failed',failure_code='infrastructure_unavailable',completed_at=now(),reviewer_id=$2,reviewed_at=now() WHERE id=$1`, itemID, testUserID)
	testHandler.reconcileIssuePoolCycle(context.Background(), ap, parseUUID(cycleID))
	var cycleStatus, runStatus, failure string
	dbfx.QueryRow(t, `SELECT status FROM issue_pool_cycle WHERE id=$1`, cycleID).Scan(&cycleStatus)
	dbfx.QueryRow(t, `SELECT status,COALESCE(failure_reason,'') FROM autopilot_run WHERE id=$1`, runID).Scan(&runStatus, &failure)
	if cycleStatus != "failed" || runStatus != "failed" || failure != "issue_pool_failed" {
		t.Fatalf("failure projection cycle/run/reason = %s/%s/%s", cycleStatus, runStatus, failure)
	}
}

func TestIssuePoolDispatchDefersIssueChangedAfterReview(t *testing.T) {
	autopilotID := newIssuePoolAutopilot(t, nil)
	cleanIssuePoolRows(t, autopilotID)
	issueID := dbfx.Issue(t, "Snapshot candidate", testutil.Cols{
		"status": "backlog", "updated_at": testutil.Raw("'2020-01-01T00:00:00Z'::timestamptz"),
	})
	putIssuePoolPolicy(t, autopilotID, map[string]any{
		"inactive_for_days": 0, "batch_limit": 1, "max_in_flight": 1,
		"require_description": false, "require_acceptance_criteria": false,
	})
	previous := testHandler.WorkflowEngine
	testHandler.WorkflowEngine = nil
	t.Cleanup(func() { testHandler.WorkflowEngine = previous })
	cycle := createIssuePoolCycle(t, autopilotID, "changed-after-review", http.StatusCreated)
	reviewIssuePoolItems(t, autopilotID, cycle.ID, []map[string]any{{"item_id": cycle.Items[0].ID, "decision": "approve"}}, http.StatusOK)
	// The reviewer approved the old snapshot. A concurrent edit must never be
	// silently fed to the Workflow under that approval.
	dbfx.Exec(t, `UPDATE issue SET title='changed after approval',updated_at=clock_timestamp()+interval '1 second' WHERE id=$1`, issueID)
	testHandler.WorkflowEngine = &workflow.Engine{Queries: testHandler.Queries, TxStarter: testPool, Schemas: workflow.DefaultSchemaRegistry}
	ap, _ := testHandler.Queries.GetAutopilot(context.Background(), parseUUID(autopilotID))
	testHandler.dispatchApprovedIssuePoolItems(context.Background(), ap, parseUUID(cycle.ID), parseUUID(testUserID))
	var status, code string
	dbfx.QueryRow(t, `SELECT status,failure_code FROM issue_pool_item WHERE id=$1`, cycle.Items[0].ID).Scan(&status, &code)
	if status != "deferred" || code != workflow.ErrCodeIdempotencyConflict {
		t.Fatalf("changed snapshot = %s/%s", status, code)
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM workflow_run WHERE idempotency_key=$1`, "issue-pool:"+cycle.Items[0].ID); got != 0 {
		t.Fatalf("changed issue started %d runs", got)
	}
}

func TestWorkflowStartExistingIssueRejectsWorkspaceBoundary(t *testing.T) {
	autopilotID := newIssuePoolAutopilot(t, nil)
	ap, _ := testHandler.Queries.GetAutopilot(context.Background(), parseUUID(autopilotID))
	foreignWorkspace := dbfx.Workspace(t, "Foreign workflow boundary", fmt.Sprintf("foreign-workflow-%d", time.Now().UnixNano()))
	foreignIssue := dbfx.Issue(t, "Foreign issue", testutil.Cols{"workspace_id": foreignWorkspace, "status": "backlog"})
	engine := &workflow.Engine{Queries: testHandler.Queries, TxStarter: testPool, Schemas: workflow.DefaultSchemaRegistry}
	_, err := engine.StartRun(context.Background(), workflow.StartRunInput{
		WorkspaceID: ap.WorkspaceID, TemplateID: ap.WorkflowTemplateID, TemplateVersionID: ap.WorkflowTemplateVersionID,
		IssueID: parseUUID(foreignIssue), Source: "autopilot", IdempotencyKey: "boundary:" + foreignIssue,
		AccountableUserID: parseUUID(testUserID), Input: json.RawMessage(`{}`),
	})
	if err == nil {
		t.Fatal("foreign-workspace issue unexpectedly started a workflow")
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM workflow_run WHERE idempotency_key=$1`, "boundary:"+foreignIssue); got != 0 {
		t.Fatalf("boundary rejection left %d runs", got)
	}
}

func TestIssuePoolCyclePaginationUsesTrueTotal(t *testing.T) {
	autopilotID := newIssuePoolAutopilot(t, nil)
	cleanIssuePoolRows(t, autopilotID)
	putIssuePoolPolicy(t, autopilotID, map[string]any{
		"inactive_for_days": 0, "batch_limit": 1, "max_in_flight": 1,
		"require_description": false, "require_acceptance_criteria": false,
	})
	createIssuePoolCycle(t, autopilotID, "page-one", http.StatusCreated)
	createIssuePoolCycle(t, autopilotID, "page-two", http.StatusCreated)

	path := "/api/autopilots/" + autopilotID + "/issue-pool/cycles?workspace_id=" + testWorkspaceID + "&page=1&page_size=1"
	req := withURLParam(newRequest(http.MethodGet, path, nil), "id", autopilotID)
	var response IssuePoolCycleListResponse
	testutil.Call(t, testHandler.ListIssuePoolCycles, req).Want(http.StatusOK).JSON(&response)
	if response.Total != 2 || len(response.Cycles) != 1 || response.Page != 1 || response.PageSize != 1 {
		t.Fatalf("cycle pagination = total %d rows %d page %d size %d", response.Total, len(response.Cycles), response.Page, response.PageSize)
	}
}

func TestIssuePoolDualDispatcherBarrierCreatesExactlyOneRunAndTask(t *testing.T) {
	autopilotID := newIssuePoolAutopilotWithDefinition(t, nil, func(agentID string) *workflow.Definition {
		return &workflow.Definition{
			SchemaVersion: workflow.SchemaVersion, EntryNode: "implement",
			Nodes: []workflow.Node{
				{Key: "implement", Type: workflow.NodeTypeAgent, Next: []string{"end"}, Routing: &workflow.Routing{Strategy: workflow.RoutingExplicit, AgentID: agentID}, SubmissionSchema: "code_change"},
				{Key: "end", Type: workflow.NodeTypeEnd},
			},
		}
	})
	cleanIssuePoolRows(t, autopilotID)
	dbfx.Issue(t, "Barrier dispatch candidate", testutil.Cols{
		"status": "backlog", "description": "dispatch exactly once",
		"updated_at": testutil.Raw("'2020-01-01T00:00:00Z'::timestamptz"),
	})
	putIssuePoolPolicy(t, autopilotID, map[string]any{
		"inactive_for_days": 0, "batch_limit": 1, "max_in_flight": 1,
		"require_description": false, "require_acceptance_criteria": false,
	})
	previous := testHandler.WorkflowEngine
	testHandler.WorkflowEngine = nil
	t.Cleanup(func() { testHandler.WorkflowEngine = previous })
	cycle := createIssuePoolCycle(t, autopilotID, "dual-dispatch-barrier", http.StatusCreated)
	reviewIssuePoolItems(t, autopilotID, cycle.ID, []map[string]any{{"item_id": cycle.Items[0].ID, "decision": "approve"}}, http.StatusOK)

	var row issuePoolDispatchRow
	dbfx.QueryRow(t, `SELECT item.id,item.issue_id,item.workspace_id,item.project_id,
		item.cycle_id,item.autopilot_id,cycle.workflow_template_id,cycle.workflow_template_version_id,
		item.reviewer_id,item.issue_snapshot,cycle.workflow_input_mapping_snapshot,cycle.policy_snapshot
		FROM issue_pool_item item JOIN issue_pool_cycle cycle ON cycle.id=item.cycle_id WHERE item.id=$1`, cycle.Items[0].ID).
		Scan(&row.ItemID, &row.IssueID, &row.WorkspaceID, &row.ProjectID, &row.CycleID, &row.AutopilotID,
			&row.TemplateID, &row.TemplateVersionID, &row.ReviewerID, &row.Snapshot, &row.Mapping, &row.PolicySnapshot)
	testHandler.WorkflowEngine = &workflow.Engine{
		Queries: testHandler.Queries, TxStarter: testPool, Router: service.NewWorkflowRouter(testHandler.Queries), Schemas: workflow.DefaultSchemaRegistry,
	}
	ap, _ := testHandler.Queries.GetAutopilot(context.Background(), parseUUID(autopilotID))
	ready := make(chan struct{}, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ready <- struct{}{}
			<-start
			testHandler.dispatchIssuePoolItem(context.Background(), ap, row)
		}()
	}
	<-ready
	<-ready
	close(start)
	wg.Wait()

	key := "issue-pool:" + cycle.Items[0].ID
	if got := dbfx.Count(t, `SELECT count(*) FROM workflow_run WHERE idempotency_key=$1`, key); got != 1 {
		t.Fatalf("dual dispatcher WorkflowRuns = %d, want 1", got)
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM agent_task_queue task JOIN workflow_step_instance step ON step.task_id=task.id JOIN workflow_run run ON run.id=step.run_id WHERE run.idempotency_key=$1`, key); got != 1 {
		t.Fatalf("dual dispatcher AgentTasks = %d, want 1", got)
	}
	var attempts int
	dbfx.QueryRow(t, `SELECT dispatch_attempts FROM issue_pool_item WHERE id=$1`, cycle.Items[0].ID).Scan(&attempts)
	if attempts != 1 {
		t.Fatalf("dispatch attempts = %d, want exactly one CAS winner", attempts)
	}
}

func TestIssuePoolAllFailedProjectsCycleAndAutopilotRunAtomically(t *testing.T) {
	autopilotID := newIssuePoolAutopilot(t, nil)
	cleanIssuePoolRows(t, autopilotID)
	dbfx.Issue(t, "Failed pool candidate", testutil.Cols{"status": "backlog", "updated_at": testutil.Raw("'2020-01-01T00:00:00Z'::timestamptz")})
	putIssuePoolPolicy(t, autopilotID, map[string]any{"inactive_for_days": 0, "batch_limit": 1, "max_in_flight": 1, "require_description": false, "require_acceptance_criteria": false})
	runID := dbfx.Insert(t, "autopilot_run", testutil.Cols{"autopilot_id": autopilotID, "source": "manual", "status": "running"})
	ap, _ := testHandler.Queries.GetAutopilot(context.Background(), parseUUID(autopilotID))
	run, _ := testHandler.Queries.GetAutopilotRun(context.Background(), parseUUID(runID))
	if err := testHandler.DispatchIssuePool(context.Background(), ap, &run, parseUUID(testUserID)); err != nil {
		t.Fatal(err)
	}
	var cycleID, itemID string
	dbfx.QueryRow(t, `SELECT cycle.id::text,item.id::text FROM issue_pool_cycle cycle JOIN issue_pool_item item ON item.cycle_id=cycle.id WHERE cycle.autopilot_run_id=$1`, runID).Scan(&cycleID, &itemID)
	dbfx.Exec(t, `UPDATE issue_pool_item SET status='failed',failure_code='infrastructure_unavailable',completed_at=now(),reviewer_id=$2,reviewed_at=now() WHERE id=$1`, itemID, testUserID)
	testHandler.reconcileIssuePoolCycle(context.Background(), ap, parseUUID(cycleID))
	var cycleStatus, runStatus, failure, resultStatus string
	var cycleCompleted, runCompleted bool
	dbfx.QueryRow(t, `SELECT cycle.status,cycle.completed_at IS NOT NULL,run.status,run.completed_at IS NOT NULL,
		COALESCE(run.failure_reason,''),COALESCE(run.result->>'status','')
		FROM issue_pool_cycle cycle JOIN autopilot_run run ON run.id=cycle.autopilot_run_id WHERE cycle.id=$1`, cycleID).
		Scan(&cycleStatus, &cycleCompleted, &runStatus, &runCompleted, &failure, &resultStatus)
	if cycleStatus != "failed" || runStatus != "failed" || failure != "issue_pool_failed" || resultStatus != "failed" || !cycleCompleted || !runCompleted {
		t.Fatalf("atomic failure projection cycle=%s/%v run=%s/%v reason=%s result=%s",
			cycleStatus, cycleCompleted, runStatus, runCompleted, failure, resultStatus)
	}
}
