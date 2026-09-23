package main

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/handler"
	"github.com/multica-ai/multica/server/internal/testutil"
)

func TestR5PublishedIntakeActualRouter(t *testing.T) {
	fx := testutil.New(testPool, testWorkspaceID, testUserID)
	key := fmt.Sprintf("r5_%d", time.Now().UnixNano())
	var tpl handler.WorkflowTemplateResponse
	response := authRequest(t, "POST", "/api/workflow-templates", map[string]any{"key": key, "name": "R5 intake router", "definition": map[string]any{"schema_version": 1, "entry_node": "end", "nodes": []map[string]any{{"key": "end", "type": "end"}}}})
	if response.StatusCode != 201 {
		t.Fatalf("create template status %d", response.StatusCode)
	}
	readJSON(t, response, &tpl)
	t.Cleanup(func() {
		ctx := context.Background()
		for _, query := range []string{
			"DELETE FROM workflow_event WHERE run_id IN (SELECT id FROM workflow_run WHERE template_id=$1)",
			"DELETE FROM workflow_step_instance WHERE run_id IN (SELECT id FROM workflow_run WHERE template_id=$1)",
			"DELETE FROM issue_subscriber WHERE issue_id IN (SELECT issue_id FROM workflow_run WHERE template_id=$1)",
			"DELETE FROM issue WHERE id IN (SELECT issue_id FROM workflow_run WHERE template_id=$1)",
			"DELETE FROM workflow_run WHERE template_id=$1",
			"DELETE FROM workflow_template_version WHERE template_id=$1",
			"DELETE FROM workflow_template WHERE id=$1",
		} {
			if _, err := testPool.Exec(ctx, query, tpl.ID); err != nil {
				t.Errorf("cleanup: %v", err)
			}
		}
	})
	published := authRequest(t, "POST", "/api/workflow-templates/"+tpl.ID+"/publish", map[string]any{})
	if published.StatusCode != 200 {
		t.Fatalf("publish status %d", published.StatusCode)
	}
	published.Body.Close()
	body := map[string]any{"source": "r5", "event_id": "router", "template_key": key, "title": "Actual router", "description": "Middleware and engine assembly"}
	path := "/api/workspaces/" + testWorkspaceID + "/workflow-intake"
	request := func(token string, payload any) *http.Request {
		req := testutil.JSONRequest("POST", path, payload)
		req.Header.Set("X-Workspace-ID", testWorkspaceID)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		return req
	}
	count := func() int {
		var n int
		if err := testPool.QueryRow(context.Background(), "SELECT count(*) FROM workflow_run WHERE template_id=$1", tpl.ID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	serve := testServer.Config.Handler.ServeHTTP
	testutil.Call(t, serve, request("", body)).Want(401)
	for _, key := range []string{"callback_destination_id", "input_instance_id", "input_instance_revision", "input_source", "execution_mode", "debug_session_id"} {
		payload := map[string]any{}
		for k, v := range body {
			payload[k] = v
		}
		payload[key] = "blocked"
		testutil.Call(t, serve, request(testToken, payload)).Want(400)
	}
	var agentID, runtimeID string
	fx.QueryRow(t, "SELECT id,runtime_id FROM agent WHERE workspace_id=$1 LIMIT 1", testWorkspaceID).Scan(&agentID, &runtimeID)
	taskID := fx.Task(t, agentID, testutil.Cols{"runtime_id": runtimeID, "status": "running"})
	machine := mintAgentTaskToken(t, agentID, taskID, testUserID)
	testutil.Call(t, serve, request(machine, body)).Want(403)
	if count() != 0 {
		t.Fatal("refusals created workflow runs")
	}
	req := request(testToken, body)
	req.Header.Set("X-Agent-ID", agentID)
	req.Header.Set("X-Task-ID", taskID)
	req.Header.Set("X-Actor-Source", "task_token")
	var receipt handler.WorkflowIntakeResponse
	testutil.Call(t, serve, req).Want(201).JSON(&receipt)
	var creatorType, creatorID string
	fx.QueryRow(t, "SELECT creator_type,creator_id FROM issue WHERE id=$1", receipt.IssueID).Scan(&creatorType, &creatorID)
	if creatorType != "member" || creatorID != testUserID {
		t.Fatal("forged headers changed attribution")
	}
	testutil.Call(t, serve, request(testToken, body)).Want(200)
	if count() != 1 {
		t.Fatal("replay created multiple runs")
	}
	otherWS := fx.Workspace(t, "Mismatched path", fmt.Sprintf("r5_other_%d", time.Now().UnixNano()))
	fx.Member(t, otherWS, testUserID, "owner")
	mismatched := testutil.JSONRequest("POST", "/api/workspaces/"+otherWS+"/workflow-intake", body)
	mismatched.Header.Set("Authorization", "Bearer "+testToken)
	mismatched.Header.Set("X-Workspace-ID", testWorkspaceID)
	testutil.Call(t, serve, mismatched).Want(403)
}
