package handler

import (
	"encoding/json"
	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/projectmemory"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"net/http"
)

func (h *Handler) memoryCoordinator() projectmemory.Coordinator {
	return projectmemory.Coordinator{DB: h.TxStarter}
}
func memoryTask(r *http.Request) (string, bool) {
	switch r.Header.Get("X-Actor-Source") {
	case "":
		return "", true
	case "task_token":
		return r.Header.Get("X-Task-ID"), r.Header.Get("X-Task-ID") != ""
	default:
		return "", false
	}
}
func (h *Handler) memoryProject(w http.ResponseWriter, r *http.Request) (db.Project, string, bool) {
	task, ok := memoryTask(r)
	if !ok {
		writeError(w, 403, "unsupported memory actor")
		return db.Project{}, "", false
	}
	p, ok := h.loadProjectForResource(w, r, chi.URLParam(r, "id"))
	return p, task, ok
}
func (h *Handler) ResolveProjectMemory(w http.ResponseWriter, r *http.Request) {
	p, task, ok := h.memoryProject(w, r)
	if !ok {
		return
	}
	b, err := h.memoryCoordinator().Resolve(r.Context(), uuidToString(p.WorkspaceID), uuidToString(p.ID), task)
	if err != nil {
		writeError(w, 409, "memory unavailable: "+err.Error())
		return
	}
	writeJSON(w, 200, b)
}
func (h *Handler) SubmitProjectMemory(w http.ResponseWriter, r *http.Request) {
	p, task, ok := h.memoryProject(w, r)
	if !ok {
		return
	}
	var op projectmemory.Operation
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, projectmemory.MaxBytes*2)).Decode(&op); err != nil {
		writeError(w, 400, "invalid memory operation")
		return
	}
	var initial *projectmemory.Binding
	if op.OwnerRuntimeID != "" {
		if task != "" || op.Action != "init" {
			writeError(w, 403, "only a human may establish the initial binding")
			return
		}
		rt, ok := h.requireDaemonRuntimeAccess(w, r, op.OwnerRuntimeID)
		if !ok {
			return
		}
		if uuidToString(rt.WorkspaceID) != uuidToString(p.WorkspaceID) {
			writeError(w, 403, "owner is outside the project workspace")
			return
		}
		initial = &projectmemory.Binding{WorkspaceID: uuidToString(p.WorkspaceID), ProjectID: uuidToString(p.ID), OwnerDaemonID: rt.DaemonID.String, Backend: "managed", Revision: 1, State: "pending"}
		// Initial source selection is exclusively from registered project resources.
		resources, err := h.Queries.ListProjectResources(r.Context(), p.ID)
		if err != nil {
			writeError(w, 500, "cannot load resources")
			return
		}
		for _, res := range resources {
			if res.ResourceType == "local_directory" {
				var ref struct {
					DaemonID  string `json:"daemon_id"`
					LocalPath string `json:"local_path"`
				}
				if json.Unmarshal(res.ResourceRef, &ref) == nil {
					initial.Backend = "source"
					initial.SourceRoot = ref.LocalPath
					initial.OwnerDaemonID = ref.DaemonID
					break
				}
			}
		}
	}
	work, err := h.memoryCoordinator().Submit(r.Context(), uuidToString(p.WorkspaceID), uuidToString(p.ID), task, op, initial)
	if err != nil {
		writeError(w, 409, err.Error())
		return
	}
	writeJSON(w, 202, work)
}
func (h *Handler) GetProjectMemoryOperation(w http.ResponseWriter, r *http.Request) {
	p, task, ok := h.memoryProject(w, r)
	if !ok {
		return
	}
	receipt, err := h.memoryCoordinator().Receipt(r.Context(), uuidToString(p.WorkspaceID), uuidToString(p.ID), task, chi.URLParam(r, "requestId"))
	if err != nil {
		writeError(w, 409, err.Error())
		return
	}
	writeJSON(w, 200, receipt)
}
func (h *Handler) NextProjectMemoryWork(w http.ResponseWriter, r *http.Request) {
	rt, ok := h.requireProjectMemoryOwner(w, r)
	if !ok {
		return
	}
	work, err := h.memoryCoordinator().Next(r.Context(), uuidToString(rt.WorkspaceID), rt.DaemonID.String)
	if err != nil {
		writeError(w, 500, "cannot claim memory operation")
		return
	}
	writeJSON(w, 200, map[string]any{"work": work})
}
func (h *Handler) CompleteProjectMemoryWork(w http.ResponseWriter, r *http.Request) {
	rt, ok := h.requireProjectMemoryOwner(w, r)
	if !ok {
		return
	}
	var result projectmemory.Result
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, projectmemory.MaxBytes*2)).Decode(&result); err != nil {
		writeError(w, 400, "invalid result")
		return
	}
	if err := h.memoryCoordinator().Complete(r.Context(), uuidToString(rt.WorkspaceID), rt.DaemonID.String, chi.URLParam(r, "requestId"), result); err != nil {
		writeError(w, 409, err.Error())
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// requireProjectMemoryOwner binds machine credentials to the runtime's actual
// daemon. User-token compatibility is restricted to the runtime's human owner;
// workspace membership alone never authorizes owner-side storage operations.
func (h *Handler) requireProjectMemoryOwner(w http.ResponseWriter, r *http.Request) (db.AgentRuntime, bool) {
	rt, ok := h.requireDaemonRuntimeAccess(w, r, chi.URLParam(r, "runtimeId"))
	if !ok {
		return rt, false
	}
	switch middleware.DaemonAuthPathFromContext(r.Context()) {
	case middleware.DaemonAuthPathDaemonToken:
		if middleware.DaemonIDFromContext(r.Context()) == rt.DaemonID.String &&
			middleware.DaemonWorkspaceIDFromContext(r.Context()) == uuidToString(rt.WorkspaceID) {
			return rt, true
		}
	case middleware.DaemonAuthPathPAT, middleware.DaemonAuthPathJWT:
		if rt.OwnerID.Valid && r.Header.Get("X-User-ID") == uuidToString(rt.OwnerID) {
			return rt, true
		}
	}
	writeError(w, http.StatusForbidden, "project memory storage requires the bound daemon or runtime owner")
	return db.AgentRuntime{}, false
}
