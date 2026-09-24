package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	agentpkg "github.com/multica-ai/multica/server/pkg/agent"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

const agentBuilderInstructions = `You are Multica Agent Builder. Help the user design one practical AI agent through a short conversation.

Your job is to propose and refine configuration, never to create resources yourself. Ask only questions that materially change behavior. Prefer making a reasonable draft immediately, then ask at most two focused questions per turn.

Every response MUST end with exactly one <agent_draft> JSON block using this shape:
<agent_draft>{"name":"","description":"","instructions":"","conversation_starters":[],"model":"","skill_ids":[],"permission_scope":"private","member_ids":[]}</agent_draft>

Rules:
- The JSON must be valid, compact JSON on one physical line. Do not wrap it in Markdown fences.
- Escape every line break inside instructions as \n. Never place a literal newline inside a JSON string.
- Preserve good existing draft fields supplied in the user's message unless the user asks to change them.
- name is concise and suitable for a workspace list.
- description is one sentence, at most 200 characters.
- instructions are a complete Markdown system prompt describing role, workflow, output, and constraints.
- conversation_starters contains up to three objects with a concise label and a complete prompt. Each should demonstrate a useful first task for this specific agent; never include generic filler.
- model must be empty, preserve current_draft.model, or exactly match an id explicitly listed in AVAILABLE RUNTIME MODELS. Never use a model label as the id.
- When AVAILABLE RUNTIME MODELS is null or empty, preserve current_draft.model and never invent a model id.
- skill_ids may only contain IDs explicitly listed in AVAILABLE WORKSPACE SKILLS.
- permission_scope must be private, workspace, or members. Default to private unless the user explicitly requests sharing.
- member_ids may only contain IDs explicitly listed in AVAILABLE WORKSPACE MEMBERS, and only when permission_scope is members.
- Never request, expose, or place secrets, tokens, passwords, or environment-variable values in the draft.
- Do not claim that the agent has been created. The user must review and confirm the draft in the UI.`

const workflowBuilderInstructions = `You are Multica Workflow Builder. Turn the user's description into one practical, reviewable workflow draft. You design configuration only; never create resources yourself.

Every response MUST end with exactly one <workflow_draft> JSON block using this shape:
<workflow_draft>{"name":"","key":"","description":"","definition":{"schema_version":1,"entry_node":"input","nodes":[]}}</workflow_draft>

Rules:
- The JSON must be valid compact JSON on one physical line, without Markdown fences.
- The user message is a JSON envelope. Its available_codex_agents array is the complete AVAILABLE CODEX AGENTS catalog; its user_request is the concrete task the workflow must accept at run time.
- key must start with a lowercase letter or digit and contain only lowercase letters, digits, hyphens, and underscores.
- Use only executable node types: input, agent, acceptance, and end. Prefer a clear linear flow.
- Every node uses "key" (never "id") and "next" is always a JSON array of node keys, even when it contains one item. Agent assignment is always nested as "routing":{"strategy":"explicit","agent_id":"<listed id>"}; never put agent_id directly on a node. acceptance_criteria and rework_targets are always JSON string arrays; never use rework_target.
- Declare exactly one input node, make it entry_node, give it exactly one next edge, and use input_fields to collect the information the workflow needs.
- Every agent node must have exactly one next edge, a specific instruction, and explicit routing to an id listed in AVAILABLE CODEX AGENTS. Never invent an agent id.
- Split the work into separate agent nodes when the request names distinct responsibilities. Use distinct listed agent ids when the catalog permits it; otherwise reuse only listed ids and make each node's responsibility explicit.
- For requirement-to-implementation workflows, prefer input(task) -> analyze/split child issues -> implement the child issues -> verify by running functional checks -> acceptance -> end. The verify agent must report checks it actually ran; test or functional failure must rework the implementation agent, while an unclear or wrongly split requirement may rework the analysis agent.
- If AVAILABLE CODEX AGENTS is empty, return a valid input -> end draft and explain that a Codex agent must be created before agent steps can be assigned.
- Acceptance nodes must have exactly one next edge, non-empty acceptance_criteria, and rework_targets pointing to an earlier agent node.
- End nodes have no outgoing edges. Every node must be reachable, and ordinary next edges must be acyclic.
- Use on_failure "block" unless a bounded rework path is clearly useful. Omit fields that do not apply.
- Preserve the user's language for human-facing names, labels, descriptions, and instructions.
- Do not claim the workflow has been created. The user must review and confirm the generated draft in the UI.`

type CreateAgentBuilderSessionRequest struct {
	RuntimeID   string `json:"runtime_id"`
	Model       string `json:"model,omitempty"`
	KnotAgentID string `json:"knot_agent_id,omitempty"`
}

type CreateAgentBuilderSessionResponse struct {
	SessionID      string `json:"session_id"`
	BuilderAgentID string `json:"builder_agent_id"`
	RuntimeID      string `json:"runtime_id"`
	KnotAgentID    string `json:"knot_agent_id,omitempty"`
}

type builderKnotRuntimeConfig struct {
	Knot struct {
		AgentID string `json:"agent_id"`
	} `json:"knot"`
}

func buildBuilderRuntimeConfig(provider, rawKnotAgentID string) ([]byte, string, error) {
	knotAgentID := strings.TrimSpace(rawKnotAgentID)
	if knotAgentID == "" {
		return []byte(`{}`), "", nil
	}
	if provider != "knot-http" {
		return nil, "", fmt.Errorf("knot_agent_id requires a knot-http runtime")
	}
	if !agentpkg.LooksLikeKnotAgentID(knotAgentID) {
		return nil, "", fmt.Errorf("knot_agent_id must be a 32-character lowercase hex id from `knot-cli list-agents`")
	}
	var cfg builderKnotRuntimeConfig
	cfg.Knot.AgentID = knotAgentID
	raw, err := json.Marshal(cfg)
	if err != nil {
		return nil, "", err
	}
	return raw, knotAgentID, nil
}

func builderKnotAgentID(raw []byte) string {
	var cfg builderKnotRuntimeConfig
	if len(raw) == 0 || json.Unmarshal(raw, &cfg) != nil {
		return ""
	}
	knotAgentID := strings.TrimSpace(cfg.Knot.AgentID)
	if !agentpkg.LooksLikeKnotAgentID(knotAgentID) {
		return ""
	}
	return knotAgentID
}

// CreateAgentBuilderSession starts a private configuration conversation on an
// existing runtime. A hidden system agent is the execution carrier because the
// chat/task pipeline is intentionally agent-backed; it never appears in normal
// agent lists and cannot be selected as an assignee.
func (h *Handler) CreateAgentBuilderSession(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	var req CreateAgentBuilderSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	runtimeID := strings.TrimSpace(req.RuntimeID)
	if runtimeID == "" {
		writeError(w, http.StatusBadRequest, "runtime_id is required")
		return
	}

	workspaceUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	runtime, ok := h.resolveBuilderRuntime(w, r, workspaceID, workspaceUUID, runtimeID, "start")
	if !ok {
		return
	}
	runtimeConfig, knotAgentID, err := buildBuilderRuntimeConfig(runtime.Provider, req.KnotAgentID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	flowID := uuid.NewString()
	ownerUUID := parseUUID(userID)
	model := strings.TrimSpace(req.Model)
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start agent builder session")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)

	// FOR KEY SHARE on the workspace row before creating the builder's chat_session
	// — the creator half of the #5219 delete/create protocol, so a session cannot
	// be created into a workspace mid-delete (see LockWorkspaceForChatSessionCreate).
	if _, err := qtx.LockWorkspaceForChatSessionCreate(r.Context(), workspaceUUID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "workspace not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to lock workspace")
		return
	}

	builder, err := qtx.CreateAgentBuilder(r.Context(), db.CreateAgentBuilderParams{
		WorkspaceID:   workspaceUUID,
		Name:          fmt.Sprintf(".multica-agent-builder-%s", flowID),
		RuntimeMode:   runtime.RuntimeMode,
		RuntimeConfig: runtimeConfig,
		RuntimeID:     runtime.ID,
		OwnerID:       ownerUUID,
		Instructions:  agentBuilderInstructions,
		Model:         pgtype.Text{String: model, Valid: model != ""},
		SystemKey: pgtype.Text{
			String: agentBuilderSystemKeyPrefix + flowID,
			Valid:  true,
		},
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to prepare agent builder")
		return
	}

	session, err := qtx.CreateChatSession(r.Context(), db.CreateChatSessionParams{
		ID:          dbid.NewV7(),
		WorkspaceID: workspaceUUID,
		AgentID:     builder.ID,
		CreatorID:   ownerUUID,
		Title:       "Create an agent",
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create agent builder session")
		return
	}
	session, err = qtx.MarkChatSessionExplicitlyCreated(r.Context(), session.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to mark agent builder session explicit")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit agent builder session")
		return
	}

	writeJSON(w, http.StatusCreated, CreateAgentBuilderSessionResponse{
		SessionID:      uuidToString(session.ID),
		BuilderAgentID: uuidToString(builder.ID),
		RuntimeID:      runtimeID,
		KnotAgentID:    knotAgentID,
	})
}

// AgentBuilderSessionSummary is one unfinished agent-creation conversation.
type AgentBuilderSessionSummary struct {
	SessionID string `json:"session_id"`
	Title     string `json:"title"`
	// RuntimeID is the carrier's runtime — where this conversation actually
	// executes. The client seeds its runtime picker from it so the picker can
	// never disagree with what answers the next message (MUL-5163).
	RuntimeID   string `json:"runtime_id"`
	KnotAgentID string `json:"knot_agent_id,omitempty"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
	// LastMessageContent is the raw stored message, still in the builder's wire
	// format (the user side is a JSON envelope, the assistant side carries an
	// <agent_draft> block). Decoding is the client's job: the protocol is
	// defined by the studio and its prompt, and duplicating it here would give
	// it a second, silently divergent implementation.
	LastMessageContent string `json:"last_message_content"`
	LastMessageRole    string `json:"last_message_role"`
	LastMessageAt      string `json:"last_message_at"`
	// Draft is the stored configuration, opaque to the server (see migration
	// 252). It ships with the list rather than behind its own fetch because the
	// studio renders this list beside the conversation it switches between, so
	// the picked row's configuration must be in hand at click time. Null when
	// the conversation has only ever been driven by the AI — the client then
	// replays the last <agent_draft> block instead.
	Draft json.RawMessage `json:"draft,omitempty"`
}

type ListAgentBuilderSessionsResponse struct {
	Sessions []AgentBuilderSessionSummary `json:"sessions"`
}

// ListAgentBuilderSessions returns the caller's unfinished agent-creation
// conversations, newest activity first.
//
// This is the only way back to a builder session: they are hidden from every
// chat surface by the `kind = 'user'` agent filter, so before this endpoint the
// studio had to delete one on navigation or leak it forever. Creator-scoped
// like every other chat read — a workspace admin cannot list someone else's
// drafts, matching loadChatSessionForUser's rule.
func (h *Handler) ListAgentBuilderSessions(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}

	rows, err := h.Queries.ListAgentBuilderSessionsByCreator(r.Context(), db.ListAgentBuilderSessionsByCreatorParams{
		WorkspaceID: workspaceUUID,
		CreatorID:   parseUUID(userID),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list agent builder sessions")
		return
	}

	sessions := make([]AgentBuilderSessionSummary, 0, len(rows))
	for _, row := range rows {
		sessions = append(sessions, AgentBuilderSessionSummary{
			SessionID:          uuidToString(row.ID),
			Title:              row.Title,
			RuntimeID:          uuidToString(row.RuntimeID),
			KnotAgentID:        builderKnotAgentID(row.RuntimeConfig),
			CreatedAt:          timestampToString(row.CreatedAt),
			UpdatedAt:          timestampToString(row.UpdatedAt),
			LastMessageContent: row.LastMessageContent,
			LastMessageRole:    row.LastMessageRole,
			LastMessageAt:      timestampToString(row.LastMessageAt),
			Draft:              json.RawMessage(row.StoredDraft),
		})
	}
	writeJSON(w, http.StatusOK, ListAgentBuilderSessionsResponse{Sessions: sessions})
}

// maxAgentBuilderDraftBytes bounds one stored configuration. The largest honest
// field is the instruction markdown, which the create API itself caps well
// below this; the limit exists so a client bug cannot grow an unbounded row.
const maxAgentBuilderDraftBytes = 256 * 1024

type SaveAgentBuilderDraftRequest struct {
	Draft json.RawMessage `json:"draft"`
}

// SaveAgentBuilderDraft stores the configuration a creation conversation has
// arrived at, including the edits the user typed but has not sent.
//
// The payload is opaque (see migration 252): its shape is the studio's
// AgentDraft, validated client-side, and nothing server-side reads a field.
// Whole-object last-write-wins is correct here because a conversation has one
// editor on one screen — a field-level merge could only reconstruct a state the
// user never saw.
//
// The upsert runs under LockChatSessionForDraftWrite, the row lock the delete
// path already takes, and re-checks the session inside it. Unlocked, this
// handler's read and its write are two statements a delete can commit between:
// the client autosaves on a debounce, so a discard confirmed mid-window used to
// leave a draft hanging off a session that no longer exists — invisible to the
// UI, and reachable by no prune but the workspace teardown. agent_builder_draft
// has no chat_session FK to reject that INSERT, so the lock is what makes the
// two orderings the only ones: either the save commits first and the delete
// prunes it, or the delete commits first and the save finds nothing to write to.
func (h *Handler) SaveAgentBuilderDraft(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	var req SaveAgentBuilderDraftRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(req.Draft) == 0 {
		writeError(w, http.StatusBadRequest, "draft is required")
		return
	}
	if len(req.Draft) > maxAgentBuilderDraftBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "draft is too large")
		return
	}
	if !json.Valid(req.Draft) {
		writeError(w, http.StatusBadRequest, "draft must be valid JSON")
		return
	}

	// Creator-only, and only for a builder carrier — the same two gates the
	// runtime switch applies. Without the carrier check this would be a way to
	// hang arbitrary JSON off any chat session the caller owns. Both are decided
	// on this unlocked read: workspace, creator and carrier are immutable for a
	// given session, so nothing the lock below could observe would change them.
	session, ok := h.loadChatSessionForUser(w, r, userID, workspaceID, chi.URLParam(r, "sessionId"))
	if !ok {
		return
	}
	agent, err := h.Queries.GetAgent(r.Context(), session.AgentID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load chat agent")
		return
	}
	if !isAgentBuilderCarrier(agent) {
		writeError(w, http.StatusNotFound, "agent builder session not found")
		return
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save agent builder draft")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)

	// Everything a concurrent writer can still change about this session —
	// whether it exists at all, and whether it is still active — is decided here,
	// under the lock, on a re-read row. A save that blocked on a delete or an
	// archive resumes holding the values it read before blocking, so the earlier
	// read cannot be trusted for either.
	locked, err := qtx.LockChatSessionForDraftWrite(r.Context(), session.ID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "chat session not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to lock chat session")
		return
	}
	if locked.Status != "active" {
		writeError(w, http.StatusBadRequest, "chat session is archived")
		return
	}

	if _, err := qtx.UpsertAgentBuilderDraft(r.Context(), db.UpsertAgentBuilderDraftParams{
		ChatSessionID: locked.ID,
		WorkspaceID:   locked.WorkspaceID,
		Draft:         req.Draft,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save agent builder draft")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save agent builder draft")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type CreateWorkflowBuilderSessionRequest struct {
	RuntimeID string `json:"runtime_id"`
}

// CreateWorkflowBuilderSession starts a one-shot workflow design conversation.
// It deliberately uses the same hidden-agent/chat pipeline as Agent Builder so
// the user's authenticated Codex CLI is the model provider; no server LLM key
// and no Claude runtime can be used by this endpoint.
func (h *Handler) CreateWorkflowBuilderSession(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	var req CreateWorkflowBuilderSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	runtimeID := strings.TrimSpace(req.RuntimeID)
	if runtimeID == "" {
		writeError(w, http.StatusBadRequest, "runtime_id is required")
		return
	}

	workspaceUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	runtime, ok := h.resolveBuilderRuntime(w, r, workspaceID, workspaceUUID, runtimeID, "start")
	if !ok {
		return
	}
	if runtime.Provider != "codex" {
		writeError(w, http.StatusConflict, "workflow builder requires an online Codex runtime")
		return
	}

	flowID := uuid.NewString()
	ownerUUID := parseUUID(userID)
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start workflow builder session")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)

	if _, err := qtx.LockWorkspaceForChatSessionCreate(r.Context(), workspaceUUID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "workspace not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to lock workspace")
		return
	}

	builder, err := qtx.CreateAgentBuilder(r.Context(), db.CreateAgentBuilderParams{
		WorkspaceID:   workspaceUUID,
		Name:          fmt.Sprintf(".multica-workflow-builder-%s", flowID),
		RuntimeMode:   runtime.RuntimeMode,
		RuntimeConfig: []byte(`{}`),
		RuntimeID:     runtime.ID,
		OwnerID:       ownerUUID,
		Instructions:  workflowBuilderInstructions,
		SystemKey: pgtype.Text{
			String: fmt.Sprintf("agent_builder:workflow:%s", flowID),
			Valid:  true,
		},
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to prepare workflow builder")
		return
	}

	session, err := qtx.CreateChatSession(r.Context(), db.CreateChatSessionParams{
		WorkspaceID: workspaceUUID,
		AgentID:     builder.ID,
		CreatorID:   ownerUUID,
		Title:       "Create a workflow",
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create workflow builder session")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit workflow builder session")
		return
	}

	writeJSON(w, http.StatusCreated, CreateAgentBuilderSessionResponse{
		SessionID:      uuidToString(session.ID),
		BuilderAgentID: uuidToString(builder.ID),
		RuntimeID:      runtimeID,
	})
}

// resolveBuilderRuntime loads a runtime the caller is allowed to execute a
// builder conversation on. Shared by session create and runtime switch so both
// enforce the same three gates in the same order: it exists in this workspace,
// this member may use it (private runtimes stay owner/admin-only), and it is
// online. verb names the attempted action in the offline error so the two call
// sites read naturally.
func (h *Handler) resolveBuilderRuntime(w http.ResponseWriter, r *http.Request, workspaceID string, workspaceUUID pgtype.UUID, runtimeID, verb string) (db.AgentRuntime, bool) {
	runtimeUUID, ok := parseUUIDOrBadRequest(w, runtimeID, "runtime_id")
	if !ok {
		return db.AgentRuntime{}, false
	}
	runtime, err := h.Queries.GetAgentRuntimeForWorkspace(r.Context(), db.GetAgentRuntimeForWorkspaceParams{
		ID:          runtimeUUID,
		WorkspaceID: workspaceUUID,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid runtime_id")
		return db.AgentRuntime{}, false
	}
	member, ok := h.workspaceMember(w, r, workspaceID)
	if !ok {
		return db.AgentRuntime{}, false
	}
	if !canUseRuntimeForAgent(member, runtime) {
		writeError(w, http.StatusForbidden, "this runtime is private; only its owner can use it")
		return db.AgentRuntime{}, false
	}
	if runtime.Status != "online" {
		writeError(w, http.StatusConflict, fmt.Sprintf("runtime must be online to %s an agent builder session", verb))
		return db.AgentRuntime{}, false
	}
	return runtime, true
}

type SwitchAgentBuilderRuntimeRequest struct {
	RuntimeID string `json:"runtime_id"`
}

type SwitchAgentBuilderRuntimeResponse struct {
	RuntimeID string `json:"runtime_id"`
}

// SwitchAgentBuilderRuntime re-points a live builder conversation at another
// runtime. The live-draft runtime picker used to mutate React state only, so the
// UI could show runtime B while every subsequent message still enqueued against
// the carrier agent frozen to runtime A at session create time (MUL-5163).
//
// The rebind runs under LockChatSessionForRuntimeBind, the same row lock
// SendDirectChatMessage takes, so "no reply is in flight" and "the carrier now
// points at B" are decided in one serialised step. Without that lock a send that
// had already read runtime A could still land its task after this handler
// returned success — reproducing the exact inconsistency this endpoint exists to
// remove.
//
// chat_session.runtime_id is deliberately left pointing at the old runtime: the
// daemon only resumes a stored provider session when that pointer matches the
// claiming task's runtime, so leaving it stale is what makes B start a fresh
// provider session instead of resuming A's. Multica-side chat history and the
// draft are untouched.
func (h *Handler) SwitchAgentBuilderRuntime(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	var req SwitchAgentBuilderRuntimeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	runtimeID := strings.TrimSpace(req.RuntimeID)
	if runtimeID == "" {
		writeError(w, http.StatusBadRequest, "runtime_id is required")
		return
	}

	// Creator-only, like every other write on a chat session.
	session, ok := h.loadChatSessionForUser(w, r, userID, workspaceID, chi.URLParam(r, "sessionId"))
	if !ok {
		return
	}
	if session.Status != "active" {
		writeError(w, http.StatusBadRequest, "chat session is archived")
		return
	}

	// Only builder carriers may be rebound. A user-authored agent changes runtime
	// through the agent update path, which has its own permission model — this
	// endpoint must not become a second, weaker way in.
	agent, err := h.Queries.GetAgent(r.Context(), session.AgentID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load chat agent")
		return
	}
	if !isAgentBuilderCarrier(agent) {
		writeError(w, http.StatusNotFound, "agent builder session not found")
		return
	}

	workspaceUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	runtime, ok := h.resolveBuilderRuntime(w, r, workspaceID, workspaceUUID, runtimeID, "switch")
	if !ok {
		return
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to switch agent builder runtime")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)

	if _, err := qtx.LockChatSessionForRuntimeBind(r.Context(), session.ID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "chat session not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to lock chat session")
		return
	}

	// Checked under the lock, so a send cannot slip in behind it. A task that is
	// still queued on an offline runtime also counts as pending — the client is
	// expected to stop it first, which restores the message to the composer.
	if _, err := qtx.GetPendingChatTask(r.Context(), session.ID); err == nil {
		writeError(w, http.StatusConflict, "stop the current reply before switching runtime")
		return
	} else if !errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusInternalServerError, "failed to check pending builder task")
		return
	}

	// Model ids are per-runtime, so the carrier's model is cleared rather than
	// carried over; the new runtime resolves its own default.
	updated, err := qtx.RebindAgentBuilderRuntime(r.Context(), db.RebindAgentBuilderRuntimeParams{
		ID:          agent.ID,
		RuntimeID:   runtime.ID,
		RuntimeMode: runtime.RuntimeMode,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "agent builder session not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to switch agent builder runtime")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit agent builder runtime switch")
		return
	}

	writeJSON(w, http.StatusOK, SwitchAgentBuilderRuntimeResponse{
		RuntimeID: uuidToString(updated.RuntimeID),
	})
}

// isAgentBuilderCarrier reports whether an agent is a hidden builder execution
// carrier. Mirrors the kind/system_key guard the builder SQL statements carry, so
// the handler rejects a non-builder session before reaching the database rather
// than relying on an UPDATE matching zero rows.
func isAgentBuilderCarrier(agent db.Agent) bool {
	return agent.Kind == "system" &&
		agent.SystemKey.Valid &&
		strings.HasPrefix(agent.SystemKey.String, agentBuilderSystemKeyPrefix)
}
