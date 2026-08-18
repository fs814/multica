package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/workflow"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestAutopilotWorkflowBindingDispatchesPinnedRunEndToEnd(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	workspaceID := parseUUID(testWorkspaceID)
	userID := parseUUID(testUserID)
	key := fmt.Sprintf("autopilot-e2e-%d", time.Now().UnixNano())
	template, err := testHandler.Queries.CreateWorkflowTemplate(ctx, db.CreateWorkflowTemplateParams{WorkspaceID: workspaceID, Key: key, Name: "Autopilot E2E", Description: "", CreatedByType: "member", CreatedByID: userID})
	if err != nil {
		t.Fatalf("create template: %v", err)
	}
	definition := &workflow.Definition{SchemaVersion: workflow.SchemaVersion, EntryNode: "end", Nodes: []workflow.Node{{Key: "end", Type: workflow.NodeTypeEnd}}}
	raw, err := workflow.MarshalDefinition(definition)
	if err != nil {
		t.Fatalf("marshal definition: %v", err)
	}
	version, err := testHandler.Queries.CreateWorkflowTemplateVersion(ctx, db.CreateWorkflowTemplateVersionParams{WorkspaceID: workspaceID, TemplateID: template.ID, Definition: raw, SchemaVersion: workflow.SchemaVersion})
	if err != nil {
		t.Fatalf("create version: %v", err)
	}
	version, err = testHandler.Queries.PublishWorkflowTemplateVersion(ctx, db.PublishWorkflowTemplateVersionParams{ID: version.ID, WorkspaceID: workspaceID, PublishedByType: "member", PublishedByID: userID})
	if err != nil {
		t.Fatalf("publish version: %v", err)
	}
	if _, err := testHandler.Queries.SetWorkflowTemplateCurrentVersion(ctx, db.SetWorkflowTemplateCurrentVersionParams{ID: template.ID, WorkspaceID: workspaceID, CurrentVersion: version.Version}); err != nil {
		t.Fatalf("set current version: %v", err)
	}

	agentID := createHandlerTestAgent(t, "Autopilot Workflow Agent", nil)
	w := httptest.NewRecorder()
	req := newRequest(http.MethodPost, "/api/autopilots?workspace_id="+testWorkspaceID, map[string]any{"title": "workflow-bound autopilot", "assignee_id": agentID, "execution_mode": "run_only", "workflow_template_id": uuidToString(template.ID)})
	testHandler.CreateAutopilot(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateAutopilot: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var response AutopilotResponse
	if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.WorkflowTemplateID == nil || *response.WorkflowTemplateID != uuidToString(template.ID) {
		t.Fatalf("template binding = %v, want %s", response.WorkflowTemplateID, uuidToString(template.ID))
	}
	if response.WorkflowTemplateVersionID == nil || *response.WorkflowTemplateVersionID != uuidToString(version.ID) {
		t.Fatalf("pinned version = %v, want %s", response.WorkflowTemplateVersionID, uuidToString(version.ID))
	}

	t.Cleanup(func() {
		bg := context.Background()
		testPool.Exec(bg, `DELETE FROM workflow_event WHERE workspace_id=$1 AND run_id IN (SELECT id FROM workflow_run WHERE template_id=$2)`, workspaceID, template.ID)
		testPool.Exec(bg, `DELETE FROM workflow_step_instance WHERE workspace_id=$1 AND run_id IN (SELECT id FROM workflow_run WHERE template_id=$2)`, workspaceID, template.ID)
		testPool.Exec(bg, `DELETE FROM workflow_run WHERE workspace_id=$1 AND template_id=$2`, workspaceID, template.ID)
		testPool.Exec(bg, `DELETE FROM autopilot_run WHERE autopilot_id=$1`, response.ID)
		testPool.Exec(bg, `DELETE FROM autopilot_rule_version WHERE autopilot_id=$1`, response.ID)
		testPool.Exec(bg, `DELETE FROM autopilot WHERE id=$1`, response.ID)
		testPool.Exec(bg, `DELETE FROM workflow_template_version WHERE template_id=$1`, template.ID)
		testPool.Exec(bg, `DELETE FROM workflow_template WHERE id=$1`, template.ID)
	})

	engine := &workflow.Engine{Queries: testHandler.Queries, TxStarter: testPool, Schemas: workflow.DefaultSchemaRegistry}
	previous := testHandler.AutopilotService.WorkflowEngine
	testHandler.AutopilotService.WorkflowEngine = engine
	t.Cleanup(func() { testHandler.AutopilotService.WorkflowEngine = previous })
	autopilot, err := testHandler.Queries.GetAutopilot(ctx, parseUUID(response.ID))
	if err != nil {
		t.Fatalf("load autopilot: %v", err)
	}
	autopilotRun, _, err := testHandler.AutopilotService.DispatchAutopilotManual(ctx, autopilot, pgtype.UUID{}, []byte(`{}`), userID)
	if err != nil {
		t.Fatalf("dispatch workflow autopilot: %v", err)
	}
	if !autopilotRun.WorkflowRunID.Valid {
		t.Fatal("autopilot run has no workflow_run_id")
	}
	workflowRun, err := testHandler.Queries.GetWorkflowRun(ctx, db.GetWorkflowRunParams{ID: autopilotRun.WorkflowRunID, WorkspaceID: workspaceID})
	if err != nil {
		t.Fatalf("load workflow run: %v", err)
	}
	if workflowRun.Status != string(workflow.RunCompleted) {
		t.Fatalf("workflow run status = %q, want completed", workflowRun.Status)
	}
	reconciler := workflow.NewReconciler(engine, testHandler.Queries)
	reconciler.StaleAfter = 0
	if err := reconciler.Sweep(ctx); err != nil {
		t.Fatalf("reconcile terminal projection: %v", err)
	}
	var status string
	if err := testPool.QueryRow(ctx, `SELECT status FROM autopilot_run WHERE id=$1`, autopilotRun.ID).Scan(&status); err != nil {
		t.Fatalf("reload autopilot run: %v", err)
	}
	if status != "completed" {
		t.Fatalf("autopilot run status = %q, want completed", status)
	}
}
