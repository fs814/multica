package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const issuePoolPolicyColumns = `
	id, autopilot_id, workspace_id, project_id, eligible_statuses, priorities,
	required_label_ids, excluded_label_ids, property_match, inactive_for_days,
	batch_limit, max_in_flight, review_mode, allow_human_assignee,
	require_description, require_acceptance_criteria, priority_weights,
	workflow_input_mapping, created_by_id, created_at, updated_at`

var defaultIssuePoolPriorityWeights = map[string]int{
	"urgent": 400,
	"high":   300,
	"medium": 200,
	"low":    100,
	"none":   0,
}

type issuePoolPolicy struct {
	ID                        pgtype.UUID
	AutopilotID               pgtype.UUID
	WorkspaceID               pgtype.UUID
	ProjectID                 pgtype.UUID
	EligibleStatuses          []string
	Priorities                []string
	RequiredLabelIDs          []pgtype.UUID
	ExcludedLabelIDs          []pgtype.UUID
	PropertyMatch             []byte
	InactiveForDays           int32
	BatchLimit                int32
	MaxInFlight               int32
	ReviewMode                string
	AllowHumanAssignee        bool
	RequireDescription        bool
	RequireAcceptanceCriteria bool
	PriorityWeights           []byte
	WorkflowInputMapping      []byte
	CreatedByID               pgtype.UUID
	CreatedAt                 pgtype.Timestamptz
	UpdatedAt                 pgtype.Timestamptz
}

type IssuePoolPolicyResponse struct {
	ID                        string            `json:"id"`
	AutopilotID               string            `json:"autopilot_id"`
	WorkspaceID               string            `json:"workspace_id"`
	ProjectID                 *string           `json:"project_id"`
	EligibleStatuses          []string          `json:"eligible_statuses"`
	Priorities                []string          `json:"priorities"`
	RequiredLabelIDs          []string          `json:"required_label_ids"`
	ExcludedLabelIDs          []string          `json:"excluded_label_ids"`
	PropertyMatch             map[string]any    `json:"property_match"`
	InactiveForDays           int32             `json:"inactive_for_days"`
	BatchLimit                int32             `json:"batch_limit"`
	MaxInFlight               int32             `json:"max_in_flight"`
	ReviewMode                string            `json:"review_mode"`
	AllowHumanAssignee        bool              `json:"allow_human_assignee"`
	RequireDescription        bool              `json:"require_description"`
	RequireAcceptanceCriteria bool              `json:"require_acceptance_criteria"`
	PriorityWeights           map[string]int    `json:"priority_weights"`
	WorkflowInputMapping      map[string]string `json:"workflow_input_mapping"`
	CreatedByID               string            `json:"created_by_id"`
	CreatedAt                 string            `json:"created_at"`
	UpdatedAt                 string            `json:"updated_at"`
}

type PutIssuePoolPolicyRequest struct {
	EligibleStatuses          []string          `json:"eligible_statuses"`
	Priorities                []string          `json:"priorities"`
	RequiredLabelIDs          []string          `json:"required_label_ids"`
	ExcludedLabelIDs          []string          `json:"excluded_label_ids"`
	PropertyMatch             map[string]any    `json:"property_match"`
	InactiveForDays           *int32            `json:"inactive_for_days"`
	BatchLimit                *int32            `json:"batch_limit"`
	MaxInFlight               *int32            `json:"max_in_flight"`
	ReviewMode                string            `json:"review_mode"`
	AllowHumanAssignee        *bool             `json:"allow_human_assignee"`
	RequireDescription        *bool             `json:"require_description"`
	RequireAcceptanceCriteria *bool             `json:"require_acceptance_criteria"`
	PriorityWeights           map[string]int    `json:"priority_weights"`
	WorkflowInputMapping      map[string]string `json:"workflow_input_mapping"`
}

type issuePoolCandidate struct {
	IssueID       pgtype.UUID
	Title         string
	Number        int32
	Status        string
	Priority      string
	ProjectID     pgtype.UUID
	LastActivity  pgtype.Timestamptz
	UpdatedAt     pgtype.Timestamptz
	Description   pgtype.Text
	Acceptance    []byte
	InactiveDays  int32
	PriorityScore int32
	Score         int32
}

type IssuePoolCandidateResponse struct {
	IssueID      string         `json:"issue_id"`
	Number       int32          `json:"number"`
	Title        string         `json:"title"`
	Status       string         `json:"status"`
	Priority     string         `json:"priority"`
	ProjectID    *string        `json:"project_id"`
	LastActivity string         `json:"last_activity_at"`
	Score        int32          `json:"score"`
	Breakdown    map[string]int `json:"score_breakdown"`
	Reasons      []string       `json:"selection_reasons"`
}

type IssuePoolPreviewResponse struct {
	PolicyID       string                       `json:"policy_id"`
	ReferenceTime  string                       `json:"reference_time"`
	ScannedCount   int                          `json:"scanned_count"`
	EligibleCount  int                          `json:"eligible_count"`
	SelectedCount  int                          `json:"selected_count"`
	Candidates     []IssuePoolCandidateResponse `json:"candidates"`
	ExcludedByRule map[string]int               `json:"excluded_by_rule"`
}

type CreateIssuePoolCycleRequest struct {
	IdempotencyKey string `json:"idempotency_key"`
}

type IssuePoolItemResponse struct {
	ID                  string         `json:"id"`
	IssueID             string         `json:"issue_id"`
	Status              string         `json:"status"`
	Score               int32          `json:"score"`
	ScoreBreakdown      map[string]int `json:"score_breakdown"`
	SelectionReasons    []string       `json:"selection_reasons"`
	IssueSnapshot       map[string]any `json:"issue_snapshot"`
	ClaimToken          string         `json:"claim_token"`
	ReviewerID          *string        `json:"reviewer_id"`
	ReviewReason        *string        `json:"review_reason"`
	ClaimedAt           string         `json:"claimed_at"`
	ReviewedAt          *string        `json:"reviewed_at"`
	WorkflowRunID       *string        `json:"workflow_run_id"`
	DispatchAttempts    int32          `json:"dispatch_attempts"`
	FailureCode         *string        `json:"failure_code"`
	FailureDetail       map[string]any `json:"failure_detail,omitempty"`
	DispatchedAt        *string        `json:"dispatched_at"`
	WaitingAcceptanceAt *string        `json:"waiting_acceptance_at"`
	CompletedAt         *string        `json:"completed_at"`
}

type IssuePoolCycleResponse struct {
	ID                        string                  `json:"id"`
	PolicyID                  string                  `json:"policy_id"`
	AutopilotID               string                  `json:"autopilot_id"`
	WorkspaceID               string                  `json:"workspace_id"`
	ProjectID                 *string                 `json:"project_id"`
	IdempotencyKey            string                  `json:"idempotency_key"`
	Status                    string                  `json:"status"`
	ScannedCount              int32                   `json:"scanned_count"`
	EligibleCount             int32                   `json:"eligible_count"`
	ClaimedCount              int32                   `json:"claimed_count"`
	CreatedAt                 string                  `json:"created_at"`
	UpdatedAt                 string                  `json:"updated_at"`
	ReviewedAt                *string                 `json:"reviewed_at"`
	AutopilotRunID            *string                 `json:"autopilot_run_id"`
	WorkflowTemplateID        *string                 `json:"workflow_template_id"`
	WorkflowTemplateVersionID *string                 `json:"workflow_template_version_id"`
	ApprovedCount             int32                   `json:"approved_count"`
	RejectedCount             int32                   `json:"rejected_count"`
	DispatchedCount           int32                   `json:"dispatched_count"`
	WaitingAcceptanceCount    int32                   `json:"waiting_acceptance_count"`
	CompletedCount            int32                   `json:"completed_count"`
	BlockedCount              int32                   `json:"blocked_count"`
	FailedCount               int32                   `json:"failed_count"`
	DeferredCount             int32                   `json:"deferred_count"`
	CompletedAt               *string                 `json:"completed_at"`
	Items                     []IssuePoolItemResponse `json:"items"`
	Idempotent                bool                    `json:"idempotent_replay,omitempty"`
}

type IssuePoolCycleListResponse struct {
	Cycles   []IssuePoolCycleResponse `json:"cycles"`
	Total    int                      `json:"total"`
	Page     int                      `json:"page"`
	PageSize int                      `json:"page_size"`
}

type ReviewIssuePoolItemsRequest struct {
	Decisions []IssuePoolReviewDecision `json:"decisions"`
}

type IssuePoolReviewDecision struct {
	ItemID   string `json:"item_id"`
	Decision string `json:"decision"`
	Reason   string `json:"reason"`
}

type issuePoolRowScanner interface {
	Scan(dest ...any) error
}

type issuePoolQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func scanIssuePoolPolicy(row issuePoolRowScanner) (issuePoolPolicy, error) {
	var p issuePoolPolicy
	err := row.Scan(
		&p.ID, &p.AutopilotID, &p.WorkspaceID, &p.ProjectID, &p.EligibleStatuses, &p.Priorities,
		&p.RequiredLabelIDs, &p.ExcludedLabelIDs, &p.PropertyMatch, &p.InactiveForDays,
		&p.BatchLimit, &p.MaxInFlight, &p.ReviewMode, &p.AllowHumanAssignee,
		&p.RequireDescription, &p.RequireAcceptanceCriteria, &p.PriorityWeights,
		&p.WorkflowInputMapping, &p.CreatedByID, &p.CreatedAt, &p.UpdatedAt,
	)
	return p, err
}

func loadIssuePoolPolicy(ctx context.Context, q issuePoolQuerier, autopilotID, workspaceID pgtype.UUID) (issuePoolPolicy, error) {
	return scanIssuePoolPolicy(q.QueryRow(ctx, `SELECT `+issuePoolPolicyColumns+`
		FROM issue_pool_policy WHERE autopilot_id = $1 AND workspace_id = $2`, autopilotID, workspaceID))
}

func issuePoolPolicyToResponse(p issuePoolPolicy) IssuePoolPolicyResponse {
	propertyMatch := map[string]any{}
	_ = json.Unmarshal(p.PropertyMatch, &propertyMatch)
	weights := map[string]int{}
	_ = json.Unmarshal(p.PriorityWeights, &weights)
	mapping := map[string]string{}
	_ = json.Unmarshal(p.WorkflowInputMapping, &mapping)
	return IssuePoolPolicyResponse{
		ID:                        uuidToString(p.ID),
		AutopilotID:               uuidToString(p.AutopilotID),
		WorkspaceID:               uuidToString(p.WorkspaceID),
		ProjectID:                 uuidToPtr(p.ProjectID),
		EligibleStatuses:          nonNilStrings(p.EligibleStatuses),
		Priorities:                nonNilStrings(p.Priorities),
		RequiredLabelIDs:          uuidSliceToStrings(p.RequiredLabelIDs),
		ExcludedLabelIDs:          uuidSliceToStrings(p.ExcludedLabelIDs),
		PropertyMatch:             propertyMatch,
		InactiveForDays:           p.InactiveForDays,
		BatchLimit:                p.BatchLimit,
		MaxInFlight:               p.MaxInFlight,
		ReviewMode:                p.ReviewMode,
		AllowHumanAssignee:        p.AllowHumanAssignee,
		RequireDescription:        p.RequireDescription,
		RequireAcceptanceCriteria: p.RequireAcceptanceCriteria,
		PriorityWeights:           weights,
		WorkflowInputMapping:      mapping,
		CreatedByID:               uuidToString(p.CreatedByID),
		CreatedAt:                 timestampToString(p.CreatedAt),
		UpdatedAt:                 timestampToString(p.UpdatedAt),
	}
}

func nonNilStrings(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

func uuidSliceToStrings(ids []pgtype.UUID) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, uuidToString(id))
	}
	return out
}

func (h *Handler) GetIssuePoolPolicy(w http.ResponseWriter, r *http.Request) {
	ap, workspaceID, ok := h.issuePoolAutopilot(w, r, false)
	if !ok {
		return
	}
	p, err := loadIssuePoolPolicy(r.Context(), h.DB, ap.ID, parseUUID(workspaceID))
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "issue pool policy not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load issue pool policy")
		return
	}
	writeJSON(w, http.StatusOK, issuePoolPolicyToResponse(p))
}

func (h *Handler) PutIssuePoolPolicy(w http.ResponseWriter, r *http.Request) {
	ap, workspaceID, ok := h.issuePoolAutopilot(w, r, true)
	if !ok {
		return
	}
	var req PutIssuePoolPolicyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	policy, ok := h.validateIssuePoolPolicy(w, r, req, ap)
	if !ok {
		return
	}
	propertyJSON, _ := json.Marshal(policy.PropertyMatch)
	weightsJSON, _ := json.Marshal(policy.PriorityWeights)
	mappingJSON, _ := json.Marshal(policy.WorkflowInputMapping)
	created := false
	err := h.DB.QueryRow(r.Context(), `
		INSERT INTO issue_pool_policy (
			autopilot_id, workspace_id, project_id, eligible_statuses, priorities,
			required_label_ids, excluded_label_ids, property_match, inactive_for_days,
			batch_limit, max_in_flight, review_mode, allow_human_assignee,
			require_description, require_acceptance_criteria, priority_weights,
			workflow_input_mapping, created_by_id
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8::jsonb,$9,$10,$11,'manual',$12,$13,$14,$15::jsonb,$16::jsonb,$17)
		ON CONFLICT (autopilot_id) DO UPDATE SET
			workspace_id = EXCLUDED.workspace_id,
			project_id = EXCLUDED.project_id,
			eligible_statuses = EXCLUDED.eligible_statuses,
			priorities = EXCLUDED.priorities,
			required_label_ids = EXCLUDED.required_label_ids,
			excluded_label_ids = EXCLUDED.excluded_label_ids,
			property_match = EXCLUDED.property_match,
			inactive_for_days = EXCLUDED.inactive_for_days,
			batch_limit = EXCLUDED.batch_limit,
			max_in_flight = EXCLUDED.max_in_flight,
			review_mode = EXCLUDED.review_mode,
			allow_human_assignee = EXCLUDED.allow_human_assignee,
			require_description = EXCLUDED.require_description,
			require_acceptance_criteria = EXCLUDED.require_acceptance_criteria,
			priority_weights = EXCLUDED.priority_weights,
			workflow_input_mapping = EXCLUDED.workflow_input_mapping,
			updated_at = now()
		RETURNING (xmax = 0), `+issuePoolPolicyColumns,
		ap.ID, ap.WorkspaceID, nullableUUID(ap.ProjectID), policy.EligibleStatuses, policy.Priorities,
		policy.RequiredLabelIDs, policy.ExcludedLabelIDs, string(propertyJSON), policy.InactiveForDays,
		policy.BatchLimit, policy.MaxInFlight, policy.AllowHumanAssignee, policy.RequireDescription,
		policy.RequireAcceptanceCriteria, string(weightsJSON), string(mappingJSON), parseUUID(userID),
	).Scan(
		&created, &policy.ID, &policy.AutopilotID, &policy.WorkspaceID, &policy.ProjectID,
		&policy.EligibleStatuses, &policy.Priorities, &policy.RequiredLabelIDs, &policy.ExcludedLabelIDs,
		&policy.PropertyMatchJSON, &policy.InactiveForDays, &policy.BatchLimit, &policy.MaxInFlight,
		&policy.ReviewMode, &policy.AllowHumanAssignee, &policy.RequireDescription,
		&policy.RequireAcceptanceCriteria, &policy.PriorityWeightsJSON, &policy.WorkflowInputMappingJSON, &policy.CreatedByID,
		&policy.CreatedAt, &policy.UpdatedAt,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save issue pool policy")
		return
	}
	stored := policy.toStored()
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	_ = workspaceID
	writeJSON(w, status, issuePoolPolicyToResponse(stored))
}

type validatedIssuePoolPolicy struct {
	issuePoolPolicy
	PropertyMatch            map[string]any
	PriorityWeights          map[string]int
	PropertyMatchJSON        []byte
	PriorityWeightsJSON      []byte
	WorkflowInputMapping     map[string]string
	WorkflowInputMappingJSON []byte
}

func (p validatedIssuePoolPolicy) toStored() issuePoolPolicy {
	p.issuePoolPolicy.PropertyMatch = p.PropertyMatchJSON
	p.issuePoolPolicy.PriorityWeights = p.PriorityWeightsJSON
	p.issuePoolPolicy.WorkflowInputMapping = p.WorkflowInputMappingJSON
	return p.issuePoolPolicy
}

func (h *Handler) validateIssuePoolPolicy(w http.ResponseWriter, r *http.Request, req PutIssuePoolPolicyRequest, ap db.Autopilot) (validatedIssuePoolPolicy, bool) {
	p := validatedIssuePoolPolicy{}
	p.EligibleStatuses = normalizeIssuePoolStrings(req.EligibleStatuses)
	if len(p.EligibleStatuses) == 0 {
		p.EligibleStatuses = []string{"backlog"}
	}
	if len(p.EligibleStatuses) > 20 || !h.validateIssuePoolStatuses(w, r, ap.WorkspaceID, p.EligibleStatuses) {
		return validatedIssuePoolPolicy{}, false
	}
	p.Priorities = normalizeIssuePoolStrings(req.Priorities)
	validPriorities := map[string]bool{"urgent": true, "high": true, "medium": true, "low": true, "none": true}
	for _, priority := range p.Priorities {
		if !validPriorities[priority] {
			writeError(w, http.StatusBadRequest, "priorities contains an invalid priority")
			return validatedIssuePoolPolicy{}, false
		}
	}
	if len(p.Priorities) > 5 {
		writeError(w, http.StatusBadRequest, "priorities must contain at most 5 values")
		return validatedIssuePoolPolicy{}, false
	}
	var ok bool
	p.RequiredLabelIDs, ok = parseIssuePoolUUIDs(w, req.RequiredLabelIDs, "required_label_ids")
	if !ok {
		return validatedIssuePoolPolicy{}, false
	}
	p.ExcludedLabelIDs, ok = parseIssuePoolUUIDs(w, req.ExcludedLabelIDs, "excluded_label_ids")
	if !ok {
		return validatedIssuePoolPolicy{}, false
	}
	if intersectsUUIDs(p.RequiredLabelIDs, p.ExcludedLabelIDs) {
		writeError(w, http.StatusBadRequest, "the same label cannot be required and excluded")
		return validatedIssuePoolPolicy{}, false
	}
	if !h.validateIssuePoolLabels(w, r, ap.WorkspaceID, append(append([]pgtype.UUID{}, p.RequiredLabelIDs...), p.ExcludedLabelIDs...)) {
		return validatedIssuePoolPolicy{}, false
	}
	p.PropertyMatch = req.PropertyMatch
	if p.PropertyMatch == nil {
		p.PropertyMatch = map[string]any{}
	}
	if !h.validateIssuePoolProperties(w, r, ap.WorkspaceID, p.PropertyMatch) {
		return validatedIssuePoolPolicy{}, false
	}
	p.InactiveForDays = 30
	if req.InactiveForDays != nil {
		p.InactiveForDays = *req.InactiveForDays
	}
	p.BatchLimit = 10
	if req.BatchLimit != nil {
		p.BatchLimit = *req.BatchLimit
	}
	p.MaxInFlight = 10
	if req.MaxInFlight != nil {
		p.MaxInFlight = *req.MaxInFlight
	}
	if p.InactiveForDays < 0 || p.InactiveForDays > 3650 || p.BatchLimit < 1 || p.BatchLimit > 100 || p.MaxInFlight < 1 || p.MaxInFlight > 100 {
		writeError(w, http.StatusBadRequest, "inactive_for_days must be 0..3650 and limits must be 1..100")
		return validatedIssuePoolPolicy{}, false
	}
	if req.ReviewMode != "" && req.ReviewMode != "manual" {
		writeError(w, http.StatusBadRequest, "review_mode must be manual in this phase")
		return validatedIssuePoolPolicy{}, false
	}
	p.ReviewMode = "manual"
	p.AllowHumanAssignee = false
	if req.AllowHumanAssignee != nil {
		p.AllowHumanAssignee = *req.AllowHumanAssignee
	}
	p.RequireDescription = true
	if req.RequireDescription != nil {
		p.RequireDescription = *req.RequireDescription
	}
	p.RequireAcceptanceCriteria = true
	if req.RequireAcceptanceCriteria != nil {
		p.RequireAcceptanceCriteria = *req.RequireAcceptanceCriteria
	}
	p.PriorityWeights = make(map[string]int, len(defaultIssuePoolPriorityWeights))
	for key, value := range defaultIssuePoolPriorityWeights {
		p.PriorityWeights[key] = value
	}
	for key, value := range req.PriorityWeights {
		if !validPriorities[key] || value < -10000 || value > 10000 {
			writeError(w, http.StatusBadRequest, "priority_weights must use known priorities and values between -10000 and 10000")
			return validatedIssuePoolPolicy{}, false
		}
		p.PriorityWeights[key] = value
	}
	p.WorkflowInputMapping = req.WorkflowInputMapping
	if p.WorkflowInputMapping == nil {
		p.WorkflowInputMapping = map[string]string{}
	}
	allowedSources := map[string]bool{
		"title": true, "description": true, "status": true, "priority": true,
		"issue_id": true, "issue_number": true, "acceptance_criteria": true,
	}
	for target, source := range p.WorkflowInputMapping {
		if strings.TrimSpace(target) == "" || !allowedSources[source] {
			writeError(w, http.StatusBadRequest, "workflow_input_mapping must map non-empty input keys to deterministic issue text fields")
			return validatedIssuePoolPolicy{}, false
		}
	}
	return p, true
}

func normalizeIssuePoolStrings(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	sort.Strings(out)
	return out
}

func (h *Handler) validateIssuePoolStatuses(w http.ResponseWriter, r *http.Request, workspaceID pgtype.UUID, statuses []string) bool {
	builtin := map[string]string{
		"backlog": "backlog", "todo": "todo", "in_progress": "in_progress", "in_review": "in_review",
		"done": "done", "blocked": "blocked", "cancelled": "cancelled",
	}
	for _, status := range statuses {
		category, found := builtin[status]
		if !found {
			err := h.DB.QueryRow(r.Context(), `SELECT category FROM issue_status
				WHERE workspace_id = $1 AND key = $2 AND archived_at IS NULL`, workspaceID, status).Scan(&category)
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(w, http.StatusBadRequest, "eligible_statuses contains an unknown or archived status")
				return false
			}
			if err != nil {
				writeError(w, http.StatusInternalServerError, "failed to validate eligible_statuses")
				return false
			}
		}
		if category != "backlog" && category != "todo" && category != "blocked" {
			writeError(w, http.StatusBadRequest, "eligible_statuses may only use backlog, todo, or blocked categories")
			return false
		}
	}
	return true
}

func parseIssuePoolUUIDs(w http.ResponseWriter, values []string, field string) ([]pgtype.UUID, bool) {
	normalized := normalizeIssuePoolStrings(values)
	out := make([]pgtype.UUID, 0, len(normalized))
	for _, value := range normalized {
		id, ok := parseUUIDOrBadRequest(w, value, field)
		if !ok {
			return nil, false
		}
		out = append(out, id)
	}
	return out, true
}

func intersectsUUIDs(a, b []pgtype.UUID) bool {
	set := map[string]bool{}
	for _, id := range a {
		set[uuidToString(id)] = true
	}
	for _, id := range b {
		if set[uuidToString(id)] {
			return true
		}
	}
	return false
}

func (h *Handler) validateIssuePoolLabels(w http.ResponseWriter, r *http.Request, workspaceID pgtype.UUID, ids []pgtype.UUID) bool {
	if len(ids) == 0 {
		return true
	}
	var count int
	if err := h.DB.QueryRow(r.Context(), `SELECT count(*) FROM issue_label WHERE workspace_id = $1 AND id = ANY($2::uuid[])`, workspaceID, ids).Scan(&count); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to validate label filters")
		return false
	}
	if count != len(ids) {
		writeError(w, http.StatusBadRequest, "label filters must reference labels in this workspace")
		return false
	}
	return true
}

func (h *Handler) validateIssuePoolProperties(w http.ResponseWriter, r *http.Request, workspaceID pgtype.UUID, match map[string]any) bool {
	for key := range match {
		id, ok := parseUUIDOrBadRequest(w, key, "property_match key")
		if !ok {
			return false
		}
		var exists bool
		if err := h.DB.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM issue_property WHERE workspace_id = $1 AND id = $2 AND archived_at IS NULL)`, workspaceID, id).Scan(&exists); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to validate property filters")
			return false
		}
		if !exists {
			writeError(w, http.StatusBadRequest, "property_match keys must reference active properties in this workspace")
			return false
		}
	}
	return true
}

func nullableUUID(id pgtype.UUID) any {
	if !id.Valid {
		return nil
	}
	return id
}

func (h *Handler) issuePoolAutopilot(w http.ResponseWriter, r *http.Request, write bool) (db.Autopilot, string, bool) {
	workspaceID := h.resolveWorkspaceID(r)
	ap, ok := h.loadAutopilotInWorkspace(w, r, chi.URLParam(r, "id"), workspaceID)
	if !ok {
		return db.Autopilot{}, "", false
	}
	if ap.ExecutionMode != "issue_pool" {
		writeError(w, http.StatusConflict, "autopilot execution_mode must be issue_pool")
		return db.Autopilot{}, "", false
	}
	if !ap.WorkflowTemplateID.Valid || !ap.WorkflowTemplateVersionID.Valid {
		writeError(w, http.StatusConflict, "issue_pool autopilot requires a fixed published workflow template version")
		return db.Autopilot{}, "", false
	}
	// Upstream MUL-7108 (3ac53a68a) changed requireAutopilotWrite to also
	// return the member it judged, for callers that need to attribute the
	// write. The issue-pool gate only needs the yes/no, so the member is
	// discarded here.
	if write {
		if _, ok := h.requireAutopilotWrite(w, r, ap, workspaceID); !ok {
			return db.Autopilot{}, "", false
		}
	}
	return ap, workspaceID, true
}

func issuePoolPolicyScopeCurrent(p issuePoolPolicy, ap db.Autopilot) bool {
	return uuidToString(p.WorkspaceID) == uuidToString(ap.WorkspaceID) &&
		uuidToString(p.ProjectID) == uuidToString(ap.ProjectID)
}

const issuePoolEvaluatedSQL = `
WITH evaluated AS (
	SELECT i.id, i.title, i.number, i.status, i.priority, i.project_id,
		i.updated_at, i.description, i.acceptance_criteria,
		i.updated_at AS last_activity_at,
		GREATEST(0, floor(extract(epoch FROM ($12::timestamptz - i.updated_at)) / 86400))::int AS inactive_days,
		CASE i.priority WHEN 'urgent' THEN $13::int WHEN 'high' THEN $14::int WHEN 'medium' THEN $15::int WHEN 'low' THEN $16::int ELSE $17::int END AS priority_score,
		CASE
			WHEN NOT (i.status = ANY($3::text[])) THEN 'status_not_eligible'
			WHEN cardinality($4::text[]) > 0 AND NOT (i.priority = ANY($4::text[])) THEN 'priority_not_eligible'
			WHEN i.updated_at > $8::timestamptz THEN 'recently_active'
			WHEN NOT $9::boolean AND i.assignee_type = 'member' THEN 'human_assignee'
			WHEN $10::boolean AND btrim(COALESCE(i.description, '')) = '' THEN 'missing_description'
			WHEN $11::boolean AND COALESCE(i.acceptance_criteria, '[]'::jsonb) = '[]'::jsonb THEN 'missing_acceptance_criteria'
			WHEN cardinality($5::uuid[]) > 0 AND (SELECT count(DISTINCT itl.label_id) FROM issue_to_label itl WHERE itl.issue_id = i.id AND itl.label_id = ANY($5::uuid[])) <> cardinality($5::uuid[]) THEN 'required_label_missing'
			WHEN cardinality($6::uuid[]) > 0 AND EXISTS (SELECT 1 FROM issue_to_label itl WHERE itl.issue_id = i.id AND itl.label_id = ANY($6::uuid[])) THEN 'excluded_label'
			WHEN NOT (i.properties @> $7::jsonb) THEN 'property_mismatch'
			WHEN EXISTS (SELECT 1 FROM agent_task_queue task WHERE task.issue_id = i.id AND task.status IN ('queued','dispatched','running','waiting_local_directory','deferred')) THEN 'active_task'
			WHEN EXISTS (SELECT 1 FROM workflow_run run WHERE run.issue_id = i.id AND run.status IN ('pending','running','waiting_acceptance','blocked')) THEN 'active_workflow'
			WHEN EXISTS (SELECT 1 FROM issue_dependency dep JOIN issue blocker ON blocker.id = dep.depends_on_issue_id WHERE dep.issue_id = i.id AND dep.type = 'blocked_by' AND issue_effective_status(blocker.workspace_id, blocker.status) NOT IN ('done','cancelled')) THEN 'blocking_dependency'
			WHEN EXISTS (SELECT 1 FROM issue_pool_item item WHERE item.issue_id = i.id AND item.status IN ('claimed','approved','dispatching','running','waiting_acceptance','blocked')) THEN 'active_claim'
			ELSE NULL
		END AS exclusion_reason
	FROM issue i
	WHERE i.workspace_id = $1 AND ($2::uuid IS NULL OR i.project_id = $2::uuid)
)
`

func issuePoolQueryArgs(p issuePoolPolicy, reference time.Time) []any {
	weights := map[string]int{}
	_ = json.Unmarshal(p.PriorityWeights, &weights)
	for key, fallback := range defaultIssuePoolPriorityWeights {
		if _, ok := weights[key]; !ok {
			weights[key] = fallback
		}
	}
	propertyMatch := string(p.PropertyMatch)
	if propertyMatch == "" {
		propertyMatch = "{}"
	}
	return []any{
		p.WorkspaceID, nullableUUID(p.ProjectID), p.EligibleStatuses, p.Priorities,
		p.RequiredLabelIDs, p.ExcludedLabelIDs, propertyMatch,
		reference.AddDate(0, 0, -int(p.InactiveForDays)), p.AllowHumanAssignee,
		p.RequireDescription, p.RequireAcceptanceCriteria, reference,
		weights["urgent"], weights["high"], weights["medium"], weights["low"], weights["none"],
	}
}

func queryIssuePoolCandidates(ctx context.Context, q interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, p issuePoolPolicy, reference time.Time, limit int32) ([]issuePoolCandidate, error) {
	args := append(issuePoolQueryArgs(p, reference), limit)
	rows, err := q.Query(ctx, issuePoolEvaluatedSQL+`
		SELECT id, title, number, status, priority, project_id, last_activity_at,
			updated_at, description, acceptance_criteria,
			inactive_days, priority_score, inactive_days + priority_score AS score
		FROM evaluated WHERE exclusion_reason IS NULL
		ORDER BY inactive_days + priority_score DESC, number ASC, id ASC LIMIT $18`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []issuePoolCandidate{}
	for rows.Next() {
		var c issuePoolCandidate
		if err := rows.Scan(&c.IssueID, &c.Title, &c.Number, &c.Status, &c.Priority, &c.ProjectID,
			&c.LastActivity, &c.UpdatedAt, &c.Description, &c.Acceptance,
			&c.InactiveDays, &c.PriorityScore, &c.Score); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func queryIssuePoolSummary(ctx context.Context, q interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, p issuePoolPolicy, reference time.Time) (int, int, map[string]int, error) {
	rows, err := q.Query(ctx, issuePoolEvaluatedSQL+`
		SELECT COALESCE(exclusion_reason, 'eligible'), count(*)::int
		FROM evaluated GROUP BY exclusion_reason ORDER BY COALESCE(exclusion_reason, 'eligible')`, issuePoolQueryArgs(p, reference)...)
	if err != nil {
		return 0, 0, nil, err
	}
	defer rows.Close()
	scanned, eligible := 0, 0
	excluded := map[string]int{}
	for rows.Next() {
		var reason string
		var count int
		if err := rows.Scan(&reason, &count); err != nil {
			return 0, 0, nil, err
		}
		scanned += count
		if reason == "eligible" {
			eligible = count
		} else {
			excluded[reason] = count
		}
	}
	return scanned, eligible, excluded, rows.Err()
}

func candidateToResponse(c issuePoolCandidate) IssuePoolCandidateResponse {
	return IssuePoolCandidateResponse{
		IssueID:      uuidToString(c.IssueID),
		Number:       c.Number,
		Title:        c.Title,
		Status:       c.Status,
		Priority:     c.Priority,
		ProjectID:    uuidToPtr(c.ProjectID),
		LastActivity: timestampToString(c.LastActivity),
		Score:        c.Score,
		Breakdown: map[string]int{
			"inactive_days":   int(c.InactiveDays),
			"priority_weight": int(c.PriorityScore),
			"total":           int(c.Score),
		},
		Reasons: []string{
			"status:" + c.Status,
			fmt.Sprintf("inactive:%dd", c.InactiveDays),
			"priority:" + c.Priority,
		},
	}
}

func (h *Handler) PreviewIssuePool(w http.ResponseWriter, r *http.Request) {
	ap, _, ok := h.issuePoolAutopilot(w, r, false)
	if !ok {
		return
	}
	p, err := loadIssuePoolPolicy(r.Context(), h.DB, ap.ID, ap.WorkspaceID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "issue pool policy not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load issue pool policy")
		return
	}
	if !issuePoolPolicyScopeCurrent(p, ap) {
		writeError(w, http.StatusConflict, "issue pool policy project scope is stale; save the policy again")
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to preview issue pool")
		return
	}
	defer tx.Rollback(r.Context())
	if _, err := tx.Exec(r.Context(), "SET TRANSACTION ISOLATION LEVEL REPEATABLE READ READ ONLY"); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to preview issue pool")
		return
	}
	reference := time.Now().UTC()
	scanned, eligible, excluded, err := queryIssuePoolSummary(r.Context(), tx, p, reference)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to preview issue pool")
		return
	}
	candidates, err := queryIssuePoolCandidates(r.Context(), tx, p, reference, p.BatchLimit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to preview issue pool")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to preview issue pool")
		return
	}
	respCandidates := make([]IssuePoolCandidateResponse, len(candidates))
	for i, candidate := range candidates {
		respCandidates[i] = candidateToResponse(candidate)
	}
	h.IssuePoolMetrics.AddSelection("scanned", scanned)
	h.IssuePoolMetrics.AddSelection("eligible", eligible)
	h.IssuePoolMetrics.AddSelection("selected", len(respCandidates))
	for reason, count := range excluded {
		h.IssuePoolMetrics.AddExclusion(reason, count)
	}
	writeJSON(w, http.StatusOK, IssuePoolPreviewResponse{
		PolicyID: uuidToString(p.ID), ReferenceTime: reference.Format(time.RFC3339Nano),
		ScannedCount: scanned, EligibleCount: eligible, SelectedCount: len(respCandidates),
		Candidates: respCandidates, ExcludedByRule: excluded,
	})
}

func (h *Handler) CreateIssuePoolCycle(w http.ResponseWriter, r *http.Request) {
	ap, _, ok := h.issuePoolAutopilot(w, r, true)
	if !ok {
		return
	}
	var req CreateIssuePoolCycleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.IdempotencyKey = strings.TrimSpace(req.IdempotencyKey)
	if req.IdempotencyKey == "" || len(req.IdempotencyKey) > 200 {
		writeError(w, http.StatusBadRequest, "idempotency_key is required and must not exceed 200 characters")
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create issue pool cycle")
		return
	}
	defer tx.Rollback(r.Context())
	p, err := loadIssuePoolPolicy(r.Context(), tx, ap.ID, ap.WorkspaceID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "issue pool policy not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create issue pool cycle")
		return
	}
	// Lock the autopilot row so a concurrent project retarget cannot move this
	// cycle outside the policy snapshot's scope while candidates are claimed.
	currentAutopilot := ap
	if err := tx.QueryRow(r.Context(), `SELECT project_id FROM autopilot
		WHERE id = $1 AND workspace_id = $2 FOR SHARE`, ap.ID, ap.WorkspaceID).Scan(&currentAutopilot.ProjectID); err != nil {
		writeError(w, http.StatusConflict, "autopilot scope changed while creating issue pool cycle")
		return
	}
	if !issuePoolPolicyScopeCurrent(p, currentAutopilot) {
		writeError(w, http.StatusConflict, "issue pool policy project scope is stale; save the policy again")
		return
	}
	policySnapshot, _ := json.Marshal(issuePoolPolicyToResponse(p))
	var cycleID pgtype.UUID
	err = tx.QueryRow(r.Context(), `
		INSERT INTO issue_pool_cycle (policy_id, autopilot_id, workspace_id, project_id,
			workflow_template_id, workflow_template_version_id, idempotency_key, status,
			policy_snapshot, workflow_input_mapping_snapshot, created_by_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7,'scanning',$8::jsonb,$9::jsonb,$10)
		ON CONFLICT (autopilot_id, idempotency_key) DO NOTHING RETURNING id`,
		p.ID, ap.ID, ap.WorkspaceID, nullableUUID(currentAutopilot.ProjectID),
		ap.WorkflowTemplateID, ap.WorkflowTemplateVersionID, req.IdempotencyKey,
		string(policySnapshot), string(p.WorkflowInputMapping), parseUUID(userID)).Scan(&cycleID)
	if errors.Is(err, pgx.ErrNoRows) {
		if err := tx.Commit(r.Context()); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to replay issue pool cycle")
			return
		}
		resp, err := h.loadIssuePoolCycle(r.Context(), ap.ID, ap.WorkspaceID, pgtype.UUID{}, req.IdempotencyKey)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to replay issue pool cycle")
			return
		}
		resp.Idempotent = true
		writeJSON(w, http.StatusOK, resp)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create issue pool cycle")
		return
	}
	// Serialize capacity accounting per pool. Issue-row locks and the active-item
	// unique index prevent duplicate issues, while this lock separately ensures
	// two cycles cannot both observe the same max_in_flight capacity and claim
	// different issues beyond it.
	if _, err := tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtextextended($1::uuid::text || ':issue_pool_claim', 0))`, ap.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to claim issue pool candidates")
		return
	}
	reference := time.Now().UTC()
	scanned, eligible, _, err := queryIssuePoolSummary(r.Context(), tx, p, reference)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to claim issue pool candidates")
		return
	}
	var active int32
	if err := tx.QueryRow(r.Context(), `SELECT count(*)::int FROM issue_pool_item
		WHERE autopilot_id = $1 AND status IN ('claimed','approved','dispatching','running','waiting_acceptance','blocked')`, ap.ID).Scan(&active); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to claim issue pool candidates")
		return
	}
	capacity := p.MaxInFlight - active
	if capacity > p.BatchLimit {
		capacity = p.BatchLimit
	}
	if capacity < 0 {
		capacity = 0
	}
	candidates := []issuePoolCandidate{}
	if capacity > 0 {
		candidates, err = lockIssuePoolCandidates(r.Context(), tx, p, reference, capacity)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to claim issue pool candidates")
			return
		}
	}
	claimed := int32(0)
	for _, candidate := range candidates {
		response := candidateToResponse(candidate)
		breakdown, _ := json.Marshal(response.Breakdown)
		reasons, _ := json.Marshal(response.Reasons)
		snapshot, _ := json.Marshal(map[string]any{
			"issue_id": response.IssueID, "number": response.Number, "title": response.Title,
			"status": response.Status, "priority": response.Priority, "project_id": response.ProjectID,
			"last_activity_at": response.LastActivity, "updated_at": timestampToString(candidate.UpdatedAt),
			"updated_at_unix_micro": candidate.UpdatedAt.Time.UnixMicro(),
			"description":           candidate.Description.String, "acceptance_criteria": json.RawMessage(candidate.Acceptance),
		})
		var itemID pgtype.UUID
		err := tx.QueryRow(r.Context(), `
			INSERT INTO issue_pool_item (cycle_id, policy_id, autopilot_id, workspace_id,
				project_id, issue_id, status, score, score_breakdown, selection_reasons, issue_snapshot)
			VALUES ($1,$2,$3,$4,$5,$6,'claimed',$7,$8::jsonb,$9::jsonb,$10::jsonb)
			ON CONFLICT DO NOTHING RETURNING id`,
			cycleID, p.ID, ap.ID, ap.WorkspaceID, nullableUUID(candidate.ProjectID), candidate.IssueID,
			candidate.Score, string(breakdown), string(reasons), string(snapshot)).Scan(&itemID)
		if err == nil {
			claimed++
			continue
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusInternalServerError, "failed to claim issue pool candidates")
			return
		}
	}
	status := "awaiting_review"
	if claimed == 0 {
		status = "completed"
	}
	if _, err := tx.Exec(r.Context(), `UPDATE issue_pool_cycle SET status=$2, scanned_count=$3,
		eligible_count=$4, claimed_count=$5, updated_at=now(),
		reviewed_at=CASE WHEN $2='completed' THEN now() ELSE NULL END,
		completed_at=CASE WHEN $2='completed' THEN now() ELSE NULL END WHERE id=$1`,
		cycleID, status, scanned, eligible, claimed); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create issue pool cycle")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create issue pool cycle")
		return
	}
	h.IssuePoolMetrics.AddSelection("scanned", scanned)
	h.IssuePoolMetrics.AddSelection("eligible", eligible)
	h.IssuePoolMetrics.AddSelection("selected", int(claimed))
	h.IssuePoolMetrics.AddItemTransition("claimed", int(claimed))
	for _, candidate := range candidates {
		h.IssuePoolMetrics.ObserveDuration("backlog_age_at_claim", float64(candidate.InactiveDays)*24*60*60)
	}
	resp, err := h.loadIssuePoolCycle(r.Context(), ap.ID, ap.WorkspaceID, cycleID, "")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load issue pool cycle")
		return
	}
	writeJSON(w, http.StatusCreated, resp)
}

const issuePoolEligibilityPredicate = `
	i.workspace_id = $1 AND ($2::uuid IS NULL OR i.project_id = $2::uuid)
	AND i.status = ANY($3::text[])
	AND (cardinality($4::text[]) = 0 OR i.priority = ANY($4::text[]))
	AND i.updated_at <= $8::timestamptz
	AND ($9::boolean OR i.assignee_type IS DISTINCT FROM 'member')
	AND (NOT $10::boolean OR btrim(COALESCE(i.description, '')) <> '')
	AND (NOT $11::boolean OR COALESCE(i.acceptance_criteria, '[]'::jsonb) <> '[]'::jsonb)
	AND (cardinality($5::uuid[]) = 0 OR (SELECT count(DISTINCT itl.label_id) FROM issue_to_label itl WHERE itl.issue_id = i.id AND itl.label_id = ANY($5::uuid[])) = cardinality($5::uuid[]))
	AND (cardinality($6::uuid[]) = 0 OR NOT EXISTS (SELECT 1 FROM issue_to_label itl WHERE itl.issue_id = i.id AND itl.label_id = ANY($6::uuid[])))
	AND i.properties @> $7::jsonb
	AND NOT EXISTS (SELECT 1 FROM agent_task_queue task WHERE task.issue_id = i.id AND task.status IN ('queued','dispatched','running','waiting_local_directory','deferred'))
	AND NOT EXISTS (SELECT 1 FROM workflow_run run WHERE run.issue_id = i.id AND run.status IN ('pending','running','waiting_acceptance','blocked'))
	AND NOT EXISTS (SELECT 1 FROM issue_dependency dep JOIN issue blocker ON blocker.id = dep.depends_on_issue_id WHERE dep.issue_id = i.id AND dep.type = 'blocked_by' AND issue_effective_status(blocker.workspace_id, blocker.status) NOT IN ('done','cancelled'))
	AND NOT EXISTS (SELECT 1 FROM issue_pool_item item WHERE item.issue_id = i.id AND item.status IN ('claimed','approved','dispatching','running','waiting_acceptance','blocked'))`

func lockIssuePoolCandidates(ctx context.Context, tx pgx.Tx, p issuePoolPolicy, reference time.Time, limit int32) ([]issuePoolCandidate, error) {
	args := append(issuePoolQueryArgs(p, reference), limit)
	rows, err := tx.Query(ctx, `SELECT i.id, i.title, i.number, i.status, i.priority, i.project_id,
		i.updated_at,
		i.updated_at, i.description, i.acceptance_criteria,
		GREATEST(0, floor(extract(epoch FROM ($12::timestamptz - i.updated_at)) / 86400))::int AS inactive_days,
		CASE i.priority WHEN 'urgent' THEN $13::int WHEN 'high' THEN $14::int WHEN 'medium' THEN $15::int WHEN 'low' THEN $16::int ELSE $17::int END AS priority_score,
		GREATEST(0, floor(extract(epoch FROM ($12::timestamptz - i.updated_at)) / 86400))::int +
		CASE i.priority WHEN 'urgent' THEN $13::int WHEN 'high' THEN $14::int WHEN 'medium' THEN $15::int WHEN 'low' THEN $16::int ELSE $17::int END AS score
		FROM issue i WHERE `+issuePoolEligibilityPredicate+`
		ORDER BY score DESC, i.number ASC, i.id ASC FOR UPDATE OF i SKIP LOCKED LIMIT $18`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []issuePoolCandidate{}
	for rows.Next() {
		var c issuePoolCandidate
		if err := rows.Scan(&c.IssueID, &c.Title, &c.Number, &c.Status, &c.Priority, &c.ProjectID,
			&c.LastActivity, &c.UpdatedAt, &c.Description, &c.Acceptance,
			&c.InactiveDays, &c.PriorityScore, &c.Score); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (h *Handler) GetIssuePoolCycle(w http.ResponseWriter, r *http.Request) {
	ap, _, ok := h.issuePoolAutopilot(w, r, false)
	if !ok {
		return
	}
	cycleID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "cycleId"), "cycle id")
	if !ok {
		return
	}
	resp, err := h.loadIssuePoolCycle(r.Context(), ap.ID, ap.WorkspaceID, cycleID, "")
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "issue pool cycle not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load issue pool cycle")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) ListIssuePoolCycles(w http.ResponseWriter, r *http.Request) {
	ap, _, ok := h.issuePoolAutopilot(w, r, false)
	if !ok {
		return
	}
	limit, offset := int32(10), int32(0)
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if value, err := strconv.Atoi(raw); err == nil && value > 0 {
			limit = int32(value)
		}
	}
	if limit > 50 {
		limit = 50
	}
	if raw := r.URL.Query().Get("offset"); raw != "" {
		if value, err := strconv.Atoi(raw); err == nil && value >= 0 {
			offset = int32(value)
		}
	}
	if r.URL.Query().Has("page") || r.URL.Query().Has("page_size") {
		page := int32(1)
		limit = 20
		if value, err := strconv.Atoi(r.URL.Query().Get("page_size")); err == nil && value > 0 && value <= 100 {
			limit = int32(value)
		}
		if value, err := strconv.Atoi(r.URL.Query().Get("page")); err == nil && value > 0 && value <= 21474836 {
			page = int32(value)
		}
		offset = (page - 1) * limit
	}
	var total int
	if err := h.DB.QueryRow(r.Context(), `SELECT count(*)::int FROM issue_pool_cycle
		WHERE autopilot_id=$1 AND workspace_id=$2`, ap.ID, ap.WorkspaceID).Scan(&total); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list issue pool cycles")
		return
	}
	rows, err := h.DB.Query(r.Context(), `SELECT id FROM issue_pool_cycle
		WHERE autopilot_id=$1 AND workspace_id=$2 ORDER BY created_at DESC,id DESC LIMIT $3 OFFSET $4`,
		ap.ID, ap.WorkspaceID, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list issue pool cycles")
		return
	}
	ids := []pgtype.UUID{}
	for rows.Next() {
		var id pgtype.UUID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			writeError(w, http.StatusInternalServerError, "failed to list issue pool cycles")
			return
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		writeError(w, http.StatusInternalServerError, "failed to list issue pool cycles")
		return
	}
	rows.Close()
	cycles := make([]IssuePoolCycleResponse, 0, len(ids))
	for _, id := range ids {
		cycle, err := h.loadIssuePoolCycle(r.Context(), ap.ID, ap.WorkspaceID, id, "")
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to list issue pool cycles")
			return
		}
		cycles = append(cycles, cycle)
	}
	writeJSON(w, http.StatusOK, IssuePoolCycleListResponse{Cycles: cycles, Total: total, Page: int(offset/limit) + 1, PageSize: int(limit)})
}

func (h *Handler) loadIssuePoolCycle(ctx context.Context, autopilotID, workspaceID, cycleID pgtype.UUID, idempotencyKey string) (IssuePoolCycleResponse, error) {
	query := `SELECT id, policy_id, autopilot_id, workspace_id, project_id, idempotency_key,
		status, scanned_count, eligible_count, claimed_count, created_at, updated_at, reviewed_at,
		autopilot_run_id,workflow_template_id,workflow_template_version_id,
		approved_count,rejected_count,completed_count,blocked_count,failed_count,deferred_count,completed_at,
        (SELECT count(*)::int FROM issue_pool_item WHERE cycle_id=issue_pool_cycle.id AND workflow_run_id IS NOT NULL),
        (SELECT count(*)::int FROM issue_pool_item WHERE cycle_id=issue_pool_cycle.id AND status='waiting_acceptance')
		FROM issue_pool_cycle WHERE autopilot_id=$1 AND workspace_id=$2`
	args := []any{autopilotID, workspaceID}
	if cycleID.Valid {
		query += " AND id=$3"
		args = append(args, cycleID)
	} else {
		query += " AND idempotency_key=$3"
		args = append(args, idempotencyKey)
	}
	var resp IssuePoolCycleResponse
	var id, policyID, apID, wsID, projectID, autopilotRunID, templateID, versionID pgtype.UUID
	var createdAt, updatedAt, reviewedAt, completedAt pgtype.Timestamptz
	err := h.DB.QueryRow(ctx, query, args...).Scan(&id, &policyID, &apID, &wsID, &projectID,
		&resp.IdempotencyKey, &resp.Status, &resp.ScannedCount, &resp.EligibleCount,
		&resp.ClaimedCount, &createdAt, &updatedAt, &reviewedAt,
		&autopilotRunID, &templateID, &versionID, &resp.ApprovedCount, &resp.RejectedCount,
		&resp.CompletedCount, &resp.BlockedCount, &resp.FailedCount, &resp.DeferredCount, &completedAt, &resp.DispatchedCount, &resp.WaitingAcceptanceCount)
	if err != nil {
		return IssuePoolCycleResponse{}, err
	}
	resp.ID, resp.PolicyID, resp.AutopilotID, resp.WorkspaceID = uuidToString(id), uuidToString(policyID), uuidToString(apID), uuidToString(wsID)
	resp.ProjectID = uuidToPtr(projectID)
	resp.CreatedAt, resp.UpdatedAt, resp.ReviewedAt = timestampToString(createdAt), timestampToString(updatedAt), timestampToPtr(reviewedAt)
	resp.AutopilotRunID, resp.WorkflowTemplateID, resp.WorkflowTemplateVersionID = uuidToPtr(autopilotRunID), uuidToPtr(templateID), uuidToPtr(versionID)
	resp.CompletedAt = timestampToPtr(completedAt)
	rows, err := h.DB.Query(ctx, `SELECT id, issue_id, status, score, score_breakdown,
		selection_reasons, issue_snapshot, claim_token, reviewer_id, review_reason, claimed_at, reviewed_at,
		workflow_run_id,dispatch_attempts,failure_code,failure_detail,dispatched_at,waiting_acceptance_at,completed_at
		FROM issue_pool_item WHERE cycle_id=$1 ORDER BY score DESC, issue_id ASC`, id)
	if err != nil {
		return IssuePoolCycleResponse{}, err
	}
	defer rows.Close()
	resp.Items = []IssuePoolItemResponse{}
	for rows.Next() {
		var item IssuePoolItemResponse
		var itemID, issueID, claimToken, reviewerID, workflowRunID pgtype.UUID
		var breakdownRaw, reasonsRaw, snapshotRaw, failureDetailRaw []byte
		var reviewReason, failureCode pgtype.Text
		var claimedAt, itemReviewedAt, dispatchedAt, waitingAt, itemCompletedAt pgtype.Timestamptz
		if err := rows.Scan(&itemID, &issueID, &item.Status, &item.Score, &breakdownRaw,
			&reasonsRaw, &snapshotRaw, &claimToken, &reviewerID, &reviewReason, &claimedAt, &itemReviewedAt,
			&workflowRunID, &item.DispatchAttempts, &failureCode, &failureDetailRaw, &dispatchedAt, &waitingAt, &itemCompletedAt); err != nil {
			return IssuePoolCycleResponse{}, err
		}
		item.ID, item.IssueID, item.ClaimToken = uuidToString(itemID), uuidToString(issueID), uuidToString(claimToken)
		item.ReviewerID, item.ReviewReason = uuidToPtr(reviewerID), textToPtr(reviewReason)
		item.WorkflowRunID, item.FailureCode = uuidToPtr(workflowRunID), textToPtr(failureCode)
		item.ClaimedAt, item.ReviewedAt = timestampToString(claimedAt), timestampToPtr(itemReviewedAt)
		item.DispatchedAt, item.WaitingAcceptanceAt, item.CompletedAt = timestampToPtr(dispatchedAt), timestampToPtr(waitingAt), timestampToPtr(itemCompletedAt)
		item.ScoreBreakdown, item.IssueSnapshot = map[string]int{}, map[string]any{}
		item.SelectionReasons = []string{}
		_ = json.Unmarshal(breakdownRaw, &item.ScoreBreakdown)
		_ = json.Unmarshal(reasonsRaw, &item.SelectionReasons)
		_ = json.Unmarshal(snapshotRaw, &item.IssueSnapshot)
		if len(failureDetailRaw) > 0 {
			_ = json.Unmarshal(failureDetailRaw, &item.FailureDetail)
		}
		resp.Items = append(resp.Items, item)
	}
	return resp, rows.Err()
}

func (h *Handler) ReviewIssuePoolItems(w http.ResponseWriter, r *http.Request) {
	ap, _, ok := h.issuePoolAutopilot(w, r, true)
	if !ok {
		return
	}
	cycleID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "cycleId"), "cycle id")
	if !ok {
		return
	}
	var req ReviewIssuePoolItemsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(req.Decisions) == 0 || len(req.Decisions) > 100 {
		writeError(w, http.StatusBadRequest, "decisions must contain 1..100 items")
		return
	}
	seen := map[string]bool{}
	for i := range req.Decisions {
		d := &req.Decisions[i]
		d.ItemID, d.Decision, d.Reason = strings.TrimSpace(d.ItemID), strings.TrimSpace(d.Decision), strings.TrimSpace(d.Reason)
		if d.Decision != "approve" && d.Decision != "reject" {
			writeError(w, http.StatusBadRequest, "decision must be approve or reject")
			return
		}
		if d.Decision == "reject" && d.Reason == "" {
			writeError(w, http.StatusBadRequest, "reason is required when rejecting an item")
			return
		}
		if seen[d.ItemID] {
			writeError(w, http.StatusBadRequest, "decisions contains a duplicate item_id")
			return
		}
		seen[d.ItemID] = true
	}
	sort.Slice(req.Decisions, func(i, j int) bool { return req.Decisions[i].ItemID < req.Decisions[j].ItemID })
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to review issue pool items")
		return
	}
	defer tx.Rollback(r.Context())
	var cycleStatus string
	if err := tx.QueryRow(r.Context(), `SELECT status FROM issue_pool_cycle
		WHERE id=$1 AND autopilot_id=$2 AND workspace_id=$3 FOR UPDATE`, cycleID, ap.ID, ap.WorkspaceID).Scan(&cycleStatus); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "issue pool cycle not found")
		} else {
			writeError(w, http.StatusInternalServerError, "failed to review issue pool items")
		}
		return
	}
	// Terminal cycles still admit exact decision replays. Each item below is
	// locked and accepts only its existing target state; a different decision
	// remains a conflict, so retrying a lost HTTP response is safe.
	reviewerID := parseUUID(userID)
	approvedTransitions, rejectedTransitions := 0, 0
	for _, decision := range req.Decisions {
		itemID, ok := parseUUIDOrBadRequest(w, decision.ItemID, "item_id")
		if !ok {
			return
		}
		var current string
		if err := tx.QueryRow(r.Context(), `SELECT status FROM issue_pool_item
			WHERE id=$1 AND cycle_id=$2 AND autopilot_id=$3 AND workspace_id=$4 FOR UPDATE`,
			itemID, cycleID, ap.ID, ap.WorkspaceID).Scan(&current); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(w, http.StatusNotFound, "issue pool item not found")
			} else {
				writeError(w, http.StatusInternalServerError, "failed to review issue pool items")
			}
			return
		}
		target := "approved"
		if decision.Decision == "reject" {
			target = "rejected"
		}
		if current != "claimed" {
			if current == target {
				continue
			}
			writeError(w, http.StatusConflict, "an issue pool item cannot be reviewed twice with different decisions")
			return
		}
		var reason any
		if decision.Reason != "" {
			reason = decision.Reason
		}
		if _, err := tx.Exec(r.Context(), `UPDATE issue_pool_item SET status=$2, reviewer_id=$3,
			review_reason=$4, reviewed_at=now(), updated_at=now() WHERE id=$1`, itemID, target, reviewerID, reason); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to review issue pool items")
			return
		}
		if target == "approved" {
			approvedTransitions++
		} else {
			rejectedTransitions++
		}
	}
	var pending, approved, rejected int
	if err := tx.QueryRow(r.Context(), `SELECT
		count(*) FILTER (WHERE status='claimed'),
		count(*) FILTER (WHERE status IN ('approved','dispatching','running','waiting_acceptance','blocked')),
		count(*) FILTER (WHERE status='rejected') FROM issue_pool_item WHERE cycle_id=$1`, cycleID).Scan(&pending, &approved, &rejected); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to review issue pool items")
		return
	}
	nextStatus := "awaiting_review"
	if pending == 0 {
		if approved > 0 {
			nextStatus = "running"
		} else {
			nextStatus = "completed"
		}
	}
	if _, err := tx.Exec(r.Context(), `UPDATE issue_pool_cycle SET status=$2, updated_at=now(),
		reviewed_at=CASE WHEN $2<>'awaiting_review' THEN COALESCE(reviewed_at, now()) ELSE NULL END,
		approved_count=$3,rejected_count=$4,
		completed_at=CASE WHEN $2='completed' THEN COALESCE(completed_at,now()) ELSE NULL END WHERE id=$1`, cycleID, nextStatus, approved, rejected); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to review issue pool items")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to review issue pool items")
		return
	}
	h.IssuePoolMetrics.AddItemTransition("approved", approvedTransitions)
	h.IssuePoolMetrics.AddItemTransition("rejected", rejectedTransitions)
	if nextStatus == "running" {
		h.dispatchApprovedIssuePoolItems(r.Context(), ap, cycleID, reviewerID)
	} else if nextStatus == "completed" {
		h.reconcileIssuePoolCycle(r.Context(), ap, cycleID)
	}
	resp, err := h.loadIssuePoolCycle(r.Context(), ap.ID, ap.WorkspaceID, cycleID, "")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load issue pool cycle")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}
