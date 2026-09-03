package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/multica-ai/multica/server/internal/cli"
)

func TestWorkflowToolsExposeClosedVersionedSchemas(t *testing.T) {
	if len(workflowTools) != 7 {
		t.Fatalf("tool count = %d, want 7", len(workflowTools))
	}
	for _, tool := range workflowTools {
		if tool.InputSchema["additionalProperties"] != false {
			t.Fatalf("%s schema is not closed", tool.Name)
		}
		properties := tool.InputSchema["properties"].(map[string]any)
		version := properties["schema_version"].(map[string]any)
		if version["const"] != workflowActionSchemaVersion {
			t.Fatalf("%s schema version = %v", tool.Name, version["const"])
		}
	}
}

func TestWorkflowActionRejectsUnknownAndUnversionedInput(t *testing.T) {
	client := cli.NewAPIClient("http://127.0.0.1", "", "")
	_, err := executeWorkflowAction(context.Background(), client, "template.list", map[string]any{})
	if err == nil || !strings.Contains(err.Error(), "schema_version") {
		t.Fatalf("unversioned input error = %v", err)
	}
	_, err = executeWorkflowAction(context.Background(), client, "template.list", map[string]any{"schema_version": "1", "surprise": true})
	if err == nil || !strings.Contains(err.Error(), "unknown argument") {
		t.Fatalf("unknown input error = %v", err)
	}
}

func TestWorkflowCLIAndMCPShareIdempotentRunAction(t *testing.T) {
	var mu sync.Mutex
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/workflow-templates/template-1/run" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		bodies = append(bodies, body)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"run-1","status":"running"}`))
	}))
	defer server.Close()
	client := cli.NewAPIClient(server.URL, "workspace-1", "token")
	input := map[string]any{"schema_version": "1", "template_id": "template-1", "idempotency_key": "retry-42", "title": "Ship", "description": "Verify"}

	if _, err := executeWorkflowAction(context.Background(), client, "run.start", input); err != nil {
		t.Fatalf("direct action: %v", err)
	}
	params, _ := json.Marshal(map[string]any{"name": "run.start", "arguments": input})
	response := handleWorkflowMCP(context.Background(), client, mcpRequest{JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "tools/call", Params: params})
	if response.Error != nil {
		t.Fatalf("MCP response error: %+v", response.Error)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 2 || bodies[0]["idempotency_key"] != "retry-42" || bodies[1]["idempotency_key"] != "retry-42" {
		t.Fatalf("run bodies = %+v", bodies)
	}
	if _, sent := bodies[0]["schema_version"]; sent {
		t.Fatalf("adapter leaked contract metadata into REST body: %+v", bodies[0])
	}
}

func TestWorkflowMCPToolsList(t *testing.T) {
	client := cli.NewAPIClient("http://127.0.0.1", "", "")
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}` + "\n")
	var out bytes.Buffer
	if err := serveWorkflowMCP(context.Background(), client, in, &out); err != nil {
		t.Fatal(err)
	}
	var response map[string]any
	if err := json.Unmarshal(out.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	result := response["result"].(map[string]any)
	if got := len(result["tools"].([]any)); got != 7 {
		t.Fatalf("tools/list count = %d", got)
	}
}

func TestNormalizeWorkflowActionErrorPreservesStableAPICode(t *testing.T) {
	err := normalizeWorkflowActionError(&cli.HTTPError{
		StatusCode: http.StatusConflict,
		Body:       `{"error":"idempotency key was reused with different input","code":"workflow_idempotency_conflict"}`,
	})
	contractErr, ok := err.(workflowContractError)
	if !ok {
		t.Fatalf("error type = %T, want workflowContractError", err)
	}
	if contractErr.Code != "workflow_idempotency_conflict" {
		t.Fatalf("error code = %q", contractErr.Code)
	}
	if contractErr.Message != "idempotency key was reused with different input" {
		t.Fatalf("error message = %q", contractErr.Message)
	}
}
