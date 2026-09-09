package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPAssignmentPromptLoadsOnlyTaskText(t *testing.T) {
	t.Parallel()
	task := Task{IssueID: "issue-id", WorkspaceID: "workspace-id", AuthToken: "mat_test-task", Agent: &AgentData{Instructions: "private-agent-settings"}, WorkspaceContext: "private-workspace-context"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/issues/issue-id" {
			t.Errorf("unexpected lookup: %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer mat_test-task" {
			t.Error("lookup did not use task credential")
		}
		if r.Header.Get("X-Workspace-ID") != task.WorkspaceID {
			t.Error("missing workspace header")
		}
		if err := json.NewEncoder(w).Encode(map[string]any{"id": task.IssueID, "title": "列出system os version - macbook", "description": "Report command output", "private_metadata": "private-response-field"}); err != nil {
			t.Error(err)
		}
	}))
	defer srv.Close()
	client := NewClient(srv.URL)
	client.SetToken("owner-secret")
	d := &Daemon{client: client}
	prompt, err := d.buildExecutionPrompt(context.Background(), task, "knot-http")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"列出system os version - macbook", "Report command output", "selected tool host", "final reply", "assigning server records"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
	for _, forbidden := range []string{task.AuthToken, task.IssueID, "owner-secret", "private-response-field", "private-agent-settings", "private-workspace-context", "multica issue get", "Ownership"} {
		if strings.Contains(prompt, forbidden) {
			t.Errorf("prompt contains forbidden context %q", forbidden)
		}
	}
	if client.token != "owner-secret" {
		t.Error("shared client token changed")
	}
}

func TestHTTPAssignmentPromptFailsWithoutOriginContext(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, body, want string
		status           int
	}{
		{"missing", "private error body", "HTTP 404", 404},
		{"unauthorized", "private error body", "HTTP 401", 401},
		{"redirect", "", "HTTP 302", 302},
		{"wrong issue", `{"id":"different","title":"task"}`, "mismatched", 200},
		{"empty title", `{"id":"issue-id"}`, "missing", 200},
		{"invalid json", "invalid", "decode", 200},
		{"oversized", strings.Repeat("x", (1<<20)+1), "exceeds", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", "/must-not-follow")
				w.WriteHeader(tc.status)
				if _, err := w.Write([]byte(tc.body)); err != nil {
					t.Error(err)
				}
			}))
			defer srv.Close()
			d := &Daemon{client: NewClient(srv.URL)}
			prompt, err := d.buildExecutionPrompt(context.Background(), Task{IssueID: "issue-id", WorkspaceID: "workspace-id", AuthToken: "mat_test"}, "knot-http")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
			if prompt != "" {
				t.Error("failed lookup produced an executable prompt")
			}
			if strings.Contains(err.Error(), "private error body") {
				t.Error("response body leaked into error")
			}
		})
	}
}

func TestHTTPAssignmentPromptRejectsMissingTaskAuth(t *testing.T) {
	t.Parallel()
	// No HTTP client: invalid identity must fail before attempting any request.
	d := &Daemon{client: &Client{}}
	for _, task := range []Task{
		{IssueID: "issue-id", WorkspaceID: "workspace-id"},
		{IssueID: "issue-id", WorkspaceID: "workspace-id", AuthToken: "owner-token"},
		{IssueID: "issue-id", AuthToken: "mat_test"},
	} {
		if _, err := d.buildExecutionPrompt(context.Background(), task, "knot-http"); err == nil {
			t.Error("missing task identity accepted")
		}
	}
}

func TestHTTPAssignmentPromptPreservesOtherTurnContracts(t *testing.T) {
	t.Parallel()
	d := &Daemon{}
	for _, task := range []Task{
		{IssueID: "issue-id", WorkflowPrompt: "workflow contract"},
		{IssueID: "issue-id", TriggerCommentID: "comment-id", TriggerCommentContent: "reply"},
		{IssueID: "issue-id", HandoffNote: "handoff"},
		{ChatSessionID: "chat-id", ChatMessage: "hello"},
		{AutopilotRunID: "run-id", AutopilotDescription: "scheduled"},
		{QuickCreatePrompt: "create task"},
	} {
		got, err := d.buildExecutionPrompt(context.Background(), task, "knot-http")
		if err != nil || got != BuildPrompt(task, "knot-http") {
			t.Errorf("changed turn contract: %v", err)
		}
	}
	task := Task{IssueID: "issue-id"}
	got, err := d.buildExecutionPrompt(context.Background(), task, "codex")
	if err != nil || got != BuildPrompt(task, "codex") {
		t.Error("local provider prompt changed")
	}
}
