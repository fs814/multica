package handler

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/workflow"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func decodeInstanceBody(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if d.Decode(v) != nil || d.Decode(&struct{}{}) != io.EOF {
		writeError(w, 400, "invalid instance request")
		return false
	}
	return true
}
func (h *Handler) instanceWorkspace(w http.ResponseWriter, r *http.Request) (pgtype.UUID, bool) {
	ws := h.resolveWorkspaceID(r)
	if _, ok := h.requireWorkspaceMember(w, r, ws, "workspace not found"); !ok {
		return pgtype.UUID{}, false
	}
	return parseUUIDOrBadRequest(w, ws, "workspace id")
}
func (h *Handler) loadManagedInstance(w http.ResponseWriter, r *http.Request) (db.WorkflowInputInstance, bool) {
	var row db.WorkflowInputInstance
	ws, ok := h.instanceWorkspace(w, r)
	if !ok {
		return row, false
	}
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "instanceID"), "instance id")
	if !ok {
		return row, false
	}
	row, err := h.Queries.GetWorkflowInputInstance(r.Context(), db.GetWorkflowInputInstanceParams{WorkspaceID: ws, ID: id})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, 404, "input instance not found")
		} else {
			writeError(w, 500, "failed to read input instance")
		}
		return row, false
	}
	return row, true
}
func instancePage(r *http.Request) (int32, int32) {
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if offset < 0 || offset > 2147483600 {
		offset = 0
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit < 1 || limit > 100 {
		limit = 30
	}
	return int32(offset), int32(limit)
}
func (h *Handler) BrowseWorkflowInstances(w http.ResponseWriter, r *http.Request) {
	ws, ok := h.instanceWorkspace(w, r)
	if !ok {
		return
	}
	var tpl pgtype.UUID
	if raw := r.URL.Query().Get("template_id"); raw != "" {
		tpl, ok = parseUUIDOrBadRequest(w, raw, "template id")
		if !ok {
			return
		}
	}
	offset, limit := instancePage(r)
	archived := r.URL.Query().Get("include_archived") == "true"
	search := strings.TrimSpace(r.URL.Query().Get("search"))
	rows, err := h.Queries.BrowseWorkflowInputInstances(r.Context(), db.BrowseWorkflowInputInstancesParams{WorkspaceID: ws, TemplateID: tpl, IncludeArchived: archived, Search: search, OffsetCount: offset, LimitCount: limit})
	if err != nil {
		writeError(w, 500, "failed to list instances")
		return
	}
	total, err := h.Queries.CountWorkflowInputInstances(r.Context(), db.CountWorkflowInputInstancesParams{WorkspaceID: ws, TemplateID: tpl, IncludeArchived: archived, Search: search})
	if err != nil {
		writeError(w, 500, "failed to count instances")
		return
	}
	out := make([]workflowInputInstanceResponse, 0, len(rows))
	for _, row := range rows {
		response := inputInstanceResponse(row.WorkflowInputInstance)
		response.TemplateName, response.VersionNumber, response.EditorName = row.TemplateName, row.VersionNumber, row.EditorName
		response.LatestRunID, response.LatestRunStatus = row.LatestRunID, row.LatestRunStatus
		out = append(out, response)
	}
	writeJSON(w, 200, map[string]any{"instances": out, "total": total})
}
func (h *Handler) GetWorkflowInstance(w http.ResponseWriter, r *http.Request) {
	row, ok := h.loadManagedInstance(w, r)
	if ok {
		writeJSON(w, 200, inputInstanceResponse(row))
	}
}
func (h *Handler) ArchiveWorkflowInstance(w http.ResponseWriter, r *http.Request) {
	row, ok := h.loadManagedInstance(w, r)
	if !ok {
		return
	}
	var req struct {
		Revision int64 `json:"revision"`
		Archive  bool  `json:"archive"`
	}
	if !decodeInstanceBody(w, r, &req) {
		return
	}
	user, ok := requireUserID(w, r)
	if !ok {
		return
	}
	userID, ok := parseUUIDOrBadRequest(w, user, "user id")
	if !ok {
		return
	}
	updated, err := h.Queries.ArchiveWorkflowInputInstance(r.Context(), db.ArchiveWorkflowInputInstanceParams{WorkspaceID: row.WorkspaceID, ID: row.ID, ExpectedRevision: req.Revision, Archive: req.Archive, UpdatedByID: userID})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 409, "instance changed; reload before archiving or restoring")
		return
	}
	if err != nil {
		writeError(w, 500, "failed to change archive state")
		return
	}
	writeJSON(w, 200, inputInstanceResponse(updated))
}
func (h *Handler) GetWorkflowInstanceVersion(w http.ResponseWriter, r *http.Request) {
	tpl, ok := h.loadInputInstanceTemplate(w, r)
	if !ok {
		return
	}
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "versionID"), "version id")
	if !ok {
		return
	}
	v, err := h.Queries.GetWorkflowTemplateVersion(r.Context(), db.GetWorkflowTemplateVersionParams{WorkspaceID: tpl.WorkspaceID, ID: id})
	if err != nil || v.TemplateID != tpl.ID || v.Status != "published" {
		writeError(w, 404, "published version not found")
		return
	}
	writeJSON(w, 200, map[string]any{"id": uuidToString(v.ID), "version": v.Version, "definition": json.RawMessage(v.Definition)})
}
func (h *Handler) ValidateWorkflowInstance(w http.ResponseWriter, r *http.Request) {
	row, ok := h.loadManagedInstance(w, r)
	if !ok {
		return
	}
	problems := []string{}
	tpl, err := h.Queries.GetWorkflowTemplate(r.Context(), db.GetWorkflowTemplateParams{WorkspaceID: row.WorkspaceID, ID: row.TemplateID})
	if row.ArchivedAt.Valid {
		problems = append(problems, "instance is archived")
	}
	if err != nil || tpl.Status == "archived" {
		problems = append(problems, "workflow is unavailable or archived")
	}
	if !row.TemplateVersionID.Valid {
		problems = append(problems, "instance needs a published version binding")
	} else {
		v, err := h.Queries.GetWorkflowTemplateVersion(r.Context(), db.GetWorkflowTemplateVersionParams{WorkspaceID: row.WorkspaceID, ID: row.TemplateVersionID})
		if err != nil || v.TemplateID != row.TemplateID || v.Status != "published" {
			problems = append(problems, "pinned version is unavailable")
		} else {
			if err := workflow.ValidateInstanceInput(r.Context(), h.Queries, row.WorkspaceID, v, row.Input, row.ProjectID, textToPtr(row.ImageAttachmentID)); err != nil {
				problems = append(problems, err.Error())
			}
		}
	}
	writeJSON(w, 200, map[string]any{"ready": len(problems) == 0, "problems": problems, "revision": row.Revision})
}
func (h *Handler) ListWorkflowInstanceRuns(w http.ResponseWriter, r *http.Request) {
	row, ok := h.loadManagedInstance(w, r)
	if !ok {
		return
	}
	offset, limit := instancePage(r)
	runs, err := h.Queries.ListWorkflowInstanceRuns(r.Context(), db.ListWorkflowInstanceRunsParams{WorkspaceID: row.WorkspaceID, InputInstanceID: row.ID, OffsetCount: offset, LimitCount: limit})
	if err != nil {
		writeError(w, 500, "failed to list instance runs")
		return
	}
	total, err := h.Queries.CountWorkflowInstanceRuns(r.Context(), db.CountWorkflowInstanceRunsParams{WorkspaceID: row.WorkspaceID, InputInstanceID: row.ID})
	if err != nil {
		writeError(w, 500, "failed to count instance runs")
		return
	}
	out := make([]WorkflowRunResponse, 0, len(runs))
	for _, run := range runs {
		out = append(out, workflowRunToResponse(run, runSummary{}))
	}
	writeJSON(w, 200, map[string]any{"runs": out, "total": total})
}
func (h *Handler) RunWorkflowInstance(w http.ResponseWriter, r *http.Request) {
	row, ok := h.loadManagedInstance(w, r)
	if !ok {
		return
	}
	engine, ok := h.requireWorkflowEngine(w)
	if !ok {
		return
	}
	user, ok := requireUserID(w, r)
	if !ok {
		return
	}
	userID, ok := parseUUIDOrBadRequest(w, user, "user id")
	if !ok {
		return
	}
	var req struct {
		Revision          int64              `json:"revision"`
		Mode              string             `json:"mode"`
		Input             map[string]*string `json:"input,omitempty"`
		ProjectID         *string            `json:"project_id,omitempty"`
		ImageAttachmentID *string            `json:"image_attachment_id,omitempty"`
		HistoryRunID      *string            `json:"history_run_id,omitempty"`
		IdempotencyKey    string             `json:"idempotency_key"`
	}
	if !decodeInstanceBody(w, r, &req) {
		return
	}
	if req.Revision < 1 || req.IdempotencyKey == "" || len(req.IdempotencyKey) > 200 {
		writeError(w, 400, "revision and idempotency_key are required")
		return
	}
	if req.Mode == "temporary" && req.Input == nil {
		writeError(w, 400, "temporary runs require the complete input snapshot")
		return
	}
	if req.Mode != "temporary" && (req.Input != nil || req.ProjectID != nil || req.ImageAttachmentID != nil) {
		writeError(w, 400, "saved and history runs cannot override input")
		return
	}
	intent := &workflow.InstanceStart{ID: row.ID, Revision: req.Revision, Mode: req.Mode, ImageAttachmentID: req.ImageAttachmentID}
	if req.ProjectID != nil && *req.ProjectID != "" {
		intent.ProjectID, ok = parseUUIDOrBadRequest(w, *req.ProjectID, "project id")
		if !ok {
			return
		}
	}
	if req.HistoryRunID != nil {
		intent.HistoryRunID, ok = parseUUIDOrBadRequest(w, *req.HistoryRunID, "history run id")
		if !ok {
			return
		}
	}
	raw, _ := json.Marshal(struct {
		Instance string
		User     string
		Body     any
	}{uuidToString(row.ID), user, req})
	sum := sha256.Sum256(raw)
	input, _ := json.Marshal(req.Input)
	started, err := engine.StartRun(r.Context(), workflow.StartRunInput{
		WorkspaceID: row.WorkspaceID, TemplateID: row.TemplateID, Instance: intent, Source: "manual",
		IdempotencyKey: "workflow-instance:v1:" + req.IdempotencyKey, RequestHash: hex.EncodeToString(sum[:]),
		AccountableUserID: userID, Input: input, ActorType: "member", ActorID: userID,
		Issue: &workflow.RunIssueInput{CreatorType: "member", CreatorID: userID, Subscribers: []workflow.RunIssueSubscriber{{UserType: "member", UserID: userID, Reason: "manual"}}},
	})
	if err != nil {
		h.writeWorkflowEngineError(w, r, err, "RunWorkflowInstance")
		return
	}
	status := http.StatusCreated
	if started.AlreadyExisted {
		status = http.StatusOK
	} else if started.Issue != nil {
		issue := *started.Issue
		if fresh, err := h.Queries.GetIssueInWorkspace(r.Context(), db.GetIssueInWorkspaceParams{ID: issue.ID, WorkspaceID: row.WorkspaceID}); err == nil {
			issue = fresh
		}
		h.publish(protocol.EventIssueCreated, uuidToString(row.WorkspaceID), "member", user, map[string]any{"issue": issueToResponse(issue, h.getIssuePrefix(r.Context(), row.WorkspaceID))})
	}
	h.writeWorkflowRunDetail(w, r, h.reloadWorkflowRun(r.Context(), started.Run), status)
}
