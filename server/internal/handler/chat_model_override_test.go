package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// createChatSessionWithModelForTest seeds a session carrying an explicit model
// override. Pass "" to leave the column NULL ("follow the agent's default").
func createChatSessionWithModelForTest(t *testing.T, agentID, model string) string {
	t.Helper()

	var sessionID string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO chat_session (workspace_id, agent_id, creator_id, title, status, model)
		VALUES ($1, $2, $3, 'Model override chat', 'active', NULLIF($4, ''))
		RETURNING id
	`, testWorkspaceID, agentID, testUserID, model).Scan(&sessionID); err != nil {
		t.Fatalf("create chat session: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM chat_session WHERE id = $1`, sessionID)
	})
	return sessionID
}

// storedChatSessionModel reads the column directly so the assertions prove what
// landed in Postgres rather than trusting the response body alone.
func storedChatSessionModel(t *testing.T, sessionID string) *string {
	t.Helper()

	var model *string
	if err := testPool.QueryRow(context.Background(), `
		SELECT model FROM chat_session WHERE id = $1
	`, sessionID).Scan(&model); err != nil {
		t.Fatalf("read chat_session.model: %v", err)
	}
	return model
}

func TestUpdateChatSession_UpdatesModel(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	agentID := createHandlerTestAgent(t, "ChatModelOverrideAgent", []byte("[]"))
	sessionID := createChatSessionWithModelForTest(t, agentID, "")

	updateModel := func(model any) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		req := withURLParam(
			withChatTestWorkspaceCtx(t, newRequest(http.MethodPatch, "/api/chat/sessions/"+sessionID, map[string]any{
				"model": model,
			})),
			"sessionId",
			sessionID,
		)
		testHandler.UpdateChatSession(w, req)
		return w
	}

	decode := func(t *testing.T, w *httptest.ResponseRecorder) ChatSessionResponse {
		t.Helper()
		var response ChatSessionResponse
		if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		return response
	}

	t.Run("sets an override", func(t *testing.T) {
		w := updateModel("claude-opus-4-8")
		if w.Code != http.StatusOK {
			t.Fatalf("set model: expected 200, got %d: %s", w.Code, w.Body.String())
		}
		response := decode(t, w)
		if response.ID != sessionID {
			t.Fatalf("session id = %q, want %q", response.ID, sessionID)
		}
		if response.Model == nil || *response.Model != "claude-opus-4-8" {
			t.Fatalf("response model = %v, want claude-opus-4-8", response.Model)
		}
		if stored := storedChatSessionModel(t, sessionID); stored == nil || *stored != "claude-opus-4-8" {
			t.Fatalf("stored model = %v, want claude-opus-4-8", stored)
		}
	})

	t.Run("replaces an override", func(t *testing.T) {
		w := updateModel("gpt-5.6-terra")
		if w.Code != http.StatusOK {
			t.Fatalf("replace model: expected 200, got %d: %s", w.Code, w.Body.String())
		}
		if response := decode(t, w); response.Model == nil || *response.Model != "gpt-5.6-terra" {
			t.Fatalf("response model = %v, want gpt-5.6-terra", response.Model)
		}
	})

	t.Run("trims surrounding whitespace", func(t *testing.T) {
		w := updateModel("  gpt-5.6-terra  ")
		if w.Code != http.StatusOK {
			t.Fatalf("trim model: expected 200, got %d: %s", w.Code, w.Body.String())
		}
		if response := decode(t, w); response.Model == nil || *response.Model != "gpt-5.6-terra" {
			t.Fatalf("response model = %v, want the trimmed id", response.Model)
		}
	})

	t.Run("accepts a model this server has never heard of", func(t *testing.T) {
		// Catalogs are per-runtime and discovered on the user's machine, so the
		// server must not reject an id it cannot classify.
		w := updateModel("some-private-fork/v9")
		if w.Code != http.StatusOK {
			t.Fatalf("custom model: expected 200, got %d: %s", w.Code, w.Body.String())
		}
		if response := decode(t, w); response.Model == nil || *response.Model != "some-private-fork/v9" {
			t.Fatalf("response model = %v, want the custom id", response.Model)
		}
	})

	t.Run("clears via empty string", func(t *testing.T) {
		if w := updateModel("gpt-5.6-terra"); w.Code != http.StatusOK {
			t.Fatalf("reseed model: expected 200, got %d: %s", w.Code, w.Body.String())
		}
		w := updateModel("")
		if w.Code != http.StatusOK {
			t.Fatalf("clear via empty string: expected 200, got %d: %s", w.Code, w.Body.String())
		}
		if response := decode(t, w); response.Model != nil {
			t.Fatalf("response model = %v, want nil", response.Model)
		}
		if stored := storedChatSessionModel(t, sessionID); stored != nil {
			t.Fatalf("stored model = %v, want NULL", stored)
		}
	})

	t.Run("clears via explicit null", func(t *testing.T) {
		if w := updateModel("gpt-5.6-terra"); w.Code != http.StatusOK {
			t.Fatalf("reseed model: expected 200, got %d: %s", w.Code, w.Body.String())
		}
		w := updateModel(nil)
		if w.Code != http.StatusOK {
			t.Fatalf("clear via null: expected 200, got %d: %s", w.Code, w.Body.String())
		}
		if response := decode(t, w); response.Model != nil {
			t.Fatalf("response model = %v, want nil", response.Model)
		}
		if stored := storedChatSessionModel(t, sessionID); stored != nil {
			t.Fatalf("stored model = %v, want NULL", stored)
		}
	})

	t.Run("rejects an over-long model", func(t *testing.T) {
		if w := updateModel(strings.Repeat("m", chatSessionModelMaxLen+1)); w.Code != http.StatusBadRequest {
			t.Fatalf("over-long model: expected 400, got %d: %s", w.Code, w.Body.String())
		}
	})

	t.Run("rejects a non-string model", func(t *testing.T) {
		if w := updateModel(42); w.Code != http.StatusBadRequest {
			t.Fatalf("numeric model: expected 400, got %d: %s", w.Code, w.Body.String())
		}
	})
}

// TestUpdateChatSession_RequiresExactlyOneField covers the exactly-one guard,
// which had to be reworked from a two-field equality check when model joined
// title and project_id.
func TestUpdateChatSession_RequiresExactlyOneField(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	agentID := createHandlerTestAgent(t, "ChatModelExclusivityAgent", []byte("[]"))
	sessionID := createChatSessionWithModelForTest(t, agentID, "")

	patch := func(body map[string]any) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		req := withURLParam(
			withChatTestWorkspaceCtx(t, newRequest(http.MethodPatch, "/api/chat/sessions/"+sessionID, body)),
			"sessionId",
			sessionID,
		)
		testHandler.UpdateChatSession(w, req)
		return w
	}

	cases := []struct {
		name string
		body map[string]any
	}{
		{"title and model", map[string]any{"title": "both", "model": "gpt-5.6-terra"}},
		{"project_id and model", map[string]any{"project_id": nil, "model": "gpt-5.6-terra"}},
		{"all three", map[string]any{"title": "all", "project_id": nil, "model": "gpt-5.6-terra"}},
		{"no fields", map[string]any{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if w := patch(tc.body); w.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
			}
		})
	}
}

// claimedChatTaskAgent claims the single queued task on the test runtime and
// returns the agent payload the daemon would receive.
func claimedChatTaskAgent(t *testing.T, runtimeID, taskID, label string) struct {
	Model         string `json:"model"`
	ThinkingLevel string `json:"thinking_level"`
	ServiceTier   string `json:"service_tier"`
} {
	t.Helper()

	w := httptest.NewRecorder()
	req := newDaemonTokenRequest(
		http.MethodPost,
		"/api/daemon/runtimes/"+runtimeID+"/claim",
		nil,
		testWorkspaceID,
		label,
	)
	req = withURLParam(req, "runtimeId", runtimeID)
	testHandler.ClaimTaskByRuntime(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("ClaimTaskByRuntime: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var response struct {
		Task *struct {
			ID    string `json:"id"`
			Agent *struct {
				Model         string `json:"model"`
				ThinkingLevel string `json:"thinking_level"`
				ServiceTier   string `json:"service_tier"`
			} `json:"agent"`
		} `json:"task"`
	}
	if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Task == nil || response.Task.ID != taskID {
		t.Fatalf("claimed task = %+v, want %s", response.Task, taskID)
	}
	if response.Task.Agent == nil {
		t.Fatal("claimed task carried no agent payload")
	}
	return *response.Task.Agent
}

// seedChatTaskForModelClaim creates an agent with a default model plus tuning, a
// session with the given override, and one queued chat task ready to claim.
func seedChatTaskForModelClaim(t *testing.T, agentName, sessionModel string) (runtimeID, taskID string) {
	t.Helper()

	ctx := context.Background()
	agentID := createHandlerTestAgent(t, agentName, []byte("[]"))
	if _, err := testPool.Exec(ctx, `
		UPDATE agent SET model = 'agent-default', thinking_level = 'high', service_tier = 'priority'
		WHERE id = $1
	`, agentID); err != nil {
		t.Fatalf("seed agent model: %v", err)
	}

	runtimeID = handlerTestRuntimeID(t)
	sessionID := createChatSessionWithModelForTest(t, agentID, sessionModel)
	if _, err := testPool.Exec(ctx, `
		INSERT INTO chat_message (chat_session_id, role, content)
		VALUES ($1, 'user', 'Which model is running?')
	`, sessionID); err != nil {
		t.Fatalf("create chat message: %v", err)
	}

	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (
			agent_id, runtime_id, status, priority, chat_session_id
		) VALUES ($1, $2, 'queued', 1000, $3)
		RETURNING id
	`, agentID, runtimeID, sessionID).Scan(&taskID); err != nil {
		t.Fatalf("create chat task: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id = $1`, taskID)
	})
	return runtimeID, taskID
}

// TestClaimTaskByRuntime_ChatSessionModelOverride is the test that proves the
// feature actually reaches the runtime: the claim payload must carry the
// session's model instead of the agent's.
func TestClaimTaskByRuntime_ChatSessionModelOverride(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	runtimeID, taskID := seedChatTaskForModelClaim(t, "ChatModelClaimAgent", "session-override")
	agent := claimedChatTaskAgent(t, runtimeID, taskID, "chat-session-model-override-test")

	if agent.Model != "session-override" {
		t.Fatalf("agent.model = %q, want the session override", agent.Model)
	}
	// thinking_level / service_tier are deliberately preserved: the daemon
	// validates both against its local per-model catalog and drops incompatible
	// values itself, so the server must not clear a choice the overriding model
	// may well support (see buildModelChangeUpdate / MUL-5390).
	if agent.ThinkingLevel != "high" {
		t.Fatalf("agent.thinking_level = %q, want it preserved as high", agent.ThinkingLevel)
	}
	if agent.ServiceTier != "priority" {
		t.Fatalf("agent.service_tier = %q, want it preserved as priority", agent.ServiceTier)
	}
}

// TestClaimTaskByRuntime_ChatSessionModelFallsBack is the additive guard: a
// session with no override must leave the agent's own model untouched.
func TestClaimTaskByRuntime_ChatSessionModelFallsBack(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	runtimeID, taskID := seedChatTaskForModelClaim(t, "ChatModelFallbackAgent", "")
	agent := claimedChatTaskAgent(t, runtimeID, taskID, "chat-session-model-fallback-test")

	if agent.Model != "agent-default" {
		t.Fatalf("agent.model = %q, want the agent default", agent.Model)
	}
	if agent.ThinkingLevel != "high" || agent.ServiceTier != "priority" {
		t.Fatalf("agent tuning = (%q, %q), want (high, priority)", agent.ThinkingLevel, agent.ServiceTier)
	}
}
