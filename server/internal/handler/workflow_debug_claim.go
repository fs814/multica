package handler

import (
	"context"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/workflow"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"net/http"
	"time"
)

// ClaimWorkflowDebugTask is a separate execution-rights entry so an old daemon
// can never obtain a debug task through the ordinary queue/reclaim SQL.
func (h *Handler) ClaimWorkflowDebugTask(w http.ResponseWriter, r *http.Request) {
	runtimeID := chi.URLParam(r, "runtimeId")
	runtime, ok := h.requireDaemonRuntimeAccess(w, r, runtimeID)
	if !ok {
		return
	}
	for _, capability := range []string{workflow.DebugStopReceiptCapability, workflow.DebugFixedEnvironmentCapability} {
		if !requestHasClientCapability(r, capability) || !runtimeHasCapability(runtime.Metadata, capability) {
			writeError(w, 503, "runtime does not support draft trial delivery")
			return
		}
	}
	engine, ok := h.requireWorkflowEngine(w)
	if !ok {
		return
	}
	var req struct {
		DaemonIncarnationID string `json:"daemon_incarnation_id"`
	}
	if !decodeDebugRequest(w, r, &req) {
		return
	}
	incarnation, ok := parseUUIDOrBadRequest(w, req.DaemonIncarnationID, "daemon incarnation id")
	if !ok {
		return
	}
	candidates, err := h.Queries.ListWorkflowDebugClaimCandidates(r.Context(), runtime.ID)
	if err != nil {
		h.writeDebugError(w, r, err)
		return
	}
	for _, task := range candidates {
		resp, _, _, _, failure := h.buildClaimedTaskResponse(r, &task, runtime, runtimeID, uuidToString(runtime.WorkspaceID))
		if failure != nil {
			continue
		}
		if err = h.applyDebugEnvironment(r.Context(), task, &resp); err != nil {
			h.writeDebugError(w, r, err)
			return
		}
		if !runtime.OwnerID.Valid {
			writeError(w, 503, "runtime owner is required")
			return
		}
		token, err := auth.GenerateAgentTaskToken()
		if err != nil {
			h.writeDebugError(w, r, err)
			return
		}
		mcpToken, mcpRows, err := remoteMCPDaemonTokenForClaim(resp, runtime)
		if err != nil {
			h.writeDebugError(w, r, err)
			return
		}
		claim, err := engine.ClaimDebugTask(r.Context(), task.ID, runtime.ID, incarnation, func(ctx context.Context, q *db.Queries, claimed db.AgentTaskQueue) error {
			if _, err := q.CreateTaskToken(ctx, db.CreateTaskTokenParams{ID: dbid.NewV7(), TokenHash: auth.HashToken(token), TaskID: claimed.ID, AgentID: claimed.AgentID, WorkspaceID: runtime.WorkspaceID, UserID: runtime.OwnerID, ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(24 * time.Hour), Valid: true}}); err != nil {
				return err
			}
			for _, row := range mcpRows {
				if _, err := q.CreateDaemonToken(ctx, row); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			if workflow.IsInvalidTransition(err) {
				continue
			}
			h.writeDebugError(w, r, err)
			return
		}
		if !claim.ID.Valid {
			continue
		}
		resp.AuthToken = token
		resp.RemoteMCPDaemonToken = mcpToken
		resp.WorkflowExecutionID = uuidToString(claim.ID)
		resp.WorkflowExecutionMode = workflow.ExecutionDraftTest
		writeJSON(w, 200, map[string]any{"task": resp})
		return
	}
	writeJSON(w, 200, map[string]any{"task": nil})
}
