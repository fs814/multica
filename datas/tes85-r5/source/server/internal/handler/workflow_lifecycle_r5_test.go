package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/workflow"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func r5ActiveRun(t *testing.T) (WorkflowRunDetailResponse, *workflow.Engine) {
	t.Helper()
	cleanupWorkflowTemplates(t)
	cleanupWorkflowRuns(t)
	engine := withWorkflowEngineForTest(t)
	labelTestAgentWithCapabilities(t, "bug_analysis", "code_change")
	tpl := seededBugFixTemplate(t)
	res := runWorkflowTemplateForTest(t, tpl.ID, map[string]any{"title": "Cancellation atomicity", "description": "Keep every rejected request free of side effects."})
	return decodeWorkflowRunDetail(t, res, "start"), engine
}

func r5Snapshot(t *testing.T, runID string) string {
	t.Helper()
	var value string
	err := testPool.QueryRow(context.Background(), "SELECT jsonb_build_object("+
		"'run',(SELECT to_jsonb(r) FROM workflow_run r WHERE id=$1),"+
		"'issue',(SELECT to_jsonb(i) FROM issue i JOIN workflow_run r ON r.issue_id=i.id WHERE r.id=$1),"+
		"'steps',(SELECT jsonb_agg(to_jsonb(s) ORDER BY s.id) FROM workflow_step_instance s WHERE run_id=$1),"+
		"'events',(SELECT jsonb_agg(to_jsonb(e) ORDER BY e.id) FROM workflow_event e WHERE run_id=$1),"+
		"'acceptances',(SELECT jsonb_agg(to_jsonb(a) ORDER BY a.id) FROM workflow_acceptance a WHERE run_id=$1),"+
		"'tasks',(SELECT jsonb_agg(to_jsonb(t) ORDER BY t.id) FROM agent_task_queue t JOIN workflow_step_instance s ON s.id=t.workflow_step_instance_id WHERE s.run_id=$1))::text", runID).Scan(&value)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestR5IssueCancellationRejectedRequestsAreAtomic(t *testing.T) {
	detail, _ := r5ActiveRun(t)
	for _, tc := range []struct {
		name   string
		fields map[string]any
		status int
	}{
		{"priority", map[string]any{"priority": "invalid"}, 400},
		{"date", map[string]any{"due_date": "not-a-date"}, 400},
		{"assignee", map[string]any{"assignee_type": "member", "assignee_id": "bad"}, 400},
		{"parent", map[string]any{"parent_issue_id": *detail.IssueID}, 400},
		{"attachment", map[string]any{"attachment_ids": []string{"bad"}}, 400},
		{"unknown attachment", map[string]any{"attachment_ids": []string{"00000000-0000-0000-0000-0000000000ff"}}, 400},
		{"revision", map[string]any{"expected_revision": int64(999999)}, 409},
		{"title conflict", map[string]any{"title": "replacement", "title_base": "unrelated"}, 409},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := r5Snapshot(t, detail.ID)
			tc.fields["status"] = "cancelled"
			req := withURLParam(newRequest("PUT", "/api/issues/"+*detail.IssueID, tc.fields), "id", *detail.IssueID)
			testutil.Call(t, testHandler.UpdateIssue, req).Want(tc.status)
			if after := r5Snapshot(t, detail.ID); after != before {
				t.Fatal("rejected request changed persisted business rows")
			}
		})
	}
}

func TestR5IssueBatchCancellationIsAtomicAndNoopIsConsistent(t *testing.T) {
	detail, _ := r5ActiveRun(t)
	issue, err := testHandler.Queries.GetIssue(context.Background(), parseUUID(*detail.IssueID))
	if err != nil {
		t.Fatal(err)
	}
	before := r5Snapshot(t, detail.ID)
	testutil.Call(t, testHandler.UpdateIssue, withURLParam(newRequest("PUT", "/api/issues/"+*detail.IssueID, map[string]any{"status": issue.Status}), "id", *detail.IssueID)).Want(200)
	testutil.Call(t, testHandler.BatchUpdateIssues, newRequest("PATCH", "/api/issues/batch?workspace_id="+testWorkspaceID, map[string]any{"issue_ids": []string{*detail.IssueID, *detail.IssueID}, "updates": map[string]any{"status": issue.Status}})).Want(200)
	if r5Snapshot(t, detail.ID) != before {
		t.Fatal("same-state update changed persisted workflow or issue")
	}
	for _, ids := range [][]string{{*detail.IssueID, "bad-id"}, {"bad-id", *detail.IssueID}} {
		testutil.Call(t, testHandler.BatchUpdateIssues, newRequest("PATCH", "/api/issues/batch?workspace_id="+testWorkspaceID, map[string]any{"issue_ids": ids, "updates": map[string]any{"status": "cancelled"}})).Want(409)
		if r5Snapshot(t, detail.ID) != before {
			t.Fatal("failed batch committed cancellation")
		}
	}
	testutil.Call(t, testHandler.BatchUpdateIssues, newRequest("PATCH", "/api/issues/batch?workspace_id="+testWorkspaceID, map[string]any{"issue_ids": []string{*detail.IssueID, *detail.IssueID}, "updates": map[string]any{"status": "cancelled"}})).Want(200)
	var events int
	if err := testPool.QueryRow(context.Background(), "SELECT count(*) FROM workflow_event WHERE run_id=$1 AND event_type='run.cancelled'", detail.ID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 1 {
		t.Fatalf("duplicate IDs emitted %d cancellation events", events)
	}
}

type r5FailStop struct{ calls int }

func (f *r5FailStop) CancelTask(context.Context, pgtype.UUID) (*db.AgentTaskQueue, error) {
	f.calls++
	return nil, errors.New("injected task service failure")
}

func TestR5CancelledRunRecoversTaskServiceFailureAfterRestart(t *testing.T) {
	detail, engine := r5ActiveRun(t)
	fail := &r5FailStop{}
	engine.TaskCanceller = fail
	steps, err := testHandler.Queries.ListWorkflowStepInstances(context.Background(), db.ListWorkflowStepInstancesParams{RunID: parseUUID(detail.ID), WorkspaceID: parseUUID(testWorkspaceID)})
	if err != nil {
		t.Fatal(err)
	}
	var agentID string
	for _, step := range steps {
		if step.AgentID.Valid {
			agentID = uuidToString(step.AgentID)
			break
		}
	}
	if agentID == "" {
		t.Fatal("fixture has no routed agent")
	}
	agent, err := testHandler.Queries.GetAgent(context.Background(), parseUUID(agentID))
	if err != nil {
		t.Fatal(err)
	}
	unrelatedAgent := dbfx.Agent(t, "Unrelated", uuidToString(agent.RuntimeID))
	unrelated := dbfx.Task(t, unrelatedAgent, testutil.Cols{"issue_id": *detail.IssueID, "runtime_id": uuidToString(agent.RuntimeID)})
	testutil.Call(t, testHandler.UpdateIssue, withURLParam(newRequest("PUT", "/api/issues/"+*detail.IssueID, map[string]any{"status": "cancelled"}), "id", *detail.IssueID)).Want(200)
	if fail.calls == 0 {
		t.Fatal("TaskService seam was not called")
	}
	ids, err := testHandler.Queries.ListActiveAgentTaskIDsForWorkflowRun(context.Background(), db.ListActiveAgentTaskIDsForWorkflowRunParams{RunID: parseUUID(detail.ID), WorkspaceID: parseUUID(testWorkspaceID)})
	if err != nil || len(ids) == 0 {
		t.Fatalf("missing durable recovery evidence: %v %v", ids, err)
	}
	// Recreate the engine/reconciler as after restart; use the real TaskService.
	restarted := *engine
	restarted.TaskCanceller = testHandler.TaskService
	reconciler := workflow.NewReconciler(&restarted, testHandler.Queries)
	if err := reconciler.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := reconciler.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	ids, err = testHandler.Queries.ListActiveAgentTaskIDsForWorkflowRun(context.Background(), db.ListActiveAgentTaskIDsForWorkflowRunParams{RunID: parseUUID(detail.ID), WorkspaceID: parseUUID(testWorkspaceID)})
	if err != nil || len(ids) != 0 {
		t.Fatalf("linked tasks remain active: %v %v", ids, err)
	}
	task, err := testHandler.Queries.GetAgentTask(context.Background(), parseUUID(unrelated))
	if err != nil || task.Status != "queued" {
		t.Fatalf("unrelated issue task was stopped: %s %v", task.Status, err)
	}
}

func TestR5IntakeExcludedControlsAndIdentity(t *testing.T) {
	cleanupWorkflowTemplates(t)
	cleanupWorkflowRuns(t)
	withWorkflowEngineForTest(t)
	tpl := seededBugFixTemplate(t)
	base := map[string]any{"source": "ticket", "event_id": "r5", "template_key": tpl.Key, "title": "Intake", "description": "Validate excluded controls"}
	counts := func() string {
		var out string
		if err := testPool.QueryRow(context.Background(), "SELECT jsonb_build_array((SELECT count(*) FROM issue WHERE workspace_id=$1),(SELECT count(*) FROM workflow_run WHERE workspace_id=$1),(SELECT count(*) FROM workflow_step_instance WHERE workspace_id=$1),(SELECT count(*) FROM workflow_event WHERE workspace_id=$1),(SELECT count(*) FROM agent_task_queue t JOIN agent a ON a.id=t.agent_id WHERE a.workspace_id=$1),(SELECT count(*) FROM issue_subscriber s JOIN issue i ON i.id=s.issue_id WHERE i.workspace_id=$1))::text", testWorkspaceID).Scan(&out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	for _, key := range []string{"callback_destination_id", "input_instance_id", "input_instance_revision", "input_source", "execution_mode", "debug_session_id", "template_version_id"} {
		for _, value := range []any{" ", "x", false, 1, map[string]any{}, []string{}} {
			t.Run(fmt.Sprint(key, value), func(t *testing.T) {
				body := map[string]any{}
				for k, v := range base {
					body[k] = v
				}
				body[key] = value
				before := counts()
				testutil.Call(t, testHandler.WorkflowIntake, withURLParam(newRequest("POST", "/api/workspaces/"+testWorkspaceID+"/workflow-intake", body), "id", testWorkspaceID)).Want(400)
				if counts() != before {
					t.Fatal("rejected intake left rows")
				}
			})
		}
	}
	for _, header := range []struct {
		key, value string
		status     int
	}{
		{"X-Workflow-Template-Version-ID", " ", 400}, {"X-Actor-Source", "task_token", 403}, {"X-Actor-Source", "cloud_pat", 403},
	} {
		before := counts()
		req := withURLParam(newRequest("POST", "/api/workspaces/"+testWorkspaceID+"/workflow-intake", base), "id", testWorkspaceID)
		req.Header.Set(header.key, header.value)
		testutil.Call(t, testHandler.WorkflowIntake, req).Want(header.status)
		if counts() != before {
			t.Fatal("identity/version rejection left rows")
		}
	}
}

func TestR5CustomCancelledCategoryAndMissingServices(t *testing.T) {
	detail, engine := r5ActiveRun(t)
	createTestCustomStatus(t, "r5_aborted", "closed")
	before := r5Snapshot(t, detail.ID)
	saved := engine.TaskCanceller
	engine.TaskCanceller = nil
	testutil.Call(t, testHandler.UpdateIssue, withURLParam(newRequest("PUT", "/api/issues/"+*detail.IssueID, map[string]any{"status": "r5_aborted"}), "id", *detail.IssueID)).Want(503)
	if r5Snapshot(t, detail.ID) != before {
		t.Fatal("missing service committed cancellation")
	}
	engine.TaskCanceller = saved
	testutil.Call(t, testHandler.UpdateIssue, withURLParam(newRequest("PUT", "/api/issues/"+*detail.IssueID, map[string]any{"status": " R5_ABORTED "}), "id", *detail.IssueID)).Want(200)
	current, err := testHandler.Queries.GetIssue(context.Background(), parseUUID(*detail.IssueID))
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != "r5_aborted" {
		t.Fatalf("custom key lost: %s", current.Status)
	}
	if getWorkflowRunForTest(t, detail.ID).Status != "cancelled" {
		t.Fatal("custom closed category did not cancel run")
	}
}

func TestR5IntakeThenManualReplayPreservesProvenance(t *testing.T) {
	cleanupWorkflowTemplates(t)
	cleanupWorkflowRuns(t)
	withWorkflowEngineForTest(t)
	labelTestAgentWithCapabilities(t, "bug_analysis", "code_change")
	tpl := seededBugFixTemplate(t)
	body := map[string]any{"source": "ticket", "event_id": "r5-replay", "template_key": tpl.Key, "title": "Replay", "description": "Both adapters converge", "idempotency_key": "r5-cross"}
	var receipt WorkflowIntakeResponse
	testutil.Call(t, testHandler.WorkflowIntake, withURLParam(newRequest("POST", "/api/workspaces/"+testWorkspaceID+"/workflow-intake", body), "id", testWorkspaceID)).Want(201).JSON(&receipt)
	before := r5Snapshot(t, receipt.WorkflowRunID)
	manual := runWorkflowTemplateForTest(t, tpl.ID, map[string]any{"title": body["title"], "description": body["description"], "idempotency_key": body["idempotency_key"]})
	if manual.Code != 200 {
		t.Fatalf("manual replay: %d %s", manual.Code, manual.Body.String())
	}
	if r5Snapshot(t, receipt.WorkflowRunID) != before {
		t.Fatal("cross-entry replay changed provenance or rows")
	}
}

type r5ObservedStarter struct{ reached chan struct{} }
type r5ObservedTx struct {
	pgx.Tx
	reached chan struct{}
	once    sync.Once
}

func (s *r5ObservedStarter) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := testPool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return &r5ObservedTx{Tx: tx, reached: s.reached}, nil
}
func (tx *r5ObservedTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	if strings.Contains(sql, "-- name: LockIssueForDescriptionUpdate") {
		tx.once.Do(func() { close(tx.reached) })
	}
	return tx.Tx.QueryRow(ctx, sql, args...)
}

type r5BlockingRouter struct {
	workflow.Router
	entered, release chan struct{}
	once             sync.Once
}

func (r *r5BlockingRouter) Route(ctx context.Context, q *db.Queries, req workflow.RouteRequest) (workflow.RouteResult, error) {
	r.once.Do(func() { close(r.entered) })
	select {
	case <-r.release:
	case <-ctx.Done():
		return workflow.RouteResult{}, ctx.Err()
	}
	return r.Router.Route(ctx, q, req)
}

func TestR5CancellationIncludesTaskActivatedWhileWaitingForIssueLock(t *testing.T) {
	detail, engine := r5ActiveRun(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var step WorkflowStepResponse
	for _, s := range detail.Steps {
		if s.NodeKey == "analyze" {
			step = s
		}
	}
	if step.TaskID == nil {
		t.Fatal("missing task")
	}
	var result struct{ Output string }
	if err := json.Unmarshal(e2eTaskResult(t, *step.TaskID, step.ID, "analysis", "Root cause"), &result); err != nil {
		t.Fatal(err)
	}
	router := &r5BlockingRouter{Router: engine.Router, entered: make(chan struct{}), release: make(chan struct{})}
	engine.Router = router
	if _, err := testPool.Exec(ctx, "UPDATE agent_task_queue SET status='running' WHERE id=$1", *step.TaskID); err != nil {
		t.Fatal(err)
	}
	advanced := make(chan error, 1)
	go func() {
		_, err := testHandler.TaskService.CompleteTask(ctx, parseUUID(*step.TaskID), e2eTaskResult(t, *step.TaskID, step.ID, "analysis", "Root cause"), "", "", "", false, "", "")
		advanced <- err
	}()
	select {
	case <-router.entered:
	case err := <-advanced:
		t.Fatalf("did not reach next-step routing: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	oldStarter := testHandler.TxStarter
	reached := make(chan struct{})
	testHandler.TxStarter = &r5ObservedStarter{reached: reached}
	t.Cleanup(func() { testHandler.TxStarter = oldStarter })
	cancelled := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rec := httptest.NewRecorder()
		req := withURLParam(newRequest("PUT", "/api/issues/"+*detail.IssueID, map[string]any{"status": "cancelled"}), "id", *detail.IssueID).WithContext(ctx)
		// Preserve chi route parameters when adding the deadline context.
		req = withURLParam(req, "id", *detail.IssueID)
		testHandler.UpdateIssue(rec, req)
		cancelled <- rec
	}()
	select {
	case <-reached:
	case <-ctx.Done():
		close(router.release)
		<-advanced
		t.Fatal(ctx.Err())
	}
	close(router.release)
	advanceErr := <-advanced
	rec := <-cancelled
	if advanceErr != nil {
		t.Fatal(advanceErr)
	}
	if rec.Code != 200 {
		t.Fatalf("cancel: %d %s", rec.Code, rec.Body.String())
	}
	if getWorkflowRunForTest(t, detail.ID).Status != "cancelled" {
		t.Fatal("run not cancelled")
	}
	ids, err := testHandler.Queries.ListActiveAgentTaskIDsForWorkflowRun(ctx, db.ListActiveAgentTaskIDsForWorkflowRunParams{RunID: parseUUID(detail.ID), WorkspaceID: parseUUID(testWorkspaceID)})
	if err != nil || len(ids) != 0 {
		t.Fatalf("activation raced past cancellation: %v %v", ids, err)
	}
	if err := engine.RecordTaskTerminal(ctx, workflow.RecordTaskTerminalInput{TaskID: parseUUID(*step.TaskID), TaskStatus: "completed", Result: result.Output}); err != nil {
		t.Fatal(err)
	}
	if getWorkflowRunForTest(t, detail.ID).Status != "cancelled" {
		t.Fatal("late completion revived run")
	}
}

type r5FailCommitStarter struct{}
type r5FailCommitTx struct{ pgx.Tx }

func (*r5FailCommitStarter) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := testPool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return &r5FailCommitTx{tx}, nil
}
func (*r5FailCommitTx) Commit(context.Context) error { return errors.New("injected commit failure") }

func TestR5IssueCancellationCommitFailureRollsBackEverything(t *testing.T) {
	detail, _ := r5ActiveRun(t)
	before := r5Snapshot(t, detail.ID)
	old := testHandler.TxStarter
	testHandler.TxStarter = &r5FailCommitStarter{}
	defer func() { testHandler.TxStarter = old }()
	testutil.Call(t, testHandler.UpdateIssue, withURLParam(newRequest("PUT", "/api/issues/"+*detail.IssueID, map[string]any{"status": "cancelled"}), "id", *detail.IssueID)).Want(500)
	if r5Snapshot(t, detail.ID) != before {
		t.Fatal("commit failure leaked database changes")
	}
}

type r5PausedStarter struct{ reached, release chan struct{} }

func (s *r5PausedStarter) Begin(ctx context.Context) (pgx.Tx, error) {
	close(s.reached)
	select {
	case <-s.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return testPool.Begin(ctx)
}
func TestR5CompletionWinningBeforeCancellationPreservesTerminalState(t *testing.T) {
	detail, engine := r5ActiveRun(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	starter := &r5PausedStarter{reached: make(chan struct{}), release: make(chan struct{})}
	old := testHandler.TxStarter
	testHandler.TxStarter = starter
	defer func() { testHandler.TxStarter = old }()
	response := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rec := httptest.NewRecorder()
		req := newRequest("PUT", "/api/issues/"+*detail.IssueID, map[string]any{"status": "cancelled"}).WithContext(ctx)
		testHandler.UpdateIssue(rec, withURLParam(req, "id", *detail.IssueID))
		response <- rec
	}()
	select {
	case <-starter.reached:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	for _, node := range []struct{ key, artifact string }{{"analyze", "analysis"}, {"implement", "code_change"}, {"validate", "test_report"}} {
		run := getWorkflowRunForTest(t, detail.ID)
		step, ok := findWorkflowStep(run.Steps, node.key)
		if !ok || step.TaskID == nil {
			close(starter.release)
			<-response
			t.Fatalf("missing %s task", node.key)
		}
		if _, err := testPool.Exec(ctx, "UPDATE agent_task_queue SET status='running' WHERE id=$1", *step.TaskID); err != nil {
			t.Fatal(err)
		}
		if _, err := testHandler.TaskService.CompleteTask(ctx, parseUUID(*step.TaskID), e2eTaskResult(t, *step.TaskID, step.ID, node.artifact, "Verified"), "", "", "", false, "", ""); err != nil {
			t.Fatal(err)
		}
	}
	acceptances, err := testHandler.Queries.ListWorkflowAcceptancesForRun(ctx, db.ListWorkflowAcceptancesForRunParams{RunID: parseUUID(detail.ID), WorkspaceID: parseUUID(testWorkspaceID)})
	if err != nil || len(acceptances) != 1 {
		close(starter.release)
		<-response
		t.Fatalf("acceptances: %v %v", acceptances, err)
	}
	if _, err := engine.DecideAcceptance(ctx, workflow.DecideAcceptanceInput{WorkspaceID: parseUUID(testWorkspaceID), AcceptanceID: acceptances[0].ID, Accept: true, ReviewerUserID: parseUUID(testUserID)}); err != nil {
		close(starter.release)
		<-response
		t.Fatal(err)
	}
	before := r5Snapshot(t, detail.ID)
	close(starter.release)
	rec := <-response
	if rec.Code != 409 {
		t.Fatalf("stale cancellation status %d: %s", rec.Code, rec.Body.String())
	}
	if r5Snapshot(t, detail.ID) != before {
		t.Fatal("stale cancellation overwrote completion")
	}
}

func TestR5SkippedWorkflowTargetPreventsPartialBatch(t *testing.T) {
	detail, _ := r5ActiveRun(t)
	ordinary := dbfx.Issue(t, "Ordinary batch target")
	before := r5Snapshot(t, detail.ID)
	// The workflow target would be skipped for parenting itself; the other target
	// could otherwise be updated. Both must remain unchanged.
	testutil.Call(t, testHandler.BatchUpdateIssues, newRequest("PATCH", "/api/issues/batch?workspace_id="+testWorkspaceID, map[string]any{
		"issue_ids": []string{ordinary, *detail.IssueID},
		"updates":   map[string]any{"status": "cancelled", "parent_issue_id": *detail.IssueID},
	})).Want(409)
	if r5Snapshot(t, detail.ID) != before {
		t.Fatal("invalid batch changed workflow")
	}
	issue, err := testHandler.Queries.GetIssue(context.Background(), parseUUID(ordinary))
	if err != nil {
		t.Fatal(err)
	}
	if issue.Status != "todo" || issue.ParentIssueID.Valid {
		t.Fatal("invalid batch partially changed ordinary target")
	}
}
