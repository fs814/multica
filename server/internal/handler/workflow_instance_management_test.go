package handler

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
)

func TestWorkflowInstanceLifecycle(t *testing.T) {
	cleanupWorkflowTemplates(t)
	cleanupWorkflowRuns(t)
	withWorkflowEngineForTest(t)
	ctx := context.Background()
	defer testPool.Exec(ctx, "DELETE FROM workflow_input_instance WHERE workspace_id=$1", testWorkspaceID)
	var tpl WorkflowTemplateDetailResponse
	graph := map[string]any{"schema_version": 1, "entry_node": "input", "nodes": []map[string]any{{"key": "input", "type": "input", "next": []string{"end"}, "input_fields": []map[string]any{{"key": "choice", "type": "select", "options": []string{"one", "two"}}}}, {"key": "end", "type": "end"}}}
	testutil.Call(t, testHandler.CreateWorkflowTemplate, newRequest("POST", "/api/workflow-templates", map[string]any{"key": "instance_lifecycle", "name": "Instance lifecycle", "definition": graph})).Want(201).JSON(&tpl)
	testutil.Call(t, testHandler.PublishWorkflowTemplate, withURLParam(newRequest("POST", "/", nil), "id", tpl.ID)).Want(200).JSON(&tpl)
	version := tpl.Versions[0].ID
	createBody := map[string]any{"name": "A", "input": map[string]string{"title": "Task", "description": "Input A", "choice": "one"}, "template_version_id": version, "image_attachment_id": "", "idempotency_key": "create-a"}
	save := func(id string, body any, status int) workflowInputInstanceResponse {
		t.Helper()
		method := "POST"
		if id != "" {
			method = "PUT"
		}
		var row workflowInputInstanceResponse
		result := testutil.Call(t, testHandler.SaveWorkflowInputInstance, withURLParams(newRequest(method, "/", body), "id", tpl.ID, "instanceID", id)).Want(status)
		if status < 300 {
			result.JSON(&row)
		}
		return row
	}
	// Match the complete Create Instance dialog payload, including metadata
	// and the input declaration that older server binaries rejected.
	createBody["description"] = "Reusable input scenario"
	createBody["input_node"] = graph["nodes"].([]map[string]any)[0]
	createBody["project_id"] = nil
	first := save("", createBody, 201)
	if first.Description != "Reusable input scenario" || len(first.InputNode) == 0 {
		t.Fatal("creation lost the form metadata or input declaration")
	}
	second := save("", createBody, 201)
	if first.ID != second.ID {
		t.Fatal("creation replay duplicated instance")
	}
	createBody["name"] = "different"
	save("", createBody, 409)
	createBody["name"] = "A"
	var count int
	if err := testPool.QueryRow(ctx, "SELECT count(*) FROM workflow_run WHERE workspace_id=$1", testWorkspaceID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("save started a run: %d %v", count, err)
	}
	run := func(body any, status int) WorkflowRunDetailResponse {
		t.Helper()
		var result WorkflowRunDetailResponse
		response := testutil.Call(t, testHandler.RunWorkflowInstance, withURLParam(newRequest("POST", "/", body), "instanceID", first.ID)).Want(status)
		if status < 300 {
			response.JSON(&result)
		}
		return result
	}
	a := run(map[string]any{"revision": 1, "mode": "saved", "idempotency_key": "saved-a"}, 201)
	again := run(map[string]any{"revision": 1, "mode": "saved", "idempotency_key": "saved-a"}, 200)
	if a.ID != again.ID || a.InputInstanceID == nil || *a.InputInstanceID != first.ID || a.InputInstanceRevision == nil || *a.InputInstanceRevision != 1 {
		t.Fatal("saved run provenance or idempotency is incorrect")
	}
	b := run(map[string]any{"revision": 1, "mode": "temporary", "idempotency_key": "temp-b", "input": map[string]string{"title": "Task", "description": "Input B", "choice": "two"}, "image_attachment_id": ""}, 201)
	if b.ID == a.ID || string(a.Input) == string(b.Input) {
		t.Fatal("temporary snapshot did not differ")
	}
	var loaded workflowInputInstanceResponse
	testutil.Call(t, testHandler.GetWorkflowInstance, withURLParam(newRequest("GET", "/", nil), "instanceID", first.ID)).Want(200).JSON(&loaded)
	if string(loaded.Input) != string(first.Input) || loaded.Revision != 1 {
		t.Fatal("temporary run changed instance")
	}
	update := map[string]any{"name": "B", "revision": 1, "idempotency_key": "ignored-for-cas-update", "template_version_id": version, "input": map[string]string{"title": "Task", "description": "Input B", "choice": "two"}, "image_attachment_id": ""}
	revised := save(first.ID, update, 200)
	if revised.Revision != 2 {
		t.Fatal("revision not advanced")
	}
	save(first.ID, update, 409)
	run(map[string]any{"revision": 1, "mode": "saved", "idempotency_key": "stale"}, 409)
	run(map[string]any{"revision": 1, "mode": "saved", "idempotency_key": "saved-a"}, 200)
	historical := run(map[string]any{"revision": 2, "mode": "history", "history_run_id": a.ID, "idempotency_key": "history-a"}, 201)
	if string(historical.Input) != string(a.Input) || historical.TemplateVersionID != version {
		t.Fatal("history rerun used current input")
	}
	current := getWorkflowRunForTest(t, a.ID)
	if string(current.Input) != string(a.Input) {
		t.Fatal("history mutated")
	}
	for _, input := range []map[string]any{{"title": "Task", "description": ""}, {"title": "Task", "description": "Brief", "choice": "invalid"}, {"title": "Task", "description": "Brief", "removed": "must not disappear"}, {"title": "Task", "description": "Brief", "choice": nil}} {
		run(map[string]any{"revision": 2, "mode": "temporary", "idempotency_key": "invalid", "input": input, "image_attachment_id": ""}, 422)
	}
	var validation struct {
		Ready bool `json:"ready"`
	}
	testutil.Call(t, testHandler.ValidateWorkflowInstance, withURLParam(newRequest("GET", "/", nil), "instanceID", first.ID)).Want(200).JSON(&validation)
	if !validation.Ready {
		t.Fatal("complete bound instance not ready")
	}
	var page struct {
		Total     int                             `json:"total"`
		Instances []workflowInputInstanceResponse `json:"instances"`
	}
	testutil.Call(t, testHandler.BrowseWorkflowInstances, newRequest("GET", "/?search=B&limit=1", nil)).Want(200).JSON(&page)
	if page.Total != 1 || len(page.Instances) != 1 {
		t.Fatal("search or pagination incorrect")
	}
	testutil.Call(t, testHandler.ArchiveWorkflowInstance, withURLParam(newRequest("POST", "/", map[string]any{"revision": 2, "archive": true}), "instanceID", first.ID)).Want(200)
	run(map[string]any{"revision": 3, "mode": "saved", "idempotency_key": "archived"}, 409)
	var history struct {
		Total int `json:"total"`
	}
	testutil.Call(t, testHandler.ListWorkflowInstanceRuns, withURLParam(newRequest("GET", "/", nil), "instanceID", first.ID)).Want(200).JSON(&history)
	if history.Total != 3 {
		t.Fatalf("archive lost history or rejected run persisted: %d", history.Total)
	}
	testutil.Call(t, testHandler.ArchiveWorkflowInstance, withURLParam(newRequest("POST", "/", map[string]any{"revision": 3, "archive": false}), "instanceID", first.ID)).Want(200)
	run(map[string]any{"revision": 4, "mode": "saved", "idempotency_key": "restored"}, 201)
	foreign := newRequest("GET", "/", nil)
	foreign.Header.Set("X-Workspace-ID", "00000000-0000-0000-0000-000000000099")
	testutil.Call(t, testHandler.GetWorkflowInstance, withURLParam(foreign, "instanceID", first.ID)).Want(404)
	draft := save("", map[string]any{"name": "Draft", "input": map[string]string{}, "input_node": map[string]any{"key": "draft", "type": "input", "input_fields": []any{}}}, 201)
	testutil.Call(t, testHandler.RunWorkflowInstance, withURLParam(newRequest("POST", "/", map[string]any{"revision": 1, "mode": "saved", "idempotency_key": "unbound"}), "instanceID", draft.ID)).Want(409)
	var node map[string]any
	if json.Unmarshal(draft.InputNode, &node) != nil || node["key"] != "draft" {
		t.Fatal("draft declaration was lost")
	}
}
