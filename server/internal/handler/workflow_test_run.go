package handler

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/workflow"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"io"
	"net/http"
	"strconv"
	"strings"
)

type workflowExecutionRef struct {
	Kind               string  `json:"kind"`
	SnapshotID         string  `json:"snapshot_id"`
	DefinitionHash     string  `json:"definition_hash"`
	GraphSchemaVersion int32   `json:"graph_schema_version"`
	BaseRevision       int64   `json:"base_revision"`
	BaseDraftVersionID *string `json:"base_draft_version_id"`
}

func (h *Handler) debugIdentity(w http.ResponseWriter, r *http.Request, edit bool) (pgtype.UUID, pgtype.UUID, bool) {
	ws, ok := parseUUIDOrBadRequest(w, h.resolveWorkspaceID(r), "workspace id")
	if !ok {
		return ws, pgtype.UUID{}, false
	}
	if edit {
		if _, ok = h.requireWorkspaceRole(w, r, uuidToString(ws), "workspace not found", "owner", "admin"); !ok {
			return ws, pgtype.UUID{}, false
		}
	} else {
		if _, ok = h.requireWorkspaceMember(w, r, uuidToString(ws), "workspace not found"); !ok {
			return ws, pgtype.UUID{}, false
		}
	}
	user, ok := requireUserID(w, r)
	if !ok {
		return ws, pgtype.UUID{}, false
	}
	id, ok := parseUUIDOrBadRequest(w, user, "user id")
	return ws, id, ok
}
func decodeDebugRequest(w http.ResponseWriter, r *http.Request, value any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 512*1024)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, 413, "draft trial request is too large")
		} else {
			writeError(w, 400, "invalid or unknown draft trial fields")
		}
		return false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeError(w, 400, "request must contain one JSON object")
		return false
	}
	return true
}
func (h *Handler) writeDebugError(w http.ResponseWriter, r *http.Request, err error) {
	var quota *workflow.DebugQuotaError
	if errors.As(err, &quota) {
		if quota.RetryAfterSeconds > 0 {
			w.Header().Set("Retry-After", strconv.FormatInt(quota.RetryAfterSeconds, 10))
		}
		writeJSON(w, 429, quota)
		return
	}
	var eng *workflow.EngineError
	if errors.As(err, &eng) {
		status := 0
		switch eng.Code {
		case "debug_bad_request":
			status = 400
		case "debug_forbidden":
			status = 403
		case "debug_details_expired":
			status = 410
		case "debug_template_changed", "debug_policy_changed", "debug_receipt_conflict", "debug_delivery_incomplete":
			status = 409
		case "debug_invalid_policy":
			status = 422
		case "debug_payload_too_large":
			status = 413
		case "debug_unavailable":
			status = 503
		}
		if status != 0 {
			writeJSON(w, status, map[string]any{"error": eng.Message, "code": eng.Code})
			return
		}
	}
	h.writeWorkflowEngineError(w, r, err, "draft trial")
}
func (h *Handler) StartWorkflowTestRun(w http.ResponseWriter, r *http.Request) {
	ws, user, ok := h.debugIdentity(w, r, false)
	if !ok {
		return
	}
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "workflow template id")
	if !ok {
		return
	}
	engine, ok := h.requireWorkflowEngine(w)
	if !ok {
		return
	}
	var req workflow.DraftTestRequest
	if !decodeDebugRequest(w, r, &req) {
		return
	}
	result, err := engine.StartDraftTest(r.Context(), ws, user, id, req)
	if err != nil {
		var validation *workflow.ValidationErrors
		if errors.As(err, &validation) {
			definition, _ := workflow.ParseDefinition(req.Definition)
			writeJSON(w, 422, workflowValidationResponse{Error: "draft snapshot is not a valid graph", Messages: validation.Messages(), Diagnostics: workflowValidationDiagnostics(err, definition)})
			return
		}
		h.writeDebugError(w, r, err)
		return
	}
	status := http.StatusCreated
	if result.AlreadyExisted {
		status = http.StatusOK
	}
	h.writeWorkflowTestRun(w, r, result.Run, status, !result.AlreadyExisted)
}
func (h *Handler) debugPolicy(ctx context.Context, ws pgtype.UUID) (workflow.DebugPolicy, int64, error) {
	row, err := h.Queries.GetWorkflowDebugPolicy(ctx, ws)
	if errors.Is(err, pgx.ErrNoRows) {
		return workflow.DefaultDebugPolicy(), 1, nil
	}
	return workflow.DebugPolicyFromRow(row), row.Revision, err
}
func (h *Handler) GetWorkflowTestSettings(w http.ResponseWriter, r *http.Request) {
	ws, _, ok := h.debugIdentity(w, r, true)
	if !ok {
		return
	}
	p, revision, err := h.debugPolicy(r.Context(), ws)
	if err != nil {
		h.writeDebugError(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"schema_version": "1", "revision": revision, "settings": p})
}
func (h *Handler) UpdateWorkflowTestSettings(w http.ResponseWriter, r *http.Request) {
	ws, user, ok := h.debugIdentity(w, r, true)
	if !ok {
		return
	}
	engine, ok := h.requireWorkflowEngine(w)
	if !ok {
		return
	}
	var req struct {
		workflow.DebugPolicy
		ExpectedRevision int64 `json:"expected_revision"`
	}
	var fields map[string]json.RawMessage
	if !decodeDebugRequest(w, r, &fields) {
		return
	}
	required := []string{"enabled", "user_active_runs", "workspace_active_runs", "user_starts_per_hour", "max_duration_seconds", "retention_seconds", "payload_capacity_bytes", "expected_revision"}
	if len(fields) != len(required) {
		writeError(w, 400, "provide the complete trial settings")
		return
	}
	for _, key := range required {
		if len(fields[key]) == 0 || string(fields[key]) == "null" {
			writeError(w, 400, "provide the complete trial settings")
			return
		}
	}
	raw, _ := json.Marshal(fields)
	if err := json.Unmarshal(raw, &req); err != nil {
		writeError(w, 400, "invalid trial settings")
		return
	}
	out, err := engine.UpdateDebugPolicy(r.Context(), ws, user, req.ExpectedRevision, req.DebugPolicy)
	if err != nil {
		h.writeDebugError(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"schema_version": "1", "revision": out.Revision, "settings": workflow.DebugPolicyFromRow(out)})
}
func (h *Handler) GetWorkflowTestCapabilities(w http.ResponseWriter, r *http.Request) {
	ws, user, ok := h.debugIdentity(w, r, false)
	if !ok {
		return
	}
	p, revision, err := h.debugPolicy(r.Context(), ws)
	if err != nil {
		h.writeDebugError(w, r, err)
		return
	}
	usage, err := h.Queries.GetWorkflowDebugUsage(r.Context(), db.GetWorkflowDebugUsageParams{WorkspaceID: ws, UserID: user})
	if err != nil {
		h.writeDebugError(w, r, err)
		return
	}
	var bytes int64
	quota, err := h.Queries.GetWorkflowDebugQuota(r.Context(), ws)
	if err == nil {
		bytes = quota.PayloadBytes
	} else if !errors.Is(err, pgx.ErrNoRows) {
		h.writeDebugError(w, r, err)
		return
	}
	enabled := h.WorkflowEngine != nil && h.WorkflowEngine.DebugReady && p.Enabled
	writeJSON(w, 200, map[string]any{"schema_version": "1", "enabled": enabled, "debug_policy_revision": revision, "settings": p, "usage": usage, "payload_bytes": bytes, "default_limits": workflow.DefaultLimits, "duration_ceiling_seconds": workflow.DefaultWorkspacePolicy.MaxDurationSeconds})
}
func (h *Handler) loadWorkflowTestRun(w http.ResponseWriter, r *http.Request) (db.WorkflowRun, bool) {
	ws, _, ok := h.debugIdentity(w, r, false)
	if !ok {
		return db.WorkflowRun{}, false
	}
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "workflow test run id")
	if !ok {
		return db.WorkflowRun{}, false
	}
	run, err := h.Queries.GetWorkflowRun(r.Context(), db.GetWorkflowRunParams{ID: id, WorkspaceID: ws})
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && run.ExecutionMode != workflow.ExecutionDraftTest) {
		writeError(w, 404, "draft trial not found")
		return db.WorkflowRun{}, false
	}
	if err != nil {
		h.writeDebugError(w, r, err)
		return db.WorkflowRun{}, false
	}
	return run, true
}
func (h *Handler) executionRef(ctx context.Context, run db.WorkflowRun) (workflowExecutionRef, error) {
	s, err := h.Queries.GetWorkflowExecutionSnapshot(ctx, db.GetWorkflowExecutionSnapshotParams{ID: run.ExecutionSnapshotID, WorkspaceID: run.WorkspaceID})
	if err != nil {
		return workflowExecutionRef{}, err
	}
	var base *string
	if s.BaseDraftVersionID.Valid {
		value := uuidToString(s.BaseDraftVersionID)
		base = &value
	}
	return workflowExecutionRef{workflow.ExecutionDraftTest, uuidToString(s.ID), s.DefinitionHash, s.GraphSchemaVersion, s.BaseRevision, base}, nil
}
func (h *Handler) writeWorkflowTestRun(w http.ResponseWriter, r *http.Request, run db.WorkflowRun, status int, created bool) {
	ref, err := h.executionRef(r.Context(), run)
	if err != nil {
		h.writeDebugError(w, r, err)
		return
	}
	detail, err := h.workflowRunDetail(r.Context(), run)
	if err != nil {
		h.writeDebugError(w, r, err)
		return
	}
	writeJSON(w, status, map[string]any{"schema_version": "1", "created": created, "run": detail, "execution_ref": ref,
		"debug": map[string]any{"effective_limits": json.RawMessage(run.Policy), "deadline_at": run.DebugDeadlineAt, "policy_revision": run.DebugPolicyRevision, "retention_seconds": run.DebugRetentionSeconds, "purge_after": run.DebugPurgeAfter, "payload_bytes": run.DebugPayloadBytes, "cleanup_state": run.DebugCleanupState, "details_purged_at": run.DetailsPurgedAt, "purge_completed_at": run.PurgeCompletedAt, "stop_requested_at": run.DebugStopRequestedAt}})
}
func (h *Handler) GetWorkflowTestRun(w http.ResponseWriter, r *http.Request) {
	run, ok := h.loadWorkflowTestRun(w, r)
	if ok {
		h.writeWorkflowTestRun(w, r, run, 200, false)
	}
}
func (h *Handler) GetWorkflowTestRunDefinition(w http.ResponseWriter, r *http.Request) {
	run, ok := h.loadWorkflowTestRun(w, r)
	if !ok {
		return
	}
	if run.DetailsPurgedAt.Valid {
		writeJSON(w, 410, map[string]string{"code": "debug_details_expired", "error": "draft trial details have expired"})
		return
	}
	ref, err := h.executionRef(r.Context(), run)
	if err != nil {
		h.writeDebugError(w, r, err)
		return
	}
	def, err := workflow.ResolveRunDefinition(r.Context(), h.Queries, run.WorkspaceID, run)
	if err != nil {
		h.writeDebugError(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"schema_version": "1", "execution_ref": ref, "definition": def})
}
func (h *Handler) ListWorkflowTestRuns(w http.ResponseWriter, r *http.Request) {
	ws, _, ok := h.debugIdentity(w, r, false)
	if !ok {
		return
	}
	limit := 20
	offset := 0
	if value := r.URL.Query().Get("limit"); value != "" {
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 || n > 100 {
			writeError(w, 400, "invalid limit")
			return
		}
		limit = n
	}
	if value := r.URL.Query().Get("offset"); value != "" {
		n, err := strconv.Atoi(value)
		if err != nil || n < 0 {
			writeError(w, 400, "invalid offset")
			return
		}
		offset = n
	}
	var template pgtype.UUID
	if value := r.URL.Query().Get("template_id"); value != "" {
		template, ok = parseUUIDOrBadRequest(w, value, "template id")
		if !ok {
			return
		}
	}
	runs, err := h.Queries.ListWorkflowDraftTestRuns(r.Context(), db.ListWorkflowDraftTestRunsParams{WorkspaceID: ws, TemplateID: template, LimitCount: int32(limit), OffsetCount: int32(offset)})
	if err != nil {
		h.writeDebugError(w, r, err)
		return
	}
	total, err := h.Queries.CountWorkflowDraftTestRuns(r.Context(), db.CountWorkflowDraftTestRunsParams{WorkspaceID: ws, TemplateID: template})
	if err != nil {
		h.writeDebugError(w, r, err)
		return
	}
	summaries := h.workflowRunSummaries(r.Context(), ws, runs)
	items := make([]any, 0, len(runs))
	for _, run := range runs {
		ref, err := h.executionRef(r.Context(), run)
		if err != nil {
			h.writeDebugError(w, r, err)
			return
		}
		items = append(items, map[string]any{"run": workflowRunToResponse(run, summaries[uuidToString(run.ID)]), "execution_ref": ref, "cleanup_state": run.DebugCleanupState, "details_purged_at": run.DetailsPurgedAt})
	}
	writeJSON(w, 200, map[string]any{"schema_version": "1", "items": items, "total": total})
}
func (h *Handler) CancelWorkflowTestRun(w http.ResponseWriter, r *http.Request) {
	run, ok := h.loadWorkflowTestRun(w, r)
	if !ok {
		return
	}
	if run.DetailsPurgedAt.Valid {
		writeError(w, 410, "draft trial details have expired")
		return
	}
	if workflow.IsTerminalRunStatus(workflow.RunStatus(run.Status)) {
		writeError(w, 409, "draft trial is already terminal")
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
	if _, err := engine.CancelRun(r.Context(), run.WorkspaceID, run.ID, userID); err != nil {
		h.writeDebugError(w, r, err)
		return
	}
	if err := engine.MaintainDebugRun(r.Context(), run.WorkspaceID, run.ID); err != nil {
		h.writeDebugError(w, r, err)
		return
	}
	h.writeWorkflowTestRun(w, r, h.reloadWorkflowRun(r.Context(), run), 200, false)
}

func (h *Handler) DecideWorkflowTestAcceptance(w http.ResponseWriter, r *http.Request) {
	run, ok := h.loadWorkflowTestRun(w, r)
	if !ok {
		return
	}
	if run.DetailsPurgedAt.Valid {
		writeError(w, 410, "draft trial details have expired")
		return
	}
	if workflow.IsTerminalRunStatus(workflow.RunStatus(run.Status)) {
		writeError(w, 409, "draft trial is already terminal")
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
	var req DecideWorkflowAcceptanceRequest
	if !decodeDebugRequest(w, r, &req) {
		return
	}
	pending, err := h.pendingWorkflowAcceptance(r.Context(), run)
	if err != nil {
		h.writeDebugError(w, r, err)
		return
	}
	if pending == nil {
		writeError(w, 409, "draft trial has no pending acceptance")
		return
	}
	_, err = engine.DecideAcceptance(r.Context(), workflow.DecideAcceptanceInput{WorkspaceID: run.WorkspaceID, AcceptanceID: pending.ID, Accept: req.Accept, Reason: sanitizeNullBytes(strings.TrimSpace(req.Reason)), ReworkTarget: strings.TrimSpace(req.ReworkTarget), ReviewerUserID: userID})
	if err != nil {
		h.writeDebugError(w, r, err)
		return
	}
	h.writeWorkflowTestRun(w, r, h.reloadWorkflowRun(r.Context(), run), 200, false)
}
