package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestWorkflowInputInstancesCRUD(t *testing.T) {
	if testPool == nil {
		t.Skip("database unavailable")
	}
	ctx := context.Background()
	tx, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	schema, err := os.ReadFile("../../migrations/491_workflow_input_instance.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, strings.Replace(string(schema), "CREATE TABLE ", "CREATE TABLE IF NOT EXISTS ", 1)); err != nil {
		t.Fatal(err)
	}
	versionSchema, err := os.ReadFile("../../migrations/495_workflow_input_instance_version.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, strings.ReplaceAll(string(versionSchema), "ADD COLUMN ", "ADD COLUMN IF NOT EXISTS ")); err != nil {
		t.Fatal(err)
	}
	h := *testHandler
	h.Queries = db.New(tx)
	var templateID string
	if err := tx.QueryRow(ctx, `INSERT INTO workflow_template (workspace_id,key,name,created_by_type,created_by_id) VALUES ($1,gen_random_uuid()::text,'Input instances','member',$2) RETURNING id`, testWorkspaceID, testUserID).Scan(&templateID); err != nil {
		t.Fatal(err)
	}
	call := func(method, id, body, workspace string, handler http.HandlerFunc) *httptest.ResponseRecorder {
		t.Helper()
		req := newRequest(method, "/api/workflow-templates/"+templateID+"/input-instances", json.RawMessage(body))
		req.Header.Set("X-Workspace-ID", workspace)
		route := chi.NewRouteContext()
		route.URLParams.Add("id", templateID)
		route.URLParams.Add("instanceID", id)
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, route))
		response := httptest.NewRecorder()
		handler(response, req)
		return response
	}
	save := func(name, value string) workflowInputInstanceResponse {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"name": name, "input": map[string]string{"title": "Same title", "description": value, "severity": "high"}})
		response := call("POST", "", string(body), testWorkspaceID, h.SaveWorkflowInputInstance)
		if response.Code != 201 {
			t.Fatalf("create: %d %s", response.Code, response.Body.String())
		}
		var row workflowInputInstanceResponse
		if err := json.Unmarshal(response.Body.Bytes(), &row); err != nil {
			t.Fatal(err)
		}
		return row
	}
	first := save("Scenario A", "Input A")
	second := save("Scenario B", "Input B")
	if first.CreatedByID == nil || *first.CreatedByID != testUserID || first.CreatedAt == "" {
		t.Fatal("instance must retain its creator and creation time")
	}
	var versionID string
	if err := tx.QueryRow(ctx, `INSERT INTO workflow_template_version (workspace_id,template_id,version,definition,schema_version,status,published_by_type,published_by_id,published_at) VALUES ($1,$2,1,'{}',1,'published','member',$3,now()) RETURNING id`, testWorkspaceID, templateID, testUserID).Scan(&versionID); err != nil {
		t.Fatal(err)
	}
	versionBody := `{"name":"Pinned","input":{"description":""},"template_version_id":"` + versionID + `"}`
	pinned := call("POST", "", versionBody, testWorkspaceID, h.SaveWorkflowInputInstance)
	if pinned.Code != 201 {
		t.Fatalf("pin version: %d %s", pinned.Code, pinned.Body.String())
	}
	var pinnedRow workflowInputInstanceResponse
	if err := json.Unmarshal(pinned.Body.Bytes(), &pinnedRow); err != nil {
		t.Fatal(err)
	}
	var pinnedInput map[string]string
	if err := json.Unmarshal(pinnedRow.Input, &pinnedInput); err != nil {
		t.Fatal(err)
	}
	emptyValue, present := pinnedInput["description"]
	if pinnedRow.TemplateVersionID == nil || *pinnedRow.TemplateVersionID != versionID || !present || emptyValue != "" || len(pinnedInput) != 1 {
		t.Fatalf("version and explicit empty value not preserved: %s", pinned.Body.String())
	}
	if response := call("DELETE", pinnedRow.ID, "", testWorkspaceID, h.DeleteWorkflowInputInstance); response.Code != 204 {
		t.Fatal(response.Body.String())
	}
	if first.ID == second.ID {
		t.Fatal("instances overwrote one another")
	}
	response := call("GET", "", "", testWorkspaceID, h.ListWorkflowInputInstances)
	var list struct {
		Instances []workflowInputInstanceResponse `json:"instances"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Instances) != 2 {
		t.Fatalf("list=%s", response.Body.String())
	}
	if !bytes.Contains(first.Input, []byte("Input A")) || !bytes.Contains(second.Input, []byte("Input B")) {
		t.Fatal("input values were not preserved")
	}
	update := `{"name":"Renamed A","input":{"title":"Edited","description":"New A"},"revision":1}`
	if response := call("PUT", first.ID, update, testWorkspaceID, h.SaveWorkflowInputInstance); response.Code != 200 {
		t.Fatalf("update: %d %s", response.Code, response.Body.String())
	}
	if response := call("PUT", first.ID, update, testWorkspaceID, h.SaveWorkflowInputInstance); response.Code != 409 {
		t.Fatalf("stale revision: %d", response.Code)
	}
	for _, body := range []string{`{"name":"","input":{}}`, `{"name":"bad","input":null}`, `{"name":"bad","input":{"count":3}}`, `{"name":"bad","input":{"value":null}}`, `{"name":"bad","input":{}} {}`, `{"name":"bad","input":{},"template_version_id":"bad"}`, `{"name":"bad","input":{},"project_id":"not-a-uuid"}`} {
		if response := call("POST", "", body, testWorkspaceID, h.SaveWorkflowInputInstance); response.Code != 400 {
			t.Fatalf("invalid input accepted: %d %s", response.Code, body)
		}
	}
	if response := call("GET", "", "", "00000000-0000-4000-8000-000000000001", h.ListWorkflowInputInstances); response.Code != 404 {
		t.Fatalf("workspace isolation: %d", response.Code)
	}
	if response := call("DELETE", first.ID, "", testWorkspaceID, h.DeleteWorkflowInputInstance); response.Code != 204 {
		t.Fatalf("delete: %d", response.Code)
	}
	response = call("GET", "", "", testWorkspaceID, h.ListWorkflowInputInstances)
	if err := json.Unmarshal(response.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Instances) != 1 || list.Instances[0].ID != second.ID {
		t.Fatal("delete affected another instance")
	}
	var runs int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM workflow_run WHERE template_id=$1`, templateID).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if runs != 0 {
		t.Fatal("saving an input instance started a workflow")
	}
}
