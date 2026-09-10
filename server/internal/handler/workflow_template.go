package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/logger"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/workflow"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

// WorkflowTemplateResponse is the list-shaped view of a template. The graph is
// deliberately absent: a definition is up to 256KB of JSONB (see migration 232)
// and the list page only needs enough to render a row, so shipping every graph
// on every list would put the largest payload in the app on the most-visited
// path.
type WorkflowTemplateResponse struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	Key         string `json:"key"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Status      string `json:"status"`
	// CurrentVersion is null until the first publish - a template with no
	// published version cannot start a Run, and the UI needs to tell those two
	// states apart rather than showing version 0.
	CurrentVersion *int32 `json:"current_version"`
	// IsBuiltin marks a platform-provided template (seeded, never authored by a
	// member) so the UI can explain why a template nobody created is present and
	// hide destructive actions the server will refuse anyway.
	IsBuiltin bool `json:"is_builtin"`
	// NodeCount is derived from the effective definition. It is a display hint,
	// not a contract: see workflowNodeCount for why an unparseable graph reports
	// 0 instead of failing the request.
	NodeCount int `json:"node_count"`
	// Revision is the optimistic-concurrency token for all editable template
	// fields, including the mutable draft definition.
	Revision  int64  `json:"revision"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// WorkflowTemplateVersionEntry is one row of the version history. The
// definition is intentionally omitted: only the selected graph is returned on
// the detail response (effective published by default, newest draft for the
// editor), because a template with a long edit history would otherwise return
// every graph it ever had.
type WorkflowTemplateVersionEntry struct {
	ID      string `json:"id"`
	Version int32  `json:"version"`
	Status  string `json:"status"`
	// PublishedAt is null for drafts. A published version is immutable, so this
	// timestamp is also the moment its graph froze.
	PublishedAt *string `json:"published_at"`
}

// WorkflowTemplateDetailResponse is the list shape plus the graph and the
// version history.
type WorkflowTemplateDetailResponse struct {
	WorkflowTemplateResponse
	// Definition is emitted as the raw stored JSONB rather than a re-marshalled
	// Definition struct. A published version is immutable and in-flight Runs
	// resolve node semantics through exactly these bytes; round-tripping them
	// through the Go struct would let an additive field change silently alter
	// what the API says a Run is executing.
	Definition json.RawMessage                `json:"definition"`
	Versions   []WorkflowTemplateVersionEntry `json:"versions"`
}

// CreateWorkflowTemplateRequest creates a template together with its first
// draft version. The two are inseparable on purpose: a template with no version
// has no graph, so it could neither be validated nor started, and would just be
// a broken row for someone else to discover.
type CreateWorkflowTemplateRequest struct {
	Key         string          `json:"key"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Definition  json.RawMessage `json:"definition"`
}

// UpdateWorkflowTemplateRequest is the draft-save body. Every field is
// optional and independent: the editor saves a rename without shipping the
// graph (and a graph without touching the name), so an absent field means
// "leave it alone" rather than "clear it".
//
// Definition is json.RawMessage rather than *workflow.Definition so the
// omitted case is distinguishable without a second decode pass, and so the
// bytes reach ParseDefinition unaltered - the strict parser is what rejects a
// typo'd field, and pre-decoding into a struct here would silently drop it.
type UpdateWorkflowTemplateRequest struct {
	Name        *string         `json:"name"`
	Description *string         `json:"description"`
	Definition  json.RawMessage `json:"definition"`
	Revision    *int64          `json:"revision"`
}

// ValidateWorkflowDefinitionRequest is the body of the standalone check that
// backs the editor's validate button. It carries no template id: the graph
// under the author's cursor may not correspond to anything stored yet.
type ValidateWorkflowDefinitionRequest struct {
	Definition json.RawMessage `json:"definition"`
}

// ValidateWorkflowDefinitionResponse is a 200 in both directions. The endpoint
// answers a question and writes nothing, so "your graph is wrong" is a
// successful answer, not a failed request - a 422 here would make every
// keystroke-triggered check look like a client error in logs and metrics.
//
// Messages is always a non-nil array so a client can iterate it without a null
// check.
type ValidateWorkflowDefinitionResponse struct {
	Valid    bool     `json:"valid"`
	Messages []string `json:"messages"`
}

// workflowValidationResponse is the 422 body. The messages are the whole point
// of the status code: the template editor points at the offending node, and
// "invalid definition" alone would force the author to guess.
type workflowValidationResponse struct {
	Error    string   `json:"error"`
	Messages []string `json:"messages"`
}

// emptyWorkflowDefinition is what the detail endpoint reports when a template
// somehow has no version row. It is a well-formed (if unrunnable) graph object
// so a client can render "no nodes" instead of crashing on a null definition.
var emptyWorkflowDefinition = json.RawMessage(`{"schema_version":1,"entry_node":"","nodes":[],"limits":{}}`)

// Keys are an external contract: plan section 9 resolves templates by key for
// intake, and idx_workflow_template_ws_key is case-insensitive. Restricting the
// alphabet up front keeps a key URL-safe and keeps "Bug Fix" and "bug-fix" from
// looking like two spellings of one identifier.
var workflowTemplateKeyRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

const (
	maxWorkflowTemplateKeyLen  = 128
	maxWorkflowTemplateNameLen = 200
	// workflowTemplateBodyLimit caps the request body on every workflow write.
	// Migration 232 bounds a STORED definition at 256KB via a CHECK, but that
	// only fires after the whole body has been read and decoded, so it is not a
	// defence against an oversized upload â€” and /validate never writes at all, so
	// no CHECK protects it. 512KB leaves generous headroom over the storage
	// bound (the graph plus name/description JSON overhead) while keeping a
	// single request from buffering unboundedly. Mirrors the http.MaxBytesReader
	// convention used by the other write handlers.
	workflowTemplateBodyLimit = 512 << 10
)

// ---------------------------------------------------------------------------
// Converters
// ---------------------------------------------------------------------------

func workflowTemplateToResponse(t db.WorkflowTemplate, nodeCount int) WorkflowTemplateResponse {
	return WorkflowTemplateResponse{
		ID:             uuidToString(t.ID),
		WorkspaceID:    uuidToString(t.WorkspaceID),
		Key:            t.Key,
		Name:           t.Name,
		Description:    t.Description,
		Status:         t.Status,
		CurrentVersion: int4ToPtr(t.CurrentVersion),
		// Provenance comes from the row the seeder actually wrote, NOT from the
		// key string. Deriving it from the key would let a member POST a template
		// under key "bug_fix" and have the UI label their arbitrary graph
		// "platform-provided" â€” and, because the archive guard used the same
		// predicate, leave them unable to delete their own mistake.
		IsBuiltin: isBuiltinWorkflowTemplateRow(t),
		NodeCount: nodeCount,
		Revision:  t.Revision,
		CreatedAt: timestampToString(t.CreatedAt),
		UpdatedAt: timestampToString(t.UpdatedAt),
	}
}

// isBuiltinWorkflowTemplateRow reports whether a template was seeded by the
// platform rather than authored by a member or agent. The seeder stamps
// created_by_type='system' (the only writer that does), which makes this
// unforgeable over the API: every create path records "member" or "agent".
func isBuiltinWorkflowTemplateRow(t db.WorkflowTemplate) bool {
	return t.CreatedByType == "system"
}

func workflowTemplateVersionToEntry(v db.WorkflowTemplateVersion) WorkflowTemplateVersionEntry {
	return WorkflowTemplateVersionEntry{
		ID:          uuidToString(v.ID),
		Version:     v.Version,
		Status:      v.Status,
		PublishedAt: timestampToPtr(v.PublishedAt),
	}
}

// workflowNodeCount counts graph nodes without running the strict parser.
//
// ParseDefinition rejects unknown fields (so a typo cannot silently disable
// rework), which is exactly right at publish time and exactly wrong here: a
// graph written by a newer server would make an otherwise healthy list row
// unrenderable. A count is a display hint, so this decodes only what it needs
// and reports 0 for anything it cannot read.
func workflowNodeCount(raw []byte) int {
	if len(raw) == 0 {
		return 0
	}
	var shallow struct {
		Nodes []json.RawMessage `json:"nodes"`
	}
	if err := json.Unmarshal(raw, &shallow); err != nil {
		return 0
	}
	return len(shallow.Nodes)
}

// effectiveWorkflowVersion picks the version whose graph the template currently
// advertises: the published row the template's current_version points at, or -
// before the first publish - the newest draft. This is the same resolution a Run
// performs (GetPublishedWorkflowTemplateVersion), so the default detail API
// shows the graph a Run started now would actually pin.
//
// versions must be ordered version DESC, as ListWorkflowTemplateVersions returns.
func effectiveWorkflowVersion(t db.WorkflowTemplate, versions []db.WorkflowTemplateVersion) (db.WorkflowTemplateVersion, bool) {
	if t.CurrentVersion.Valid {
		for _, v := range versions {
			if v.Version == t.CurrentVersion.Int32 && v.Status == "published" {
				return v, true
			}
		}
	}
	if len(versions) > 0 {
		return versions[0], true
	}
	return db.WorkflowTemplateVersion{}, false
}

// editableWorkflowVersion returns the newest mutable draft when one exists.
// Published templates deliberately keep advertising current_version to Runs,
// but an editor recovering from a CAS conflict must reload the winning draft,
// not the older graph a newly started Run would pin.
func editableWorkflowVersion(t db.WorkflowTemplate, versions []db.WorkflowTemplateVersion) (db.WorkflowTemplateVersion, bool) {
	for _, v := range versions {
		if v.Status == "draft" {
			return v, true
		}
	}
	return effectiveWorkflowVersion(t, versions)
}

// workflowTemplateDetail assembles the detail response: version history plus
// the effective graph, or the newest editable draft when requested.
func (h *Handler) workflowTemplateDetail(ctx context.Context, t db.WorkflowTemplate, preferDraft bool) (WorkflowTemplateDetailResponse, error) {
	versions, err := h.Queries.ListWorkflowTemplateVersions(ctx, db.ListWorkflowTemplateVersionsParams{
		TemplateID:  t.ID,
		WorkspaceID: t.WorkspaceID,
	})
	if err != nil {
		return WorkflowTemplateDetailResponse{}, err
	}

	definition := emptyWorkflowDefinition
	nodeCount := 0
	selected, found := effectiveWorkflowVersion(t, versions)
	if preferDraft {
		selected, found = editableWorkflowVersion(t, versions)
	}
	if found && json.Valid(selected.Definition) {
		definition = json.RawMessage(selected.Definition)
		nodeCount = workflowNodeCount(selected.Definition)
	}

	entries := make([]WorkflowTemplateVersionEntry, len(versions))
	for i, v := range versions {
		entries[i] = workflowTemplateVersionToEntry(v)
	}
	return WorkflowTemplateDetailResponse{
		WorkflowTemplateResponse: workflowTemplateToResponse(t, nodeCount),
		Definition:               definition,
		Versions:                 entries,
	}, nil
}

// ---------------------------------------------------------------------------
// Validation
// ---------------------------------------------------------------------------

func validateWorkflowTemplateKey(raw string) (string, error) {
	key := strings.ToLower(strings.TrimSpace(raw))
	if key == "" {
		return "", errors.New("key is required")
	}
	if len(key) > maxWorkflowTemplateKeyLen {
		return "", errors.New("key must be 128 characters or fewer")
	}
	if !workflowTemplateKeyRE.MatchString(key) {
		return "", errors.New("key must start with a letter or digit and contain only lowercase letters, digits, hyphens, and underscores")
	}
	return key, nil
}

func validateWorkflowTemplateName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" {
		return "", errors.New("name is required")
	}
	if utf8.RuneCountInString(name) > maxWorkflowTemplateNameLen {
		return "", errors.New("name must be 200 characters or fewer")
	}
	return name, nil
}

// workflowTemplateCopyKey returns the first readable key that is not already
// used in the workspace. Template keys are ASCII by contract, so byte slicing
// is safe here. The caller holds the workspace copy advisory lock while it
// builds existing, which makes two duplicate requests choose different keys.
func workflowTemplateCopyKey(source string, existing map[string]struct{}) string {
	for copyNumber := 1; ; copyNumber++ {
		suffix := "_copy"
		if copyNumber > 1 {
			suffix = fmt.Sprintf("_copy_%d", copyNumber)
		}
		base := source
		if maxBase := maxWorkflowTemplateKeyLen - len(suffix); len(base) > maxBase {
			base = base[:maxBase]
		}
		candidate := base + suffix
		if _, found := existing[strings.ToLower(candidate)]; !found {
			return candidate
		}
	}
}

func workflowTemplateCopyName(source string) string {
	const suffix = " Copy"
	runes := []rune(strings.TrimSpace(source))
	maxBase := maxWorkflowTemplateNameLen - len([]rune(suffix))
	if len(runes) > maxBase {
		runes = runes[:maxBase]
	}
	return string(runes) + suffix
}

// workflowValidationMessages flattens a Validate error into the message list
// the API returns. Validate collects every problem in one pass, so surfacing
// all of them avoids an author fixing a graph one round-trip per error; a
// non-ValidationErrors failure still yields one message rather than an empty
// list, because a client that renders "invalid" with nothing to fix is worse
// than a terse reason.
func workflowValidationMessages(err error) []string {
	var verrs *workflow.ValidationErrors
	if errors.As(err, &verrs) {
		return verrs.Messages()
	}
	return []string{err.Error()}
}

// parseAndValidateWorkflowDefinition runs the same gate a publish must pass.
// Validating at create time rather than only at publish time means an invalid
// graph never reaches the database, so nothing can later be published (and
// pinned into Runs) without having been checked.
//
// Returns ok=false after writing the response; callers must return immediately.
func parseAndValidateWorkflowDefinition(w http.ResponseWriter, raw []byte) (*workflow.Definition, bool) {
	if len(raw) == 0 {
		writeError(w, http.StatusBadRequest, "definition is required")
		return nil, false
	}
	def, err := workflow.ParseDefinition(raw)
	if err != nil {
		// A parse failure is a definition problem, not a malformed HTTP body:
		// the client sent well-formed JSON that is not a well-formed graph.
		writeJSON(w, http.StatusUnprocessableEntity, workflowValidationResponse{
			Error:    "invalid workflow definition",
			Messages: []string{err.Error()},
		})
		return nil, false
	}
	if err := workflow.ValidateDraft(def, workflow.DefaultWorkspacePolicy, workflow.DefaultSchemaRegistry); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, workflowValidationResponse{
			Error:    "invalid workflow definition",
			Messages: workflowValidationMessages(err),
		})
		return nil, false
	}
	return def, true
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

// ListWorkflowTemplates returns the workspace's templates, seeding the
// platform built-ins first.
//
// GET is the seeding trigger (rather than a migration) because workspaces are
// created continuously and a data migration would only cover the ones that
// existed when it ran. A seeding failure is logged and ignored: the built-in
// being absent for one request is a far smaller problem than the workflows page
// failing to load.
func (h *Handler) ListWorkflowTemplates(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}

	h.seedBuiltinWorkflowTemplates(r, wsUUID)

	templates, err := h.Queries.ListWorkflowTemplates(r.Context(), db.ListWorkflowTemplatesParams{
		WorkspaceID:     wsUUID,
		IncludeArchived: r.URL.Query().Get("include_archived") == "true",
	})
	if err != nil {
		slog.Warn("ListWorkflowTemplates failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to list workflow templates")
		return
	}

	nodeCounts := h.workflowNodeCounts(r, wsUUID)
	resp := make([]WorkflowTemplateResponse, len(templates))
	for i, t := range templates {
		resp[i] = workflowTemplateToResponse(t, nodeCounts[uuidToString(t.ID)])
	}
	writeJSON(w, http.StatusOK, map[string]any{"templates": resp, "total": len(resp)})
}

// seedBuiltinWorkflowTemplates installs the platform built-ins for a workspace
// inside ONE transaction.
//
// Atomicity is the whole point. The seeder writes four rows (template, version,
// publish, set-current-version) and runs on the request context of a GET, so any
// client disconnect, timeout, or pod restart mid-sequence would otherwise commit
// a template with no version â€” status 'draft', current_version NULL. That state
// is permanently wedged and unreachable by every repair path: the seeder itself
// short-circuits on "the row exists", publish finds no draft version (409), and
// archive refuses built-ins (409). The user would see a "Bug Fix" row with no
// steps and a Publish button that always fails, fixable only by hand-written SQL.
// One transaction means the four rows either all land or none do, so a retried
// request simply seeds cleanly.
//
// A failure is logged and swallowed: the built-in missing for one request is a
// far smaller problem than the workflows page failing to load.
func (h *Handler) seedBuiltinWorkflowTemplates(r *http.Request, workspaceID pgtype.UUID) {
	if h.TxStarter == nil {
		// No transaction available (test handlers construct without one). Fall
		// back to the non-atomic path rather than skipping seeding entirely.
		if err := service.EnsureBuiltinWorkflowTemplates(r.Context(), h.Queries, workspaceID); err != nil {
			slog.Warn("EnsureBuiltinWorkflowTemplates failed", append(logger.RequestAttrs(r), "error", err)...)
		}
		return
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		slog.Warn("begin builtin workflow seed tx failed", append(logger.RequestAttrs(r), "error", err)...)
		return
	}
	defer tx.Rollback(r.Context())

	// The transaction doubles as the schema prober so the check and the writes see
	// one consistent view: probing on a different connection could observe a
	// migration this transaction cannot yet use, or the reverse.
	if err := service.EnsureBuiltinWorkflowTemplatesWithProbe(r.Context(), h.Queries.WithTx(tx), tx, workspaceID); err != nil {
		slog.Warn("EnsureBuiltinWorkflowTemplates failed", append(logger.RequestAttrs(r), "error", err)...)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		slog.Warn("commit builtin workflow seed failed", append(logger.RequestAttrs(r), "error", err)...)
	}
}

// workflowNodeCounts returns node_count per template id in one round trip.
//
// The alternative - reading each template's effective version through the
// generated queries - is an N+1 on the most-visited workflow endpoint just to
// produce a display hint. The count is computed in Go rather than with
// jsonb_array_length so a graph shaped differently than expected yields 0 for
// that row instead of a SQL error that fails the whole list.
//
// On query failure every count is 0: an absent hint must not cost the list.
func (h *Handler) workflowNodeCounts(r *http.Request, workspaceID pgtype.UUID) map[string]int {
	counts := map[string]int{}
	if h.DB == nil {
		return counts
	}
	// The LATERAL picks the same version effectiveWorkflowVersion does: the
	// published row current_version points at, else the newest version.
	rows, err := h.DB.Query(r.Context(), `
		SELECT t.id, v.definition
		FROM workflow_template t
		JOIN LATERAL (
			SELECT wtv.definition
			FROM workflow_template_version wtv
			WHERE wtv.template_id = t.id AND wtv.workspace_id = t.workspace_id
			ORDER BY COALESCE(wtv.status = 'published' AND wtv.version = t.current_version, false) DESC,
			         wtv.version DESC
			LIMIT 1
		) v ON TRUE
		WHERE t.workspace_id = $1
	`, workspaceID)
	if err != nil {
		slog.Warn("workflow template node counts failed", append(logger.RequestAttrs(r), "error", err)...)
		return counts
	}
	defer rows.Close()
	for rows.Next() {
		var id pgtype.UUID
		var definition []byte
		if err := rows.Scan(&id, &definition); err != nil {
			slog.Warn("workflow template node count row failed", append(logger.RequestAttrs(r), "error", err)...)
			return counts
		}
		counts[uuidToString(id)] = workflowNodeCount(definition)
	}
	if err := rows.Err(); err != nil {
		slog.Warn("workflow template node counts iteration failed", append(logger.RequestAttrs(r), "error", err)...)
	}
	return counts
}

// loadWorkflowTemplate resolves {id} inside the caller's workspace. The
// workspace is part of the WHERE clause, so a guessed UUID from another
// workspace is a 404 rather than a leak (plan section 4).
func (h *Handler) loadWorkflowTemplate(w http.ResponseWriter, r *http.Request) (db.WorkflowTemplate, bool) {
	idUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "workflow template id")
	if !ok {
		return db.WorkflowTemplate{}, false
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, h.resolveWorkspaceID(r), "workspace id")
	if !ok {
		return db.WorkflowTemplate{}, false
	}
	tpl, err := h.Queries.GetWorkflowTemplate(r.Context(), db.GetWorkflowTemplateParams{
		ID:          idUUID,
		WorkspaceID: wsUUID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "workflow template not found")
			return db.WorkflowTemplate{}, false
		}
		slog.Warn("GetWorkflowTemplate failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to get workflow template")
		return db.WorkflowTemplate{}, false
	}
	return tpl, true
}

// writeWorkflowTemplateDetail assembles and writes the detail response.
func (h *Handler) writeWorkflowTemplateDetail(w http.ResponseWriter, r *http.Request, tpl db.WorkflowTemplate, status int, preferDraft bool) {
	detail, err := h.workflowTemplateDetail(r.Context(), tpl, preferDraft)
	if err != nil {
		slog.Warn("ListWorkflowTemplateVersions failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to load workflow template versions")
		return
	}
	writeJSON(w, status, detail)
}

func (h *Handler) GetWorkflowTemplate(w http.ResponseWriter, r *http.Request) {
	tpl, ok := h.loadWorkflowTemplate(w, r)
	if !ok {
		return
	}
	preferDraft := r.URL.Query().Get("definition") == "draft"
	if preferDraft {
		member, ok := h.workspaceMember(w, r, uuidToString(tpl.WorkspaceID))
		if !ok {
			return
		}
		// Read-only members keep the existing effective-definition view. Drafts
		// are unpublished authoring state and must not become more widely visible
		// merely because the editor uses an opt-in query parameter.
		preferDraft = roleAllowed(member.Role, "owner", "admin")
	}
	h.writeWorkflowTemplateDetail(w, r, tpl, http.StatusOK, preferDraft)
}

// CreateWorkflowTemplate creates a template plus its first draft version.
func (h *Handler) CreateWorkflowTemplate(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, workflowTemplateBodyLimit)
	var req CreateWorkflowTemplateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	key, err := validateWorkflowTemplateKey(req.Key)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Built-in keys are reserved. Without this, a member could create a template
	// under key "bug_fix" in a workspace nobody has opened the workflows page in
	// yet, which would permanently block the real built-in from ever seeding (the
	// seeder skips a key that already exists).
	if service.IsBuiltinWorkflowTemplateKey(key) {
		writeError(w, http.StatusConflict, "that key is reserved for a built-in workflow template")
		return
	}
	name, err := validateWorkflowTemplateName(req.Name)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	def, ok := parseAndValidateWorkflowDefinition(w, req.Definition)
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
	wsUUID, ok := parseUUIDOrBadRequest(w, h.resolveWorkspaceID(r), "workspace id")
	if !ok {
		return
	}

	// Re-marshal from the parsed graph rather than storing the request bytes:
	// ParseDefinition already proved these fields are the only ones present, and
	// canonicalizing here keeps the stored JSONB free of client whitespace and
	// key ordering that would otherwise show up in every diff of the version.
	definition, err := workflow.MarshalDefinition(def)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "definition could not be encoded")
		return
	}

	// Template and its first version are written together: a template with no
	// version has no graph and could neither be validated nor started.
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start transaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)

	tpl, err := qtx.CreateWorkflowTemplate(r.Context(), db.CreateWorkflowTemplateParams{
		WorkspaceID:   wsUUID,
		Key:           key,
		Name:          name,
		Description:   sanitizeNullBytes(strings.TrimSpace(req.Description)),
		CreatedByType: "member",
		CreatedByID:   userUUID,
	})
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "a workflow template with that key already exists")
			return
		}
		slog.Warn("CreateWorkflowTemplate failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to create workflow template")
		return
	}
	if _, err := qtx.CreateWorkflowTemplateVersion(r.Context(), db.CreateWorkflowTemplateVersionParams{
		WorkspaceID:   wsUUID,
		TemplateID:    tpl.ID,
		Definition:    definition,
		SchemaVersion: workflowDefinitionSchemaVersion(definition),
	}); err != nil {
		if isCheckViolation(err) {
			// The only CHECK a validated graph can still trip is the 256KB
			// definition size bound (migration 232).
			writeError(w, http.StatusUnprocessableEntity, "definition is too large")
			return
		}
		slog.Warn("CreateWorkflowTemplateVersion failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to create workflow template version")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit workflow template")
		return
	}

	h.writeWorkflowTemplateDetail(w, r, tpl, http.StatusCreated, false)
}

// DuplicateBuiltinWorkflowTemplate forks the built-in's effective graph into
// a member-authored template whose first version is published immediately.
// Built-ins stay immutable and seeder-owned; the copy is an ordinary template
// that admins can run immediately and edit independently. A later edit opens a
// new draft while Runs keep pinning this published version until that draft is
// published.
func (h *Handler) DuplicateBuiltinWorkflowTemplate(w http.ResponseWriter, r *http.Request) {
	source, ok := h.loadWorkflowTemplate(w, r)
	if !ok {
		return
	}
	if _, ok := h.requireWorkspaceRole(w, r, uuidToString(source.WorkspaceID), "workflow template not found", "owner", "admin"); !ok {
		return
	}
	if !isBuiltinWorkflowTemplateRow(source) {
		writeError(w, http.StatusConflict, "only built-in workflow templates can be duplicated")
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

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start transaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)

	// Serialise key selection per workspace. Without the lock two clicks can
	// both observe bug_fix_copy as free and one loses to the unique index.
	lockKey := "workflow-template-copy:" + uuidToString(source.WorkspaceID)
	if _, err := tx.Exec(r.Context(), "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", lockKey); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to lock workflow template copy")
		return
	}

	versions, err := qtx.ListWorkflowTemplateVersions(r.Context(), db.ListWorkflowTemplateVersionsParams{
		TemplateID:  source.ID,
		WorkspaceID: source.WorkspaceID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load workflow template versions")
		return
	}
	effective, found := effectiveWorkflowVersion(source, versions)
	if !found || !json.Valid(effective.Definition) {
		writeError(w, http.StatusConflict, "built-in workflow template has no copyable version")
		return
	}

	templates, err := qtx.ListWorkflowTemplates(r.Context(), db.ListWorkflowTemplatesParams{
		WorkspaceID:     source.WorkspaceID,
		IncludeArchived: true,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to choose workflow template key")
		return
	}
	existingKeys := make(map[string]struct{}, len(templates))
	for _, tpl := range templates {
		existingKeys[strings.ToLower(tpl.Key)] = struct{}{}
	}

	copy, err := qtx.CreateWorkflowTemplate(r.Context(), db.CreateWorkflowTemplateParams{
		WorkspaceID:   source.WorkspaceID,
		Key:           workflowTemplateCopyKey(source.Key, existingKeys),
		Name:          workflowTemplateCopyName(source.Name),
		Description:   source.Description,
		CreatedByType: "member",
		CreatedByID:   userUUID,
	})
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "a workflow template with the generated copy key already exists")
			return
		}
		slog.Warn("CreateWorkflowTemplate copy failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to duplicate workflow template")
		return
	}
	version, err := qtx.CreateWorkflowTemplateVersion(r.Context(), db.CreateWorkflowTemplateVersionParams{
		WorkspaceID:   source.WorkspaceID,
		TemplateID:    copy.ID,
		Definition:    effective.Definition,
		SchemaVersion: effective.SchemaVersion,
	})
	if err != nil {
		slog.Warn("CreateWorkflowTemplateVersion copy failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to duplicate workflow template")
		return
	}
	published, err := qtx.PublishWorkflowTemplateVersion(r.Context(), db.PublishWorkflowTemplateVersionParams{
		ID:              version.ID,
		WorkspaceID:     source.WorkspaceID,
		PublishedByType: "member",
		PublishedByID:   userUUID,
	})
	if err != nil {
		slog.Warn("PublishWorkflowTemplateVersion copy failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to duplicate workflow template")
		return
	}
	copy, err = qtx.SetWorkflowTemplateCurrentVersion(r.Context(), db.SetWorkflowTemplateCurrentVersionParams{
		ID:             copy.ID,
		WorkspaceID:    source.WorkspaceID,
		CurrentVersion: published.Version,
	})
	if err != nil {
		slog.Warn("SetWorkflowTemplateCurrentVersion copy failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to duplicate workflow template")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit workflow template copy")
		return
	}

	h.writeWorkflowTemplateDetail(w, r, copy, http.StatusCreated, false)
}

// PublishWorkflowTemplate publishes the newest draft version and points the
// template at it.
//
// Admin-only: publishing decides which graph every future Run of this process
// pins, including its cost and rework budgets, so it is a workspace-policy
// decision rather than an editing one.
func (h *Handler) PublishWorkflowTemplate(w http.ResponseWriter, r *http.Request) {
	tpl, ok := h.loadWorkflowTemplate(w, r)
	if !ok {
		return
	}
	if _, ok := h.requireWorkspaceRole(w, r, uuidToString(tpl.WorkspaceID), "workflow template not found", "owner", "admin"); !ok {
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
		writeError(w, http.StatusConflict, "an archived workflow template cannot be published")
		return
	}

	versions, err := h.Queries.ListWorkflowTemplateVersions(r.Context(), db.ListWorkflowTemplateVersionsParams{
		TemplateID:  tpl.ID,
		WorkspaceID: tpl.WorkspaceID,
	})
	if err != nil {
		slog.Warn("ListWorkflowTemplateVersions failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to load workflow template versions")
		return
	}
	// Versions are ordered version DESC, so the first draft is the newest one.
	// It is always above current_version because version numbers only increase
	// and published -> draft is not a transition the schema allows; publishing
	// therefore cannot move a template backwards.
	var draft db.WorkflowTemplateVersion
	found := false
	for _, v := range versions {
		if v.Status == "draft" {
			draft = v
			found = true
			break
		}
	}
	if !found {
		writeError(w, http.StatusConflict, "workflow template has no draft version to publish")
		return
	}

	// Re-validate the stored draft instead of trusting that create-time
	// validation still holds: DefaultWorkspacePolicy and the schema registry can
	// tighten between draft and publish, and a published version is immutable
	// once Runs pin it.
	def, err := workflow.ParseDefinition(draft.Definition)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, workflowValidationResponse{
			Error:    "invalid workflow definition",
			Messages: []string{err.Error()},
		})
		return
	}
	if err := workflow.Validate(def, workflow.DefaultWorkspacePolicy, workflow.DefaultSchemaRegistry); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, workflowValidationResponse{
			Error:    "invalid workflow definition",
			Messages: workflowValidationMessages(err),
		})
		return
	}

	// The version flip and the template's advertised current_version must agree,
	// or a Run would resolve a version the template does not point at.
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start transaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)

	published, err := qtx.PublishWorkflowTemplateVersion(r.Context(), db.PublishWorkflowTemplateVersionParams{
		ID:              draft.ID,
		WorkspaceID:     tpl.WorkspaceID,
		PublishedByType: "member",
		PublishedByID:   userUUID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// The status = 'draft' guard lost: a concurrent publish already
			// froze this version. Publishing twice is a conflict, not a retry.
			writeError(w, http.StatusConflict, "workflow template version is already published")
			return
		}
		slog.Warn("PublishWorkflowTemplateVersion failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to publish workflow template version")
		return
	}
	updated, err := qtx.SetWorkflowTemplateCurrentVersion(r.Context(), db.SetWorkflowTemplateCurrentVersionParams{
		ID:             tpl.ID,
		WorkspaceID:    tpl.WorkspaceID,
		CurrentVersion: published.Version,
	})
	if err != nil {
		slog.Warn("SetWorkflowTemplateCurrentVersion failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to publish workflow template")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit workflow template publish")
		return
	}

	h.writeWorkflowTemplateDetail(w, r, updated, http.StatusOK, false)
}

// ArchiveWorkflowTemplate hides a template from new Runs. Archival is one-way
// and does not touch in-flight Runs: they hold a pinned version and must be
// allowed to finish (plan section 12).
//
// Admin-only, and refused for built-ins: the seeder re-creates a missing
// built-in on the next list, so archiving one would only produce a template that
// silently comes back.
func (h *Handler) ArchiveWorkflowTemplate(w http.ResponseWriter, r *http.Request) {
	tpl, ok := h.loadWorkflowTemplate(w, r)
	if !ok {
		return
	}
	if _, ok := h.requireWorkspaceRole(w, r, uuidToString(tpl.WorkspaceID), "workflow template not found", "owner", "admin"); !ok {
		return
	}
	if isBuiltinWorkflowTemplateRow(tpl) {
		writeError(w, http.StatusConflict, "built-in workflow templates cannot be archived")
		return
	}
	if tpl.Status == "archived" {
		writeError(w, http.StatusConflict, "workflow template is already archived")
		return
	}

	archived, err := h.Queries.ArchiveWorkflowTemplate(r.Context(), db.ArchiveWorkflowTemplateParams{
		ID:          tpl.ID,
		WorkspaceID: tpl.WorkspaceID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// The status <> 'archived' guard lost a race with another archive.
			writeError(w, http.StatusConflict, "workflow template is already archived")
			return
		}
		slog.Warn("ArchiveWorkflowTemplate failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to archive workflow template")
		return
	}

	// The list shape is enough here: archiving does not change the graph, and
	// the client's next action is to drop the row from the list.
	nodeCount := 0
	if detail, err := h.workflowTemplateDetail(r.Context(), archived, false); err == nil {
		nodeCount = detail.NodeCount
	}
	writeJSON(w, http.StatusOK, workflowTemplateToResponse(archived, nodeCount))
}

// ValidateWorkflowDefinition checks a graph without storing it. This backs the
// editor's validate button, which an author presses on a graph that may not
// correspond to any stored version yet.
//
// Open to any workspace member, unlike PATCH/publish/archive: the handler
// writes nothing and reads nothing workspace-scoped, so the only information it
// can disclose is what the validator thinks of bytes the caller already had.
// Gating it on admin would mean a member could not see why their own draft is
// rejected until they tried to save it.
func (h *Handler) ValidateWorkflowDefinition(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, workflowTemplateBodyLimit)
	var req ValidateWorkflowDefinitionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		// A body that is not JSON is a client bug, not an invalid graph: there is
		// no graph to report messages about.
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(req.Definition) == 0 {
		writeError(w, http.StatusBadRequest, "definition is required")
		return
	}

	// A parse failure is reported through the same valid:false channel as a
	// validation failure. To the author both mean "the editor cannot save this
	// yet", and splitting them across status codes would force the frontend to
	// implement the check twice.
	def, err := workflow.ParseDefinition(req.Definition)
	if err != nil {
		writeJSON(w, http.StatusOK, ValidateWorkflowDefinitionResponse{
			Valid:    false,
			Messages: []string{err.Error()},
		})
		return
	}
	if err := workflow.Validate(def, workflow.DefaultWorkspacePolicy, workflow.DefaultSchemaRegistry); err != nil {
		writeJSON(w, http.StatusOK, ValidateWorkflowDefinitionResponse{
			Valid:    false,
			Messages: workflowValidationMessages(err),
		})
		return
	}
	writeJSON(w, http.StatusOK, ValidateWorkflowDefinitionResponse{Valid: true, Messages: []string{}})
}

// UpdateWorkflowTemplate saves an editor draft: metadata onto the template row,
// the graph onto a draft version.
//
// The version half is the subtle part. A published version is immutable (plan
// section 4) because in-flight Runs resolve node semantics through exactly its
// bytes, so this never writes one. Instead:
//
//   - if a draft version exists, its definition is overwritten (drafts are the
//     scratch space; there is no value in accumulating one version per save),
//   - if every version is published, a NEW draft is created. That is what lets an
//     author evolve a published template: edits pile up in a draft that no Run
//     can pin until someone publishes it, and history stays byte-identical.
//
// Admin-only like publish/archive: a draft is the thing publish freezes, so
// whoever can shape it effectively chooses what will be published.
func (h *Handler) UpdateWorkflowTemplate(w http.ResponseWriter, r *http.Request) {
	tpl, ok := h.loadWorkflowTemplate(w, r)
	if !ok {
		return
	}
	if _, ok := h.requireWorkspaceRole(w, r, uuidToString(tpl.WorkspaceID), "workflow template not found", "owner", "admin"); !ok {
		return
	}
	// Built-ins are owned by the seeder, which compares against the embedded
	// JSON by key. An edited built-in would either be silently reverted or
	// permanently diverge from the shipped graph depending on seeder order, and
	// neither is a state an author can reason about. Fork it instead.
	if isBuiltinWorkflowTemplateRow(tpl) {
		writeError(w, http.StatusConflict, "built-in workflow templates cannot be edited")
		return
	}
	if tpl.Status == "archived" {
		// Archival is one-way (plan section 12). Editing an archived template
		// would produce a draft nobody can publish.
		writeError(w, http.StatusConflict, "an archived workflow template cannot be edited")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, workflowTemplateBodyLimit)
	var req UpdateWorkflowTemplateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	params := db.UpdateWorkflowTemplateParams{
		ID:          tpl.ID,
		WorkspaceID: tpl.WorkspaceID,
	}
	if req.Name != nil {
		name, err := validateWorkflowTemplateName(*req.Name)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		params.Name = pgtype.Text{String: name, Valid: true}
	}
	if req.Description != nil {
		params.Description = pgtype.Text{String: sanitizeNullBytes(strings.TrimSpace(*req.Description)), Valid: true}
	}

	// Validate BEFORE opening the transaction, let alone writing: a version's
	// bytes become unrunnable-but-immutable the moment a Run pins them, so an
	// invalid graph must never reach the table even transiently.
	var definition []byte
	if len(req.Definition) > 0 {
		def, ok := parseAndValidateWorkflowDefinition(w, req.Definition)
		if !ok {
			return
		}
		// Re-marshal from the parsed graph for the same reason create does:
		// ParseDefinition proved these are the only fields present, and
		// canonicalizing keeps client whitespace and key ordering out of every
		// version diff.
		marshalled, err := workflow.MarshalDefinition(def)
		if err != nil {
			writeError(w, http.StatusUnprocessableEntity, "definition could not be encoded")
			return
		}
		definition = marshalled
	}

	if req.Name == nil && req.Description == nil && definition == nil {
		// Nothing to do. Returning the current detail rather than 400 keeps an
		// autosave that fires with no pending edits harmless.
		h.writeWorkflowTemplateDetail(w, r, tpl, http.StatusOK, false)
		return
	}
	if req.Revision == nil || *req.Revision <= 0 {
		writeErrorCode(w, http.StatusBadRequest, "validation_error", "revision is required and must be positive")
		return
	}
	params.ExpectedRevision = *req.Revision

	// Metadata and graph move together: a rename that lands without its graph
	// (or the reverse) would leave the editor showing a state the author never
	// authored, and they have no way to tell which half committed.
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start transaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)

	// This guarded update is also the definition-only save fence: two editors
	// that both read revision N cannot overwrite each other. Exactly one moves
	// the row to N+1; the other receives a conflict before draft bytes are written.
	updated, err := qtx.UpdateWorkflowTemplate(r.Context(), params)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeErrorCode(w, http.StatusConflict, "workflow_template_revision_conflict", "workflow template changed; copy your JSON if needed, then reload before retrying")
			return
		}
		slog.Warn("UpdateWorkflowTemplate failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to update workflow template")
		return
	}

	if definition != nil {
		if ok := h.saveWorkflowTemplateDraftDefinition(w, r, qtx, tpl, definition); !ok {
			return
		}
	}

	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit workflow template update")
		return
	}

	// Same shape as GET so the client can drop the response straight into its
	// cache instead of refetching.
	h.writeWorkflowTemplateDetail(w, r, updated, http.StatusOK, false)
}

// saveWorkflowTemplateDraftDefinition writes definition onto the template's
// draft version, creating one if every existing version is published.
//
// Returns false after writing the response; callers must return immediately.
func (h *Handler) saveWorkflowTemplateDraftDefinition(
	w http.ResponseWriter,
	r *http.Request,
	qtx *db.Queries,
	tpl db.WorkflowTemplate,
	definition []byte,
) bool {
	// Read the version list inside the transaction: an out-of-transaction read
	// could see a draft that a concurrent publish froze before we write, and
	// the guarded UPDATE would then silently match zero rows.
	versions, err := qtx.ListWorkflowTemplateVersions(r.Context(), db.ListWorkflowTemplateVersionsParams{
		TemplateID:  tpl.ID,
		WorkspaceID: tpl.WorkspaceID,
	})
	if err != nil {
		slog.Warn("ListWorkflowTemplateVersions failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to load workflow template versions")
		return false
	}

	// Ordered version DESC, so the first draft is the newest one - the same row
	// publish would freeze. Editing an older draft would let publish pick up a
	// graph the author is no longer looking at.
	var draft db.WorkflowTemplateVersion
	foundDraft := false
	for _, v := range versions {
		if v.Status == "draft" {
			draft = v
			foundDraft = true
			break
		}
	}

	if foundDraft {
		_, err := qtx.UpdateWorkflowTemplateVersionDefinition(r.Context(), db.UpdateWorkflowTemplateVersionDefinitionParams{
			ID:          draft.ID,
			WorkspaceID: tpl.WorkspaceID,
			Definition:  definition,
		})
		if err == nil {
			return true
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			if isCheckViolation(err) {
				// The only CHECK a validated graph can still trip is the 256KB
				// definition size bound (migration 232).
				writeError(w, http.StatusUnprocessableEntity, "definition is too large")
				return false
			}
			slog.Warn("UpdateWorkflowTemplateVersionDefinition failed", append(logger.RequestAttrs(r), "error", err)...)
			writeError(w, http.StatusInternalServerError, "failed to update workflow template definition")
			return false
		}
		// The status='draft' guard matched nothing: this version was published
		// between the list above and the update. The edit is still valid, it
		// just cannot land on frozen bytes - fall through and open a new draft.
	}

	if _, err := qtx.CreateWorkflowTemplateVersion(r.Context(), db.CreateWorkflowTemplateVersionParams{
		WorkspaceID:   tpl.WorkspaceID,
		TemplateID:    tpl.ID,
		Definition:    definition,
		SchemaVersion: workflowDefinitionSchemaVersion(definition),
	}); err != nil {
		if isCheckViolation(err) {
			writeError(w, http.StatusUnprocessableEntity, "definition is too large")
			return false
		}
		if isUniqueViolation(err) {
			// idx_workflow_template_version_template_version rejected a version
			// number a concurrent save already claimed. A retry picks the next
			// number, so this is a conflict rather than a server fault.
			writeError(w, http.StatusConflict, "another draft was saved concurrently; reload and retry")
			return false
		}
		slog.Warn("CreateWorkflowTemplateVersion failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to create workflow template version")
		return false
	}
	return true
}

func workflowDefinitionSchemaVersion(raw json.RawMessage) int32 {
	var header struct {
		SchemaVersion int32 `json:"schema_version"`
	}
	_ = json.Unmarshal(raw, &header)
	if header.SchemaVersion == 0 {
		return 1
	}
	return header.SchemaVersion
}
