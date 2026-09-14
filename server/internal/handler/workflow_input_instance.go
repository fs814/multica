package handler

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/workflow"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type workflowInputInstanceResponse struct {
	TemplateName    string `json:"template_name"`
	VersionNumber   int32  `json:"version_number"`
	EditorName      string `json:"editor_name"`
	LatestRunID     string `json:"latest_run_id"`
	LatestRunStatus string `json:"latest_run_status"`

	Description       string          `json:"description"`
	InputNode         json.RawMessage `json:"input_node"`
	ImageAttachmentID *string         `json:"image_attachment_id"`
	UpdatedByID       *string         `json:"updated_by_id"`
	ArchivedAt        *string         `json:"archived_at"`

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
	return workflowInputInstanceResponse{Description: row.Description, InputNode: row.InputNode, ImageAttachmentID: textToPtr(row.ImageAttachmentID), UpdatedByID: uuidToPtr(row.UpdatedByID), ArchivedAt: timestampToPtr(row.ArchivedAt), TemplateVersionID: uuidToPtr(row.TemplateVersionID), CreatedByID: uuidToPtr(row.CreatedByID), CreatedAt: timestampToString(row.CreatedAt), ID: uuidToString(row.ID), TemplateID: uuidToString(row.TemplateID), Name: row.Name, Input: row.Input, ProjectID: projectID, Revision: row.Revision, UpdatedAt: timestampToString(row.UpdatedAt)}
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
// pinned published graph when the user later runs the selected instance.
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
		Description       string             `json:"description"`
		InputNode         *workflow.Node     `json:"input_node"`
		ImageAttachmentID *string            `json:"image_attachment_id"`
		IdempotencyKey    string             `json:"idempotency_key"`
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
		writeError(w, 400, inputInstanceDecodeMessage(err))
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeError(w, 400, "invalid input instance body: expected exactly one JSON object")
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
	params.Description = pgtype.Text{String: req.Description, Valid: true}
	if utf8.RuneCountInString(req.Description) > 20000 || len(req.IdempotencyKey) > 200 {
		writeError(w, 400, "description or idempotency key is too long")
		return
	}
	if req.InputNode != nil {
		if req.InputNode.Type != workflow.NodeTypeInput {
			writeError(w, 400, "input_node must be an input node")
			return
		}
		params.InputNode, _ = json.Marshal(req.InputNode)
	}
	if req.ImageAttachmentID != nil {
		if err := workflow.ValidateInstanceImage(r.Context(), h.Queries, tpl.WorkspaceID, *req.ImageAttachmentID); err != nil {
			h.writeWorkflowEngineError(w, r, err, "ValidateInstanceImage")
			return
		}
		params.ImageAttachmentID = pgtype.Text{String: *req.ImageAttachmentID, Valid: true}
	}
	if req.IdempotencyKey != "" {
		params.IdempotencyKey = pgtype.Text{String: req.IdempotencyKey, Valid: true}
		raw, _ := json.Marshal(struct {
			Template string
			User     string
			Body     any
		}{uuidToString(tpl.ID), userID, req})
		sum := sha256.Sum256(raw)
		params.RequestHash = pgtype.Text{String: hex.EncodeToString(sum[:]), Valid: true}
	}
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
		def, err := workflow.ParseDefinition(version.Definition)
		if err != nil {
			writeError(w, 409, "published input declaration is unavailable")
			return
		}
		entry, _ := def.EntryInputNode()
		if req.InputNode != nil && !workflow.SameInputSchema(req.InputNode, entry) {
			writeError(w, 409, "input declaration differs from the selected published version; save without a version binding or review the upgrade")
			return
		}
		if req.InputNode == nil && entry != nil {
			params.InputNode, _ = json.Marshal(entry)
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
		row, err = h.Queries.UpdateWorkflowInputInstance(r.Context(), db.UpdateWorkflowInputInstanceParams{ID: id, WorkspaceID: tpl.WorkspaceID, TemplateID: tpl.ID, Name: req.Name, Input: input, ProjectID: params.ProjectID, TemplateVersionID: params.TemplateVersionID, ExpectedRevision: req.Revision, Description: params.Description, InputNode: params.InputNode, ImageAttachmentID: params.ImageAttachmentID, UpdatedByID: creatorID})
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
	if chi.URLParam(r, "instanceID") == "" && params.IdempotencyKey.Valid && row.RequestHash != params.RequestHash {
		writeError(w, 409, "idempotency key was already used with different instance data")
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

// Explain schema mismatches without returning input values or the request body.
func inputInstanceDecodeMessage(err error) string {
	const prefix = "invalid input instance body: "
	var sizeErr *http.MaxBytesError
	if errors.As(err, &sizeErr) {
		return prefix + "request exceeds 1 MiB"
	}
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) {
		return prefix + fmt.Sprintf("field %q must be %s", typeErr.Field, typeErr.Type)
	}
	if field, ok := strings.CutPrefix(err.Error(), "json: unknown field "); ok {
		if len(field) > 128 {
			field = field[:128] + "..."
		}
		return prefix + "unrecognized field " + field + "; check the field name and ensure the client and server are up to date"
	}
	return prefix + "expected valid JSON with string-valued input fields"
}
