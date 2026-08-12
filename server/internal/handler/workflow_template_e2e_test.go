package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/workflow"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// newFreshWorkspaceForWorkflowE2E creates a workspace that has never seen the
// seeder, which is the whole point of the test: the built-in must appear for a
// workspace that predates the feature, with no migration and no backfill. The
// shared handler fixture workspace cannot prove that, because earlier tests in
// the suite may already have seeded it.
func newFreshWorkspaceForWorkflowE2E(t *testing.T) string {
	t.Helper()
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	ctx := context.Background()
	const slug = "workflow-e2e-fresh"

	// Drop any residue from an interrupted run first: the workflow tables carry
	// no foreign keys by design (plan section 4), so a leftover template row
	// would survive its workspace and make "freshly seeded" unverifiable.
	cleanupFreshWorkflowWorkspace(ctx, slug)

	var workspaceID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO workspace (name, slug, description, issue_prefix)
		VALUES ($1, $2, $3, $4)
		RETURNING id
	`, "Workflow E2E Fresh", slug, "Workspace that has never been seeded", "WFE").Scan(&workspaceID); err != nil {
		t.Fatalf("create fresh workspace: %v", err)
	}
	if _, err := testPool.Exec(ctx, `
		INSERT INTO member (workspace_id, user_id, role)
		VALUES ($1, $2, 'owner')
	`, workspaceID, testUserID); err != nil {
		t.Fatalf("add member to fresh workspace: %v", err)
	}

	t.Cleanup(func() { cleanupFreshWorkflowWorkspace(context.Background(), slug) })
	return workspaceID
}

func cleanupFreshWorkflowWorkspace(ctx context.Context, slug string) {
	testPool.Exec(ctx, `
		DELETE FROM workflow_template_version
		WHERE workspace_id IN (SELECT id FROM workspace WHERE slug = $1)
	`, slug)
	testPool.Exec(ctx, `
		DELETE FROM workflow_template
		WHERE workspace_id IN (SELECT id FROM workspace WHERE slug = $1)
	`, slug)
	testPool.Exec(ctx, `DELETE FROM workspace WHERE slug = $1`, slug)
}

// newRequestForWorkspace is newRequest scoped to an explicit workspace, since
// the workflow endpoints take the workspace from the header rather than a URL
// segment.
func newRequestForWorkspace(workspaceID, method, path string) *http.Request {
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-ID", testUserID)
	req.Header.Set("X-Workspace-ID", workspaceID)
	return req
}

// TestBugFixBuiltinIsSeededAndServedEndToEnd is the end-to-end proof for the
// user-visible complaint: "a workspace has no workflow to run".
//
// It walks the full seam - seeder -> database rows -> validator -> HTTP list
// handler - for a workspace created without any workflow rows, and asserts the
// four properties that together make the built-in usable:
//
//  1. the template exists after EnsureBuiltinWorkflowTemplates,
//  2. it is published with current_version set (an unpublished template cannot
//     start a Run, so a draft built-in would be indistinguishable from missing),
//  3. the bytes pinned into that published version still pass workflow.Validate
//     (a published version is immutable, so it can never be re-validated later -
//     if it were invalid, every Run pinned to it would be unrunnable), and
//  4. GET /api/workflow-templates returns it with is_builtin true, which is what
//     the workflows page actually renders.
func TestBugFixBuiltinIsSeededAndServedEndToEnd(t *testing.T) {
	workspaceID := newFreshWorkspaceForWorkflowE2E(t)
	wsUUID := parseUUID(workspaceID)
	ctx := context.Background()

	// Pre-condition: nothing to run yet. This is the state every existing
	// workspace is in before the seeder ships.
	before, err := testHandler.Queries.ListWorkflowTemplates(ctx, db.ListWorkflowTemplatesParams{
		WorkspaceID:     wsUUID,
		IncludeArchived: true,
	})
	if err != nil {
		t.Fatalf("list templates before seeding: %v", err)
	}
	if len(before) != 0 {
		t.Fatalf("fresh workspace already has %d workflow templates: %+v", len(before), before)
	}

	// (1) The seeder is the only thing that runs - no migration, no backfill.
	if err := service.EnsureBuiltinWorkflowTemplates(ctx, testHandler.Queries, wsUUID); err != nil {
		t.Fatalf("EnsureBuiltinWorkflowTemplates: %v", err)
	}

	tpl, err := testHandler.Queries.GetWorkflowTemplateByKey(ctx, db.GetWorkflowTemplateByKeyParams{
		WorkspaceID: wsUUID,
		Key:         "bug_fix",
	})
	if err != nil {
		t.Fatalf("bug_fix template absent after seeding: %v", err)
	}
	if tpl.Name != "Bug Fix" {
		t.Errorf("name = %q, want %q", tpl.Name, "Bug Fix")
	}

	// (2) Published, with current_version pinned. Both matter: status is what the
	// UI shows, current_version is what a Run resolves.
	if tpl.Status != "published" {
		t.Errorf("status = %q, want %q", tpl.Status, "published")
	}
	if !tpl.CurrentVersion.Valid {
		t.Fatal("current_version is null; a template with no published version cannot start a Run")
	}
	if tpl.CurrentVersion.Int32 != 1 {
		t.Errorf("current_version = %d, want 1", tpl.CurrentVersion.Int32)
	}

	// (3) The pinned bytes still validate. This resolves the version exactly the
	// way a Run does, so it proves the graph a Run would execute is legal.
	version, err := testHandler.Queries.GetPublishedWorkflowTemplateVersion(ctx, db.GetPublishedWorkflowTemplateVersionParams{
		TemplateID:  tpl.ID,
		WorkspaceID: wsUUID,
	})
	if err != nil {
		t.Fatalf("no published version for the seeded built-in: %v", err)
	}
	if version.Version != tpl.CurrentVersion.Int32 {
		t.Errorf("published version %d does not match current_version %d", version.Version, tpl.CurrentVersion.Int32)
	}
	if !version.PublishedAt.Valid {
		t.Error("published version has no published_at timestamp")
	}
	def, err := workflow.ParseDefinition(version.Definition)
	if err != nil {
		t.Fatalf("pinned definition does not parse: %v", err)
	}
	if err := workflow.Validate(def, workflow.DefaultWorkspacePolicy, workflow.DefaultSchemaRegistry); err != nil {
		t.Fatalf("pinned definition does not validate: %v", err)
	}
	// The spine from plan section 11 - if this drifts, the built-in still
	// validates but stops being the Bug Fix process.
	//
	// The entry node is `intake`, an INPUT node, and the graph has six nodes. Both
	// were different before this feature (entry `analyze`, five nodes): the seeded
	// graph had nothing representing where the bug report enters, so an author
	// reading the canvas could not see the workflow took an input at all. The
	// assertion is updated, not relaxed - it went from naming one node to naming the
	// node AND its kind AND what it declares, because "the entry node is an input
	// node declaring the fields the dialog collects" is the property the feature
	// adds and the one a future revision must not silently drop.
	if def.EntryNode != "intake" {
		t.Errorf("entry_node = %q, want %q", def.EntryNode, "intake")
	}
	intake, hasIntake := def.EntryInputNode()
	if !hasIntake {
		t.Fatalf("the seeded graph's entry node %q is not an input node; the Run dialog would fall back to hardcoded fields", def.EntryNode)
	}
	if len(intake.InputFields) != 2 {
		t.Errorf("intake declares %d fields, want 2 (title, description)", len(intake.InputFields))
	}
	if len(def.Nodes) != 6 {
		t.Errorf("node count = %d, want 6 (intake, analyze, implement, validate, acceptance, end)", len(def.Nodes))
	}
	// The pre-existing spine must all still be there: a revision that added intake
	// and dropped a step would satisfy every count above.
	for _, key := range []string{"analyze", "implement", "validate", "acceptance", "end"} {
		if _, ok := def.NodeByKey(key); !ok {
			t.Errorf("the seeded graph has no %q node", key)
		}
	}

	// (4) What the workflows page actually receives. Note this request runs the
	// seeder a second time; the count assertion below is therefore also an
	// idempotency check on the path that fires on every page load.
	w := httptest.NewRecorder()
	testHandler.ListWorkflowTemplates(w, newRequestForWorkspace(workspaceID, "GET", "/api/workflow-templates"))
	if w.Code != http.StatusOK {
		t.Fatalf("ListWorkflowTemplates: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var body workflowTemplateListBody
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("ListWorkflowTemplates: decode body: %v", err)
	}
	matches := 0
	var served WorkflowTemplateResponse
	for _, candidate := range body.Templates {
		if candidate.Key == "bug_fix" {
			matches++
			served = candidate
		}
	}
	if matches != 1 {
		t.Fatalf("expected exactly 1 bug_fix template in the list, got %d: %+v", matches, body.Templates)
	}
	if !served.IsBuiltin {
		t.Error("is_builtin = false; the UI cannot explain a template nobody authored, and would offer an archive the server refuses")
	}
	if served.Status != "published" {
		t.Errorf("served status = %q, want %q", served.Status, "published")
	}
	if served.CurrentVersion == nil || *served.CurrentVersion != 1 {
		t.Errorf("served current_version = %v, want 1", served.CurrentVersion)
	}
	if served.NodeCount != 6 {
		t.Errorf("served node_count = %d, want 6", served.NodeCount)
	}
	if served.WorkspaceID != workspaceID {
		t.Errorf("served workspace_id = %q, want %q", served.WorkspaceID, workspaceID)
	}
}
