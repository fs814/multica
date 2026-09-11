package service

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"path"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/workflow"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

//go:embed builtin_workflows
var builtinWorkflowsFS embed.FS

const builtinWorkflowsRoot = "builtin_workflows"

// builtinWorkflowNames maps an embedded file to the template identity it seeds.
// Key and Name are deliberately declared here rather than read out of the JSON:
// the graph file describes the *graph*, and `key` is an external contract
// (plan section 9 resolves templates by key), so it must not drift silently
// when someone edits the definition.
//
// PriorRevisionFiles is the shipping history: every earlier revision of this
// built-in's graph that a released binary ever seeded, oldest first. It exists so
// the seeder can decide whether a workspace's copy is still PRISTINE — see
// builtinWorkflowIsPristine. Keeping the old bytes embedded costs a couple of KB
// and is the only way to answer that question without guessing; the alternative
// tried and rejected in review was inferring it from node count or version
// number, both of which say "unedited" about a template someone edited.
//
// Shipping a new revision means: copy the current file into revisions/ under the
// next vN name, APPEND it here, then edit the live file. Never rewrite an entry
// already listed — a released binary seeded those exact bytes into real
// workspaces, and forgetting one turns those workspaces into "edited" forever.
var builtinWorkflowNames = []struct {
	File               string
	PriorRevisionFiles []string
	Key                string
	Name               string
	Description        string
}{
	{
		File: "bug_fix.json",
		// v1 was the five-node graph whose entry was `analyze`; the run's title
		// and description were collected by a hardcoded Run dialog and appeared
		// nowhere on the canvas. v2 (the live file) adds the `intake` input node
		// that declares them.
		PriorRevisionFiles: []string{"revisions/bug_fix.v1.json"},
		Key:                "bug_fix",
		Name:               "Bug Fix",
		Description:        "Analyze the defect, implement a bounded fix, validate it, then require human acceptance before the run completes.",
	},
}

// BuiltinWorkflowTemplate is one platform-provided workflow, parsed and
// validated at load time. Raw is retained alongside Definition because the
// version row stores the exact bytes that were validated: re-marshalling would
// let a struct change silently alter what a published (immutable) version says.
type BuiltinWorkflowTemplate struct {
	Key         string
	Name        string
	Description string
	Definition  *workflow.Definition
	Raw         []byte

	// ShippedFingerprints holds one fingerprint per revision this built-in has
	// EVER shipped, including the current one. It is what makes "this workspace's
	// copy is untouched" a decidable fact rather than a guess; see
	// builtinWorkflowFingerprint for what a fingerprint is and why comparing raw
	// bytes would not work.
	ShippedFingerprints []string

	// ShippedDefinitions holds the raw bytes of those same revisions, in the same
	// order, so ShippedDefinitions[i] is what ShippedFingerprints[i] was computed
	// from. The last entry is Raw.
	//
	// Exported for one reason, and it is a test-integrity reason rather than a
	// convenience: a test in ANOTHER package that wants to reproduce "a workspace
	// seeded by an older binary" must use the bytes that binary really seeded. A
	// hand-typed previous revision does not fingerprint as one we shipped, so the
	// seeder correctly refuses to upgrade it — and every upgrade assertion then
	// quietly exercises the refuse-to-upgrade path, stays green, and proves
	// nothing. That exact blind-fixture defect is in this feature's history, so
	// the real bytes are made reachable rather than left to be re-typed.
	//
	// Populated from the same read as ShippedFingerprints, so the two cannot
	// describe different graphs.
	ShippedDefinitions [][]byte
}

var (
	builtinWorkflowsOnce sync.Once
	builtinWorkflowsList []BuiltinWorkflowTemplate
)

// BuiltinWorkflowTemplates returns the built-in workflow graphs embedded at
// compile time. The result is computed once because parsing and validating is
// pure and the answer cannot change at runtime, and because the list is read on
// every GET /api/workflow-templates (the seeder runs there).
//
// A graph that fails to parse or fails Validate is dropped, not returned. This
// mirrors loadBuiltinSkill's posture: shipping a broken built-in would create a
// template every workspace inherits and no user can repair, whereas dropping it
// degrades to "the built-in is missing" and is caught by the eval test below.
func BuiltinWorkflowTemplates() []BuiltinWorkflowTemplate {
	builtinWorkflowsOnce.Do(func() {
		builtinWorkflowsList = loadBuiltinWorkflowTemplates()
	})
	return builtinWorkflowsList
}

func loadBuiltinWorkflowTemplates() []BuiltinWorkflowTemplate {
	var out []BuiltinWorkflowTemplate
	for _, meta := range builtinWorkflowNames {
		tpl, ok := loadBuiltinWorkflowTemplate(meta.File, meta.Key, meta.Name, meta.Description)
		if !ok {
			continue
		}
		// Fingerprint every revision this built-in has shipped, current one last.
		// A prior revision that fails to load is fatal for the UPGRADE decision,
		// not for the template: dropping the fingerprint would silently reclassify
		// every workspace still on that revision as "user-edited" and strand it on
		// an old graph forever. So the template is dropped entirely, which is the
		// same loud-by-absence posture loadBuiltinWorkflowTemplate already takes.
		fingerprints, ok := builtinWorkflowFingerprints(meta.PriorRevisionFiles, tpl.Raw)
		if !ok {
			slog.Error("builtin workflow: a prior revision could not be fingerprinted; dropping the built-in rather than treating seeded workspaces as user-edited",
				"file", meta.File, "key", meta.Key)
			continue
		}
		definitions, ok := builtinWorkflowRevisionBytes(meta.PriorRevisionFiles, tpl.Raw)
		if !ok {
			// Unreachable: builtinWorkflowFingerprints just read the same files
			// successfully. Kept as a hard drop so the two lists can never end up
			// describing different revisions.
			slog.Error("builtin workflow: revision bytes and fingerprints disagree; dropping the built-in",
				"file", meta.File, "key", meta.Key)
			continue
		}
		tpl.ShippedFingerprints = fingerprints
		tpl.ShippedDefinitions = definitions
		out = append(out, tpl)
	}
	return out
}

// builtinWorkflowFingerprints fingerprints each prior revision file plus the
// current bytes, in shipping order. The bool is false if any prior revision is
// missing or unparseable.
func builtinWorkflowFingerprints(priorFiles []string, current []byte) ([]string, bool) {
	out := make([]string, 0, len(priorFiles)+1)
	for _, rel := range priorFiles {
		raw, err := fs.ReadFile(builtinWorkflowsFS, path.Join(builtinWorkflowsRoot, rel))
		if err != nil {
			slog.Error("builtin workflow: prior revision missing from the embedded FS", "file", rel, "error", err)
			return nil, false
		}
		fp, err := builtinWorkflowFingerprint(raw)
		if err != nil {
			slog.Error("builtin workflow: prior revision does not normalize", "file", rel, "error", err)
			return nil, false
		}
		out = append(out, fp)
	}
	fp, err := builtinWorkflowFingerprint(current)
	if err != nil {
		// Unreachable in practice: the caller already ran ParseDefinition on
		// these bytes. Kept as a hard failure so an impossible state does not
		// become a silent "never upgrade".
		slog.Error("builtin workflow: current revision does not normalize", "error", err)
		return nil, false
	}
	return append(out, fp), true
}

// builtinWorkflowRevisionBytes reads each prior revision file plus the current
// bytes, in the same shipping order builtinWorkflowFingerprints uses.
//
// Each slice is a copy. embed.FS hands back the compiled-in bytes, and these are
// exported on BuiltinWorkflowTemplate, so returning the FS's own slice would let a
// caller that indexed into it mutate the embedded revision for the whole process -
// after which every pristineness check for the rest of the binary's life compares
// against the mutated graph.
func builtinWorkflowRevisionBytes(priorFiles []string, current []byte) ([][]byte, bool) {
	out := make([][]byte, 0, len(priorFiles)+1)
	for _, rel := range priorFiles {
		raw, err := fs.ReadFile(builtinWorkflowsFS, path.Join(builtinWorkflowsRoot, rel))
		if err != nil {
			slog.Error("builtin workflow: prior revision missing from the embedded FS", "file", rel, "error", err)
			return nil, false
		}
		out = append(out, append([]byte(nil), raw...))
	}
	return append(out, append([]byte(nil), current...)), true
}

// builtinWorkflowFingerprint reduces a definition to a value that can be
// compared across a Postgres round trip.
//
// Raw bytes CANNOT be compared directly. The definition is stored as JSONB, and
// Postgres does not preserve the text it was given: it drops insignificant
// whitespace, reorders object keys into its own internal order, and normalizes
// number syntax. So the bytes read back out of workflow_template_version are
// never byte-identical to the embedded file even when nothing was edited, and a
// naive bytes.Equal would classify every workspace as edited and upgrade nothing.
//
// Normalization is therefore: ParseDefinition (which rejects unknown fields, so
// this cannot silently ignore an author's extra key) and then MarshalDefinition,
// giving Go's canonical field order and spacing for the exact struct the engine
// interprets. Both sides of the comparison go through it, so the fingerprint
// answers precisely the question the seeder is asking: "does this stored graph
// MEAN the same thing as a graph we shipped?"
//
// Why that is safe, i.e. why it cannot report pristine for an edited template:
// every field the engine reads is a field of Definition and therefore survives
// into the marshalled form, so any edit a user can make through the editor —
// an instruction, a routing strategy, a rework target, a limit, a node added or
// removed — changes the fingerprint. What it deliberately ignores is exactly the
// set of differences that carry no meaning: key order, whitespace, and the
// omission of a zero-valued optional field. A user cannot express intent in
// those, and Postgres destroys them regardless.
//
// The digest rather than the normalized JSON itself: the comparison is
// equality-only, and a 32-byte value keeps the shipping history cheap to hold in
// memory as the revision list grows.
func builtinWorkflowFingerprint(raw []byte) (string, error) {
	def, err := workflow.ParseDefinition(raw)
	if err != nil {
		return "", err
	}
	normalized, err := workflow.MarshalDefinition(def)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(normalized)
	return hex.EncodeToString(sum[:]), nil
}

// matchesShippedRevision reports whether stored is byte-equivalent (after
// normalization) to some revision of this built-in that we shipped.
func (b BuiltinWorkflowTemplate) matchesShippedRevision(stored []byte) bool {
	fp, err := builtinWorkflowFingerprint(stored)
	if err != nil {
		// A stored definition we cannot even parse is not something we shipped —
		// and it is certainly not something to overwrite on a guess.
		return false
	}
	for _, shipped := range b.ShippedFingerprints {
		if shipped == fp {
			return true
		}
	}
	return false
}

// isCurrentRevision reports whether stored already means what the live embedded
// file means. This is what makes the upgrade idempotent: the second seed sees the
// version it just published and stops, instead of publishing v3 with the same
// graph.
func (b BuiltinWorkflowTemplate) isCurrentRevision(stored []byte) bool {
	if len(b.ShippedFingerprints) == 0 {
		return false
	}
	fp, err := builtinWorkflowFingerprint(stored)
	if err != nil {
		return false
	}
	return fp == b.ShippedFingerprints[len(b.ShippedFingerprints)-1]
}

func loadBuiltinWorkflowTemplate(file, key, name, description string) (BuiltinWorkflowTemplate, bool) {
	raw, err := fs.ReadFile(builtinWorkflowsFS, path.Join(builtinWorkflowsRoot, file))
	if err != nil {
		slog.Error("builtin workflow: embedded graph missing", "file", file, "error", err)
		return BuiltinWorkflowTemplate{}, false
	}
	def, err := workflow.ParseDefinition(raw)
	if err != nil {
		slog.Error("builtin workflow: graph does not parse", "file", file, "error", err)
		return BuiltinWorkflowTemplate{}, false
	}
	// Validate with the same policy and schema registry a user-authored template
	// must satisfy at publish time. A built-in gets no exemption: the seeder
	// publishes version 1 immediately, and an unvalidated published version
	// would be pinned into every Run started from it.
	if err := workflow.Validate(def, workflow.DefaultWorkspacePolicy, workflow.DefaultSchemaRegistry); err != nil {
		slog.Error("builtin workflow: graph is invalid", "file", file, "error", err)
		return BuiltinWorkflowTemplate{}, false
	}
	return BuiltinWorkflowTemplate{
		Key:         key,
		Name:        name,
		Description: description,
		Definition:  def,
		Raw:         raw,
	}, true
}

// EnsureBuiltinWorkflowTemplates makes the built-in templates present in a
// workspace, creating each missing one as a published version 1 and upgrading an
// UNTOUCHED one to the newest shipped revision.
//
// This is a seeder rather than a migration on purpose: workspaces are created
// continuously, and a data migration would only cover the ones that existed
// when it ran. Calling this from the list endpoint means an existing workspace
// picks up a newly shipped built-in the first time someone opens the workflows
// page, with no backfill step to remember.
//
// It is idempotent by (workspace_id, key), and idempotent again across revisions:
// once a workspace is on the newest shipped revision, further seeds do nothing.
//
// The upgrade never rewrites a published version — those are immutable and
// in-flight Runs resolve node semantics through the exact bytes they pinned — it
// publishes a NEW version and moves current_version. And it only ever touches a
// copy it can prove nobody edited: if a user changed their Bug Fix, that copy
// wins, unchanged, forever. See ensureBuiltinWorkflowTemplate.
func EnsureBuiltinWorkflowTemplates(ctx context.Context, q *db.Queries, workspaceID pgtype.UUID) error {
	return EnsureBuiltinWorkflowTemplatesWithProbe(ctx, q, nil, workspaceID)
}

// EnsureBuiltinWorkflowTemplatesWithProbe is the seeding entry point that can also
// verify the live schema before publishing.
//
// The probe is a separate parameter rather than being derived from q because
// *db.Queries deliberately hides its handle: a caller may be inside a
// transaction, and the probe must see that transaction's view. Passing nil skips
// the check, which is correct for callers that have already established the
// schema is current (migrations run to completion in tests and in `migrate up`).
func EnsureBuiltinWorkflowTemplatesWithProbe(ctx context.Context, q *db.Queries, probe schemaProber, workspaceID pgtype.UUID) error {
	if q == nil || !workspaceID.Valid {
		return nil
	}
	for _, builtin := range BuiltinWorkflowTemplates() {
		// A revision the live schema cannot execute must not become anyone's
		// current version. See workflowSchemaAdmitsNodeTypes: during a rolling
		// deploy the new binary runs against the old schema, and publishing a graph
		// whose node types the CHECK still rejects would leave a published, current,
		// permanently unrunnable template behind - the workspace's runs would 500 on
		// the first step insert, and the seeder would never revisit it because a
		// published current version is exactly what it treats as finished.
		ok, err := workflowSchemaAdmitsNodeTypes(ctx, probe, builtin.Definition)
		if err != nil {
			return fmt.Errorf("probe schema for builtin workflow template %q: %w", builtin.Key, err)
		}
		if !ok {
			slog.Warn("skipping builtin workflow template: the database schema does not yet admit every node type it uses",
				"key", builtin.Key,
				"hint", "apply pending migrations; the template is seeded or upgraded on a later request")
			continue
		}
		if err := ensureBuiltinWorkflowTemplate(ctx, q, workspaceID, builtin); err != nil {
			return err
		}
	}
	return nil
}

// workflowSchemaAdmitsNodeTypes reports whether the live workflow_step_instance
// node_type CHECK accepts every node type this definition uses.
//
// The binary and the schema are deployed separately, so a new build routinely
// runs for a while against the previous migration state. Nothing else in the
// seeding path notices that: the graph passes workflow.Validate (which knows the
// Go vocabulary, not the database's), the template and version rows carry no
// node_type at all, and the CHECK only fires much later when a Run tries to
// insert its first step. The failure would therefore surface as a 500 on a run of
// a template that looks perfectly healthy, in a workspace the seeder already
// considers finished — and the seeder would never revisit it, because a published
// current version is exactly what it treats as "done".
//
// Probed rather than assumed because "did migration 251 run" is a fact only the
// database holds. This reads the catalog directly rather than going through a
// generated query: sqlc type-checks queries against the migration DDL and has no
// model of pg_catalog, so a catalog query cannot be generated at all. It is also
// the more honest home for it — this asks about the SCHEMA, not about data.
//
// Deliberately a WHITELIST test. A constraint that is missing, renamed, or written
// in a shape this does not recognize reports "not admitted", so the seeder holds
// off rather than publishing a graph it cannot prove is runnable. Holding off
// costs a log line and a later retry; guessing wrong costs an unrunnable template
// nobody can repair through the API.
func workflowSchemaAdmitsNodeTypes(ctx context.Context, probe schemaProber, def *workflow.Definition) (bool, error) {
	if def == nil || probe == nil {
		return true, nil
	}
	seen := make(map[string]bool, len(def.Nodes))
	types := make([]string, 0, len(def.Nodes))
	for i := range def.Nodes {
		t := string(def.Nodes[i].Type)
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		types = append(types, t)
	}
	if len(types) == 0 {
		return true, nil
	}

	// Ask Postgres which of the candidates the constraint text mentions as a
	// quoted literal. Both 235 and 251 write the vocabulary that way, and matching
	// the rendered definition means this cannot drift from what an INSERT will do
	// the way a hardcoded "migration 251 exists" check would.
	const q = `
		SELECT NOT EXISTS (
			SELECT 1
			FROM unnest($1::text[]) AS candidate(node_type)
			WHERE NOT EXISTS (
				SELECT 1
				FROM pg_constraint c
				JOIN pg_class t ON t.oid = c.conrelid
				WHERE t.relname = 'workflow_step_instance'
				  AND c.contype = 'c'
				  AND pg_get_constraintdef(c.oid) LIKE '%node_type%'
				  AND pg_get_constraintdef(c.oid) LIKE ('%''' || candidate.node_type || '''%')
			)
		)`
	var admitted bool
	if err := probe.QueryRow(ctx, q, types).Scan(&admitted); err != nil {
		return false, err
	}
	return admitted, nil
}

// schemaProber is the single method workflowSchemaAdmitsNodeTypes needs. Declared
// here rather than taking a concrete pool so a caller holding a transaction can
// pass it and see its own uncommitted DDL, and so a test can stub it.
type schemaProber interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}


func ensureBuiltinWorkflowTemplate(ctx context.Context, q *db.Queries, workspaceID pgtype.UUID, builtin BuiltinWorkflowTemplate) error {
	existing, err := q.GetWorkflowTemplateByKey(ctx, db.GetWorkflowTemplateByKeyParams{
		WorkspaceID: workspaceID,
		Key:         builtin.Key,
	})
	if err == nil {
		// The template exists. Either we shipped a newer graph and this copy is
		// still ours to move, or a human owns it now and we must not touch it.
		return upgradeBuiltinWorkflowTemplate(ctx, q, workspaceID, builtin, existing)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("look up builtin workflow template %q: %w", builtin.Key, err)
	}

	// No transaction is available at this call site (the signature takes a plain
	// *db.Queries so a handler can pass either the pool queries or a tx-bound
	// one). The write is therefore split across three statements, and the unique
	// index idx_workflow_template_ws_key is what keeps two concurrent callers
	// from both seeding: the loser sees 23505 and treats it as success.
	tpl, err := q.CreateWorkflowTemplate(ctx, db.CreateWorkflowTemplateParams{
		WorkspaceID: workspaceID,
		Key:         builtin.Key,
		Name:        builtin.Name,
		Description: builtin.Description,
		// "system" (allowed by the created_by_type CHECK) records that no member
		// authored this, so the UI can mark it built-in without a separate flag
		// and audit trails do not attribute it to whoever first opened the page.
		CreatedByType: "system",
		CreatedByID:   pgtype.UUID{Valid: true},
	})
	if err != nil {
		if isUniqueViolation(err) {
			return nil
		}
		return fmt.Errorf("create builtin workflow template %q: %w", builtin.Key, err)
	}

	if err := publishBuiltinWorkflowVersion(ctx, q, workspaceID, builtin, tpl.ID, nil); err != nil {
		return err
	}
	return nil
}

// upgradeBuiltinWorkflowTemplate publishes the newest shipped revision over a
// workspace's copy of a built-in, but ONLY when that copy is provably pristine.
//
// "Pristine" is decided from provenance and content, never from a version number
// or a node count — both of those say "unedited" about a template someone edited,
// and the cost of getting it wrong is overwriting a user's process silently. All
// four of these must hold:
//
//  1. created_by_type = 'system'. A member-authored template that happens to use
//     the key "bug_fix" is not ours to revise. (Handler tests already cover a
//     workspace that squatted the key before the seeder first ran.)
//  2. There is a published version to compare against. A template mid-authoring
//     with no published version is a human at work.
//  3. Every version row on the template is one WE shipped. This is the check that
//     catches the case a content-comparison of the published row alone would
//     miss: a user who created a draft — edited or not — is authoring, and a
//     publish under them would either bury their draft behind a newer version or
//     leave them editing a graph that is no longer current. Their intent wins.
//  4. The published definition matches a revision from this built-in's shipping
//     history. Someone who published an edit is the owner of their copy now.
//
// When the published version is already the newest revision, this returns nil
// without writing: that is what makes a second and third seed a no-op rather than
// a v3.
//
// In-flight Runs are untouched by construction. A Run pins version_id at start
// and resolves its graph through that row; publishing a new row and moving
// current_version changes only what a FUTURE Run picks up.
func upgradeBuiltinWorkflowTemplate(ctx context.Context, q *db.Queries, workspaceID pgtype.UUID, builtin BuiltinWorkflowTemplate, existing db.WorkflowTemplate) error {
	// (1) Provenance. Checked from the row rather than inferred from the key,
	// because a key is a string any client can send.
	if existing.CreatedByType != "system" {
		return nil
	}
	// An archived built-in is left alone. Archival is a deliberate act, and
	// publishing into it would make a hidden template start advertising a new
	// version nobody asked for.
	if existing.Status == "archived" || existing.ArchivedAt.Valid {
		return nil
	}

	published, err := q.GetPublishedWorkflowTemplateVersion(ctx, db.GetPublishedWorkflowTemplateVersionParams{
		WorkspaceID: workspaceID,
		TemplateID:  existing.ID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// (2) No published version at current_version: either a half-finished
			// seed or a human's draft-only template. Neither is a safe base to
			// publish a revision on top of, and the "missing built-in" case the
			// original seeder covered is handled by CreateWorkflowTemplate above.
			return nil
		}
		return fmt.Errorf("load published version of builtin workflow template %q: %w", builtin.Key, err)
	}

	// Already current: nothing to do, and saying so BEFORE the version scan keeps
	// the common path (every list request in an up-to-date workspace) to one read.
	if builtin.isCurrentRevision(published.Definition) {
		return nil
	}

	versions, err := q.ListWorkflowTemplateVersions(ctx, db.ListWorkflowTemplateVersionsParams{
		WorkspaceID: workspaceID,
		TemplateID:  existing.ID,
	})
	if err != nil {
		return fmt.Errorf("list versions of builtin workflow template %q: %w", builtin.Key, err)
	}
	if len(versions) == 0 {
		// Contradicts the published row we just read; treat an inconsistent view
		// as "do not touch" rather than as permission.
		return nil
	}
	// A draft already carrying the newest revision's bytes is our own interrupted
	// upgrade (this seeder is not transactional — see publishBuiltinWorkflowVersion).
	// Resuming that row instead of creating another is what keeps a crash between
	// the two writes from accumulating one dead draft per page load.
	//
	// ListWorkflowTemplateVersions orders version DESC, so this picks the newest
	// such draft — the same row the handler's publish path would freeze.
	var resume *db.WorkflowTemplateVersion
	for i := range versions {
		// (3) and (4) together: EVERY row, draft or published, must be bytes we
		// shipped. A single unrecognized row means a human has been in here.
		if !builtin.matchesShippedRevision(versions[i].Definition) {
			return nil
		}
		if resume == nil && versions[i].Status == "draft" && builtin.isCurrentRevision(versions[i].Definition) {
			resume = &versions[i]
		}
	}

	slog.Info("builtin workflow: upgrading an untouched built-in to the newest shipped revision",
		"key", builtin.Key, "workspace_id", workspaceID, "from_version", published.Version,
		"resumed_draft", resume != nil)
	return publishBuiltinWorkflowVersion(ctx, q, workspaceID, builtin, existing.ID, resume)
}

// publishBuiltinWorkflowVersion creates (or resumes), publishes, and points
// current_version at a version carrying the embedded bytes.
//
// Shared by the create and upgrade paths so both write a version the same way;
// the only difference between seeding and upgrading is the decision to call this,
// which is where all the judgement lives. `resume` is non-nil only when the
// upgrade path found its own leftover draft.
//
// Like the create path it cannot assume a transaction. The failure modes are
// benign in a specific way worth stating: a crash after CreateWorkflowTemplateVersion
// leaves an unpublished draft, which the pristine check above still recognizes as
// shipped bytes and hands back here as `resume`, so the next seed finishes the job
// rather than starting a second one. A crash after PublishWorkflowTemplateVersion
// leaves a published row that current_version does not point at, and
// GetPublishedWorkflowTemplateVersion (which joins on current_version) then returns
// the OLD row, so the next seed likewise retries — and the publish below tolerates
// the already-frozen row. No intermediate state is one a Run can pin, because a Run
// resolves through current_version.
func publishBuiltinWorkflowVersion(ctx context.Context, q *db.Queries, workspaceID pgtype.UUID, builtin BuiltinWorkflowTemplate, templateID pgtype.UUID, resume *db.WorkflowTemplateVersion) error {
	version := db.WorkflowTemplateVersion{}
	if resume != nil {
		version = *resume
	} else {
		created, err := q.CreateWorkflowTemplateVersion(ctx, db.CreateWorkflowTemplateVersionParams{
			WorkspaceID: workspaceID,
			TemplateID:  templateID,
			// Store the embedded bytes verbatim: these are the bytes Validate
			// accepted at load time.
			Definition:    builtin.Raw,
			SchemaVersion: int32(workflow.SchemaVersion),
		})
		if err != nil {
			return fmt.Errorf("create builtin workflow template version %q: %w", builtin.Key, err)
		}
		version = created
	}

	if _, err := q.PublishWorkflowTemplateVersion(ctx, db.PublishWorkflowTemplateVersionParams{
		ID:              version.ID,
		WorkspaceID:     workspaceID,
		PublishedByType: "system",
		PublishedByID:   pgtype.UUID{Valid: true},
	}); err != nil {
		// The status='draft' guard matched nothing, which for these bytes can only
		// mean a concurrent seeder already froze this exact row. That is the
		// outcome we wanted, so fall through and make current_version agree rather
		// than failing a request over a race we won either way.
		if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("publish builtin workflow template version %q: %w", builtin.Key, err)
		}
	}

	// Point the template at the version we just published so its advertised
	// current_version and the published row agree; a Run started without an
	// explicit version resolves through this column.
	if _, err := q.SetWorkflowTemplateCurrentVersion(ctx, db.SetWorkflowTemplateCurrentVersionParams{
		ID:             templateID,
		WorkspaceID:    workspaceID,
		CurrentVersion: version.Version,
	}); err != nil {
		return fmt.Errorf("set builtin workflow template current version %q: %w", builtin.Key, err)
	}
	return nil
}

// IsBuiltinWorkflowTemplateKey reports whether a key belongs to a platform
// built-in. The API surfaces this as is_builtin so the UI can explain why a
// template it did not create is present.
func IsBuiltinWorkflowTemplateKey(key string) bool {
	for _, builtin := range BuiltinWorkflowTemplates() {
		if strings.EqualFold(builtin.Key, key) {
			return true
		}
	}
	return false
}

// BuiltinWorkflowShippedDefinition returns the raw bytes of the nth revision this
// built-in has shipped, oldest first, with the newest last. Revision 0 of bug_fix
// is the five-node pre-input-node graph; the last index is the live embedded file.
//
// This exists so a test in another package can reproduce "a workspace seeded by an
// older binary" using the bytes that binary REALLY seeded. That is not a
// convenience: the seeder decides whether to upgrade by fingerprinting the stored
// definition against this shipping history, so a hand-typed previous revision
// fingerprints as something we never shipped and is correctly refused. An upgrade
// test built on a typed fixture therefore asserts the refuse-to-upgrade path while
// appearing to assert the upgrade path - green, and proving the opposite of what it
// says. Handing out the real bytes removes the opportunity.
//
// The slice is copied, so a caller mutating it (a test editing one instruction to
// build an "edited built-in" fixture, for instance) cannot corrupt the process-wide
// shipping history every later pristineness check compares against.
func BuiltinWorkflowShippedDefinition(key string, revision int) ([]byte, bool) {
	for _, builtin := range BuiltinWorkflowTemplates() {
		if !strings.EqualFold(builtin.Key, key) {
			continue
		}
		if revision < 0 || revision >= len(builtin.ShippedDefinitions) {
			return nil, false
		}
		return append([]byte(nil), builtin.ShippedDefinitions[revision]...), true
	}
	return nil, false
}

// isUniqueViolation now lives in plugin.go: upstream added an identical helper
// to this same package, so the copy that used to sit here would be a duplicate
// declaration. For the seeder a duplicate row is not an error but proof that a
// concurrent caller already did the work.
