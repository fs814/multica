package handler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/logger"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/internal/workflow"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

// WorkflowRunResponse is the list-shaped view of a Run.
//
// TemplateName/TemplateKey are denormalized onto the row rather than left for
// the client to join. A Run's whole identity to a reader is "which process is
// this", and a list that only carried template_id would render as a column of
// UUIDs until a second request resolved them. StepCount and CurrentNodeKey are
// the two progress signals the list needs; the full trace lives on the detail
// response because it is unbounded in the number of rework attempts.
type WorkflowRunResponse struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	// IssueID is null for a Run started without an Issue. The Run endpoint
	// always creates one, but intake and API callers may not, so a client must
	// not assume it is present.
	IssueID           *string `json:"issue_id"`
	TemplateID        string  `json:"template_id"`
	TemplateVersionID string  `json:"template_version_id"`
	Status            string  `json:"status"`
	Source            string  `json:"source"`
	SourceEventID     *string `json:"source_event_id"`
	AccountableUserID *string `json:"accountable_user_id"`
	// BlockedReason and FailureReason are the bounded reason codes from
	// workflow/errors.go, never free text: they are what the UI branches on to
	// tell "no eligible agent" from "the agent said it failed". Both can be
	// present at once on a Run that was blocked and later failed.
	BlockedReason *string `json:"blocked_reason"`
	FailureReason *string `json:"failure_reason"`
	// FailureDetail is the free-text diagnosis behind those codes - the router's
	// per-candidate refusal list ("Ada: runtime offline; Bob: not permitted"), or
	// the validator's complaint about an unparseable submission. The code alone
	// tells an operator the CLASS of problem; only this says which agent to fix.
	// Dropping it turns every blocked run into "something went wrong somewhere".
	FailureDetail *string `json:"failure_detail"`
	StartedAt     *string `json:"started_at"`
	CompletedAt   *string `json:"completed_at"`
	CreatedAt     string  `json:"created_at"`
	UpdatedAt     string  `json:"updated_at"`

	TemplateName string `json:"template_name"`
	TemplateKey  string `json:"template_key"`
	StepCount    int    `json:"step_count"`
	// CurrentNodeKey is the node the Run is on (or the node it stopped on, for a
	// terminal Run). Null only while a Run has no Steps at all.
	CurrentNodeKey *string `json:"current_node_key"`
}

// WorkflowSubmissionResponse is one Agent deliverable.
//
// Artifact is emitted as the stored JSONB rather than a typed struct: the
// artifact shape is chosen by the node's submission_schema, so typing it here
// would force this handler to know every schema and would silently drop fields
// from a schema it did not. ValidationErrors is non-nil only on a submission the
// contract rejected, and it is exactly the evidence a human needs to see why an
// otherwise-correct agent's step blocked.
type WorkflowSubmissionResponse struct {
	Verdict          string          `json:"verdict"`
	Artifact         json.RawMessage `json:"artifact"`
	Rationale        string          `json:"rationale"`
	Confidence       *float64        `json:"confidence"`
	RootCause        *string         `json:"root_cause"`
	ValidationErrors json.RawMessage `json:"validation_errors"`
	SubmittedAt      string          `json:"submitted_at"`
}

// WorkflowStepResponse is one Step attempt.
//
// Attempts are NOT collapsed: rework preserves history, and the point of the
// trace is that a reader can see the first attempt, why it came back, and what
// changed. A response that showed only the latest attempt per node would hide
// the rework loop the run is actually stuck in.
type WorkflowStepResponse struct {
	ID       string  `json:"id"`
	NodeKey  string  `json:"node_key"`
	NodeType string  `json:"node_type"`
	Attempt  int32   `json:"attempt"`
	Status   string  `json:"status"`
	AgentID  *string `json:"agent_id"`
	// AgentName is resolved so the trace names the specialist that ran the step.
	// Empty when the agent row is gone (agents are archived, not deleted, so this
	// is rare) - an absent name must not cost the step its row.
	AgentName *string `json:"agent_name"`
	TaskID    *string `json:"task_id"`
	// RoutingReason is the router's own explanation ("capability:bug_analysis ->
	// analyst"), persisted at activation when routing SUCCEEDED. When it failed
	// there is no chosen agent to explain, so the per-candidate refusal list lands
	// in FailureDetail instead - that is the field that names which agent to fix.
	RoutingReason *string                     `json:"routing_reason"`
	FailureReason *string                     `json:"failure_reason"`
	FailureDetail *string                     `json:"failure_detail"`
	StartedAt     *string                     `json:"started_at"`
	CompletedAt   *string                     `json:"completed_at"`
	Submission    *WorkflowSubmissionResponse `json:"submission"`
}

// WorkflowAcceptanceResponse is the human review gate.
//
// Criteria and ReworkTargets are read from the Run's PINNED definition, not the
// template's current version: a reviewer must be shown the criteria the Run was
// started under, and must only be offered targets that version's graph declared.
// Reading them live would let a template edit change what an in-flight review is
// judging, or offer a target the engine will then refuse.
type WorkflowAcceptanceResponse struct {
	ID     string  `json:"id"`
	StepID string  `json:"step_id"`
	Status string  `json:"status"`
	Reason *string `json:"reason"`
	// ReworkTargetNodeKey is the target a rejection chose. Null while pending.
	ReworkTargetNodeKey *string `json:"rework_target_node_key"`
	// Criteria and ReworkTargets are always non-nil arrays so a client can
	// iterate them without a null check.
	Criteria      []string `json:"criteria"`
	ReworkTargets []string `json:"rework_targets"`
	CreatedAt     string   `json:"created_at"`
}

// WorkflowRunDetailResponse is the list shape plus the full trace.
type WorkflowRunDetailResponse struct {
	WorkflowRunResponse
	// Input is the Run's freeform input (title/description as submitted). Emitted
	// as stored JSONB rather than a typed struct so typed input fields can be
	// added later without this endpoint dropping the ones it does not know.
	Input json.RawMessage        `json:"input"`
	Steps []WorkflowStepResponse `json:"steps"`
	// Acceptance is the pending review, or the most recent decided one when none
	// is pending. Null when the Run has never reached an acceptance node.
	Acceptance *WorkflowAcceptanceResponse `json:"acceptance"`
}

// RunWorkflowTemplateRequest starts a Run from a template.
//
// Title and Description are first-class and always required. Every template
// published before input nodes existed carries only those, a published version is
// immutable, so the pair can never stop being the shape a Run can be started
// with.
//
// DECLARED FIELDS ARRIVE AS UNMODELLED TOP-LEVEL KEYS, not as a nested object.
// An input node's declaration names the key its value is stored under in
// workflow_run.input, and workflow.ParseRunInputFor reads that FLAT bag. A
// `fields: {...}` envelope here would mean the handler has to flatten it, and the
// two shapes would then be free to disagree about what the bag looks like.
//
// Which is why this struct alone is not the whole request: json.Decode into a
// struct DISCARDS every key the struct does not name, so decoding only into this
// would silently drop a declared `severity` and the engine would then answer 422
// "Severity is required" about a value the submitter did send. See
// decodeRunWorkflowTemplateRequest, which decodes both this and the raw bag.
type RunWorkflowTemplateRequest struct {
	Title          string  `json:"title"`
	Description    string  `json:"description"`
	ProjectID      *string `json:"project_id"`
	IdempotencyKey string  `json:"idempotency_key"`
}

// runWorkflowTemplateFields are the request keys this handler models itself, and
// therefore the ones that must NOT be copied into the run input bag a second time
// under their raw name.
//
// title and description are excluded because RunInput gives them dedicated slots
// (and RenderPrompt dedicated formatting); project_id because it addresses the
// Issue this endpoint creates, not the workflow - putting it in the bag would
// offer it to a graph that declared a field of the same name as if a human had
// typed it.
var runWorkflowTemplateFields = map[string]bool{
	"title":           true,
	"description":     true,
	"project_id":      true,
	"idempotency_key": true,
}

// DecideWorkflowAcceptanceRequest records a reviewer's verdict.
//
// ReworkTarget is required on a rejection and must be one the pinned graph
// permits; the engine enforces both, and this handler surfaces its refusal as a
// 409 rather than translating it, so the API cannot drift from the state machine.
type DecideWorkflowAcceptanceRequest struct {
	Accept       bool   `json:"accept"`
	Reason       string `json:"reason"`
	ReworkTarget string `json:"rework_target"`
}

const (
	// workflowRunBodyLimit caps the run/acceptance request bodies. Both are small
	// (a title, a prose description, a node key), but Description reaches an
	// agent's prompt and is stored on every Step's input, so an unbounded upload
	// would be copied once per Step. 64KB is far more defect report than anyone
	// writes and still bounds the fan-out. Mirrors workflowTemplateBodyLimit.
	workflowRunBodyLimit = 64 << 10

	maxWorkflowRunTitleLen = 200
	// maxWorkflowRunDescriptionLen bounds what is echoed into every Step's input
	// and every Agent prompt. The engine truncates a prompt field independently;
	// rejecting here instead means the submitter learns their text was too long
	// rather than discovering silently-cut instructions in an agent's output.
	maxWorkflowRunDescriptionLen = 20000

	// maxWorkflowRunDeclaredFieldLen bounds one input-node-declared field's value.
	// Smaller than the description ceiling because description is the ONE field
	// meant to hold a full defect report, while a declared field is a severity, a
	// component, a repro list; and a graph may declare many, so the per-field bound
	// is what keeps the whole bag - copied onto every Step's input - proportional.
	maxWorkflowRunDeclaredFieldLen = 4000

	// defaultWorkflowRunPageSize / maxWorkflowRunPageSize bound the list.
	defaultWorkflowRunPageSize = 20
	maxWorkflowRunPageSize     = 100
)

// ---------------------------------------------------------------------------
// Converters
// ---------------------------------------------------------------------------

// runSummary is the per-run data the list shape needs beyond the run row itself.
// Resolved in batch (see workflowRunSummaries) so a page of runs costs a fixed
// number of queries.
type runSummary struct {
	templateName   string
	templateKey    string
	stepCount      int
	currentNodeKey string
}

func workflowRunToResponse(run db.WorkflowRun, sum runSummary) WorkflowRunResponse {
	resp := WorkflowRunResponse{
		ID:                uuidToString(run.ID),
		WorkspaceID:       uuidToString(run.WorkspaceID),
		IssueID:           uuidToPtr(run.IssueID),
		TemplateID:        uuidToString(run.TemplateID),
		TemplateVersionID: uuidToString(run.TemplateVersionID),
		Status:            run.Status,
		Source:            run.Source,
		SourceEventID:     textToPtr(run.SourceEventID),
		AccountableUserID: uuidToPtr(run.AccountableUserID),
		BlockedReason:     textToPtr(run.BlockedReason),
		FailureReason:     textToPtr(run.FailureReason),
		FailureDetail:     textToPtr(run.FailureDetail),
		StartedAt:         timestampToPtr(run.StartedAt),
		CompletedAt:       timestampToPtr(run.CompletedAt),
		CreatedAt:         timestampToString(run.CreatedAt),
		UpdatedAt:         timestampToString(run.UpdatedAt),
		TemplateName:      sum.templateName,
		TemplateKey:       sum.templateKey,
		StepCount:         sum.stepCount,
	}
	if sum.currentNodeKey != "" {
		key := sum.currentNodeKey
		resp.CurrentNodeKey = &key
	}
	return resp
}

func workflowSubmissionToResponse(s db.WorkflowSubmission) WorkflowSubmissionResponse {
	resp := WorkflowSubmissionResponse{
		Verdict:     s.Verdict,
		Artifact:    json.RawMessage(`{}`),
		Rationale:   s.Rationale,
		RootCause:   textToPtr(s.RootCause),
		SubmittedAt: timestampToString(s.SubmittedAt),
	}
	// json.Valid rather than a blind cast: these bytes go straight into the
	// response body, and an unreadable artifact must degrade to an empty object
	// instead of producing a response the client cannot parse at all.
	if json.Valid(s.Artifact) {
		resp.Artifact = json.RawMessage(s.Artifact)
	}
	if s.Confidence.Valid {
		c := s.Confidence.Float64
		resp.Confidence = &c
	}
	if len(s.ValidationErrors) > 0 && json.Valid(s.ValidationErrors) {
		resp.ValidationErrors = json.RawMessage(s.ValidationErrors)
	}
	return resp
}

func workflowStepToResponse(step db.WorkflowStepInstance, agentName string, sub *db.WorkflowSubmission) WorkflowStepResponse {
	resp := WorkflowStepResponse{
		ID:            uuidToString(step.ID),
		NodeKey:       step.NodeKey,
		NodeType:      step.NodeType,
		Attempt:       step.Attempt,
		Status:        step.Status,
		AgentID:       uuidToPtr(step.AgentID),
		TaskID:        uuidToPtr(step.TaskID),
		RoutingReason: textToPtr(step.RoutingReason),
		FailureReason: textToPtr(step.FailureReason),
		FailureDetail: textToPtr(step.FailureDetail),
		StartedAt:     timestampToPtr(step.StartedAt),
		CompletedAt:   timestampToPtr(step.CompletedAt),
	}
	if agentName != "" {
		resp.AgentName = &agentName
	}
	if sub != nil {
		s := workflowSubmissionToResponse(*sub)
		resp.Submission = &s
	}
	return resp
}

// workflowAcceptanceToResponse renders a review gate, taking criteria and
// permitted rework targets from the pinned node rather than the acceptance row's
// own context blob.
//
// The row's context does carry a snapshot written at activation, but the pinned
// definition is the authority the engine itself checks against in
// DecideAcceptance (node.AllowsReworkTo). Reading anything else here would let
// the UI offer a target the engine then refuses, which reads to a reviewer as a
// broken button rather than as a graph that never allowed it.
func workflowAcceptanceToResponse(a db.WorkflowAcceptance, node *workflow.Node) WorkflowAcceptanceResponse {
	resp := WorkflowAcceptanceResponse{
		ID:                  uuidToString(a.ID),
		StepID:              uuidToString(a.StepID),
		Status:              a.Status,
		Reason:              textToPtr(a.Reason),
		ReworkTargetNodeKey: textToPtr(a.ReworkTargetNodeKey),
		Criteria:            []string{},
		ReworkTargets:       []string{},
		CreatedAt:           timestampToString(a.CreatedAt),
	}
	if node != nil {
		if len(node.AcceptanceCriteria) > 0 {
			resp.Criteria = node.AcceptanceCriteria
		}
		if len(node.ReworkTargets) > 0 {
			resp.ReworkTargets = node.ReworkTargets
		}
	}
	return resp
}

// ---------------------------------------------------------------------------
// Engine error mapping
// ---------------------------------------------------------------------------

// writeWorkflowEngineError maps a typed engine error onto a status code.
//
// The mapping matters because these are the only signal a client has for what to
// do next: a 409 means "this Run's state moved under you, refetch", a 422 means
// "the pinned graph is unrunnable, nothing you send will help", and a 500 means
// "our problem". Collapsing them into 500 would make a normal double-click look
// like an outage; collapsing them into 400 would make the client retry forever.
//
// A non-EngineError is a 500 with the detail logged rather than returned: an
// unclassified failure is a wrapped SQL error, and its text can name columns.
func (h *Handler) writeWorkflowEngineError(w http.ResponseWriter, r *http.Request, err error, op string) {
	var engErr *workflow.EngineError
	if errors.As(err, &engErr) {
		status := http.StatusInternalServerError
		switch engErr.Code {
		case workflow.ErrCodeNotFound:
			status = http.StatusNotFound
		case workflow.ErrCodeAcceptanceConflict,
			workflow.ErrCodeInvalidTransition,
			workflow.ErrCodeReworkLimit,
			workflow.ErrCodeIdempotencyConflict,
			workflow.ErrCodeBudgetExceeded:
			status = http.StatusConflict
		case workflow.ErrCodeInvalidDefinition:
			// The pinned version cannot execute. A client retry cannot fix it, so
			// this is deliberately not a 409.
			status = http.StatusUnprocessableEntity
		case workflow.ErrCodeInvalidSubmission:
			status = http.StatusUnprocessableEntity
		case workflow.ErrCodeRoutingFailed, workflow.ErrCodeInvariantViolation:
			// Both are server-side: routing with no router configured, or a Step
			// referencing a node absent from its own pinned version. Neither is
			// something the caller did.
			status = http.StatusInternalServerError
		}
		if status == http.StatusInternalServerError {
			slog.Error(op+" failed", append(logger.RequestAttrs(r), "error", err, "code", engErr.Code)...)
			writeError(w, status, "workflow command failed")
			return
		}
		// The code travels alongside the message so a client can branch on it
		// without string-matching (plan section 9).
		writeJSON(w, status, map[string]string{"error": engErr.Message, "code": engErr.Code})
		return
	}

	var defErr *workflow.DefinitionError
	if errors.As(err, &defErr) {
		writeJSON(w, http.StatusUnprocessableEntity, workflowValidationResponse{
			Error:    "the pinned workflow version is not a valid graph",
			Messages: []string{defErr.Error()},
		})
		return
	}
	var verrs *workflow.ValidationErrors
	if errors.As(err, &verrs) {
		writeJSON(w, http.StatusUnprocessableEntity, workflowValidationResponse{
			Error:    "the pinned workflow version is not a valid graph",
			Messages: verrs.Messages(),
		})
		return
	}

	slog.Error(op+" failed", append(logger.RequestAttrs(r), "error", err)...)
	writeError(w, http.StatusInternalServerError, "workflow command failed")
}

// requireWorkflowEngine returns the engine or writes 503.
//
// h.WorkflowEngine is assigned after handler.New in cmd/server/router.go, so any
// construction path that skips that wiring (tests, a future embedded mode) leaves
// it nil. A 503 says "this deployment cannot run workflows" — a panic would take
// the request down with a stack trace that says nothing about the cause.
func (h *Handler) requireWorkflowEngine(w http.ResponseWriter) (*workflow.Engine, bool) {
	if h.WorkflowEngine == nil {
		writeError(w, http.StatusServiceUnavailable, "workflow execution is not enabled on this server")
		return nil, false
	}
	return h.WorkflowEngine, true
}

// ---------------------------------------------------------------------------
// Shared loaders
// ---------------------------------------------------------------------------

// loadWorkflowRun resolves {id} inside the caller's workspace and confirms
// membership.
//
// Membership is checked explicitly rather than relied upon from middleware,
// because these handlers are called directly in tests and a future non-middleware
// route would otherwise silently lose the check. A run in another workspace is a
// 404, not a 403: distinguishing them would confirm the id exists.
func (h *Handler) loadWorkflowRun(w http.ResponseWriter, r *http.Request) (db.WorkflowRun, bool) {
	idUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "workflow run id")
	if !ok {
		return db.WorkflowRun{}, false
	}
	workspaceID := h.resolveWorkspaceID(r)
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return db.WorkflowRun{}, false
	}
	if _, ok := h.requireWorkspaceMember(w, r, workspaceID, "workflow run not found"); !ok {
		return db.WorkflowRun{}, false
	}
	run, err := h.Queries.GetWorkflowRun(r.Context(), db.GetWorkflowRunParams{
		ID:          idUUID,
		WorkspaceID: wsUUID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "workflow run not found")
			return db.WorkflowRun{}, false
		}
		slog.Warn("GetWorkflowRun failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to get workflow run")
		return db.WorkflowRun{}, false
	}
	return run, true
}

// workflowRunSummaries resolves template labels, step counts, and current node
// for a page of runs in a fixed number of queries.
//
// Two batched reads regardless of page size. The alternative - resolving each run
// as it is converted - is an N+1 on the list endpoint, and the summary fields are
// display data: on a query failure every run reports empty labels and zero steps
// rather than the list failing, because a Run the user cannot see at all is worse
// than one whose progress column is blank.
func (h *Handler) workflowRunSummaries(ctx context.Context, workspaceID pgtype.UUID, runs []db.WorkflowRun) map[string]runSummary {
	out := make(map[string]runSummary, len(runs))
	if len(runs) == 0 {
		return out
	}

	runIDs := make([]pgtype.UUID, 0, len(runs))
	templateIDs := make([]pgtype.UUID, 0, len(runs))
	seenTemplate := make(map[string]struct{}, len(runs))
	for _, run := range runs {
		runIDs = append(runIDs, run.ID)
		key := uuidToString(run.TemplateID)
		if _, dup := seenTemplate[key]; !dup {
			seenTemplate[key] = struct{}{}
			templateIDs = append(templateIDs, run.TemplateID)
		}
		out[uuidToString(run.ID)] = runSummary{}
	}

	templates, err := h.Queries.ListWorkflowTemplatesByIDs(ctx, db.ListWorkflowTemplatesByIDsParams{
		WorkspaceID: workspaceID,
		Ids:         templateIDs,
	})
	if err != nil {
		slog.Warn("ListWorkflowTemplatesByIDs failed", "error", err)
	}
	byTemplate := make(map[string]db.WorkflowTemplate, len(templates))
	for _, t := range templates {
		byTemplate[uuidToString(t.ID)] = t
	}

	steps, err := h.Queries.SummarizeWorkflowStepsForRuns(ctx, db.SummarizeWorkflowStepsForRunsParams{
		WorkspaceID: workspaceID,
		RunIds:      runIDs,
	})
	if err != nil {
		slog.Warn("SummarizeWorkflowStepsForRuns failed", "error", err)
	}
	byRun := make(map[string]db.SummarizeWorkflowStepsForRunsRow, len(steps))
	for _, s := range steps {
		byRun[uuidToString(s.RunID)] = s
	}

	for _, run := range runs {
		runKey := uuidToString(run.ID)
		sum := runSummary{}
		if t, ok := byTemplate[uuidToString(run.TemplateID)]; ok {
			sum.templateName = t.Name
			sum.templateKey = t.Key
		}
		if s, ok := byRun[runKey]; ok {
			sum.stepCount = int(s.StepCount)
			sum.currentNodeKey = s.CurrentNodeKey
		}
		out[runKey] = sum
	}
	return out
}

// pinnedWorkflowDefinition parses the Run's pinned version.
//
// Always the version the Run pinned, never the template's current version: a Run
// executes the graph it started with, and reading the current version would show
// a reviewer criteria from a graph the Run never ran. Returns nil (not an error)
// when the version is missing or unparseable — the trace is still worth serving,
// it just loses acceptance criteria and rework targets.
func (h *Handler) pinnedWorkflowDefinition(ctx context.Context, run db.WorkflowRun) *workflow.Definition {
	version, err := h.Queries.GetWorkflowTemplateVersion(ctx, db.GetWorkflowTemplateVersionParams{
		ID:          run.TemplateVersionID,
		WorkspaceID: run.WorkspaceID,
	})
	if err != nil {
		slog.Warn("load pinned workflow version failed",
			"error", err, "run_id", uuidToString(run.ID))
		return nil
	}
	def, err := workflow.ParseDefinition(version.Definition)
	if err != nil {
		// A published version is immutable and was validated at publish time, so
		// this means it was written by a newer server or edited out of band.
		slog.Warn("pinned workflow version does not parse",
			"error", err, "run_id", uuidToString(run.ID))
		return nil
	}
	return def
}

// workflowRunDetail assembles run + steps + submissions + acceptance.
func (h *Handler) workflowRunDetail(ctx context.Context, run db.WorkflowRun) (WorkflowRunDetailResponse, error) {
	sums := h.workflowRunSummaries(ctx, run.WorkspaceID, []db.WorkflowRun{run})
	detail := WorkflowRunDetailResponse{
		WorkflowRunResponse: workflowRunToResponse(run, sums[uuidToString(run.ID)]),
		Input:               json.RawMessage(`{}`),
		Steps:               []WorkflowStepResponse{},
	}
	if json.Valid(run.Input) {
		detail.Input = json.RawMessage(run.Input)
	}

	steps, err := h.Queries.ListWorkflowStepInstances(ctx, db.ListWorkflowStepInstancesParams{
		RunID:       run.ID,
		WorkspaceID: run.WorkspaceID,
	})
	if err != nil {
		return WorkflowRunDetailResponse{}, fmt.Errorf("list workflow steps: %w", err)
	}

	// One submissions read for the whole Run, indexed by step, rather than
	// GetLatestWorkflowSubmissionForStep per step: rework means a Run legitimately
	// has many steps, and the trace is the one place that always wants all of
	// them. The query orders submitted_at ASC, so a later row overwrites an
	// earlier one and the map ends up holding each step's latest.
	submissions, err := h.Queries.ListWorkflowSubmissionsForRun(ctx, db.ListWorkflowSubmissionsForRunParams{
		RunID:       run.ID,
		WorkspaceID: run.WorkspaceID,
	})
	if err != nil {
		return WorkflowRunDetailResponse{}, fmt.Errorf("list workflow submissions: %w", err)
	}
	latestByStep := make(map[string]db.WorkflowSubmission, len(submissions))
	for _, s := range submissions {
		latestByStep[uuidToString(s.StepID)] = s
	}

	agentNames := h.workflowStepAgentNames(ctx, run.WorkspaceID, steps)

	detail.Steps = make([]WorkflowStepResponse, len(steps))
	for i, step := range steps {
		var sub *db.WorkflowSubmission
		if s, ok := latestByStep[uuidToString(step.ID)]; ok {
			found := s
			sub = &found
		}
		detail.Steps[i] = workflowStepToResponse(step, agentNames[uuidToString(step.AgentID)], sub)
	}

	acceptance, err := h.workflowRunAcceptance(ctx, run)
	if err != nil {
		return WorkflowRunDetailResponse{}, err
	}
	detail.Acceptance = acceptance
	return detail, nil
}

// workflowStepAgentNames resolves agent id -> name for the trace in one query.
//
// Uses the workspace-wide agent list rather than N GetAgent calls, and
// ListAllAgents rather than ListAgents so an ARCHIVED agent still gets a name: a
// Run that ran last week and whose specialist was archived since must still say
// who did the work, or its audit trail loses the only interesting fact about the
// step. A lookup failure yields no names rather than failing the trace.
func (h *Handler) workflowStepAgentNames(ctx context.Context, workspaceID pgtype.UUID, steps []db.WorkflowStepInstance) map[string]string {
	names := map[string]string{}
	wanted := false
	for _, s := range steps {
		if s.AgentID.Valid {
			wanted = true
			break
		}
	}
	if !wanted {
		return names
	}
	agents, err := h.Queries.ListAllAgents(ctx, workspaceID)
	if err != nil {
		slog.Warn("ListAllAgents for workflow trace failed", "error", err)
		return names
	}
	for _, a := range agents {
		names[uuidToString(a.ID)] = a.Name
	}
	return names
}

// workflowRunAcceptance returns the Run's pending review, or the most recent
// decided one when nothing is pending.
//
// Falling back to a decided row rather than returning null is deliberate: after a
// rejection the reviewer's reason IS the reason the Run went back to an earlier
// node, and dropping it the moment it was decided would delete the explanation
// exactly when the rework attempt makes someone ask for it.
func (h *Handler) workflowRunAcceptance(ctx context.Context, run db.WorkflowRun) (*WorkflowAcceptanceResponse, error) {
	acceptances, err := h.Queries.ListWorkflowAcceptancesForRun(ctx, db.ListWorkflowAcceptancesForRunParams{
		RunID:       run.ID,
		WorkspaceID: run.WorkspaceID,
	})
	if err != nil {
		return nil, fmt.Errorf("list workflow acceptances: %w", err)
	}
	if len(acceptances) == 0 {
		return nil, nil
	}
	// Ordered created_at ASC. Prefer a pending row (there is at most one per step,
	// and the engine only opens a new one after the previous is decided); else the
	// newest row of any status.
	chosen := acceptances[len(acceptances)-1]
	for _, a := range acceptances {
		if a.Status == "pending" {
			chosen = a
			break
		}
	}

	// Resolve the acceptance node through the Step, not by scanning the graph for
	// a node of type "acceptance": a graph may have several, and only the Step
	// says which one this review belongs to.
	var node *workflow.Node
	if def := h.pinnedWorkflowDefinition(ctx, run); def != nil {
		if step, err := h.Queries.GetWorkflowStepInstance(ctx, db.GetWorkflowStepInstanceParams{
			ID:          chosen.StepID,
			WorkspaceID: run.WorkspaceID,
		}); err == nil {
			if n, ok := def.NodeByKey(step.NodeKey); ok {
				node = n
			}
		}
	}
	resp := workflowAcceptanceToResponse(chosen, node)
	return &resp, nil
}

// ---------------------------------------------------------------------------
// POST /api/workflow-templates/{id}/run
// ---------------------------------------------------------------------------

// RunWorkflowTemplate atomically creates an Issue and starts a Run on it.
// Engine.StartRun owns the transaction containing the Issue, subscriber, Run,
// first event, entry Step, and initial Agent task, so no orphan window exists.
//
// Browser callers may use the server-derived double-click key. Automation
// callers provide a stable key shared by HTTP, CLI, and MCP. Two identical
// requests converge on one Run, and the
// point is that the second must return the first's Run rather than start a
// parallel one that burns a second set of agent tasks. Deriving from the request
// content is what makes that true without any client cooperation; a client-chosen
// key would fail exactly the case it exists for, because a double click sends the
// same body twice and a naive client would generate a fresh UUID each time.
// Including the user id keeps two people independently reporting the same defect
// from colliding, which would silently hand the second one the first's Run.
func (h *Handler) RunWorkflowTemplate(w http.ResponseWriter, r *http.Request) {
	tpl, ok := h.loadWorkflowTemplate(w, r)
	if !ok {
		return
	}
	// Any workspace member may start a Run. Publishing is the admin-gated
	// decision (it chooses the graph and its cost budget); running a published
	// process is the ordinary use of it.
	if _, ok := h.requireWorkspaceMember(w, r, uuidToString(tpl.WorkspaceID), "workflow template not found"); !ok {
		return
	}
	engine, ok := h.requireWorkflowEngine(w)
	if !ok {
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	userUUID, ok := parseUUIDOrBadRequest(w, userID, "user id")
	if !ok {
		return
	}

	if tpl.Status == "archived" {
		// Archival stops NEW runs; in-flight ones finish (plan section 12).
		writeError(w, http.StatusConflict, "an archived workflow template cannot be run")
		return
	}
	if !tpl.CurrentVersion.Valid {
		// No published version means nothing to pin. 409 rather than 422: the
		// request is fine, the template's state is not, and publishing fixes it.
		writeError(w, http.StatusConflict, "workflow template has no published version to run")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, workflowRunBodyLimit)
	// Read the body ONCE into memory, then decode it twice: into the typed struct
	// for the fields this handler validates itself, and into a generic bag for the
	// declared fields it cannot know the names of. json.Decoder cannot be rewound,
	// and re-reading r.Body after the first Decode yields nothing.
	rawBody, err := io.ReadAll(r.Body)
	if err != nil {
		// MaxBytesReader surfaces an over-limit body here rather than at Decode.
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	var req RunWorkflowTemplateRequest
	if err := json.Unmarshal(rawBody, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	title := sanitizeNullBytes(strings.TrimSpace(req.Title))
	description := sanitizeNullBytes(strings.TrimSpace(req.Description))
	if title == "" {
		writeError(w, http.StatusBadRequest, "title is required")
		return
	}
	if utf8.RuneCountInString(title) > maxWorkflowRunTitleLen {
		writeError(w, http.StatusBadRequest, "title must be 200 characters or fewer")
		return
	}
	// Description is required, not optional. Every Agent step's prompt is the
	// node's generic instruction plus this text; without it the first agent is
	// told to "reproduce the reported defect" with no defect named, and the Run
	// burns an attempt producing a guess.
	if description == "" {
		writeError(w, http.StatusBadRequest, "description is required")
		return
	}
	if utf8.RuneCountInString(description) > maxWorkflowRunDescriptionLen {
		writeError(w, http.StatusBadRequest, "description must be 20000 characters or fewer")
		return
	}

	var projectID pgtype.UUID
	if req.ProjectID != nil && *req.ProjectID != "" {
		parsed, ok := parseUUIDOrBadRequest(w, *req.ProjectID, "project_id")
		if !ok {
			return
		}
		// Workspace-scoped: a project id from another tenant must be refused
		// before it is written onto an issue row.
		if _, err := h.Queries.GetProjectInWorkspace(r.Context(), db.GetProjectInWorkspaceParams{
			ID:          parsed,
			WorkspaceID: tpl.WorkspaceID,
		}); err != nil {
			writeError(w, http.StatusBadRequest, "project not found in this workspace")
			return
		}
		projectID = parsed
	}

	var templateVersionID pgtype.UUID
	if rawID := r.Header.Get("X-Workflow-Template-Version-ID"); rawID != "" {
		versionID, ok := parseUUIDOrBadRequest(w, rawID, "template version id")
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
			writeError(w, 409, "run must reference a published version of this workflow")
			return
		}
		templateVersionID = version.ID
	}
	input, err := buildWorkflowRunInput(title, description, rawBody)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to encode run input")
		return
	}
	requestHash, err := workflowStartRequestHash(tpl.ID, userUUID, projectID, input)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if templateVersionID.Valid {
		sum := sha256.Sum256([]byte(requestHash + ":" + uuidToString(templateVersionID)))
		requestHash = hex.EncodeToString(sum[:])
	}
	idempotencyKey := strings.TrimSpace(req.IdempotencyKey)
	if idempotencyKey == "" {
		// Browser clients created before Workflow Action Contract v1 rely on the
		// deterministic content key. CLI and MCP v1 require an explicit key.
		idempotencyKey = workflowRunIdempotencyKey(tpl.ID, userUUID, title, description)
	} else {
		if len(idempotencyKey) > 220 {
			writeErrorCode(w, http.StatusBadRequest, "validation_error", "idempotency_key must be 220 characters or fewer")
			return
		}
		idempotencyKey = "workflow-start:v1:" + idempotencyKey
	}

	// Short-circuit the common replay before entering the engine transaction.
	// StartRun repeats the same check and catches the unique-index race, so this
	// is only a latency optimization and never the idempotency authority.
	if existing, err := h.Queries.GetWorkflowRunByIdempotencyKey(r.Context(), db.GetWorkflowRunByIdempotencyKeyParams{
		WorkspaceID:    tpl.WorkspaceID,
		IdempotencyKey: idempotencyKey,
	}); err == nil {
		if existing.RequestHash.Valid && existing.RequestHash.String != requestHash {
			writeErrorCode(w, http.StatusConflict, workflow.ErrCodeIdempotencyConflict, "the idempotency key was already used with a different payload")
			return
		}
		// 200, not 201: nothing was created. The body is the original Run, so a
		// double-clicked button lands on the run the first click started.
		h.writeWorkflowRunDetail(w, r, existing, http.StatusOK)
		return
	} else if !errors.Is(err, pgx.ErrNoRows) {
		slog.Warn("GetWorkflowRunByIdempotencyKey failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to start workflow run")
		return
	}

	started, err := engine.StartRun(r.Context(), workflow.StartRunInput{
		WorkspaceID: tpl.WorkspaceID,
		TemplateID:  tpl.ID,
		// Pin the displayed immutable graph when supplied by the client.
		// Older clients continue to resolve the current publication in the engine.
		TemplateVersionID: templateVersionID,
		Source:            "manual",
		IdempotencyKey:    idempotencyKey,
		RequestHash:       requestHash,
		// The member who pressed Run is answerable for the Run, and routing checks
		// every candidate agent's invocation permission against exactly this user.
		AccountableUserID: userUUID,
		Issue: &workflow.RunIssueInput{
			Title:       title,
			Description: description,
			CreatorType: "member",
			CreatorID:   userUUID,
			ProjectID:   projectID,
			Subscribers: []workflow.RunIssueSubscriber{{UserType: "member", UserID: userUUID, Reason: "manual"}},
		},
		Input:     input,
		ActorType: "member",
		ActorID:   userUUID,
	})
	if err != nil {
		h.writeWorkflowEngineError(w, r, err, "StartRun")
		return
	}

	// Announce the issue only after the Run exists, and only when we created it.
	// The event drives activity/notification listeners, and firing it for an issue
	// whose Run then failed would notify subscribers about work that is not
	// happening. Reload first because StartRun projects its final Run status onto
	// the Issue in the same transaction. Publishing the pre-StartRun `todo`
	// snapshot after the engine's `issue:updated` event would otherwise move the
	// client back to stale state.
	if !started.AlreadyExisted {
		issue := *started.Issue
		if fresh, err := h.Queries.GetIssueInWorkspace(r.Context(), db.GetIssueInWorkspaceParams{
			ID:          issue.ID,
			WorkspaceID: tpl.WorkspaceID,
		}); err != nil {
			slog.Warn("reload workflow run issue before create event failed",
				append(logger.RequestAttrs(r), "error", err, "issue_id", uuidToString(issue.ID))...)
		} else {
			issue = fresh
		}
		prefix := h.getIssuePrefix(r.Context(), tpl.WorkspaceID)
		h.publish(protocol.EventIssueCreated, uuidToString(tpl.WorkspaceID), "member", userID,
			map[string]any{"issue": issueToResponse(issue, prefix)})
	}

	// A replay that raced past the read above returns 200 with the winner's Run.
	status := http.StatusCreated
	if started.AlreadyExisted {
		status = http.StatusOK
	}
	h.writeWorkflowRunDetail(w, r, h.reloadWorkflowRun(r.Context(), started.Run), status)
}

// reloadWorkflowRun re-reads a Run the engine just returned.
//
// StartRun and the other commands return the Run row as it stood BEFORE the
// activation they performed in the same transaction: StartRun marks the row
// running and then activates the entry node, and activation can immediately move
// the Run again — to waiting_acceptance if the entry node is an acceptance gate,
// to blocked if routing finds no eligible agent, even to completed for a
// degenerate graph. Returning the engine's value verbatim would report `running`
// for a Run that is already blocked, and the client would render a spinner for
// work that has permanently stopped.
//
// Falls back to the stale row on a read failure: a slightly-behind status is a
// better answer than a 500 for a command that actually succeeded, and the client's
// next refetch corrects it.
func (h *Handler) reloadWorkflowRun(ctx context.Context, run db.WorkflowRun) db.WorkflowRun {
	fresh, err := h.Queries.GetWorkflowRun(ctx, db.GetWorkflowRunParams{
		ID:          run.ID,
		WorkspaceID: run.WorkspaceID,
	})
	if err != nil {
		slog.Warn("reload workflow run failed", "error", err, "run_id", uuidToString(run.ID))
		return run
	}
	return fresh
}

// workflowRunIdempotencyKey derives the StartRun dedup key from the request.
//
// Hashed rather than concatenated because the column is bounded at 256 chars
// (migration 235) and the description is not; a truncated concatenation would
// make two long, differing reports collide, and the second submitter would
// silently receive the first's Run. The prefix keeps the key self-describing in
// the workflow_run row a human is reading.
func workflowRunIdempotencyKey(templateID, userID pgtype.UUID, title, description string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		util.UUIDToString(templateID),
		util.UUIDToString(userID),
		title,
		description,
	}, "\x00")))
	return "manual-run:" + hex.EncodeToString(sum[:])
}

// workflowStartRequestHash hashes transport-independent workflow semantics so
// the same explicit key can converge across HTTP, CLI, and MCP adapters.
func workflowStartRequestHash(templateID, ownerID, projectID pgtype.UUID, input []byte) (string, error) {
	var inputObject map[string]any
	if err := json.Unmarshal(input, &inputObject); err != nil {
		return "", err
	}
	canonical, err := json.Marshal(map[string]any{
		"template_id": uuidToString(templateID),
		"owner_id":    uuidToString(ownerID),
		"project_id":  uuidToString(projectID),
		"input":       inputObject,
	})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}

// buildWorkflowRunInput assembles the Run's input JSONB from the validated
// title/description plus whatever ELSE the caller sent.
//
// This function is the fix for a defect the shape of the code made invisible: the
// endpoint used to build the bag as
// `json.Marshal(workflow.RunInput{Title, Description})`, and json.Unmarshal into
// RunWorkflowTemplateRequest discards every key that struct does not name. So an
// input node declaring `severity` produced a Run dialog that collected severity,
// sent severity, and a server that dropped it on the floor - after which
// engine.StartRun's ValidateRunInput answered 422 "Severity is required" about a
// value the human had just typed. A required declared field made the workflow
// unstartable; an optional one silently never reached the agent. Neither is
// visible from any test that only ever sends title and description.
//
// The bag stays FLAT, keyed exactly as the declaration keys it, because that is
// what workflow.ParseRunInputFor and workflow.ValidateRunInput read. Title and
// description are written from the VALIDATED values (trimmed, null-bytes
// stripped) rather than copied from the raw body, so the bag cannot disagree with
// the Issue this endpoint creates from the same two strings.
//
// Passthrough is deliberately narrow:
//   - Keys this handler models itself are skipped (runWorkflowTemplateFields), so
//     project_id does not become run input and title/description are not written
//     twice.
//   - Only JSON strings are carried. A declared field's value is text in every
//     kind the declaration can express (text/textarea/select), and the read path
//     skips a non-string anyway - copying an object or an array through would put
//     an unbounded blob in the Run's input that nothing can render.
//   - Values are sanitized the same way title and description are. This text
//     reaches an agent's prompt, and a NUL or invalid UTF-8 sequence would either
//     break the JSONB write or travel into the prompt.
//
// It does NOT filter against the pinned graph's declaration. That would need the
// version resolved here, which StartRun deliberately does inside its own
// transaction so a concurrent publish cannot change the answer; and it is not
// needed, because ParseRunInputFor only ever reads DECLARED keys - an undeclared
// extra sits inert in the bag and reaches no prompt.
func buildWorkflowRunInput(title, description string, rawBody []byte) ([]byte, error) {
	bag := map[string]any{}
	if len(rawBody) > 0 {
		// A body that decoded into RunWorkflowTemplateRequest above may still not be
		// an OBJECT (`"a string"` decodes into a struct as an error, but `null` does
		// not), so this can fail benignly. An unreadable bag means no extra keys, not
		// a failed request: title and description already validated.
		if err := json.Unmarshal(rawBody, &bag); err != nil {
			bag = map[string]any{}
		}
	}
	out := make(map[string]any, len(bag)+2)
	for key, value := range bag {
		if runWorkflowTemplateFields[key] {
			continue
		}
		s, ok := value.(string)
		if !ok {
			continue
		}
		trimmed := sanitizeNullBytes(strings.TrimSpace(s))
		if trimmed == "" {
			// An empty declared value is the same as an absent one: ValidateRunInput
			// treats blank as missing, and carrying "" through would put an empty
			// labelled field in the agent's prompt.
			continue
		}
		if utf8.RuneCountInString(trimmed) > maxWorkflowRunDeclaredFieldLen {
			// Bounded like description, and for the same reason: this text is copied
			// onto every Step's input and into every Agent prompt. Truncated rather
			// than refused, because the field is not one this handler declared and
			// refusing would make a graph unstartable over a value the endpoint has
			// no rule for. The marker is visible so nobody mistakes the clipped value
			// for the whole story.
			trimmed = string([]rune(trimmed)[:maxWorkflowRunDeclaredFieldLen]) + "…[truncated]"
		}
		out[key] = trimmed
	}
	// Written last so a caller cannot override them through the bag.
	out["title"] = title
	out["description"] = description
	return json.Marshal(out)
}

// ---------------------------------------------------------------------------
// GET /api/workflow-runs
// ---------------------------------------------------------------------------

// ListWorkflowRuns returns the workspace's Runs, newest first.
func (h *Handler) ListWorkflowRuns(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	if _, ok := h.requireWorkspaceMember(w, r, workspaceID, "workspace not found"); !ok {
		return
	}

	var statusFilter pgtype.Text
	if s := strings.TrimSpace(r.URL.Query().Get("status")); s != "" {
		// Passed through unvalidated: the query matches on equality, so an unknown
		// status returns an empty page rather than a wrong one, and hardcoding the
		// enum here would need editing every time the state machine grows.
		statusFilter = pgtype.Text{String: s, Valid: true}
	}
	var templateFilter pgtype.UUID
	if t := strings.TrimSpace(r.URL.Query().Get("template_id")); t != "" {
		parsed, ok := parseUUIDOrBadRequest(w, t, "template_id")
		if !ok {
			return
		}
		templateFilter = parsed
	}

	limit := int32(defaultWorkflowRunPageSize)
	offset := int32(0)
	if l := r.URL.Query().Get("limit"); l != "" {
		if v, err := strconv.Atoi(l); err == nil && v > 0 {
			limit = int32(v)
		}
	}
	if limit > maxWorkflowRunPageSize {
		limit = maxWorkflowRunPageSize
	}
	if o := r.URL.Query().Get("offset"); o != "" {
		if v, err := strconv.Atoi(o); err == nil && v >= 0 {
			offset = int32(v)
		}
	}

	runs, err := h.Queries.ListWorkflowRuns(r.Context(), db.ListWorkflowRunsParams{
		WorkspaceID: wsUUID,
		Status:      statusFilter,
		TemplateID:  templateFilter,
		LimitCount:  limit,
		OffsetCount: offset,
	})
	if err != nil {
		slog.Warn("ListWorkflowRuns failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to list workflow runs")
		return
	}
	// total is the size of the whole filtered set, not of this page: a client
	// paginating needs to know there is more, and len(runs) would report the page
	// size forever.
	total, err := h.Queries.CountWorkflowRuns(r.Context(), db.CountWorkflowRunsParams{
		WorkspaceID: wsUUID,
		Status:      statusFilter,
		TemplateID:  templateFilter,
	})
	if err != nil {
		slog.Warn("CountWorkflowRuns failed", append(logger.RequestAttrs(r), "error", err)...)
		total = int64(len(runs))
	}

	sums := h.workflowRunSummaries(r.Context(), wsUUID, runs)
	resp := make([]WorkflowRunResponse, len(runs))
	for i, run := range runs {
		resp[i] = workflowRunToResponse(run, sums[uuidToString(run.ID)])
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": resp, "total": total})
}

// ---------------------------------------------------------------------------
// GET /api/workflow-runs/{id}
// ---------------------------------------------------------------------------

func (h *Handler) GetWorkflowRun(w http.ResponseWriter, r *http.Request) {
	run, ok := h.loadWorkflowRun(w, r)
	if !ok {
		return
	}
	h.writeWorkflowRunDetail(w, r, run, http.StatusOK)
}

// writeWorkflowRunDetail assembles and writes the detail response. Every
// mutating endpoint returns this same shape so a client can drop the response
// straight into its cache instead of refetching the Run it just changed.
func (h *Handler) writeWorkflowRunDetail(w http.ResponseWriter, r *http.Request, run db.WorkflowRun, status int) {
	detail, err := h.workflowRunDetail(r.Context(), run)
	if err != nil {
		slog.Warn("assemble workflow run detail failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to load the workflow run")
		return
	}
	writeJSON(w, status, detail)
}

// ---------------------------------------------------------------------------
// POST /api/workflow-runs/{id}/cancel
// ---------------------------------------------------------------------------

// CancelWorkflowRun cancels a Run, its Steps, its pending acceptances, and the
// Agent Tasks it started.
//
// The task cancellation is the point. engine.CancelRun cascades to workflow rows
// only — it deliberately does not touch agent_task_queue, because TaskService is
// canonical for task state (plan section 7). Without the second half, a cancelled
// Run's agent keeps running: burning tokens, writing files, and eventually
// reporting a result for a Run that no longer exists.
//
// Tasks are cancelled AFTER the Run, not before. Cancelling first would let the
// terminal-task hook fire against a still-running Run and take it down the
// failure-policy path — recording a rework attempt or a failure_reason for what
// was actually a user cancellation. With this order the Run is already terminal,
// its Steps are already cancelled, and the hook's own terminal-status guard makes
// each task's cancellation a no-op on workflow state.
func (h *Handler) CancelWorkflowRun(w http.ResponseWriter, r *http.Request) {
	run, ok := h.loadWorkflowRun(w, r)
	if !ok {
		return
	}
	engine, ok := h.requireWorkflowEngine(w)
	if !ok {
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	userUUID, ok := parseUUIDOrBadRequest(w, userID, "user id")
	if !ok {
		return
	}

	// Read the task ids BEFORE cancelling the Run: CancelWorkflowStepInstancesForRun
	// does not clear task_id, so the join still resolves afterwards — but reading
	// first also means a Step activated concurrently with this cancel is captured
	// rather than missed.
	taskIDs, err := h.Queries.ListActiveAgentTaskIDsForWorkflowRun(r.Context(), db.ListActiveAgentTaskIDsForWorkflowRunParams{
		RunID:       run.ID,
		WorkspaceID: run.WorkspaceID,
	})
	if err != nil {
		// Not fatal to the cancel: a Run left running because its task list could
		// not be read is worse than a Run cancelled with one straggler task. The
		// straggler's completion hits an already-terminal Step and is discarded.
		slog.Warn("list workflow run tasks for cancel failed", append(logger.RequestAttrs(r), "error", err)...)
	}

	cancelled, err := engine.CancelRun(r.Context(), run.WorkspaceID, run.ID, userUUID)
	if err != nil {
		h.writeWorkflowEngineError(w, r, err, "CancelRun")
		return
	}

	if h.TaskService != nil {
		for _, taskID := range taskIDs {
			if _, err := h.TaskService.CancelTask(r.Context(), taskID); err != nil {
				// Per-task, logged and continued: one unstoppable task must not stop
				// the others from being stopped.
				slog.Warn("cancel workflow run agent task failed",
					append(logger.RequestAttrs(r), "error", err, "task_id", uuidToString(taskID))...)
			}
		}
	}

	h.writeWorkflowRunDetail(w, r, cancelled, http.StatusOK)
}

// ReconcileWorkflowRun lets a workspace owner/admin request the same bounded,
// idempotent repair the background worker performs. It is intentionally not a
// generic state-edit endpoint.
func (h *Handler) ReconcileWorkflowRun(w http.ResponseWriter, r *http.Request) {
	run, ok := h.loadWorkflowRun(w, r)
	if !ok {
		return
	}
	if _, ok := h.requireWorkspaceRole(w, r, uuidToString(run.WorkspaceID), "workflow run not found", "owner", "admin"); !ok {
		return
	}
	engine, ok := h.requireWorkflowEngine(w)
	if !ok {
		return
	}
	if err := engine.ReconcileRun(r.Context(), run.WorkspaceID, run.ID); err != nil {
		h.writeWorkflowEngineError(w, r, err, "ReconcileRun")
		return
	}
	updated, err := h.Queries.GetWorkflowRun(r.Context(), db.GetWorkflowRunParams{ID: run.ID, WorkspaceID: run.WorkspaceID})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to reload workflow run")
		return
	}
	h.writeWorkflowRunDetail(w, r, updated, http.StatusOK)
}

// ---------------------------------------------------------------------------
// POST /api/workflow-runs/{id}/acceptance
// ---------------------------------------------------------------------------

// DecideWorkflowAcceptance records a reviewer's accept or reject.
//
// This is the seam that makes "the agent said it was done" different from "a
// human agreed it was done" (plan section 4). Accepting advances past the
// acceptance node, normally into End, which is the only path to a completed Run.
// Rejecting opens a NEW attempt at a permitted target with the reason attached,
// so the agent is told what to change.
//
// Validation of reason/target is left to the engine rather than duplicated here.
// DecideAcceptance checks the target against the PINNED node
// (node.AllowsReworkTo), and this handler has no business re-deriving that: a
// second copy would eventually disagree, and the disagreement would present as a
// reviewer's rejection being accepted by the API and then refused by the engine.
func (h *Handler) DecideWorkflowAcceptance(w http.ResponseWriter, r *http.Request) {
	run, ok := h.loadWorkflowRun(w, r)
	if !ok {
		return
	}
	engine, ok := h.requireWorkflowEngine(w)
	if !ok {
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	userUUID, ok := parseUUIDOrBadRequest(w, userID, "user id")
	if !ok {
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, workflowRunBodyLimit)
	var req DecideWorkflowAcceptanceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// The acceptance is resolved from the Run rather than taken from the request:
	// the contract has no acceptance id in the path, and accepting an id the
	// client names would let a stale UI decide a review that has since been
	// superseded by a newer attempt's gate.
	pending, err := h.pendingWorkflowAcceptance(r.Context(), run)
	if err != nil {
		slog.Warn("load pending workflow acceptance failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to load the pending acceptance")
		return
	}
	if pending == nil {
		// 409, not 404: the Run exists and is visible, it just is not waiting for a
		// decision. A 404 would read as "this run is gone".
		writeError(w, http.StatusConflict, "this workflow run has no pending acceptance")
		return
	}

	if _, err := engine.DecideAcceptance(r.Context(), workflow.DecideAcceptanceInput{
		WorkspaceID:    run.WorkspaceID,
		AcceptanceID:   pending.ID,
		Accept:         req.Accept,
		Reason:         sanitizeNullBytes(strings.TrimSpace(req.Reason)),
		ReworkTarget:   strings.TrimSpace(req.ReworkTarget),
		ReviewerUserID: userUUID,
	}); err != nil {
		h.writeWorkflowEngineError(w, r, err, "DecideAcceptance")
		return
	}

	// Re-read the Run: the decision may have completed it, opened a rework
	// attempt, or blocked it on a spent rework budget, and the caller's next
	// render depends on which. DecideAcceptance returns the acceptance row, not
	// the Run, so there is nothing to reload from.
	updated, err := h.Queries.GetWorkflowRun(r.Context(), db.GetWorkflowRunParams{
		ID:          run.ID,
		WorkspaceID: run.WorkspaceID,
	})
	if err != nil {
		slog.Warn("reload workflow run after acceptance failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "the decision was recorded but the run could not be reloaded")
		return
	}
	h.writeWorkflowRunDetail(w, r, updated, http.StatusOK)
}

// pendingWorkflowAcceptance returns the Run's one pending review, or nil.
//
// Returns nil rather than an error when nothing is pending: "not waiting for a
// decision" is an ordinary state of a Run (it is the state of every Run that has
// not reached the gate yet, and of every one that is past it).
func (h *Handler) pendingWorkflowAcceptance(ctx context.Context, run db.WorkflowRun) (*db.WorkflowAcceptance, error) {
	acceptances, err := h.Queries.ListWorkflowAcceptancesForRun(ctx, db.ListWorkflowAcceptancesForRunParams{
		RunID:       run.ID,
		WorkspaceID: run.WorkspaceID,
	})
	if err != nil {
		return nil, fmt.Errorf("list workflow acceptances: %w", err)
	}
	for _, a := range acceptances {
		if a.Status == "pending" {
			found := a
			return &found, nil
		}
	}
	return nil, nil
}
