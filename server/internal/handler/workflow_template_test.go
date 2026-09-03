package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/workflow"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// The workflow tables carry no foreign keys by design (plan section 4), so
// dropping the fixture workspace does not remove template rows. Every workflow
// test cleans up explicitly, or a later run of the suite would see templates
// seeded by an earlier one.
func cleanupWorkflowTemplates(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		ctx := context.Background()
		testPool.Exec(ctx, `DELETE FROM workflow_template_version WHERE workspace_id = $1`, testWorkspaceID)
		testPool.Exec(ctx, `DELETE FROM workflow_template WHERE workspace_id = $1`, testWorkspaceID)
	})
}

// builtinBugFixDefinition returns the embedded Bug Fix graph, which is the
// canonical example of a definition that passes Validate. Reusing it keeps this
// test from drifting out of agreement with the validator when the graph
// contract changes.
func builtinBugFixDefinition(t *testing.T) json.RawMessage {
	t.Helper()
	for _, b := range service.BuiltinWorkflowTemplates() {
		if b.Key == "bug_fix" {
			return json.RawMessage(b.Raw)
		}
	}
	t.Fatalf("builtin bug_fix workflow template is not registered")
	return nil
}

// workflowDefinitionNodeCount counts the nodes in a definition, so a test whose
// subject is not the built-in's shape can assert against the fixture it was handed
// rather than a literal that goes stale when the shipped graph changes.
func workflowDefinitionNodeCount(t *testing.T, raw json.RawMessage) int {
	t.Helper()
	_, n := workflowDefinitionShape(t, raw)
	return n
}

// workflowDefinitionShape returns a definition's entry node key and node count.
func workflowDefinitionShape(t *testing.T, raw json.RawMessage) (string, int) {
	t.Helper()
	def, err := workflow.ParseDefinition(raw)
	if err != nil {
		t.Fatalf("read the definition shape: it does not parse: %v", err)
	}
	return def.EntryNode, len(def.Nodes)
}

type workflowTemplateListBody struct {
	Templates []WorkflowTemplateResponse `json:"templates"`
	Total     int                        `json:"total"`
}

func listWorkflowTemplatesForTest(t *testing.T) workflowTemplateListBody {
	t.Helper()
	w := httptest.NewRecorder()
	testHandler.ListWorkflowTemplates(w, newRequest("GET", "/api/workflow-templates", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("ListWorkflowTemplates: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var body workflowTemplateListBody
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("ListWorkflowTemplates: decode body: %v", err)
	}
	return body
}

func findWorkflowTemplateByKey(list []WorkflowTemplateResponse, key string) (WorkflowTemplateResponse, bool) {
	for _, tpl := range list {
		if tpl.Key == key {
			return tpl, true
		}
	}
	return WorkflowTemplateResponse{}, false
}

// TestWorkflowTemplateListSeedsBuiltin asserts the load-bearing property of the
// list endpoint: it seeds the platform built-ins, so an existing workspace gets
// Bug Fix without a migration. The second call also pins idempotency - seeding
// on every list must not create a second copy.
func TestWorkflowTemplateListSeedsBuiltin(t *testing.T) {
	cleanupWorkflowTemplates(t)

	body := listWorkflowTemplatesForTest(t)
	bugFix, ok := findWorkflowTemplateByKey(body.Templates, "bug_fix")
	if !ok {
		t.Fatalf("ListWorkflowTemplates: bug_fix was not seeded; got %+v", body.Templates)
	}
	if bugFix.Name != "Bug Fix" {
		t.Fatalf("expected name %q, got %q", "Bug Fix", bugFix.Name)
	}
	if !bugFix.IsBuiltin {
		t.Fatalf("expected is_builtin true for bug_fix")
	}
	// The seeder publishes version 1 immediately: a template with no published
	// version cannot start a Run.
	if bugFix.Status != "published" {
		t.Fatalf("expected status published, got %q", bugFix.Status)
	}
	if bugFix.CurrentVersion == nil || *bugFix.CurrentVersion != 1 {
		t.Fatalf("expected current_version 1, got %v", bugFix.CurrentVersion)
	}
	// intake -> analyze -> implement -> validate -> acceptance -> end.
	//
	// Six, not five: the shipped revision now leads with an `intake` input node that
	// declares the title and description the Run dialog collects, so the graph says
	// on its face where the bug report enters. Was 5 before that node existed.
	if bugFix.NodeCount != 6 {
		t.Fatalf("expected node_count 6, got %d", bugFix.NodeCount)
	}

	// Seeding is idempotent by (workspace_id, key).
	second := listWorkflowTemplatesForTest(t)
	count := 0
	for _, tpl := range second.Templates {
		if tpl.Key == "bug_fix" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 bug_fix template after a second list, got %d", count)
	}

	// Detail must return the graph of the published version plus its history.
	w := httptest.NewRecorder()
	req := withURLParam(newRequest("GET", "/api/workflow-templates/"+bugFix.ID, nil), "id", bugFix.ID)
	testHandler.GetWorkflowTemplate(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GetWorkflowTemplate: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var detail WorkflowTemplateDetailResponse
	if err := json.NewDecoder(w.Body).Decode(&detail); err != nil {
		t.Fatalf("GetWorkflowTemplate: decode body: %v", err)
	}
	var graph struct {
		EntryNode string            `json:"entry_node"`
		Nodes     []json.RawMessage `json:"nodes"`
	}
	if err := json.Unmarshal(detail.Definition, &graph); err != nil {
		t.Fatalf("GetWorkflowTemplate: definition is not an object: %v", err)
	}
	// Both updated for the shipped revision's `intake` input node (was analyze / 5).
	// This assertion's subject is that the DETAIL endpoint serves the published
	// version's graph, so it is checked against the built-in fixture rather than
	// against literals - the endpoint is right if it serves what the seeder seeded.
	wantEntry, wantNodes := workflowDefinitionShape(t, builtinBugFixDefinition(t))
	if graph.EntryNode != wantEntry {
		t.Fatalf("expected entry_node %q, got %q", wantEntry, graph.EntryNode)
	}
	if len(graph.Nodes) != wantNodes {
		t.Fatalf("expected %d nodes in definition, got %d", wantNodes, len(graph.Nodes))
	}
	// And the served graph really is the intake revision. Comparing against the
	// fixture alone would keep passing if the built-in lost its input node, since
	// both sides would change together.
	served, err := workflow.ParseDefinition(detail.Definition)
	if err != nil {
		t.Fatalf("the served definition does not parse: %v", err)
	}
	if _, ok := served.EntryInputNode(); !ok {
		t.Fatalf("the served built-in graph has no entry input node (entry=%q)", served.EntryNode)
	}
	if len(detail.Versions) != 1 || detail.Versions[0].Version != 1 || detail.Versions[0].Status != "published" {
		t.Fatalf("expected one published version 1, got %+v", detail.Versions)
	}
	if detail.Versions[0].PublishedAt == nil {
		t.Fatalf("expected published_at on a published version")
	}
}

// TestWorkflowTemplateCreateInvalidDefinition asserts a rejected graph comes
// back as 422 *with the validator's messages*. The messages are the point: the
// editor points at the offending node, and a bare "invalid definition" would
// force the author to guess.
func TestWorkflowTemplateCreateInvalidDefinition(t *testing.T) {
	cleanupWorkflowTemplates(t)

	// Three independent problems: a dangling edge, no End node (so a Run could
	// never complete), and on_failure=rework with no rework_targets (so the
	// graph's cycles would not be enumerable).
	invalid := map[string]any{
		"schema_version": 1,
		"entry_node":     "analyze",
		"nodes": []map[string]any{
			{
				"key":               "analyze",
				"type":              "agent",
				"next":              []string{"implement"},
				"routing":           map[string]any{"strategy": "capability", "capability": "bug_analysis"},
				"submission_schema": "analysis",
				"on_failure":        "rework",
			},
		},
	}

	w := httptest.NewRecorder()
	testHandler.CreateWorkflowTemplate(w, newRequest("POST", "/api/workflow-templates", map[string]any{
		"key":        "broken_graph",
		"name":       "Broken Graph",
		"definition": invalid,
	}))
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("CreateWorkflowTemplate: expected 422, got %d: %s", w.Code, w.Body.String())
	}
	var resp workflowValidationResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("CreateWorkflowTemplate: decode body: %v", err)
	}
	if len(resp.Messages) == 0 {
		t.Fatalf("expected validation messages on a 422, got %+v", resp)
	}
	joined := strings.Join(resp.Messages, "\n")
	if !strings.Contains(joined, "rework_targets") {
		t.Fatalf("expected a rework_targets problem in messages, got:\n%s", joined)
	}

	// The rejected template must not exist: an invalid graph never reaches the
	// database, so nothing can later be published from it.
	if _, ok := findWorkflowTemplateByKey(listWorkflowTemplatesForTest(t).Templates, "broken_graph"); ok {
		t.Fatalf("a rejected definition created a template row")
	}

	// An unparseable definition (unknown field) is also a 422, not a 400: the
	// body is valid JSON, the graph is not.
	w = httptest.NewRecorder()
	testHandler.CreateWorkflowTemplate(w, newRequest("POST", "/api/workflow-templates", map[string]any{
		"key":  "typo_graph",
		"name": "Typo Graph",
		"definition": map[string]any{
			"schema_version": 1,
			"entry_node":     "end",
			"nodes":          []map[string]any{{"key": "end", "type": "end", "rework_target": []string{"end"}}},
		},
	}))
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("CreateWorkflowTemplate with unknown field: expected 422, got %d: %s", w.Code, w.Body.String())
	}

	// An empty key is a client mistake about the request, not the graph: 400.
	w = httptest.NewRecorder()
	testHandler.CreateWorkflowTemplate(w, newRequest("POST", "/api/workflow-templates", map[string]any{
		"key":        "",
		"name":       "No Key",
		"definition": builtinBugFixDefinition(t),
	}))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("CreateWorkflowTemplate with empty key: expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

// TestWorkflowTemplateArchiveBuiltinRefused pins the built-in guard: the seeder
// re-creates a missing built-in on the next list, so archiving one would only
// produce a template that silently comes back.
func TestWorkflowTemplateArchiveBuiltinRefused(t *testing.T) {
	cleanupWorkflowTemplates(t)

	bugFix, ok := findWorkflowTemplateByKey(listWorkflowTemplatesForTest(t).Templates, "bug_fix")
	if !ok {
		t.Fatalf("bug_fix was not seeded")
	}

	w := httptest.NewRecorder()
	req := withURLParam(newRequest("POST", "/api/workflow-templates/"+bugFix.ID+"/archive", nil), "id", bugFix.ID)
	testHandler.ArchiveWorkflowTemplate(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("ArchiveWorkflowTemplate on a built-in: expected 409, got %d: %s", w.Code, w.Body.String())
	}

	// Still listed and still published.
	after, ok := findWorkflowTemplateByKey(listWorkflowTemplatesForTest(t).Templates, "bug_fix")
	if !ok || after.Status != "published" {
		t.Fatalf("built-in was modified by a refused archive: %+v", after)
	}
}

// TestWorkflowTemplateCreatePublishArchive walks the author lifecycle for a
// user-authored template: created as a draft, published (which pins
// current_version), then archived.
// TestWorkflowTemplateBuiltinKeyIsReserved covers a squatting hole found in
// review: is_builtin used to be derived from the key string, so a member could
// create a template under key "bug_fix" in a workspace whose workflows page
// nobody had opened yet. That row would then (a) render as "platform-provided",
// (b) be un-archivable because the archive guard matched on the same key, and
// (c) permanently block the real built-in, since the seeder skips a key that
// already exists. Provenance now comes from created_by_type, and the key is
// refused outright at create.
func TestWorkflowTemplateBuiltinKeyIsReserved(t *testing.T) {
	cleanupWorkflowTemplates(t)

	for _, key := range []string{"bug_fix", "BUG_FIX"} {
		w := httptest.NewRecorder()
		testHandler.CreateWorkflowTemplate(w, newRequest("POST", "/api/workflow-templates", map[string]any{
			"key":        key,
			"name":       "Impostor",
			"definition": builtinBugFixDefinition(t),
		}))
		if w.Code != http.StatusConflict {
			t.Fatalf("creating a template under reserved key %q: expected 409, got %d: %s", key, w.Code, w.Body.String())
		}
	}

	// The real built-in must still seed afterwards, and be the only bug_fix row.
	list := listWorkflowTemplatesForTest(t)
	builtin, found := findWorkflowTemplateByKey(list.Templates, "bug_fix")
	if !found {
		t.Fatalf("the platform built-in must still seed after a squatting attempt")
	}
	if !builtin.IsBuiltin {
		t.Fatalf("the seeded built-in must report is_builtin")
	}

	// A user-authored template under a free key must never claim to be built-in,
	// even though its graph is byte-identical to the platform one.
	w := httptest.NewRecorder()
	testHandler.CreateWorkflowTemplate(w, newRequest("POST", "/api/workflow-templates", map[string]any{
		"key":        "my_bug_fix",
		"name":       "My Bug Fix",
		"definition": builtinBugFixDefinition(t),
	}))
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateWorkflowTemplate: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var mine WorkflowTemplateDetailResponse
	if err := json.NewDecoder(w.Body).Decode(&mine); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if mine.IsBuiltin {
		t.Fatalf("a member-authored template must not report is_builtin even with an identical graph")
	}
}

func duplicateBuiltinWorkflowTemplateForTest(t *testing.T, id string) WorkflowTemplateDetailResponse {
	t.Helper()
	w := httptest.NewRecorder()
	req := withURLParam(newRequest("POST", "/api/workflow-templates/"+id+"/duplicate", nil), "id", id)
	testHandler.DuplicateBuiltinWorkflowTemplate(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("DuplicateBuiltinWorkflowTemplate: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var copied WorkflowTemplateDetailResponse
	if err := json.NewDecoder(w.Body).Decode(&copied); err != nil {
		t.Fatalf("DuplicateBuiltinWorkflowTemplate: decode body: %v", err)
	}
	return copied
}

// TestWorkflowTemplateDuplicateBuiltin proves the action promised by the
// built-in read-only message: copying forks the effective built-in graph into
// an ordinary editable and immediately runnable template without changing the
// seeded source.
func TestWorkflowTemplateDuplicateBuiltin(t *testing.T) {
	cleanupWorkflowTemplates(t)

	builtin, ok := findWorkflowTemplateByKey(listWorkflowTemplatesForTest(t).Templates, "bug_fix")
	if !ok {
		t.Fatal("bug_fix was not seeded")
	}
	first := duplicateBuiltinWorkflowTemplateForTest(t, builtin.ID)
	if first.Key != "bug_fix_copy" || first.Name != "Bug Fix Copy" {
		t.Fatalf("unexpected first copy identity: key=%q name=%q", first.Key, first.Name)
	}
	if first.IsBuiltin {
		t.Fatal("a copied built-in must be member-authored")
	}
	if first.Status != "published" || first.CurrentVersion == nil || *first.CurrentVersion != 1 {
		t.Fatalf("copy must start runnable at version 1: status=%q current_version=%v", first.Status, first.CurrentVersion)
	}
	if len(first.Versions) != 1 || first.Versions[0].Status != "published" || first.Versions[0].PublishedAt == nil {
		t.Fatalf("copy must have one published version, got %+v", first.Versions)
	}
	runVersion, err := testHandler.Queries.GetPublishedWorkflowTemplateVersion(
		context.Background(),
		db.GetPublishedWorkflowTemplateVersionParams{
			TemplateID:  parseUUID(first.ID),
			WorkspaceID: parseUUID(testWorkspaceID),
		},
	)
	if err != nil {
		t.Fatalf("the Run resolver cannot load the copied template version: %v", err)
	}
	if runVersion.Version != 1 || runVersion.Status != "published" {
		t.Fatalf("unexpected copied Run version: %+v", runVersion)
	}
	var wantGraph, gotGraph any
	if err := json.Unmarshal(builtinBugFixDefinition(t), &wantGraph); err != nil {
		t.Fatalf("decode built-in graph: %v", err)
	}
	if err := json.Unmarshal(first.Definition, &gotGraph); err != nil {
		t.Fatalf("decode copied graph: %v", err)
	}
	if !reflect.DeepEqual(gotGraph, wantGraph) {
		t.Fatalf("copied definition differs from built-in\nwant: %s\ngot:  %s", builtinBugFixDefinition(t), first.Definition)
	}

	second := duplicateBuiltinWorkflowTemplateForTest(t, builtin.ID)
	if second.Key != "bug_fix_copy_2" {
		t.Fatalf("expected collision-safe second key bug_fix_copy_2, got %q", second.Key)
	}

	// The result is not merely labelled non-built-in: the normal editor write
	// path accepts it, which is the product reason this endpoint exists.
	w := patchWorkflowTemplateForTest(t, first.ID, map[string]any{"name": "My Bug Fix"})
	if w.Code != http.StatusOK {
		t.Fatalf("PATCH copied template: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var edited WorkflowTemplateDetailResponse
	if err := json.NewDecoder(w.Body).Decode(&edited); err != nil {
		t.Fatalf("decode edited copy: %v", err)
	}
	if edited.CurrentVersion == nil || *edited.CurrentVersion != 1 {
		t.Fatalf("editing the copy must preserve its runnable version: current_version=%v", edited.CurrentVersion)
	}
	if len(edited.Versions) != 1 || edited.Versions[0].Status != "published" {
		t.Fatalf("editing copy metadata must preserve its runnable version: %+v", edited.Versions)
	}
	listedBuiltin, ok := findWorkflowTemplateByKey(listWorkflowTemplatesForTest(t).Templates, "bug_fix")
	if !ok || !listedBuiltin.IsBuiltin || listedBuiltin.Name != builtin.Name {
		t.Fatalf("copying or editing the copy changed the built-in: %+v", listedBuiltin)
	}
}

func TestWorkflowTemplateDuplicateRejectsUserTemplate(t *testing.T) {
	cleanupWorkflowTemplates(t)
	created := createWorkflowTemplateForTest(t, "ordinary_template")

	w := httptest.NewRecorder()
	req := withURLParam(newRequest("POST", "/api/workflow-templates/"+created.ID+"/duplicate", nil), "id", created.ID)
	testHandler.DuplicateBuiltinWorkflowTemplate(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("duplicating a user template: expected 409, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkflowTemplateCreatePublishArchive(t *testing.T) {
	cleanupWorkflowTemplates(t)

	w := httptest.NewRecorder()
	testHandler.CreateWorkflowTemplate(w, newRequest("POST", "/api/workflow-templates", map[string]any{
		"key":         "Custom_Fix",
		"name":        "Custom Fix",
		"description": "A copy of the built-in graph under a different key.",
		"definition":  builtinBugFixDefinition(t),
	}))
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateWorkflowTemplate: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var created WorkflowTemplateDetailResponse
	if err := json.NewDecoder(w.Body).Decode(&created); err != nil {
		t.Fatalf("CreateWorkflowTemplate: decode body: %v", err)
	}
	// Keys are normalized to lowercase because idx_workflow_template_ws_key is
	// case-insensitive and intake resolves templates by key.
	if created.Key != "custom_fix" {
		t.Fatalf("expected key to be lowercased to custom_fix, got %q", created.Key)
	}
	if created.IsBuiltin {
		t.Fatalf("a user-authored template must not report is_builtin")
	}
	// A new template has a draft version and no current_version: it cannot start
	// a Run until someone publishes it.
	if created.Status != "draft" || created.CurrentVersion != nil {
		t.Fatalf("expected a draft with no current_version, got status=%q current_version=%v", created.Status, created.CurrentVersion)
	}
	if len(created.Versions) != 1 || created.Versions[0].Status != "draft" || created.Versions[0].PublishedAt != nil {
		t.Fatalf("expected one unpublished draft version, got %+v", created.Versions)
	}
	// Derived from the fixture rather than written as a literal. This test is about
	// the create/publish/archive lifecycle, not about the built-in's shape, and the
	// literal it used to carry (5) went stale the moment the shipped graph grew its
	// intake node - failing this test for a reason that has nothing to do with what
	// it asserts.
	wantNodes := workflowDefinitionNodeCount(t, builtinBugFixDefinition(t))
	if created.NodeCount != wantNodes {
		t.Fatalf("expected node_count %d, got %d", wantNodes, created.NodeCount)
	}

	// Duplicate key (case-insensitive) is a 409.
	w = httptest.NewRecorder()
	testHandler.CreateWorkflowTemplate(w, newRequest("POST", "/api/workflow-templates", map[string]any{
		"key":        "CUSTOM_FIX",
		"name":       "Custom Fix Again",
		"definition": builtinBugFixDefinition(t),
	}))
	if w.Code != http.StatusConflict {
		t.Fatalf("duplicate key: expected 409, got %d: %s", w.Code, w.Body.String())
	}

	// Publish.
	w = httptest.NewRecorder()
	req := withURLParam(newRequest("POST", "/api/workflow-templates/"+created.ID+"/publish", nil), "id", created.ID)
	testHandler.PublishWorkflowTemplate(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("PublishWorkflowTemplate: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var published WorkflowTemplateDetailResponse
	if err := json.NewDecoder(w.Body).Decode(&published); err != nil {
		t.Fatalf("PublishWorkflowTemplate: decode body: %v", err)
	}
	if published.Status != "published" {
		t.Fatalf("expected status published, got %q", published.Status)
	}
	if published.CurrentVersion == nil || *published.CurrentVersion != 1 {
		t.Fatalf("expected current_version 1, got %v", published.CurrentVersion)
	}
	if len(published.Versions) != 1 || published.Versions[0].Status != "published" || published.Versions[0].PublishedAt == nil {
		t.Fatalf("expected version 1 published with a timestamp, got %+v", published.Versions)
	}

	// A second publish has no draft to freeze - a published version is
	// immutable, so this is a conflict rather than a no-op success.
	w = httptest.NewRecorder()
	req = withURLParam(newRequest("POST", "/api/workflow-templates/"+created.ID+"/publish", nil), "id", created.ID)
	testHandler.PublishWorkflowTemplate(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("re-publish: expected 409, got %d: %s", w.Code, w.Body.String())
	}

	// Archive is allowed for a user-authored template.
	w = httptest.NewRecorder()
	req = withURLParam(newRequest("POST", "/api/workflow-templates/"+created.ID+"/archive", nil), "id", created.ID)
	testHandler.ArchiveWorkflowTemplate(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("ArchiveWorkflowTemplate: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var archived WorkflowTemplateResponse
	if err := json.NewDecoder(w.Body).Decode(&archived); err != nil {
		t.Fatalf("ArchiveWorkflowTemplate: decode body: %v", err)
	}
	if archived.Status != "archived" {
		t.Fatalf("expected status archived, got %q", archived.Status)
	}

	// Archived templates drop out of the default list but stay reachable with
	// include_archived - in-flight Runs pinned to this graph must remain
	// inspectable (plan section 12).
	if _, ok := findWorkflowTemplateByKey(listWorkflowTemplatesForTest(t).Templates, "custom_fix"); ok {
		t.Fatalf("archived template still appears in the default list")
	}
	w = httptest.NewRecorder()
	testHandler.ListWorkflowTemplates(w, newRequest("GET", "/api/workflow-templates?include_archived=true", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("ListWorkflowTemplates(include_archived): expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var withArchived workflowTemplateListBody
	if err := json.NewDecoder(w.Body).Decode(&withArchived); err != nil {
		t.Fatalf("ListWorkflowTemplates(include_archived): decode body: %v", err)
	}
	if _, ok := findWorkflowTemplateByKey(withArchived.Templates, "custom_fix"); !ok {
		t.Fatalf("archived template missing from include_archived=true list")
	}

	// Archiving twice is a conflict: archival is one-way.
	w = httptest.NewRecorder()
	req = withURLParam(newRequest("POST", "/api/workflow-templates/"+created.ID+"/archive", nil), "id", created.ID)
	testHandler.ArchiveWorkflowTemplate(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("re-archive: expected 409, got %d: %s", w.Code, w.Body.String())
	}
}

// TestWorkflowTemplateNotFound covers the cross-workspace posture: the
// workspace is part of every WHERE clause, so a valid UUID from elsewhere is a
// 404 rather than a leak.
func TestWorkflowTemplateNotFound(t *testing.T) {
	cleanupWorkflowTemplates(t)

	const stranger = "00000000-0000-0000-0000-0000000000ff"
	w := httptest.NewRecorder()
	testHandler.GetWorkflowTemplate(w, withURLParam(newRequest("GET", "/api/workflow-templates/"+stranger, nil), "id", stranger))
	if w.Code != http.StatusNotFound {
		t.Fatalf("GetWorkflowTemplate(unknown id): expected 404, got %d: %s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	testHandler.GetWorkflowTemplate(w, withURLParam(newRequest("GET", "/api/workflow-templates/not-a-uuid", nil), "id", "not-a-uuid"))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("GetWorkflowTemplate(malformed id): expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

// ---------------------------------------------------------------------------
// Draft save (PATCH) and standalone validate
// ---------------------------------------------------------------------------

// brokenWorkflowDefinition returns a graph the validator rejects for several
// independent reasons at once (dangling edge to an undeclared node, no End node
// so a Run could never complete, on_failure=rework with no rework_targets so
// the graph's cycles are not enumerable). Used to prove the endpoints report
// *all* the problems rather than the first.
func brokenWorkflowDefinition() map[string]any {
	return map[string]any{
		"schema_version": 1,
		"entry_node":     "analyze",
		"nodes": []map[string]any{
			{
				"key":               "analyze",
				"type":              "agent",
				"next":              []string{"implement"},
				"routing":           map[string]any{"strategy": "capability", "capability": "bug_analysis"},
				"submission_schema": "analysis",
				"on_failure":        "rework",
			},
		},
	}
}

// renamedWorkflowDefinition returns the built-in Bug Fix graph with its entry
// node's name changed, i.e. a still-valid edit that is byte-distinguishable
// from the original. Renaming a name (not a key) keeps every edge and rework
// target intact, so the only thing under test is whether the save landed.
func renamedWorkflowDefinition(t *testing.T, name string) map[string]any {
	t.Helper()
	var graph map[string]any
	if err := json.Unmarshal(builtinBugFixDefinition(t), &graph); err != nil {
		t.Fatalf("unmarshal builtin definition: %v", err)
	}
	nodes, ok := graph["nodes"].([]any)
	if !ok || len(nodes) == 0 {
		t.Fatalf("builtin definition has no nodes: %+v", graph)
	}
	entry, ok := nodes[0].(map[string]any)
	if !ok {
		t.Fatalf("builtin definition node 0 is not an object: %+v", nodes[0])
	}
	entry["name"] = name
	return graph
}

// createWorkflowTemplateForTest creates a user-authored draft template from the
// built-in graph and returns its detail response.
func createWorkflowTemplateForTest(t *testing.T, key string) WorkflowTemplateDetailResponse {
	t.Helper()
	w := httptest.NewRecorder()
	testHandler.CreateWorkflowTemplate(w, newRequest("POST", "/api/workflow-templates", map[string]any{
		"key":         key,
		"name":        "Editable",
		"description": "Created by the draft-save test.",
		"definition":  builtinBugFixDefinition(t),
	}))
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateWorkflowTemplate(%s): expected 201, got %d: %s", key, w.Code, w.Body.String())
	}
	var created WorkflowTemplateDetailResponse
	if err := json.NewDecoder(w.Body).Decode(&created); err != nil {
		t.Fatalf("CreateWorkflowTemplate(%s): decode body: %v", key, err)
	}
	return created
}

func patchWorkflowTemplateForTest(t *testing.T, id string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	if _, ok := body["revision"]; !ok {
		body["revision"] = int64(1)
	}
	w := httptest.NewRecorder()
	req := withURLParam(newRequest("PATCH", "/api/workflow-templates/"+id, body), "id", id)
	testHandler.UpdateWorkflowTemplate(w, req)
	return w
}

// entryNodeName reads nodes[0].name out of a stored/returned definition. The
// tests assert on it because it is the field renamedWorkflowDefinition moves.
func entryNodeName(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var graph struct {
		Nodes []struct {
			Name string `json:"name"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal(raw, &graph); err != nil {
		t.Fatalf("definition is not a graph object: %v", err)
	}
	if len(graph.Nodes) == 0 {
		t.Fatalf("definition has no nodes: %s", string(raw))
	}
	return graph.Nodes[0].Name
}

// TestWorkflowTemplatePatchUpdatesDraft is the happy path for the editor's save
// button: metadata lands on the template row, the graph lands on the existing
// draft version (rather than accumulating a version per keystroke), and the
// response is the same detail shape as GET so the client can replace its cache
// without a refetch.
func TestWorkflowTemplatePatchUpdatesDraft(t *testing.T) {
	cleanupWorkflowTemplates(t)

	created := createWorkflowTemplateForTest(t, "patch_draft")
	draftVersionID := created.Versions[0].ID

	w := patchWorkflowTemplateForTest(t, created.ID, map[string]any{
		"name":        "Renamed Fix",
		"description": "Edited by PATCH.",
		"definition":  renamedWorkflowDefinition(t, "Investigate the report"),
	})
	if w.Code != http.StatusOK {
		t.Fatalf("UpdateWorkflowTemplate: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var updated WorkflowTemplateDetailResponse
	if err := json.NewDecoder(w.Body).Decode(&updated); err != nil {
		t.Fatalf("UpdateWorkflowTemplate: decode body: %v", err)
	}
	if updated.Name != "Renamed Fix" || updated.Description != "Edited by PATCH." {
		t.Fatalf("metadata did not update: name=%q description=%q", updated.Name, updated.Description)
	}
	// `key` is immutable: intake and autopilots resolve templates by it.
	if updated.Key != created.Key {
		t.Fatalf("key changed from %q to %q", created.Key, updated.Key)
	}
	if got := entryNodeName(t, updated.Definition); got != "Investigate the report" {
		t.Fatalf("definition did not update: entry node name = %q", got)
	}
	// The draft is scratch space - overwritten in place, not versioned per save.
	if len(updated.Versions) != 1 {
		t.Fatalf("expected the save to reuse the single draft, got %+v", updated.Versions)
	}
	if updated.Versions[0].ID != draftVersionID {
		t.Fatalf("expected draft version %s to be updated in place, got %s", draftVersionID, updated.Versions[0].ID)
	}
	if updated.Versions[0].Status != "draft" || updated.Versions[0].PublishedAt != nil {
		t.Fatalf("a save must not publish: %+v", updated.Versions[0])
	}
	// A save is not a publish: nothing can pin this graph yet.
	if updated.Status != "draft" || updated.CurrentVersion != nil {
		t.Fatalf("expected status draft with no current_version, got status=%q current_version=%v", updated.Status, updated.CurrentVersion)
	}

	// GET must agree with the PATCH response - the client trusts the PATCH body
	// enough to skip a refetch.
	w = httptest.NewRecorder()
	testHandler.GetWorkflowTemplate(w, withURLParam(newRequest("GET", "/api/workflow-templates/"+created.ID, nil), "id", created.ID))
	if w.Code != http.StatusOK {
		t.Fatalf("GetWorkflowTemplate after PATCH: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var reread WorkflowTemplateDetailResponse
	if err := json.NewDecoder(w.Body).Decode(&reread); err != nil {
		t.Fatalf("GetWorkflowTemplate after PATCH: decode body: %v", err)
	}
	if got := entryNodeName(t, reread.Definition); got != "Investigate the report" {
		t.Fatalf("GET does not reflect the saved graph: entry node name = %q", got)
	}

	// A second writer holding the original revision must lose without changing
	// either metadata or the graph. Its working copy remains client-owned and can
	// be copied before the author reloads the winning revision.
	stale := patchWorkflowTemplateForTest(t, created.ID, map[string]any{
		"revision":   created.Revision,
		"name":       "Stale Writer",
		"definition": renamedWorkflowDefinition(t, "Stale edit"),
	})
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale PATCH: expected 409, got %d: %s", stale.Code, stale.Body.String())
	}
	var conflictBody struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(stale.Body).Decode(&conflictBody); err != nil {
		t.Fatalf("decode stale PATCH: %v", err)
	}
	if conflictBody.Code != "workflow_template_revision_conflict" {
		t.Fatalf("stale PATCH code = %q", conflictBody.Code)
	}

	// A metadata-only PATCH must leave the graph alone: the editor renames a
	// template without shipping the canvas.
	w = patchWorkflowTemplateForTest(t, created.ID, map[string]any{"revision": int64(2), "name": "Renamed Again"})
	if w.Code != http.StatusOK {
		t.Fatalf("metadata-only PATCH: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var metaOnly WorkflowTemplateDetailResponse
	if err := json.NewDecoder(w.Body).Decode(&metaOnly); err != nil {
		t.Fatalf("metadata-only PATCH: decode body: %v", err)
	}
	if metaOnly.Name != "Renamed Again" {
		t.Fatalf("expected name Renamed Again, got %q", metaOnly.Name)
	}
	if metaOnly.Description != "Edited by PATCH." {
		t.Fatalf("an omitted description must be preserved, got %q", metaOnly.Description)
	}
	if got := entryNodeName(t, metaOnly.Definition); got != "Investigate the report" {
		t.Fatalf("a metadata-only PATCH changed the graph: entry node name = %q", got)
	}
}

// TestWorkflowTemplateSQLConcurrentRevisionCAS is deliberately below HTTP: two
// independent callers race the generated UPDATE with the same revision. The
// database, rather than handler timing, must choose exactly one winner.
func TestWorkflowTemplateSQLConcurrentRevisionCAS(t *testing.T) {
	cleanupWorkflowTemplates(t)
	created := createWorkflowTemplateForTest(t, "sql_concurrent_cas")

	type result struct {
		name string
		row  db.WorkflowTemplate
		err  error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	for _, name := range []string{"Writer A", "Writer B"} {
		name := name
		go func() {
			<-start
			row, err := testHandler.Queries.UpdateWorkflowTemplate(context.Background(), db.UpdateWorkflowTemplateParams{
				ID:               parseUUID(created.ID),
				WorkspaceID:      parseUUID(testWorkspaceID),
				Name:             pgtype.Text{String: name, Valid: true},
				ExpectedRevision: created.Revision,
			})
			results <- result{name: name, row: row, err: err}
		}()
	}
	close(start)

	var winner string
	wins, conflicts := 0, 0
	for range 2 {
		got := <-results
		switch {
		case got.err == nil:
			wins++
			winner = got.name
			if got.row.Revision != created.Revision+1 {
				t.Errorf("winner revision = %d, want %d", got.row.Revision, created.Revision+1)
			}
		case errors.Is(got.err, pgx.ErrNoRows):
			conflicts++
		default:
			t.Fatalf("writer %q error = %v", got.name, got.err)
		}
	}
	if wins != 1 || conflicts != 1 {
		t.Fatalf("concurrent CAS results: wins=%d conflicts=%d", wins, conflicts)
	}

	var storedName string
	var storedRevision int64
	if err := testPool.QueryRow(context.Background(),
		"SELECT name, revision FROM workflow_template WHERE id = $1 AND workspace_id = $2",
		created.ID, testWorkspaceID,
	).Scan(&storedName, &storedRevision); err != nil {
		t.Fatalf("reload concurrent CAS winner: %v", err)
	}
	if storedName != winner || storedRevision != created.Revision+1 {
		t.Fatalf("stored template = name %q revision %d, want winner %q revision %d", storedName, storedRevision, winner, created.Revision+1)
	}
}

// TestWorkflowTemplatePatchInvalidDefinitionRejectedBeforeWrite pins the
// ordering that matters: validation runs before the transaction, so a rejected
// graph leaves the stored draft byte-identical. Persisting an invalid graph
// would let it be published into an unrunnable, immutable version.
func TestWorkflowTemplatePatchInvalidDefinitionRejectedBeforeWrite(t *testing.T) {
	cleanupWorkflowTemplates(t)

	created := createWorkflowTemplateForTest(t, "patch_invalid")
	before := string(created.Definition)

	// The name is valid and would normally land; it must be rolled back with
	// the graph, or the author sees a rename they cannot explain.
	w := patchWorkflowTemplateForTest(t, created.ID, map[string]any{
		"name":       "Should Not Land",
		"definition": brokenWorkflowDefinition(),
	})
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("PATCH with an invalid graph: expected 422, got %d: %s", w.Code, w.Body.String())
	}
	var resp workflowValidationResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("PATCH invalid: decode body: %v", err)
	}
	if resp.Error != "invalid workflow definition" {
		t.Fatalf("expected error %q, got %q", "invalid workflow definition", resp.Error)
	}
	if len(resp.Messages) == 0 {
		t.Fatalf("expected validation messages on a 422, got %+v", resp)
	}
	if joined := strings.Join(resp.Messages, "\n"); !strings.Contains(joined, "rework_targets") {
		t.Fatalf("expected a rework_targets problem in messages, got:\n%s", joined)
	}

	w = httptest.NewRecorder()
	testHandler.GetWorkflowTemplate(w, withURLParam(newRequest("GET", "/api/workflow-templates/"+created.ID, nil), "id", created.ID))
	if w.Code != http.StatusOK {
		t.Fatalf("GetWorkflowTemplate after rejected PATCH: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var after WorkflowTemplateDetailResponse
	if err := json.NewDecoder(w.Body).Decode(&after); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if string(after.Definition) != before {
		t.Fatalf("a rejected definition was written:\nbefore: %s\nafter:  %s", before, string(after.Definition))
	}
	if after.Name != created.Name {
		t.Fatalf("a rejected PATCH renamed the template: %q -> %q", created.Name, after.Name)
	}
	if len(after.Versions) != 1 {
		t.Fatalf("a rejected PATCH created a version: %+v", after.Versions)
	}

	// An unparseable graph (unknown field) is also 422, not 400: the HTTP body
	// is valid JSON, the graph is not.
	w = patchWorkflowTemplateForTest(t, created.ID, map[string]any{
		"definition": map[string]any{
			"schema_version": 1,
			"entry_node":     "end",
			"nodes":          []map[string]any{{"key": "end", "type": "end", "rework_target": []string{"end"}}},
		},
	})
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("PATCH with an unknown field: expected 422, got %d: %s", w.Code, w.Body.String())
	}

	// An empty name is a request-level mistake, not a graph problem: 400.
	w = patchWorkflowTemplateForTest(t, created.ID, map[string]any{"name": "   "})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("PATCH with a blank name: expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

// TestWorkflowTemplatePatchBuiltinRefused pins the built-in guard. The seeder
// owns those rows and matches on key, so an edited built-in would either be
// silently reverted or permanently diverge from the shipped JSON.
func TestWorkflowTemplatePatchBuiltinRefused(t *testing.T) {
	cleanupWorkflowTemplates(t)

	bugFix, ok := findWorkflowTemplateByKey(listWorkflowTemplatesForTest(t).Templates, "bug_fix")
	if !ok {
		t.Fatalf("bug_fix was not seeded")
	}

	w := patchWorkflowTemplateForTest(t, bugFix.ID, map[string]any{
		"name":       "Hijacked",
		"definition": renamedWorkflowDefinition(t, "Hijacked entry"),
	})
	if w.Code != http.StatusConflict {
		t.Fatalf("PATCH on a built-in: expected 409, got %d: %s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	testHandler.GetWorkflowTemplate(w, withURLParam(newRequest("GET", "/api/workflow-templates/"+bugFix.ID, nil), "id", bugFix.ID))
	if w.Code != http.StatusOK {
		t.Fatalf("GetWorkflowTemplate: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var after WorkflowTemplateDetailResponse
	if err := json.NewDecoder(w.Body).Decode(&after); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if after.Name != bugFix.Name {
		t.Fatalf("a refused PATCH renamed the built-in: %q -> %q", bugFix.Name, after.Name)
	}
	if len(after.Versions) != 1 || after.Versions[0].Status != "published" {
		t.Fatalf("a refused PATCH touched built-in version history: %+v", after.Versions)
	}
}

// TestWorkflowTemplatePatchPublishedCreatesNewDraft is the immutability
// invariant (plan section 4). Editing a template whose only version is
// published must open a NEW draft instead of rewriting frozen bytes: in-flight
// Runs resolve node semantics through exactly the published definition, so
// mutating it would retroactively change what a running process is executing.
func TestWorkflowTemplatePatchPublishedCreatesNewDraft(t *testing.T) {
	cleanupWorkflowTemplates(t)

	created := createWorkflowTemplateForTest(t, "patch_published")

	w := httptest.NewRecorder()
	testHandler.PublishWorkflowTemplate(w, withURLParam(newRequest("POST", "/api/workflow-templates/"+created.ID+"/publish", nil), "id", created.ID))
	if w.Code != http.StatusOK {
		t.Fatalf("PublishWorkflowTemplate: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var published WorkflowTemplateDetailResponse
	if err := json.NewDecoder(w.Body).Decode(&published); err != nil {
		t.Fatalf("PublishWorkflowTemplate: decode body: %v", err)
	}
	if len(published.Versions) != 1 || published.Versions[0].Status != "published" {
		t.Fatalf("expected exactly one published version, got %+v", published.Versions)
	}
	publishedVersionID := published.Versions[0].ID
	publishedBytes := string(published.Definition)

	w = patchWorkflowTemplateForTest(t, created.ID, map[string]any{
		"definition": renamedWorkflowDefinition(t, "Edited after publish"),
	})
	if w.Code != http.StatusOK {
		t.Fatalf("PATCH on a published template: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var updated WorkflowTemplateDetailResponse
	if err := json.NewDecoder(w.Body).Decode(&updated); err != nil {
		t.Fatalf("PATCH published: decode body: %v", err)
	}
	if len(updated.Versions) != 2 {
		t.Fatalf("expected a new draft version alongside the published one, got %+v", updated.Versions)
	}
	// Ordered version DESC: the new draft is first.
	newDraft := updated.Versions[0]
	if newDraft.Status != "draft" || newDraft.Version != 2 || newDraft.PublishedAt != nil {
		t.Fatalf("expected an unpublished draft version 2, got %+v", newDraft)
	}
	if newDraft.ID == publishedVersionID {
		t.Fatalf("the published version was rewritten instead of a new draft being created")
	}
	// The published row must be byte-identical - and must remain the version the
	// template advertises until someone publishes the draft, or a Run started
	// now would silently pick up unreviewed edits.
	stillPublished := updated.Versions[1]
	if stillPublished.ID != publishedVersionID || stillPublished.Status != "published" || stillPublished.PublishedAt == nil {
		t.Fatalf("published version 1 was altered: %+v", stillPublished)
	}
	if updated.CurrentVersion == nil || *updated.CurrentVersion != 1 {
		t.Fatalf("expected current_version to stay 1 until the draft is published, got %v", updated.CurrentVersion)
	}
	// The detail response resolves the *effective* version, which is still the
	// published one - the draft is invisible to Runs.
	if string(updated.Definition) != publishedBytes {
		t.Fatalf("the effective definition changed before publish:\nwas: %s\nnow: %s", publishedBytes, string(updated.Definition))
	}

	var storedPublished string
	if err := testPool.QueryRow(context.Background(),
		`SELECT definition::text FROM workflow_template_version WHERE id = $1`,
		publishedVersionID,
	).Scan(&storedPublished); err != nil {
		t.Fatalf("read stored published definition: %v", err)
	}
	if entryNodeName(t, json.RawMessage(storedPublished)) == "Edited after publish" {
		t.Fatalf("the published version's stored bytes were mutated: %s", storedPublished)
	}

	// A second save reuses the draft it just created rather than stacking a
	// version 3.
	w = patchWorkflowTemplateForTest(t, created.ID, map[string]any{
		"revision":   int64(2),
		"definition": renamedWorkflowDefinition(t, "Edited twice"),
	})
	if w.Code != http.StatusOK {
		t.Fatalf("second PATCH: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var second WorkflowTemplateDetailResponse
	if err := json.NewDecoder(w.Body).Decode(&second); err != nil {
		t.Fatalf("second PATCH: decode body: %v", err)
	}
	if len(second.Versions) != 2 || second.Versions[0].ID != newDraft.ID {
		t.Fatalf("a second save must reuse the draft, got %+v", second.Versions)
	}

	// Publishing the draft is what makes the edit effective.
	w = httptest.NewRecorder()
	testHandler.PublishWorkflowTemplate(w, withURLParam(newRequest("POST", "/api/workflow-templates/"+created.ID+"/publish", nil), "id", created.ID))
	if w.Code != http.StatusOK {
		t.Fatalf("publishing the new draft: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var republished WorkflowTemplateDetailResponse
	if err := json.NewDecoder(w.Body).Decode(&republished); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if republished.CurrentVersion == nil || *republished.CurrentVersion != 2 {
		t.Fatalf("expected current_version 2 after publishing the draft, got %v", republished.CurrentVersion)
	}
	if got := entryNodeName(t, republished.Definition); got != "Edited twice" {
		t.Fatalf("expected the published edit to become effective, entry node name = %q", got)
	}
}

func builderAllNodeTypesDefinition() map[string]any {
	return map[string]any{
		"schema_version": 1,
		"entry_node":     "intake",
		"nodes": []map[string]any{
			{"key": "intake", "type": "input", "input_mode": "text", "next": []string{"plan"}},
			{
				"key": "plan", "type": "agent", "next": []string{"gate"},
				"routing":           map[string]any{"strategy": "capability", "capability": "code_change"},
				"submission_schema": "code_change",
			},
			{
				"key": "gate", "type": "condition",
				"branches": []map[string]any{
					{"when_verdict": "pass", "target": "spread"},
					{"when_verdict": "fail", "target": "acceptance"},
				},
			},
			{"key": "spread", "type": "fan_out", "next": []string{"worker"}, "fan_out_max": 3},
			{
				"key": "worker", "type": "agent", "next": []string{"gather"},
				"routing":           map[string]any{"strategy": "capability", "capability": "code_change"},
				"submission_schema": "code_change",
			},
			{
				"key": "gather", "type": "join", "next": []string{"acceptance"},
				"join_policy": "fail_fast", "join_sources": []string{"worker"},
			},
			{"key": "acceptance", "type": "acceptance", "next": []string{"end"}, "rework_targets": []string{"plan"}},
			{"key": "end", "type": "end"},
		},
	}
}

// TestWorkflowTemplateValidateAcceptsBuilderAllNodeTypes is the cross-language
// contract for the builder's outgoing JSON: every currently supported node kind
// is present, but input_mode belongs only to the input node. This goes through
// the HTTP decoder and the same strict validator used by save and publish.
func TestWorkflowTemplateValidateAcceptsBuilderAllNodeTypes(t *testing.T) {
	body := map[string]any{"definition": builderAllNodeTypesDefinition()}
	w := httptest.NewRecorder()
	testHandler.ValidateWorkflowDefinition(w, newRequest("POST", "/api/workflow-templates/validate", body))
	if w.Code != http.StatusOK {
		t.Fatalf("validate(builder payload): expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp ValidateWorkflowDefinitionResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("validate(builder payload): decode body: %v", err)
	}
	if !resp.Valid || len(resp.Messages) != 0 {
		t.Fatalf("builder payload must pass strict server validation, got %+v", resp)
	}
}

func assertOnlyInputNodeHasInputMode(t *testing.T, raw json.RawMessage) {
	t.Helper()
	var definition struct {
		Nodes []map[string]any `json:"nodes"`
	}
	if err := json.Unmarshal(raw, &definition); err != nil {
		t.Fatalf("decode stored builder definition: %v", err)
	}
	wantTypes := map[string]bool{
		"input": false, "agent": false, "condition": false, "fan_out": false,
		"join": false, "acceptance": false, "end": false,
	}
	for _, node := range definition.Nodes {
		typeName, _ := node["type"].(string)
		if _, known := wantTypes[typeName]; known {
			wantTypes[typeName] = true
		}
		mode, hasMode := node["input_mode"]
		if typeName == "input" {
			if !hasMode || mode != "text" {
				t.Fatalf("input node lost input_mode: %+v", node)
			}
		} else if hasMode {
			t.Fatalf("%s node retained forbidden input_mode: %+v", typeName, node)
		}
	}
	for typeName, seen := range wantTypes {
		if !seen {
			t.Fatalf("reloaded definition is missing %s node: %s", typeName, string(raw))
		}
	}
}

func TestWorkflowTemplateBuilderAllNodeTypesSavePublishReload(t *testing.T) {
	cleanupWorkflowTemplates(t)
	definition := builderAllNodeTypesDefinition()
	w := httptest.NewRecorder()
	testHandler.CreateWorkflowTemplate(w, newRequest("POST", "/api/workflow-templates", map[string]any{
		"key": "builder_all_node_types", "name": "Builder All Node Types", "definition": definition,
	}))
	if w.Code != http.StatusCreated {
		t.Fatalf("create(builder payload): expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var created WorkflowTemplateDetailResponse
	if err := json.NewDecoder(w.Body).Decode(&created); err != nil {
		t.Fatalf("create(builder payload): decode body: %v", err)
	}

	w = patchWorkflowTemplateForTest(t, created.ID, map[string]any{"definition": definition})
	if w.Code != http.StatusOK {
		t.Fatalf("save(builder payload): expected 200, got %d: %s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	testHandler.PublishWorkflowTemplate(w, withURLParam(newRequest("POST", "/api/workflow-templates/"+created.ID+"/publish", nil), "id", created.ID))
	if w.Code != http.StatusOK {
		t.Fatalf("publish(builder payload): expected 200, got %d: %s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	testHandler.GetWorkflowTemplate(w, withURLParam(newRequest("GET", "/api/workflow-templates/"+created.ID, nil), "id", created.ID))
	if w.Code != http.StatusOK {
		t.Fatalf("reload(builder payload): expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var reloaded WorkflowTemplateDetailResponse
	if err := json.NewDecoder(w.Body).Decode(&reloaded); err != nil {
		t.Fatalf("reload(builder payload): decode body: %v", err)
	}
	if reloaded.CurrentVersion == nil || *reloaded.CurrentVersion != 1 {
		t.Fatalf("reloaded builder template is not published: current_version=%v", reloaded.CurrentVersion)
	}
	assertOnlyInputNodeHasInputMode(t, reloaded.Definition)
}

// TestWorkflowTemplateValidateEndpoint pins the deliberate status-code choice:
// this endpoint answers a question and writes nothing, so an invalid graph is a
// 200 with valid:false. Returning 422 would make every keystroke-triggered
// check in the editor look like a client error in logs and metrics.
func TestWorkflowTemplateValidateEndpoint(t *testing.T) {
	cleanupWorkflowTemplates(t)

	validate := func(body any) (int, ValidateWorkflowDefinitionResponse) {
		t.Helper()
		w := httptest.NewRecorder()
		testHandler.ValidateWorkflowDefinition(w, newRequest("POST", "/api/workflow-templates/validate", body))
		var resp ValidateWorkflowDefinitionResponse
		if w.Code == http.StatusOK {
			if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
				t.Fatalf("validate: decode body: %v", err)
			}
		}
		return w.Code, resp
	}

	code, resp := validate(map[string]any{"definition": builtinBugFixDefinition(t)})
	if code != http.StatusOK {
		t.Fatalf("validate(builtin): expected 200, got %d", code)
	}
	if !resp.Valid {
		t.Fatalf("the built-in graph must validate, got messages: %+v", resp.Messages)
	}
	// Non-nil so a client can iterate without a null check.
	if resp.Messages == nil {
		t.Fatalf("expected an empty messages array on success, got null")
	}
	if len(resp.Messages) != 0 {
		t.Fatalf("expected no messages on a valid graph, got %+v", resp.Messages)
	}

	code, resp = validate(map[string]any{"definition": brokenWorkflowDefinition()})
	if code != http.StatusOK {
		t.Fatalf("validate(broken): expected 200 (a wrong graph is a successful answer), got %d", code)
	}
	if resp.Valid {
		t.Fatalf("a graph with a dangling edge and no End node must not validate")
	}
	if len(resp.Messages) == 0 {
		t.Fatalf("expected messages explaining why the graph is invalid")
	}
	if joined := strings.Join(resp.Messages, "\n"); !strings.Contains(joined, "rework_targets") {
		t.Fatalf("expected a rework_targets problem in messages, got:\n%s", joined)
	}

	// An unparseable graph reports through the same valid:false channel: to the
	// author both mean "this cannot be saved yet".
	code, resp = validate(map[string]any{
		"definition": map[string]any{
			"schema_version": 1,
			"entry_node":     "end",
			"nodes":          []map[string]any{{"key": "end", "type": "end", "rework_target": []string{"end"}}},
		},
	})
	if code != http.StatusOK || resp.Valid || len(resp.Messages) == 0 {
		t.Fatalf("validate(unparseable): expected 200 valid:false with messages, got %d %+v", code, resp)
	}

	// A missing definition is a client bug, not a graph verdict: there is
	// nothing to report messages about.
	if code, _ := validate(map[string]any{}); code != http.StatusBadRequest {
		t.Fatalf("validate(no definition): expected 400, got %d", code)
	}

	// Validation writes nothing: the only templates present are the built-ins
	// the list endpoint's own seeder created.
	if got, want := len(listWorkflowTemplatesForTest(t).Templates), len(service.BuiltinWorkflowTemplates()); got != want {
		t.Fatalf("the validate endpoint must not create templates: got %d templates, want %d", got, want)
	}
}
