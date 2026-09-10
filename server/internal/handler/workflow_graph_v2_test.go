package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWorkflowTemplateV2IncompleteDraftPublishAndReload(t *testing.T) {
	cleanupWorkflowTemplates(t)
	incomplete := map[string]any{"schema_version": 2, "entry_node": "input", "nodes": []map[string]any{{"key": "input", "type": "input"}}}
	w := httptest.NewRecorder()
	testHandler.CreateWorkflowTemplate(w, newRequest("POST", "/api/workflow-templates", map[string]any{"key": "v2-draft", "name": "V2 draft", "definition": incomplete}))
	if w.Code != http.StatusCreated {
		t.Fatalf("save draft: %d %s", w.Code, w.Body.String())
	}
	var created WorkflowTemplateDetailResponse
	if err := json.NewDecoder(w.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	testHandler.PublishWorkflowTemplate(w, withURLParam(newRequest("POST", "/api/workflow-templates/"+created.ID+"/publish", nil), "id", created.ID))
	if w.Code != http.StatusUnprocessableEntity && w.Code != http.StatusBadRequest {
		t.Fatalf("published incomplete draft: %d %s", w.Code, w.Body.String())
	}
	complete := map[string]any{"schema_version": 2, "entry_node": "input", "nodes": []map[string]any{{"key": "input", "type": "input", "next": []string{"end"}, "next_ids": []string{"flow-1"}}, {"key": "end", "type": "end"}}}
	w = patchWorkflowTemplateForTest(t, created.ID, map[string]any{"definition": complete})
	if w.Code != http.StatusOK {
		t.Fatalf("patch: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	testHandler.PublishWorkflowTemplate(w, withURLParam(newRequest("POST", "/api/workflow-templates/"+created.ID+"/publish", nil), "id", created.ID))
	if w.Code != http.StatusOK {
		t.Fatalf("publish: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	testHandler.GetWorkflowTemplate(w, withURLParam(newRequest("GET", "/api/workflow-templates/"+created.ID, nil), "id", created.ID))
	if w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	var loaded WorkflowTemplateDetailResponse
	if err := json.NewDecoder(w.Body).Decode(&loaded); err != nil {
		t.Fatal(err)
	}
	var schema int
	if err := testPool.QueryRow(context.Background(), `SELECT schema_version FROM workflow_template_version WHERE id=$1`, loaded.Versions[0].ID).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	if schema != 2 {
		t.Fatalf("stored schema version: %d", schema)
	}
	var graph map[string]any
	if err := json.Unmarshal(loaded.Definition, &graph); err != nil {
		t.Fatal(err)
	}
	if graph["schema_version"] != float64(2) {
		t.Fatal("definition was downgraded")
	}
}
