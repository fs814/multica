package handler

import (
	"encoding/json"
	"github.com/multica-ai/multica/server/internal/projectmemory"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"net/http"
)

func (h *Handler) attachProjectMemory(r *http.Request, t *db.AgentTaskQueue, rt db.AgentRuntime, resp *AgentTaskResponse) error {
	// Old sessions lack a frozen project scope. Start fresh until scoped provider adapters prove reuse.
	resp.PriorSessionID = ""
	resp.PriorWorkDir = ""
	if resp.ProjectID == "" {
		return nil
	}
	c := projectmemory.Context{WorkspaceID: uuidToString(rt.WorkspaceID), ProjectID: resp.ProjectID}
	switch {
	case t.IssueID.Valid:
		c.ScopeKind = "issue"
		c.ScopeID = uuidToString(t.IssueID)
	case t.ChatSessionID.Valid:
		c.ScopeKind = "chat_session"
		c.ScopeID = uuidToString(t.ChatSessionID)
	default:
		c.ScopeKind = "task"
		c.ScopeID = uuidToString(t.ID)
	}
	b := projectmemory.Binding{WorkspaceID: c.WorkspaceID, ProjectID: c.ProjectID, OwnerDaemonID: rt.DaemonID.String, Backend: "managed", Revision: 1, State: "pending"}
	for _, res := range resp.ProjectResources {
		if res.ResourceType == "local_directory" {
			var ref struct {
				DaemonID  string `json:"daemon_id"`
				LocalPath string `json:"local_path"`
			}
			if json.Unmarshal(res.ResourceRef, &ref) == nil {
				b.OwnerDaemonID = ref.DaemonID
				b.Backend = "source"
				b.SourceRoot = ref.LocalPath
				break
			}
		}
	}
	if err := projectmemory.ValidateBinding(b); err != nil {
		return err
	}
	frozen, b, err := h.memoryCoordinator().Claim(r.Context(), uuidToString(t.ID), c, b)
	if err != nil {
		return err
	}
	resp.ProjectMemory = &frozen
	resp.ProjectMemoryBinding = &b
	return nil
}
