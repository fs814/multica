package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestAutopilotAPIRejectsIssuePoolWithoutAgent(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	agentID := createHandlerTestAgent(t, fmt.Sprintf("issue-pool-contract-%d", time.Now().UnixNano()), nil)
	var squadID string
	if err := testPool.QueryRow(ctx, `INSERT INTO squad(workspace_id,name,leader_id,creator_id)
		VALUES($1,$2,$3,$4) RETURNING id`, testWorkspaceID,
		fmt.Sprintf("Issue Pool Contract %d", time.Now().UnixNano()), agentID, testUserID).Scan(&squadID); err != nil {
		t.Fatalf("create squad fixture: %v", err)
	}
	var squadAutopilotID string
	if err := testPool.QueryRow(ctx, `INSERT INTO autopilot(
		workspace_id,title,assignee_type,assignee_id,execution_mode,created_by_type,created_by_id,status
	) VALUES($1,$2,'squad',$3,'run_only','member',$4,'active') RETURNING id`,
		testWorkspaceID, "Squad mode switch fixture", squadID, testUserID).Scan(&squadAutopilotID); err != nil {
		t.Fatalf("create squad autopilot fixture: %v", err)
	}
	issuePoolAutopilotID := newIssuePoolAutopilot(t, nil)
	var issuePoolAgentID string
	if err := testPool.QueryRow(ctx, `SELECT assignee_id FROM autopilot WHERE id=$1`, issuePoolAutopilotID).Scan(&issuePoolAgentID); err != nil {
		t.Fatalf("load valid issue-pool autopilot fixture: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM autopilot WHERE id=$1`, squadAutopilotID)
		testPool.Exec(context.Background(), `DELETE FROM squad WHERE id=$1`, squadID)
	})

	assertRejected := func(t *testing.T, w *httptest.ResponseRecorder) {
		t.Helper()
		if w.Code != http.StatusBadRequest {
			t.Fatalf("status=%d body=%s, want 400", w.Code, w.Body.String())
		}
		var body struct {
			Error string `json:"error"`
			Code  string `json:"code"`
		}
		if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
			t.Fatalf("decode error response: %v", err)
		}
		if body.Code != issuePoolRequiresAgentCode || body.Error != "issue_pool execution requires an agent assignee" {
			t.Fatalf("error=%q code=%q", body.Error, body.Code)
		}
	}

	t.Run("create squad", func(t *testing.T) {
		w := httptest.NewRecorder()
		r := newRequest(http.MethodPost, "/api/autopilots?workspace_id="+testWorkspaceID, map[string]any{
			"title": "invalid squad issue pool", "assignee_type": "squad", "assignee_id": squadID, "execution_mode": "issue_pool",
		})
		testHandler.CreateAutopilot(w, r)
		assertRejected(t, w)
		var persisted int
		if err := testPool.QueryRow(ctx, `SELECT count(*) FROM autopilot WHERE workspace_id=$1 AND title=$2`,
			testWorkspaceID, "invalid squad issue pool").Scan(&persisted); err != nil || persisted != 0 {
			t.Fatalf("rejected create persisted=%d err=%v", persisted, err)
		}
	})

	t.Run("squad switches to issue pool", func(t *testing.T) {
		w := httptest.NewRecorder()
		r := newRequest(http.MethodPatch, "/api/autopilots/"+squadAutopilotID+"?workspace_id="+testWorkspaceID,
			map[string]any{"execution_mode": "issue_pool"})
		r = withURLParam(r, "id", squadAutopilotID)
		testHandler.UpdateAutopilot(w, r)
		assertRejected(t, w)
		var mode string
		if err := testPool.QueryRow(ctx, `SELECT execution_mode FROM autopilot WHERE id=$1`, squadAutopilotID).Scan(&mode); err != nil || mode != "run_only" {
			t.Fatalf("rejected mode update left mode=%q err=%v", mode, err)
		}
	})

	t.Run("issue pool agent switches to squad", func(t *testing.T) {
		w := httptest.NewRecorder()
		r := newRequest(http.MethodPatch, "/api/autopilots/"+issuePoolAutopilotID+"?workspace_id="+testWorkspaceID,
			map[string]any{"assignee_type": "squad", "assignee_id": squadID})
		r = withURLParam(r, "id", issuePoolAutopilotID)
		testHandler.UpdateAutopilot(w, r)
		assertRejected(t, w)
		var assigneeType, assigneeID string
		if err := testPool.QueryRow(ctx, `SELECT assignee_type,assignee_id FROM autopilot WHERE id=$1`, issuePoolAutopilotID).
			Scan(&assigneeType, &assigneeID); err != nil || assigneeType != "agent" || assigneeID != issuePoolAgentID {
			t.Fatalf("rejected assignee update left type=%q id=%q err=%v", assigneeType, assigneeID, err)
		}
	})
}
