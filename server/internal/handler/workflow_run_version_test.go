package handler

import (
	"net/http"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
)

func TestWorkflowRunPinsDisplayedVersion(t *testing.T) {
	cleanupWorkflowTemplates(t)
	cleanupWorkflowRuns(t)
	withWorkflowEngineForTest(t)
	graph := func(required bool) map[string]any {
		return map[string]any{
			"schema_version": 1, "entry_node": "intake",
			"nodes": []map[string]any{
				{"key": "intake", "type": "input", "next": []string{"end"},
					"input_fields": []map[string]any{{"key": "extra", "type": "text", "required": required}}},
				{"key": "end", "type": "end"},
			},
		}
	}
	create := func(key string) WorkflowTemplateDetailResponse {
		var tpl WorkflowTemplateDetailResponse
		testutil.Call(t, testHandler.CreateWorkflowTemplate, newRequest("POST", "/api/workflow-templates", map[string]any{
			"key": key, "name": key, "definition": graph(false),
		})).Want(http.StatusCreated).JSON(&tpl)
		return tpl
	}
	publish := func(id string) WorkflowTemplateDetailResponse {
		var tpl WorkflowTemplateDetailResponse
		testutil.Call(t, testHandler.PublishWorkflowTemplate,
			withURLParam(newRequest("POST", "/api/workflow-templates/"+id+"/publish", nil), "id", id),
		).Want(http.StatusOK).JSON(&tpl)
		return tpl
	}
	tpl := create("instance_version_test")
	first := publish(tpl.ID)
	firstVersion := first.Versions[0].ID
	var changed WorkflowTemplateDetailResponse
	testutil.Call(t, testHandler.UpdateWorkflowTemplate,
		withURLParam(newRequest("PATCH", "/api/workflow-templates/"+tpl.ID, map[string]any{
			"revision": first.Revision, "definition": graph(true),
		}), "id", tpl.ID),
	).Want(http.StatusOK).JSON(&changed)
	second := publish(tpl.ID)
	secondVersion := second.Versions[0].ID
	other := create("instance_other_template")
	other = publish(other.ID)
	draft := create("instance_draft_template")
	run := func(version, key, description string, status int) WorkflowRunDetailResponse {
		var result WorkflowRunDetailResponse
		req := withURLParam(newRequest("POST", "/api/workflow-templates/"+tpl.ID+"/run", map[string]any{
			"title": "Scenario", "description": description, "idempotency_key": key,
		}), "id", tpl.ID)
		if version != "" {
			req.Header.Set("X-Workflow-Template-Version-ID", version)
		}
		response := testutil.Call(t, testHandler.RunWorkflowTemplate, req).Want(status)
		if status == http.StatusCreated || status == http.StatusOK {
			response.JSON(&result)
		}
		return result
	}
	a := run(firstVersion, "pinned-a", "Input A", http.StatusCreated)
	if a.TemplateVersionID != firstVersion {
		t.Fatal("run did not pin the displayed publication")
	}
	run(firstVersion, "pinned-a", "Input A", http.StatusOK)
	run(secondVersion, "pinned-a", "Input A", http.StatusConflict)
	run("", "current", "Input A", http.StatusUnprocessableEntity)
	run(other.Versions[0].ID, "foreign", "Input A", http.StatusConflict)
	run(draft.Versions[0].ID, "draft", "Input A", http.StatusConflict)
	run("not-a-uuid", "invalid", "Input A", http.StatusBadRequest)
	b := run(firstVersion, "pinned-b", "Input B", http.StatusCreated)
	if a.ID == b.ID {
		t.Fatal("a new run reused the old input snapshot")
	}
	history := getWorkflowRunForTest(t, a.ID)
	if string(history.Input) != string(a.Input) {
		t.Fatal("later inputs changed an earlier run")
	}
}
