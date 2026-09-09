package handler

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type workflowInputInstanceResponse struct {
	TemplateVersionID *string         `json:"template_version_id"`
	CreatedByID       *string         `json:"created_by_id"`
	CreatedAt         string          `json:"created_at"`
	ID                string          `json:"id"`
	TemplateID        string          `json:"template_id"`
	Name              string          `json:"name"`
	Input             json.RawMessage `json:"input"`
	ProjectID         *string         `json:"project_id"`
	Revision          int64           `json:"revision"`
	UpdatedAt         string          `json:"updated_at"`
}

func inputInstanceResponse(row db.WorkflowInputInstance) workflowInputInstanceResponse {
	var projectID *string
	if row.ProjectID.Valid {
		id := uuidToString(row.ProjectID)
		projectID = &id
	}
	return workflowInputInstanceResponse{TemplateVersionID: uuidToPtr(row.TemplateVersionID), CreatedByID: uuidToPtr(row.CreatedByID), CreatedAt: timestampToString(row.CreatedAt), ID: uuidToString(row.ID), TemplateID: uuidToString(row.TemplateID), Name: row.Name, Input: row.Input, ProjectID: projectID, Revision: row.Revision, UpdatedAt: timestampToString(row.UpdatedAt)}
}

func (h *Handler) loadInputInstanceTemplate(w http.ResponseWriter, r *http.Request) (db.WorkflowTemplate, bool) {
	tpl, ok := h.loadWorkflowTemplate(w, r)
	if !ok {
		return tpl, false
	}
	_, ok = h.requireWorkspaceMember(w, r, uuidToString(tpl.WorkspaceID), "workflow template not found")
	return tpl, ok
}

func (h *Handler) ListWorkflowInputInstances(w http.ResponseWriter, r *http.Request) {
	tpl, ok := h.loadInputInstanceTemplate(w, r)
	if !ok {
		return
	}
	rows, err := h.Queries.ListWorkflowInputInstances(r.Context(), db.ListWorkflowInputInstancesParams{WorkspaceID: tpl.WorkspaceID, TemplateID: tpl.ID})
	if err != nil {
		writeError(w, 500, "failed to list input instances")
		return
	}
	instances := make([]workflowInputInstanceResponse, 0, len(rows))
	for _, row := range rows {
		instances = append(instances, inputInstanceResponse(row))
	}
	writeJSON(w, 200, map[string]any{"instances": instances})
}

// Saving an instance does not start a run. Inputs are revalidated against the
// currently published graph when the user later runs the selected instance.
func (h *Handler) SaveWorkflowInputInstance(w http.ResponseWriter, r *http.Request) {
	tpl, ok := h.loadInputInstanceTemplate(w, r)
	if !ok {
		return
	}
	if tpl.Status == "archived" {
		writeError(w, 409, "archived workflow cannot save input instances")
		return
	}
	var req struct {
		TemplateVersionID *string            `json:"template_version_id"`
		Name              string             `json:"name"`
		Input             map[string]*string `json:"input"`
		ProjectID         *string            `json:"project_id"`
		Revision          int64              `json:"revision"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		writeError(w, 400, "invalid input instance body")
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeError(w, 400, "invalid input instance body")
		return
	}
	for _, value := range req.Input {
		if value == nil {
			writeError(w, 400, "input values must be strings; null is not supported")
			return
		}
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || utf8.RuneCountInString(req.Name) > 100 || req.Input == nil {
		writeError(w, 400, "name must be 1 to 100 characters and input must be a string-valued object")
		return
	}
	input, err := json.Marshal(req.Input)
	if err != nil {
		writeError(w, 400, "invalid input")
		return
	}
	params := db.CreateWorkflowInputInstanceParams{WorkspaceID: tpl.WorkspaceID, TemplateID: tpl.ID, Name: req.Name, Input: input}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	creatorID, ok := parseUUIDOrBadRequest(w, userID, "user id")
	if !ok {
		return
	}
	params.CreatedByID = creatorID
	if req.TemplateVersionID != nil && *req.TemplateVersionID != "" {
		versionID, ok := parseUUIDOrBadRequest(w, *req.TemplateVersionID, "template version id")
		if !ok {
			return
		}
		version, err := h.Queries.GetWorkflowTemplateVersion(r.Context(), db.GetWorkflowTemplateVersionParams{ID: versionID, WorkspaceID: tpl.WorkspaceID})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(w, 404, "template version not found")
			} else {
				writeError(w, 500, "failed to read template version")
			}
			return
		}
		if version.TemplateID != tpl.ID || version.Status != "published" {
			writeError(w, 409, "input instances must reference a published version of this workflow")
			return
		}
		params.TemplateVersionID = version.ID
	}
	if req.ProjectID != nil && *req.ProjectID != "" {
		projectID, ok := parseUUIDOrBadRequest(w, *req.ProjectID, "project id")
		if !ok {
			return
		}
		if _, err := h.Queries.GetProjectInWorkspace(r.Context(), db.GetProjectInWorkspaceParams{ID: projectID, WorkspaceID: tpl.WorkspaceID}); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(w, 404, "project not found")
			} else {
				writeError(w, 500, "failed to read project")
			}
			return
		}
		params.ProjectID = projectID
	}
	var row db.WorkflowInputInstance
	status := http.StatusCreated
	if rawID := chi.URLParam(r, "instanceID"); rawID != "" {
		id, ok := parseUUIDOrBadRequest(w, rawID, "input instance id")
		if !ok {
			return
		}
		if req.Revision < 1 {
			writeError(w, 400, "revision is required for updates")
			return
		}
		row, err = h.Queries.UpdateWorkflowInputInstance(r.Context(), db.UpdateWorkflowInputInstanceParams{ID: id, WorkspaceID: tpl.WorkspaceID, TemplateID: tpl.ID, Name: req.Name, Input: input, ProjectID: params.ProjectID, TemplateVersionID: params.TemplateVersionID, ExpectedRevision: req.Revision})
		status = http.StatusOK
	} else {
		row, err = h.Queries.CreateWorkflowInputInstance(r.Context(), params)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 409, "input instance changed or no longer exists; reload before saving")
		return
	}
	if err != nil {
		writeError(w, 500, "failed to save input instance")
		return
	}
	writeJSON(w, status, inputInstanceResponse(row))
}

func (h *Handler) DeleteWorkflowInputInstance(w http.ResponseWriter, r *http.Request) {
	tpl, ok := h.loadInputInstanceTemplate(w, r)
	if !ok {
		return
	}
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "instanceID"), "input instance id")
	if !ok {
		return
	}
	count, err := h.Queries.DeleteWorkflowInputInstance(r.Context(), db.DeleteWorkflowInputInstanceParams{ID: id, WorkspaceID: tpl.WorkspaceID, TemplateID: tpl.ID})
	if err != nil {
		writeError(w, 500, "failed to delete input instance")
		return
	}
	if count == 0 {
		writeError(w, 404, "input instance not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
