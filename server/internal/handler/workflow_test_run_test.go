package handler

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/workflow"
)

func TestWorkflowDraftTrialHTTPIsolationSettingsAndAcceptance(t *testing.T) {
	cleanupWorkflowTemplates(t)
	cleanupWorkflowRuns(t)
	engine := withWorkflowEngineForTest(t)
	engine.DebugReady = true
	engine.ResolveDraftEnvironment = testHandler.ResolveDraftEnvironment
	engine.RevalidateDebugEnvironment = testHandler.RevalidateDebugEnvironment
	fx := testutil.New(testPool, testWorkspaceID, testUserID)
	for _, table := range []string{"workflow_execution_snapshot", "workflow_debug_quota", "workflow_debug_policy", "workflow_debug_task_execution", "workflow_debug_stop_request", "workflow_debug_cleanup_object", "workflow_debug_upload"} {
		fx.Cleanup(t, "DELETE FROM "+table+" WHERE workspace_id=$1", testWorkspaceID)
	}
	var settings struct {
		Revision int64                `json:"revision"`
		Settings workflow.DebugPolicy `json:"settings"`
	}
	testutil.Call(t, testHandler.GetWorkflowTestSettings, newRequest("GET", "/", nil)).Want(200).JSON(&settings)
	if settings.Settings.Enabled || settings.Revision != 1 {
		t.Fatal("settings must default off revision 1")
	}
	body := map[string]any{"enabled": true, "user_active_runs": 2, "workspace_active_runs": 5, "user_starts_per_hour": 20, "max_duration_seconds": 1800, "retention_seconds": 2592000, "payload_capacity_bytes": 104857600, "expected_revision": 1}
	testutil.Call(t, testHandler.UpdateWorkflowTestSettings, newRequest("PATCH", "/", body)).Want(200).JSON(&settings)
	if settings.Revision != 2 {
		t.Fatal("revision did not advance")
	}
	body["expected_revision"] = 2
	testutil.Call(t, testHandler.UpdateWorkflowTestSettings, newRequest("PATCH", "/", body)).Want(200).JSON(&settings)
	if settings.Revision != 2 {
		t.Fatal("same settings bumped revision")
	}
	body["expected_revision"] = 1
	testutil.Call(t, testHandler.UpdateWorkflowTestSettings, newRequest("PATCH", "/", body)).Want(409)
	body["expected_revision"] = 2
	body["retention_seconds"] = 10
	testutil.Call(t, testHandler.UpdateWorkflowTestSettings, newRequest("PATCH", "/", body)).Want(422)
	body["retention_seconds"] = 2592000
	body["unknown"] = true
	testutil.Call(t, testHandler.UpdateWorkflowTestSettings, newRequest("PATCH", "/", body)).Want(400)
	delete(body, "unknown")
	delete(body, "enabled")
	testutil.Call(t, testHandler.UpdateWorkflowTestSettings, newRequest("PATCH", "/", body)).Want(400)
	body["enabled"] = nil
	testutil.Call(t, testHandler.UpdateWorkflowTestSettings, newRequest("PATCH", "/", body)).Want(400)
	for _, schema := range []int{1, 2} {
		t.Run(map[int]string{1: "v1", 2: "v2"}[schema], func(t *testing.T) {
			graph := map[string]any{"schema_version": schema, "entry_node": "input", "nodes": []map[string]any{{"key": "input", "type": "input", "next": []string{"bridge"}, "next_ids": []string{"input-bridge"}}, {"key": "bridge", "type": "join", "next": []string{"review"}, "next_ids": []string{"bridge-review"}, "join_sources": []string{"input"}}, {"key": "review", "type": "acceptance", "next": []string{"end"}, "next_ids": []string{"review-end"}, "rework_targets": []string{"bridge"}, "acceptance_criteria": []string{"controlled approval"}}, {"key": "end", "type": "end"}}}
			for _, node := range graph["nodes"].([]map[string]any) {
				if schema == 1 {
					delete(node, "next_ids")
				} else {
					delete(node, "rework_targets")
				}
			}
			if schema == 1 {
				graph["nodes"] = []map[string]any{{"key": "input", "type": "input", "next": []string{"end"}}, {"key": "end", "type": "end"}}
			}
			var tpl WorkflowTemplateDetailResponse
			testutil.Call(t, testHandler.CreateWorkflowTemplate, newRequest("POST", "/", map[string]any{"key": "draft-http-" + map[int]string{1: "v1", 2: "v2"}[schema], "name": "Draft HTTP", "definition": graph})).Want(201).JSON(&tpl)
			req := map[string]any{"schema_version": "1", "expected_revision": tpl.Revision, "expected_debug_policy_revision": 2, "base_draft_version_id": tpl.Versions[0].ID, "definition": graph, "input": map[string]string{"title": "Snapshot B", "description": "controlled"}, "project_id": nil, "image_attachment_id": nil, "idempotency_key": "http-trial", "execution_acknowledged": true}
			req["unexpected"] = true
			testutil.Call(t, testHandler.StartWorkflowTestRun, withURLParam(newRequest("POST", "/", req), "id", tpl.ID)).Want(400)
			delete(req, "unexpected")
			var result struct {
				Run     WorkflowRunDetailResponse `json:"run"`
				Ref     workflowExecutionRef      `json:"execution_ref"`
				Created bool                      `json:"created"`
				Debug   struct {
					EffectiveLimits workflow.Limits `json:"effective_limits"`
				} `json:"debug"`
			}
			testutil.Call(t, testHandler.StartWorkflowTestRun, withURLParam(newRequest("POST", "/", req), "id", tpl.ID)).Want(201).JSON(&result)
			if !result.Created || result.Ref.Kind != "draft_test" || result.Run.IssueID != nil || result.Run.TemplateVersionID != "" || result.Run.Status != map[int]string{1: "completed", 2: "waiting_acceptance"}[schema] {
				t.Fatalf("wrong trial result %+v", result)
			}
			if result.Debug.EffectiveLimits.MaxDurationSeconds != 1800 || result.Debug.EffectiveLimits.MaxTotalSteps != workflow.DefaultLimits.MaxTotalSteps {
				t.Fatal("effective execution limits missing or wrong")
			}
			id := result.Run.ID
			testutil.Call(t, testHandler.GetWorkflowRun, withURLParam(newRequest("GET", "/", nil), "id", id)).Want(404)
			testutil.Call(t, testHandler.CancelWorkflowRun, withURLParam(newRequest("POST", "/", nil), "id", id)).Want(404)
			var snapshot map[string]json.RawMessage
			testutil.Call(t, testHandler.GetWorkflowTestRunDefinition, withURLParam(newRequest("GET", "/", nil), "id", id)).Want(200).JSON(&snapshot)
			if len(snapshot["definition"]) == 0 {
				t.Fatal("missing snapshot graph")
			}
			if schema == 2 {
				testutil.Call(t, testHandler.DecideWorkflowTestAcceptance, withURLParam(newRequest("POST", "/", map[string]bool{"accept": true}), "id", id)).Want(200).JSON(&result)
			}
			if result.Run.Status != "completed" {
				t.Fatalf("acceptance: %s", result.Run.Status)
			}
			testutil.Call(t, testHandler.DecideWorkflowTestAcceptance, withURLParam(newRequest("POST", "/", map[string]bool{"accept": true}), "id", id)).Want(409)
			engine.DebugReady = false
			testutil.Call(t, testHandler.StartWorkflowTestRun, withURLParam(newRequest("POST", "/", req), "id", tpl.ID)).Want(200).JSON(&result)
			if result.Created || result.Run.ID != id {
				t.Fatal("replay after disable did not return same execution")
			}
			req["idempotency_key"] = "new-disabled"
			testutil.Call(t, testHandler.StartWorkflowTestRun, withURLParam(newRequest("POST", "/", req), "id", tpl.ID)).Want(503)
			fx.Exec(t, "UPDATE workflow_run SET debug_purge_after=now()-interval '1 second' WHERE id=$1", id)
			var runID, wsID pgtype.UUID
			_ = runID.Scan(id)
			_ = wsID.Scan(testWorkspaceID)
			if err := engine.PurgeDebugRun(context.Background(), wsID, runID); err != nil {
				t.Fatal(err)
			}
			testutil.Call(t, testHandler.GetWorkflowTestRun, withURLParam(newRequest("GET", "/", nil), "id", id)).Want(200)
			testutil.Call(t, testHandler.GetWorkflowTestRunDefinition, withURLParam(newRequest("GET", "/", nil), "id", id)).Want(410)
			req["idempotency_key"] = "http-trial"
			testutil.Call(t, testHandler.StartWorkflowTestRun, withURLParam(newRequest("POST", "/", req), "id", tpl.ID)).Want(410)
			engine.DebugReady = true
		})
	}
}
