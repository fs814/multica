package handler

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/logger"
	obsmetrics "github.com/multica-ai/multica/server/internal/metrics"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// Runtime CLI directory (TES-140).
//
// The panel lists "CLIs that are ready on this machine" and runs one on click.
// The security model is structural, not filter-based:
//
//   - The request carries a registry key plus typed parameter values. There is
//     no field for a command, an argv, or a shell string, so "escape the
//     whitelist by shaping a string" is not a reachable state.
//   - The authoritative whitelist is the machine-local registry file owned by
//     the daemon. The server validates shape only and never resolves an
//     executable path — a compromised server still cannot name a binary.
//   - Authorization is the runtime owner, matching requireRuntimeLocalSkillAccess.
//     Reading files off the owner's machine already requires ownership;
//     executing commands there is strictly more dangerous, so it must not be
//     laxer. Workspace owners/admins are not exempt.
//
// This file holds the server-side request lifecycle. The daemon-side registry
// read, argv construction and process execution live in
// server/internal/daemon/runtime_cli.go.

type RuntimeCLIRequestStatus string

const (
	RuntimeCLIPending   RuntimeCLIRequestStatus = "pending"
	RuntimeCLIRunning   RuntimeCLIRequestStatus = "running"
	RuntimeCLICompleted RuntimeCLIRequestStatus = "completed"
	RuntimeCLIFailed    RuntimeCLIRequestStatus = "failed"
	RuntimeCLITimeout   RuntimeCLIRequestStatus = "timeout"
)

const (
	// cliListPendingTimeout bounds how long a registry-discovery request may
	// sit unclaimed. The daemon is nudged immediately via requestDaemonPendingWork,
	// so this only has to cover a missed nudge plus the next scheduled tick.
	cliListPendingTimeout = 30 * time.Second
	cliListRunningTimeout = 60 * time.Second

	// cliRunPendingTimeout mirrors the list queue. The running bound is
	// per-request because a registry entry declares its own timeout_seconds:
	// the server stores the clamped value the caller asked for plus a grace
	// margin, and the daemon independently clamps against its own registry.
	// A caller inflating the hint can therefore only make the SERVER wait
	// longer; it cannot make the daemon run longer.
	cliRunPendingTimeout   = 30 * time.Second
	cliRunRunningGrace     = 15 * time.Second
	cliRunDefaultTimeoutS  = 60
	cliRunMaxTimeoutS      = 600
	cliStoreRetention      = 10 * time.Minute
	cliMaxParams           = 16
	cliMaxParamValueLen    = 4096
	cliActivityInitiated   = "runtime_cli_run_initiated"
	cliActivityReported    = "runtime_cli_run_reported"
	cliAuditMaxParamLength = 256
)

// cliKeyPattern is the only shape the server accepts for a registry key. The
// daemon enforces the same pattern when it reads the registry, so a key that
// passes here is still re-checked against the machine-local file.
var cliKeyPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// cliParamNamePattern mirrors the registry's parameter naming so a malformed
// name is rejected before it reaches the daemon.
var cliParamNamePattern = regexp.MustCompile(`^[a-z0-9_]{1,32}$`)

// RuntimeCLIParamDescriptor is the redacted, non-secret description of one
// parameter slot. The daemon reports these so the panel can render a form
// without the server ever learning an executable path or an env value.
type RuntimeCLIParamDescriptor struct {
	Name     string   `json:"name"`
	Type     string   `json:"type"` // "enum" | "string"
	Required bool     `json:"required"`
	Values   []string `json:"values,omitempty"`
	MaxLen   int      `json:"max_len,omitempty"`
}

// RuntimeCLISummary is one registry entry as shown in the panel. It is
// deliberately non-secret: no executable path, no interpreter, no env value,
// no content hash.
type RuntimeCLISummary struct {
	Key            string                      `json:"key"`
	Label          string                      `json:"label"`
	Description    string                      `json:"description,omitempty"`
	Params         []RuntimeCLIParamDescriptor `json:"params,omitempty"`
	TimeoutSeconds int                         `json:"timeout_seconds"`
	MaxOutputBytes int                         `json:"max_output_bytes"`
	// Available is false when the entry is declared but its pinned executable
	// is not present or does not match its recorded hash. The panel shows it
	// and explains why, rather than hiding a broken install.
	Available bool `json:"available"`
	// UnavailableReason is a short, non-secret explanation ("executable not found",
	// "executable hash mismatch", …). Empty when Available.
	UnavailableReason string `json:"unavailable_reason,omitempty"`
}

// RuntimeCLIListRequest tracks one registry-discovery round trip.
type RuntimeCLIListRequest struct {
	ID        string                  `json:"id"`
	RuntimeID string                  `json:"runtime_id"`
	Status    RuntimeCLIRequestStatus `json:"status"`
	CLIs      []RuntimeCLISummary     `json:"clis,omitempty"`
	// RegistryPath is the absolute path of the machine-local registry file the
	// daemon read. It is machine configuration, not a secret, and the panel
	// shows it so "where do I add an entry" is answerable without docs.
	RegistryPath string     `json:"registry_path,omitempty"`
	Error        string     `json:"error,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	RunStartedAt *time.Time `json:"-"`
}

// CLIRunResult is what the daemon reports back after one execution.
type CLIRunResult struct {
	Output       string   `json:"output"`
	Truncated    bool     `json:"truncated"`
	ExitCode     *int     `json:"exit_code,omitempty"`
	DurationMs   int64    `json:"duration_ms"`
	OutputBytes  int64    `json:"output_bytes"`
	ResolvedArgv []string `json:"resolved_argv,omitempty"`
}

// RuntimeCLIRunRequest tracks one CLI execution from enqueue to terminal state.
type RuntimeCLIRunRequest struct {
	ID          string                  `json:"id"`
	RuntimeID   string                  `json:"runtime_id"`
	WorkspaceID string                  `json:"workspace_id,omitempty"`
	CLIKey      string                  `json:"cli_key"`
	Params      map[string]string       `json:"params,omitempty"`
	Status      RuntimeCLIRequestStatus `json:"status"`

	Output       string   `json:"output,omitempty"`
	Truncated    bool     `json:"truncated,omitempty"`
	ExitCode     *int     `json:"exit_code,omitempty"`
	DurationMs   int64    `json:"duration_ms,omitempty"`
	OutputBytes  int64    `json:"output_bytes,omitempty"`
	ResolvedArgv []string `json:"resolved_argv,omitempty"`
	Error        string   `json:"error,omitempty"`

	CreatedAt    time.Time     `json:"created_at"`
	UpdatedAt    time.Time     `json:"updated_at"`
	InitiatorID  string        `json:"-"`
	RunningLimit time.Duration `json:"-"`
	RunStartedAt *time.Time    `json:"-"`
}

// CLIRunRequestInput carries the fields needed to enqueue one execution.
type CLIRunRequestInput struct {
	RuntimeID       string
	WorkspaceID     string
	CLIKey          string
	Params          map[string]string
	InitiatorUserID string
	// RunningTimeout is the server-side running bound, already clamped and
	// already carrying its grace margin.
	RunningTimeout time.Duration
}

// RuntimeCLIListStore tracks registry-discovery requests. Same stateless
// contract as the other heartbeat-carried stores: the server keeps nothing in
// process memory that a sibling node needs to see.
type RuntimeCLIListStore interface {
	Create(ctx context.Context, runtimeID string) (*RuntimeCLIListRequest, error)
	Get(ctx context.Context, id string) (*RuntimeCLIListRequest, error)
	HasPending(ctx context.Context, runtimeID string) (bool, error)
	PopPending(ctx context.Context, runtimeID string) (*RuntimeCLIListRequest, error)
	Complete(ctx context.Context, id string, clis []RuntimeCLISummary, registryPath string) error
	Fail(ctx context.Context, id string, errMsg string) error
}

// RuntimeCLIRunStore tracks CLI execution requests.
type RuntimeCLIRunStore interface {
	Create(ctx context.Context, input CLIRunRequestInput) (*RuntimeCLIRunRequest, error)
	Get(ctx context.Context, id string) (*RuntimeCLIRunRequest, error)
	HasPending(ctx context.Context, runtimeID string) (bool, error)
	PopPending(ctx context.Context, runtimeID string) (*RuntimeCLIRunRequest, error)
	Complete(ctx context.Context, id string, result CLIRunResult) error
	Fail(ctx context.Context, id string, errMsg string) error
}

func runtimeCLIRequestTerminal(status RuntimeCLIRequestStatus) bool {
	return status == RuntimeCLICompleted || status == RuntimeCLIFailed || status == RuntimeCLITimeout
}

func applyCLIListTimeout(req *RuntimeCLIListRequest, now time.Time) bool {
	switch req.Status {
	case RuntimeCLIPending:
		if now.Sub(req.CreatedAt) > cliListPendingTimeout {
			req.Status = RuntimeCLITimeout
			req.Error = "daemon did not respond within 30 seconds"
			req.UpdatedAt = now
			return true
		}
	case RuntimeCLIRunning:
		if req.RunStartedAt != nil && now.Sub(*req.RunStartedAt) > cliListRunningTimeout {
			req.Status = RuntimeCLITimeout
			req.Error = "daemon did not finish within 60 seconds"
			req.UpdatedAt = now
			return true
		}
	}
	return false
}

func applyCLIRunTimeout(req *RuntimeCLIRunRequest, now time.Time) bool {
	switch req.Status {
	case RuntimeCLIPending:
		if now.Sub(req.CreatedAt) > cliRunPendingTimeout {
			req.Status = RuntimeCLITimeout
			req.Error = "daemon did not claim the run within 30 seconds"
			req.UpdatedAt = now
			return true
		}
	case RuntimeCLIRunning:
		limit := req.RunningLimit
		if limit <= 0 {
			limit = time.Duration(cliRunDefaultTimeoutS)*time.Second + cliRunRunningGrace
		}
		if req.RunStartedAt != nil && now.Sub(*req.RunStartedAt) > limit {
			req.Status = RuntimeCLITimeout
			req.Error = "the CLI did not finish within its declared timeout"
			req.UpdatedAt = now
			return true
		}
	}
	return false
}

// clampCLIRunTimeout turns an optional client hint into the server's running
// bound. The hint is advisory: the daemon clamps against its own registry, so
// a hostile hint buys nothing beyond a longer server-side wait.
func clampCLIRunTimeout(hintSeconds int) time.Duration {
	if hintSeconds <= 0 {
		hintSeconds = cliRunDefaultTimeoutS
	}
	if hintSeconds > cliRunMaxTimeoutS {
		hintSeconds = cliRunMaxTimeoutS
	}
	return time.Duration(hintSeconds)*time.Second + cliRunRunningGrace
}

// ---------------------------------------------------------------------------
// In-memory stores (single node: local dev and the in-process test suite)
// ---------------------------------------------------------------------------

type InMemoryRuntimeCLIListStore struct {
	mu       sync.Mutex
	requests map[string]*RuntimeCLIListRequest
}

func NewInMemoryRuntimeCLIListStore() *InMemoryRuntimeCLIListStore {
	return &InMemoryRuntimeCLIListStore{requests: make(map[string]*RuntimeCLIListRequest)}
}

func (s *InMemoryRuntimeCLIListStore) Create(_ context.Context, runtimeID string) (*RuntimeCLIListRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	for id, req := range s.requests {
		if now.Sub(req.CreatedAt) > cliStoreRetention {
			delete(s.requests, id)
		}
	}
	req := &RuntimeCLIListRequest{
		ID:        randomID(),
		RuntimeID: runtimeID,
		Status:    RuntimeCLIPending,
		CreatedAt: now,
		UpdatedAt: now,
	}
	s.requests[req.ID] = req
	return req, nil
}

func (s *InMemoryRuntimeCLIListStore) Get(_ context.Context, id string) (*RuntimeCLIListRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	req, ok := s.requests[id]
	if !ok {
		return nil, nil
	}
	applyCLIListTimeout(req, time.Now())
	return req, nil
}

func (s *InMemoryRuntimeCLIListStore) HasPending(_ context.Context, runtimeID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for _, req := range s.requests {
		applyCLIListTimeout(req, now)
		if req.RuntimeID == runtimeID && req.Status == RuntimeCLIPending {
			return true, nil
		}
	}
	return false, nil
}

func (s *InMemoryRuntimeCLIListStore) PopPending(_ context.Context, runtimeID string) (*RuntimeCLIListRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var oldest *RuntimeCLIListRequest
	now := time.Now()
	for _, req := range s.requests {
		applyCLIListTimeout(req, now)
		if req.RuntimeID == runtimeID && req.Status == RuntimeCLIPending {
			if oldest == nil || req.CreatedAt.Before(oldest.CreatedAt) {
				oldest = req
			}
		}
	}
	if oldest != nil {
		oldest.Status = RuntimeCLIRunning
		started := now
		oldest.RunStartedAt = &started
		oldest.UpdatedAt = now
	}
	return oldest, nil
}

func (s *InMemoryRuntimeCLIListStore) Complete(_ context.Context, id string, clis []RuntimeCLISummary, registryPath string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if req, ok := s.requests[id]; ok {
		req.Status = RuntimeCLICompleted
		req.CLIs = clis
		req.RegistryPath = registryPath
		req.UpdatedAt = time.Now()
	}
	return nil
}

func (s *InMemoryRuntimeCLIListStore) Fail(_ context.Context, id string, errMsg string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if req, ok := s.requests[id]; ok {
		req.Status = RuntimeCLIFailed
		req.Error = errMsg
		req.UpdatedAt = time.Now()
	}
	return nil
}

type InMemoryRuntimeCLIRunStore struct {
	mu       sync.Mutex
	requests map[string]*RuntimeCLIRunRequest
}

func NewInMemoryRuntimeCLIRunStore() *InMemoryRuntimeCLIRunStore {
	return &InMemoryRuntimeCLIRunStore{requests: make(map[string]*RuntimeCLIRunRequest)}
}

func (s *InMemoryRuntimeCLIRunStore) Create(_ context.Context, input CLIRunRequestInput) (*RuntimeCLIRunRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	for id, req := range s.requests {
		if now.Sub(req.CreatedAt) > cliStoreRetention {
			delete(s.requests, id)
		}
	}
	// One run at a time per runtime: concurrent CLIs on the same machine
	// compete for the same resources and make the panel's output attribution
	// ambiguous. Mirrors errUpdateInProgress.
	for _, req := range s.requests {
		if req.RuntimeID == input.RuntimeID && (req.Status == RuntimeCLIPending || req.Status == RuntimeCLIRunning) {
			return nil, errCLIRunInProgress
		}
	}
	req := &RuntimeCLIRunRequest{
		ID:           randomID(),
		RuntimeID:    input.RuntimeID,
		WorkspaceID:  input.WorkspaceID,
		CLIKey:       input.CLIKey,
		Params:       input.Params,
		Status:       RuntimeCLIPending,
		CreatedAt:    now,
		UpdatedAt:    now,
		InitiatorID:  input.InitiatorUserID,
		RunningLimit: input.RunningTimeout,
	}
	s.requests[req.ID] = req
	return req, nil
}

var errCLIRunInProgress = &cliError{msg: "a CLI run is already in progress for this runtime"}

type cliError struct{ msg string }

func (e *cliError) Error() string { return e.msg }

func (s *InMemoryRuntimeCLIRunStore) Get(_ context.Context, id string) (*RuntimeCLIRunRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	req, ok := s.requests[id]
	if !ok {
		return nil, nil
	}
	applyCLIRunTimeout(req, time.Now())
	return req, nil
}

func (s *InMemoryRuntimeCLIRunStore) HasPending(_ context.Context, runtimeID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for _, req := range s.requests {
		applyCLIRunTimeout(req, now)
		if req.RuntimeID == runtimeID && req.Status == RuntimeCLIPending {
			return true, nil
		}
	}
	return false, nil
}

func (s *InMemoryRuntimeCLIRunStore) PopPending(_ context.Context, runtimeID string) (*RuntimeCLIRunRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var oldest *RuntimeCLIRunRequest
	now := time.Now()
	for _, req := range s.requests {
		applyCLIRunTimeout(req, now)
		if req.RuntimeID == runtimeID && req.Status == RuntimeCLIPending {
			if oldest == nil || req.CreatedAt.Before(oldest.CreatedAt) {
				oldest = req
			}
		}
	}
	if oldest != nil {
		oldest.Status = RuntimeCLIRunning
		started := now
		oldest.RunStartedAt = &started
		oldest.UpdatedAt = now
	}
	return oldest, nil
}

func (s *InMemoryRuntimeCLIRunStore) Complete(_ context.Context, id string, result CLIRunResult) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if req, ok := s.requests[id]; ok {
		req.Status = RuntimeCLICompleted
		req.Output = result.Output
		req.Truncated = result.Truncated
		req.ExitCode = result.ExitCode
		req.DurationMs = result.DurationMs
		req.OutputBytes = result.OutputBytes
		req.ResolvedArgv = result.ResolvedArgv
		req.UpdatedAt = time.Now()
	}
	return nil
}

func (s *InMemoryRuntimeCLIRunStore) Fail(_ context.Context, id string, errMsg string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if req, ok := s.requests[id]; ok {
		req.Status = RuntimeCLIFailed
		req.Error = errMsg
		req.UpdatedAt = time.Now()
	}
	return nil
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

// InitiateListCLIs enqueues a registry-discovery request for one runtime.
// Owner-only: the registry file is machine configuration.
func (h *Handler) InitiateListCLIs(w http.ResponseWriter, r *http.Request) {
	runtimeID := chi.URLParam(r, "runtimeId")
	rt, ok := h.requireRuntimeLocalSkillAccess(w, r, obsmetrics.RuntimeLookupSourceRuntimeAPI, runtimeID)
	if !ok {
		return
	}
	if rt.status != "online" {
		writeError(w, http.StatusServiceUnavailable, "runtime is offline")
		return
	}

	req, err := h.CLIListStore.Create(r.Context(), rt.runtimeID)
	if err != nil {
		slog.Error("CLIListStore Create failed", "error", err, "runtime_id", rt.runtimeID)
		writeError(w, http.StatusInternalServerError, "failed to enqueue the CLI registry request")
		return
	}
	h.requestDaemonPendingWork(rt.runtimeID, protocol.PendingWorkKindCLIList)
	writeJSON(w, http.StatusOK, req)
}

// GetCLIListRequest returns the state of a registry-discovery request.
func (h *Handler) GetCLIListRequest(w http.ResponseWriter, r *http.Request) {
	runtimeID := chi.URLParam(r, "runtimeId")
	rt, ok := h.requireRuntimeLocalSkillAccess(w, r, obsmetrics.RuntimeLookupSourceRuntimeCLIPoll, runtimeID)
	if !ok {
		return
	}

	req, err := h.CLIListStore.Get(r.Context(), chi.URLParam(r, "requestId"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load the CLI registry request")
		return
	}
	if req == nil || req.RuntimeID != rt.runtimeID {
		writeError(w, http.StatusNotFound, "request not found")
		return
	}
	writeJSON(w, http.StatusOK, req)
}

// CreateRuntimeCLIRunRequest is the run body. It has no field for an
// executable, an argv, or a shell string — the whitelist is the registry key,
// and "bypass the whitelist" is not expressible in this shape.
type CreateRuntimeCLIRunRequest struct {
	Params map[string]string `json:"params,omitempty"`
	// TimeoutSeconds is an advisory hint used only to size the server-side
	// running bound. The daemon clamps against its own registry.
	TimeoutSeconds int `json:"timeout_seconds,omitempty"`
}

// InitiateCLIRun enqueues one execution of a registry entry.
func (h *Handler) InitiateCLIRun(w http.ResponseWriter, r *http.Request) {
	runtimeID := chi.URLParam(r, "runtimeId")
	rt, ok := h.requireRuntimeLocalSkillAccess(w, r, obsmetrics.RuntimeLookupSourceRuntimeAPI, runtimeID)
	if !ok {
		return
	}
	if rt.status != "online" {
		writeError(w, http.StatusServiceUnavailable, "runtime is offline")
		return
	}

	cliKey := strings.TrimSpace(chi.URLParam(r, "cliKey"))
	if !cliKeyPattern.MatchString(cliKey) {
		writeError(w, http.StatusBadRequest, "invalid cli_key")
		return
	}

	var body CreateRuntimeCLIRunRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	params, ok := sanitizeCLIParams(body.Params)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid params")
		return
	}

	initiatorID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	// Audit before dispatch, fail-closed. A trigger we cannot record is
	// indistinguishable from an unaudited one, so it must not proceed —
	// same contract as the agent-env reveal.
	details, _ := json.Marshal(map[string]any{
		"runtime_id":   rt.runtimeID,
		"cli_key":      cliKey,
		"params":       auditParams(params),
		"initiator":    map[string]any{"type": "member", "id": initiatorID},
		"executor":     cliExecutorIdentity(rt),
		"cli_registry": "machine-local",
	})
	if _, err := h.Queries.CreateActivity(r.Context(), db.CreateActivityParams{
		ID:          dbid.NewV7(),
		WorkspaceID: parseUUID(rt.workspaceID),
		IssueID:     pgtype.UUID{}, // a CLI run is not tied to an issue
		ActorType:   pgtype.Text{String: "member", Valid: true},
		ActorID:     parseUUID(initiatorID),
		Action:      cliActivityInitiated,
		Details:     details,
	}); err != nil {
		slog.Error("runtime_cli_run_initiated audit write failed; refusing to dispatch",
			append(logger.RequestAttrs(r), "error", err, "runtime_id", rt.runtimeID, "cli_key", cliKey)...)
		writeError(w, http.StatusInternalServerError, "audit log write failed; refusing to run a CLI without a recorded trigger")
		return
	}

	run, err := h.CLIRunStore.Create(r.Context(), CLIRunRequestInput{
		RuntimeID:       rt.runtimeID,
		WorkspaceID:     rt.workspaceID,
		CLIKey:          cliKey,
		Params:          params,
		InitiatorUserID: initiatorID,
		RunningTimeout:  clampCLIRunTimeout(body.TimeoutSeconds),
	})
	if err != nil {
		if err == errCLIRunInProgress {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		slog.Error("CLIRunStore Create failed", "error", err, "runtime_id", rt.runtimeID)
		writeError(w, http.StatusInternalServerError, "failed to start the CLI run")
		return
	}
	h.requestDaemonPendingWork(rt.runtimeID, protocol.PendingWorkKindCLIRun)
	writeJSON(w, http.StatusOK, run)
}

// GetCLIRun returns the state of one execution.
func (h *Handler) GetCLIRun(w http.ResponseWriter, r *http.Request) {
	runtimeID := chi.URLParam(r, "runtimeId")
	rt, ok := h.requireRuntimeLocalSkillAccess(w, r, obsmetrics.RuntimeLookupSourceRuntimeCLIPoll, runtimeID)
	if !ok {
		return
	}

	run, err := h.CLIRunStore.Get(r.Context(), chi.URLParam(r, "runId"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load the CLI run")
		return
	}
	if run == nil || run.RuntimeID != rt.runtimeID {
		writeError(w, http.StatusNotFound, "run not found")
		return
	}
	writeJSON(w, http.StatusOK, run)
}

// ReportCLIListResult receives the daemon's redacted registry listing.
func (h *Handler) ReportCLIListResult(w http.ResponseWriter, r *http.Request) {
	runtimeID := chi.URLParam(r, "runtimeId")
	if _, ok := h.requireDaemonRuntimeAccess(w, r, runtimeID); !ok {
		return
	}

	requestID := chi.URLParam(r, "requestId")
	existing, err := h.CLIListStore.Get(r.Context(), requestID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load the CLI registry request")
		return
	}
	if existing == nil || existing.RuntimeID != runtimeID {
		writeError(w, http.StatusNotFound, "request not found")
		return
	}
	if runtimeCLIRequestTerminal(existing.Status) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}

	var body struct {
		Status       string              `json:"status"`
		CLIs         []RuntimeCLISummary `json:"clis"`
		RegistryPath string              `json:"registry_path"`
		Error        string              `json:"error"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	switch body.Status {
	case "completed":
		if err := h.CLIListStore.Complete(r.Context(), requestID, body.CLIs, body.RegistryPath); err != nil {
			slog.Error("CLIListStore Complete failed", "error", err, "request_id", requestID)
			writeError(w, http.StatusInternalServerError, "failed to persist the CLI registry listing")
			return
		}
	case "failed":
		if err := h.CLIListStore.Fail(r.Context(), requestID, body.Error); err != nil {
			slog.Error("CLIListStore Fail failed", "error", err, "request_id", requestID)
			writeError(w, http.StatusInternalServerError, "failed to persist the CLI registry failure")
			return
		}
	default:
		writeError(w, http.StatusBadRequest, "invalid status: "+body.Status)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ReportCLIRunResult receives the daemon's execution result. The payload
// carries resolved_argv because the server never knew the real command line —
// without it the audit trail would record an intent rather than what ran.
func (h *Handler) ReportCLIRunResult(w http.ResponseWriter, r *http.Request) {
	runtimeID := chi.URLParam(r, "runtimeId")
	if _, ok := h.requireDaemonRuntimeAccess(w, r, runtimeID); !ok {
		return
	}

	runID := chi.URLParam(r, "runId")
	existing, err := h.CLIRunStore.Get(r.Context(), runID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load the CLI run")
		return
	}
	if existing == nil || existing.RuntimeID != runtimeID {
		writeError(w, http.StatusNotFound, "run not found")
		return
	}
	if runtimeCLIRequestTerminal(existing.Status) {
		slog.Debug("ignoring stale CLI run report", "runtime_id", runtimeID, "run_id", runID, "status", existing.Status)
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}

	var body struct {
		Status       string   `json:"status"`
		Output       string   `json:"output"`
		Truncated    bool     `json:"truncated"`
		ExitCode     *int     `json:"exit_code"`
		DurationMs   int64    `json:"duration_ms"`
		OutputBytes  int64    `json:"output_bytes"`
		ResolvedArgv []string `json:"resolved_argv"`
		Error        string   `json:"error"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	switch body.Status {
	case "completed":
		if err := h.CLIRunStore.Complete(r.Context(), runID, CLIRunResult{
			Output:       body.Output,
			Truncated:    body.Truncated,
			ExitCode:     body.ExitCode,
			DurationMs:   body.DurationMs,
			OutputBytes:  body.OutputBytes,
			ResolvedArgv: body.ResolvedArgv,
		}); err != nil {
			slog.Error("CLIRunStore Complete failed", "error", err, "run_id", runID)
			writeError(w, http.StatusInternalServerError, "failed to persist the CLI run result")
			return
		}
	case "failed":
		if err := h.CLIRunStore.Fail(r.Context(), runID, body.Error); err != nil {
			slog.Error("CLIRunStore Fail failed", "error", err, "run_id", runID)
			writeError(w, http.StatusInternalServerError, "failed to persist the CLI run failure")
			return
		}
	default:
		writeError(w, http.StatusBadRequest, "invalid status: "+body.Status)
		return
	}

	// Best-effort audit of the reported outcome. The row records both
	// identities: who clicked (initiator) and which machine identity executed
	// (executor) — they are different principals and conflating them makes an
	// incident unreconstructable after the fact.
	details, _ := json.Marshal(map[string]any{
		"run_id":        runID,
		"runtime_id":    runtimeID,
		"cli_key":       existing.CLIKey,
		"params":        auditParams(existing.Params),
		"resolved_argv": body.ResolvedArgv,
		"exit_code":     body.ExitCode,
		"duration_ms":   body.DurationMs,
		"output_bytes":  body.OutputBytes,
		"truncated":     body.Truncated,
		"status":        body.Status,
		"initiator":     map[string]any{"type": "member", "id": existing.InitiatorID},
		"executor":      map[string]any{"type": "runtime", "id": runtimeID},
	})
	if _, err := h.Queries.CreateActivity(r.Context(), db.CreateActivityParams{
		ID:          dbid.NewV7(),
		WorkspaceID: parseUUID(existing.WorkspaceID),
		IssueID:     pgtype.UUID{},
		ActorType:   pgtype.Text{String: "system", Valid: true},
		ActorID:     parseUUID(runtimeID),
		Action:      cliActivityReported,
		Details:     details,
	}); err != nil {
		// The run already happened; failing the report would strand the record
		// in "running" until timeout and lose the output the user is waiting
		// for. Log loudly instead.
		slog.Error("runtime_cli_run_reported audit write failed", "error", err, "run_id", runID, "runtime_id", runtimeID)
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// cliExecutorIdentity names the principal that actually runs the command: the
// runtime, not the member who clicked. The registry is authoritative on that
// machine, so the machine's runtime identity is the executor.
func cliExecutorIdentity(rt runtimeIDAndWorkspace) map[string]any {
	return map[string]any{
		"type":         "runtime",
		"id":           rt.runtimeID,
		"workspace_id": rt.workspaceID,
		"provider":     rt.provider,
		"owner_id":     rt.ownerID,
	}
}

// sanitizeCLIParams enforces shape only. Value semantics (enum membership,
// length, pattern) belong to the daemon, which owns the authoritative
// registry; duplicating them here would create a second source of truth that
// can drift from the machine.
func sanitizeCLIParams(in map[string]string) (map[string]string, bool) {
	if len(in) > cliMaxParams {
		return nil, false
	}
	if len(in) == 0 {
		return map[string]string{}, true
	}
	out := make(map[string]string, len(in))
	for name, value := range in {
		if !cliParamNamePattern.MatchString(name) {
			return nil, false
		}
		if len(value) > cliMaxParamValueLen {
			return nil, false
		}
		out[name] = value
	}
	return out, true
}

// auditParams truncates values for the audit row. Parameter values are
// declared non-secret by construction (secrets belong in env_file), but an
// audit row is not the place for an unbounded blob either.
func auditParams(params map[string]string) map[string]string {
	if len(params) == 0 {
		return nil
	}
	names := make([]string, 0, len(params))
	for name := range params {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make(map[string]string, len(params))
	for _, name := range names {
		value := params[name]
		if len(value) > cliAuditMaxParamLength {
			value = value[:cliAuditMaxParamLength] + "…(truncated)"
		}
		out[name] = value
	}
	return out
}
