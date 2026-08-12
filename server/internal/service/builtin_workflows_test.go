package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/workflow"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Evals for the platform's built-in workflow graphs, and for the seeder that
// keeps a workspace's copy of them current.
//
// The graph shape is asserted explicitly rather than snapshot-compared because
// the shape IS the product contract (plan section 11): the intake node that makes
// the run's input visible on the canvas, capability routing on analyze,
// previous_step routing on validate so the fix and its validation land with the
// same Agent, and rework targets everywhere a rejection can occur. A silent edit
// to any of those changes how every Run of the built-in behaves.
//
// loadBuiltinWorkflowTemplate drops an invalid graph instead of returning an
// error, so a regression there would present as "the built-in vanished". These
// tests are what turns that into a failure.
//
// The upgrade tests below all seed a workspace with the REAL v1 bytes read out of
// the embedded revisions/ directory, never a hand-typed approximation. A
// hand-typed fixture would be compared against a fingerprint computed from the
// real file, so it would drift into "not a shipped revision" and every upgrade
// test would then pass by asserting the no-upgrade path — proving nothing. This
// is the blind-fixture failure mode this feature keeps producing.

func TestBuiltinBugFixTemplateParsesAndValidates(t *testing.T) {
	tpl, ok := builtinByKey("bug_fix")
	if !ok {
		t.Fatal("bug_fix built-in workflow was not loaded; the embedded graph failed to parse or validate (see logs)")
	}

	// Re-run the checks the loader ran, so a failure here names the reason
	// rather than just reporting an absent template.
	def, err := workflow.ParseDefinition(tpl.Raw)
	if err != nil {
		t.Fatalf("parse embedded bug_fix graph: %v", err)
	}
	if err := workflow.Validate(def, workflow.DefaultWorkspacePolicy, workflow.DefaultSchemaRegistry); err != nil {
		t.Fatalf("embedded bug_fix graph does not validate: %v", err)
	}

	if def.SchemaVersion != workflow.SchemaVersion {
		t.Errorf("schema_version = %d, want %d", def.SchemaVersion, workflow.SchemaVersion)
	}
	if def.EntryNode != "intake" {
		t.Errorf("entry_node = %q, want %q", def.EntryNode, "intake")
	}
	if tpl.Name != "Bug Fix" {
		t.Errorf("name = %q, want %q", tpl.Name, "Bug Fix")
	}
	if tpl.Description == "" {
		t.Error("built-in has no description; the list UI shows it as the only explanation of the process")
	}
}

func TestBuiltinBugFixTemplateGraphShape(t *testing.T) {
	tpl, ok := builtinByKey("bug_fix")
	if !ok {
		t.Fatal("bug_fix built-in workflow was not loaded")
	}
	def := tpl.Definition

	if len(def.Nodes) != 6 {
		t.Fatalf("node count = %d, want 6 (intake, analyze, implement, validate, acceptance, end)", len(def.Nodes))
	}

	want := []struct {
		key           string
		nodeType      workflow.NodeType
		next          []string
		schema        string
		onFailure     workflow.FailurePolicy
		reworkTargets []string
	}{
		{
			// intake is the whole point of this revision: the run's input is
			// declared on the graph instead of hardcoded in the Run dialog.
			key: "intake", nodeType: workflow.NodeTypeInput, next: []string{"analyze"},
		},
		{
			key: "analyze", nodeType: workflow.NodeTypeAgent, next: []string{"implement"},
			schema: "analysis", onFailure: workflow.FailurePolicyBlock,
		},
		{
			key: "implement", nodeType: workflow.NodeTypeAgent, next: []string{"validate"},
			schema: "code_change", onFailure: workflow.FailurePolicyRework,
			reworkTargets: []string{"analyze"},
		},
		{
			key: "validate", nodeType: workflow.NodeTypeAgent, next: []string{"acceptance"},
			schema: "test_report", onFailure: workflow.FailurePolicyRework,
			reworkTargets: []string{"implement"},
		},
		{
			// The acceptance node's rework targets are the whole reason a
			// rejection is routable rather than terminal. Note intake is NOT
			// among them, and cannot be: Validate rejects a rework edge into an
			// input node because the engine cannot re-prompt a human mid-run.
			key: "acceptance", nodeType: workflow.NodeTypeAcceptance, next: []string{"end"},
			reworkTargets: []string{"analyze", "implement", "validate"},
		},
		{key: "end", nodeType: workflow.NodeTypeEnd},
	}

	for _, w := range want {
		n, found := def.NodeByKey(w.key)
		if !found {
			t.Errorf("node %q is missing", w.key)
			continue
		}
		if n.Type != w.nodeType {
			t.Errorf("node %q type = %q, want %q", w.key, n.Type, w.nodeType)
		}
		if !equalStrings(n.Next, w.next) {
			t.Errorf("node %q next = %v, want %v", w.key, n.Next, w.next)
		}
		if n.SubmissionSchema != w.schema {
			t.Errorf("node %q submission_schema = %q, want %q", w.key, n.SubmissionSchema, w.schema)
		}
		if w.onFailure != "" && n.EffectiveOnFailure() != w.onFailure {
			t.Errorf("node %q on_failure = %q, want %q", w.key, n.EffectiveOnFailure(), w.onFailure)
		}
		if !equalStrings(n.ReworkTargets, w.reworkTargets) {
			t.Errorf("node %q rework_targets = %v, want %v", w.key, n.ReworkTargets, w.reworkTargets)
		}
	}

	// Routing is asserted separately: capability routing is what lets the
	// built-in ship without knowing any workspace's Agent IDs, and
	// previous_step on validate is what keeps the fix and its validation with
	// the same Agent.
	analyze, _ := def.NodeByKey("analyze")
	if analyze.Routing == nil || analyze.Routing.Strategy != workflow.RoutingCapability || analyze.Routing.Capability != "bug_analysis" {
		t.Errorf("analyze routing = %+v, want capability routing on %q", analyze.Routing, "bug_analysis")
	}
	implement, _ := def.NodeByKey("implement")
	if implement.Routing == nil || implement.Routing.Strategy != workflow.RoutingCapability || implement.Routing.Capability != "code_change" {
		t.Errorf("implement routing = %+v, want capability routing on %q", implement.Routing, "code_change")
	}
	validate, _ := def.NodeByKey("validate")
	if validate.Routing == nil || validate.Routing.Strategy != workflow.RoutingPreviousStep || validate.Routing.FromNode != "implement" {
		t.Errorf("validate routing = %+v, want previous_step routing from %q", validate.Routing, "implement")
	}

	acceptance, _ := def.NodeByKey("acceptance")
	if !equalStrings(acceptance.AcceptanceCriteria, []string{"happy path verified", "edge case covered"}) {
		t.Errorf("acceptance criteria = %v, want the two pilot criteria", acceptance.AcceptanceCriteria)
	}

	if def.Limits.MaxAttemptsPerNode != 3 {
		t.Errorf("limits.max_attempts_per_node = %d, want 3", def.Limits.MaxAttemptsPerNode)
	}
	if def.Limits.MaxReworkRounds != 3 {
		t.Errorf("limits.max_rework_rounds = %d, want 3", def.Limits.MaxReworkRounds)
	}
}

// TestBuiltinBugFixIntakeDeclaresTheRunDialogFields pins the declaration the Run
// dialog renders and StartRun validates against.
//
// The field KEYS are asserted, not just their count, because "title" and
// "description" are not arbitrary names: they are the two keys the pre-input-node
// run input bag has always used, they are the two RunInput has first-class slots
// for, and they are the two ParseRunInputFor deliberately skips when building
// RunInput.Fields so the reporter's text is not printed twice in one prompt.
// Renaming either here would leave the values reaching the prompt through neither
// path.
func TestBuiltinBugFixIntakeDeclaresTheRunDialogFields(t *testing.T) {
	tpl, ok := builtinByKey("bug_fix")
	if !ok {
		t.Fatal("bug_fix built-in workflow was not loaded")
	}

	intake, ok := tpl.Definition.EntryInputNode()
	if !ok {
		t.Fatalf("the bug_fix entry node is not an input node; the Run dialog would fall back to hardcoded fields (entry_node=%q)",
			tpl.Definition.EntryNode)
	}
	if intake.Key != "intake" {
		t.Errorf("entry input node key = %q, want %q", intake.Key, "intake")
	}
	if intake.Name == "" {
		t.Error("the intake node has no name; the canvas would render an unlabelled box where work enters")
	}

	want := []struct {
		key       string
		fieldType workflow.InputFieldType
		required  bool
	}{
		{"title", workflow.InputFieldText, true},
		{"description", workflow.InputFieldTextarea, true},
	}
	if len(intake.InputFields) != len(want) {
		t.Fatalf("intake declares %d fields, want %d (%+v)", len(intake.InputFields), len(want), intake.InputFields)
	}
	for i, w := range want {
		got := intake.InputFields[i]
		if got.Key != w.key {
			t.Errorf("input_fields[%d].key = %q, want %q", i, got.Key, w.key)
		}
		if got.EffectiveType() != w.fieldType {
			t.Errorf("input_fields[%d] (%s) type = %q, want %q", i, got.Key, got.EffectiveType(), w.fieldType)
		}
		if got.Required != w.required {
			t.Errorf("input_fields[%d] (%s) required = %v, want %v", i, got.Key, got.Required, w.required)
		}
		if got.Label == "" {
			t.Errorf("input_fields[%d] (%s) has no label; the dialog would show the raw key", i, got.Key)
		}
	}

	// The declaration must actually gate a run: this is the check StartRun runs
	// before any durable state exists.
	if err := workflow.ValidateRunInput([]byte(`{"title":"crash on save"}`), intake); err == nil {
		t.Error("ValidateRunInput accepted a run input with no description, but the field is declared required")
	}
	if err := workflow.ValidateRunInput([]byte(`{"title":"crash on save","description":"steps to reproduce"}`), intake); err != nil {
		t.Errorf("ValidateRunInput rejected a complete bug report: %v", err)
	}
}

// TestBuiltinBugFixAnalyzeInstructionNamesTheIntakeFields is a content assertion,
// which is unusual and deliberate. The instruction is the ONLY thing that tells
// the first agent where the defect report came from; the previous revision read
// as though the text arrived from nowhere, which is the authoring gap this whole
// revision exists to close. If someone rewrites the instruction and drops the
// reference, the graph still validates and every other test still passes.
func TestBuiltinBugFixAnalyzeInstructionNamesTheIntakeFields(t *testing.T) {
	tpl, ok := builtinByKey("bug_fix")
	if !ok {
		t.Fatal("bug_fix built-in workflow was not loaded")
	}
	analyze, found := tpl.Definition.NodeByKey("analyze")
	if !found {
		t.Fatal("no analyze node")
	}
	intake, _ := tpl.Definition.EntryInputNode()

	lowered := strings.ToLower(analyze.Instruction)
	if !strings.Contains(lowered, "intake") {
		t.Errorf("the analyze instruction never mentions intake, so it implies the defect report arrives from nowhere:\n%s", analyze.Instruction)
	}
	// Every declared field's label must appear, so adding a field to intake
	// without telling the first agent about it fails here.
	for i := range intake.InputFields {
		label := strings.ToLower(intake.InputFields[i].DisplayLabel())
		if !strings.Contains(lowered, label) {
			t.Errorf("the analyze instruction never names the intake field %q, which the reporter is being asked to fill in:\n%s",
				intake.InputFields[i].DisplayLabel(), analyze.Instruction)
		}
	}
}

// TestBuiltinBugFixShippedRevisionsAllValidate guards the shipping history
// itself. A prior revision that no longer parses would make
// builtinWorkflowFingerprints drop the whole built-in, and the symptom would be
// "Bug Fix disappeared from every workspace" rather than anything mentioning
// revisions/.
func TestBuiltinBugFixShippedRevisionsAllValidate(t *testing.T) {
	tpl, ok := builtinByKey("bug_fix")
	if !ok {
		t.Fatal("bug_fix built-in workflow was not loaded")
	}

	meta, found := builtinMetaByKey("bug_fix")
	if !found {
		t.Fatal("bug_fix has no entry in builtinWorkflowNames")
	}
	if len(meta.PriorRevisionFiles) == 0 {
		t.Fatal("bug_fix declares no prior revisions, so the seeder can never recognize an already-seeded workspace as pristine")
	}
	// One fingerprint per prior revision plus the current one.
	if len(tpl.ShippedFingerprints) != len(meta.PriorRevisionFiles)+1 {
		t.Fatalf("ShippedFingerprints = %d, want %d (%d prior revisions + the current one)",
			len(tpl.ShippedFingerprints), len(meta.PriorRevisionFiles)+1, len(meta.PriorRevisionFiles))
	}
	seen := map[string]string{}
	for _, rel := range meta.PriorRevisionFiles {
		raw := readBuiltinWorkflowRevision(t, rel)
		def, err := workflow.ParseDefinition(raw)
		if err != nil {
			t.Fatalf("prior revision %s does not parse: %v", rel, err)
		}
		// Prior revisions are not required to still satisfy today's Validate — a
		// future rule could legitimately outlaw an old graph — but they MUST
		// fingerprint, because that is what the seeder needs from them.
		fp, err := builtinWorkflowFingerprint(raw)
		if err != nil {
			t.Fatalf("prior revision %s does not fingerprint: %v", rel, err)
		}
		if prev, dup := seen[fp]; dup {
			t.Errorf("revisions %s and %s have the same fingerprint; one of them is a duplicate entry", prev, rel)
		}
		seen[fp] = rel
		if !tpl.matchesShippedRevision(raw) {
			t.Errorf("revision %s is not recognized as a shipped revision of bug_fix", rel)
		}
		if tpl.isCurrentRevision(raw) {
			t.Errorf("revision %s fingerprints as the CURRENT revision; a prior revision must differ from the live file or the upgrade is a no-op", rel)
		}
		_ = def
	}

	if !tpl.isCurrentRevision(tpl.Raw) {
		t.Error("the live bug_fix.json does not fingerprint as the current revision")
	}
}

// TestBuiltinWorkflowFingerprintSurvivesJSONBReformatting is the reason the
// seeder compares fingerprints rather than raw bytes. Postgres stores jsonb in
// its own normalized form: whitespace is gone and object keys come back in
// Postgres's order, so bytes.Equal against the embedded file is ALWAYS false and
// would classify every workspace as user-edited.
//
// This exercises the property directly, without a database, by reordering keys
// and stripping whitespace the way jsonb does.
func TestBuiltinWorkflowFingerprintSurvivesJSONBReformatting(t *testing.T) {
	tpl, ok := builtinByKey("bug_fix")
	if !ok {
		t.Fatal("bug_fix built-in workflow was not loaded")
	}

	// Round-trip through a generic map: Go's encoder emits map keys sorted, which
	// is a different order from the file's, and drops the file's indentation.
	var bag map[string]any
	if err := json.Unmarshal(tpl.Raw, &bag); err != nil {
		t.Fatalf("unmarshal embedded graph: %v", err)
	}
	reformatted, err := json.Marshal(bag)
	if err != nil {
		t.Fatalf("re-marshal: %v", err)
	}
	if string(reformatted) == string(tpl.Raw) {
		t.Fatal("the reformatted bytes are identical to the file, so this test cannot distinguish byte comparison from fingerprint comparison")
	}

	if !tpl.isCurrentRevision(reformatted) {
		t.Error("a reformatted copy of the current graph does not fingerprint as current; the seeder would refuse to recognize its own bytes after a Postgres round trip")
	}

	// And the fingerprint must still MEAN something: a one-word instruction edit
	// has to change it, or the pristine check would wave through a user's edits.
	edited := strings.Replace(string(tpl.Raw), "Reproduce the defect", "Ignore the defect", 1)
	if edited == string(tpl.Raw) {
		t.Fatal("the instruction this test edits is no longer present; pick another edit or the mutation is a no-op")
	}
	if tpl.matchesShippedRevision([]byte(edited)) {
		t.Error("an edited instruction still matches a shipped revision; the fingerprint is not sensitive to graph content")
	}
}

func TestIsBuiltinWorkflowTemplateKey(t *testing.T) {
	if !IsBuiltinWorkflowTemplateKey("bug_fix") {
		t.Error(`IsBuiltinWorkflowTemplateKey("bug_fix") = false, want true`)
	}
	// GetWorkflowTemplateByKey is case-insensitive (it matches
	// idx_workflow_template_ws_key), so this predicate must be too or a
	// seeded template would render as user-authored.
	if !IsBuiltinWorkflowTemplateKey("BUG_FIX") {
		t.Error(`IsBuiltinWorkflowTemplateKey("BUG_FIX") = false, want true (key matching is case-insensitive)`)
	}
	if IsBuiltinWorkflowTemplateKey("my_custom_flow") {
		t.Error(`IsBuiltinWorkflowTemplateKey("my_custom_flow") = true, want false`)
	}
}

// TestEnsureBuiltinWorkflowTemplatesIsIdempotent is the load-bearing DB eval:
// the seeder runs on every list request, so a non-idempotent version would
// create a duplicate template (or bump a version) on every page load.
func TestEnsureBuiltinWorkflowTemplatesIsIdempotent(t *testing.T) {
	ctx := context.Background()
	pool, workspaceID := setupBuiltinWorkflowTestWorkspace(t)
	q := db.New(pool)

	if err := EnsureBuiltinWorkflowTemplates(ctx, q, workspaceID); err != nil {
		t.Fatalf("first seed: %v", err)
	}
	// Second call simulates the next list request against an already-seeded
	// workspace.
	if err := EnsureBuiltinWorkflowTemplates(ctx, q, workspaceID); err != nil {
		t.Fatalf("second seed: %v", err)
	}

	var templateCount int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM workflow_template WHERE workspace_id = $1 AND key = 'bug_fix'`,
		workspaceID).Scan(&templateCount); err != nil {
		t.Fatalf("count templates: %v", err)
	}
	if templateCount != 1 {
		t.Fatalf("bug_fix template count after two seeds = %d, want 1", templateCount)
	}

	tpl, err := q.GetWorkflowTemplateByKey(ctx, db.GetWorkflowTemplateByKeyParams{
		WorkspaceID: workspaceID, Key: "bug_fix",
	})
	if err != nil {
		t.Fatalf("get seeded template: %v", err)
	}
	if tpl.Status != "published" {
		t.Errorf("template status = %q, want published (a draft built-in could not start a Run)", tpl.Status)
	}
	if !tpl.CurrentVersion.Valid || tpl.CurrentVersion.Int32 != 1 {
		t.Errorf("current_version = %+v, want 1", tpl.CurrentVersion)
	}
	if tpl.CreatedByType != "system" {
		t.Errorf("created_by_type = %q, want system", tpl.CreatedByType)
	}

	// Exactly one version, still version 1: a second seed must not have
	// published a version 2, because published versions are immutable and Runs
	// pin them.
	versions, err := q.ListWorkflowTemplateVersions(ctx, db.ListWorkflowTemplateVersionsParams{
		WorkspaceID: workspaceID, TemplateID: tpl.ID,
	})
	if err != nil {
		t.Fatalf("list versions: %v", err)
	}
	if len(versions) != 1 {
		t.Fatalf("version count after two seeds = %d, want 1", len(versions))
	}
	if versions[0].Version != 1 || versions[0].Status != "published" {
		t.Errorf("version = %d status = %q, want 1/published", versions[0].Version, versions[0].Status)
	}

	// The stored definition must be the graph the loader validated, so a Run
	// resolving node semantics through this row sees the same graph the test
	// above asserted.
	published, err := q.GetPublishedWorkflowTemplateVersion(ctx, db.GetPublishedWorkflowTemplateVersionParams{
		WorkspaceID: workspaceID, TemplateID: tpl.ID,
	})
	if err != nil {
		t.Fatalf("get published version: %v", err)
	}
	storedDef, err := workflow.ParseDefinition(published.Definition)
	if err != nil {
		t.Fatalf("stored definition does not parse: %v", err)
	}
	if storedDef.EntryNode != "intake" || len(storedDef.Nodes) != 6 {
		t.Errorf("stored definition entry=%q nodes=%d, want intake/6", storedDef.EntryNode, len(storedDef.Nodes))
	}
	// A fresh workspace must get the intake node directly — not v1 followed by an
	// upgrade, which would leave a version 2 nobody needed.
	if _, ok := storedDef.EntryInputNode(); !ok {
		t.Error("a freshly seeded workspace has no entry input node; the Run dialog would fall back to hardcoded fields")
	}
}

// TestEnsureBuiltinWorkflowTemplatesUpgradesAnUntouchedBuiltin is the upgrade
// path: a workspace seeded by an older binary carries v1, and opening the
// workflows page must move it to the shipped revision.
//
// The v1 fixture is the REAL embedded previous bytes. If it were hand-typed it
// would not fingerprint as a shipped revision, the seeder would correctly refuse
// to touch it, and this test would fail — which is why it is not hand-typed, and
// why the "does not upgrade" tests below are only meaningful alongside this one.
func TestEnsureBuiltinWorkflowTemplatesUpgradesAnUntouchedBuiltin(t *testing.T) {
	ctx := context.Background()
	pool, workspaceID := setupBuiltinWorkflowTestWorkspace(t)
	q := db.New(pool)

	v1 := readBuiltinWorkflowRevision(t, "revisions/bug_fix.v1.json")
	templateID, v1ID := seedLegacyBugFix(t, pool, workspaceID, "system", v1)

	// Sanity: the fixture really is the old graph, or this test would prove
	// nothing about upgrading.
	if legacy, err := workflow.ParseDefinition(v1); err != nil {
		t.Fatalf("v1 fixture does not parse: %v", err)
	} else if legacy.EntryNode != "analyze" || len(legacy.Nodes) != 5 {
		t.Fatalf("v1 fixture entry=%q nodes=%d, want analyze/5; the fixture is not the previous revision",
			legacy.EntryNode, len(legacy.Nodes))
	}

	// Capture the v1 row's exact stored bytes BEFORE the upgrade, so immutability
	// is checked against what was really there rather than against the file.
	v1BytesBefore := storedDefinitionBytes(t, pool, v1ID)

	if err := EnsureBuiltinWorkflowTemplates(ctx, q, workspaceID); err != nil {
		t.Fatalf("seed over a v1 workspace: %v", err)
	}

	tpl, err := q.GetWorkflowTemplateByKey(ctx, db.GetWorkflowTemplateByKeyParams{
		WorkspaceID: workspaceID, Key: "bug_fix",
	})
	if err != nil {
		t.Fatalf("get template: %v", err)
	}
	if !tpl.CurrentVersion.Valid || tpl.CurrentVersion.Int32 != 2 {
		t.Fatalf("current_version = %+v, want 2; the untouched built-in was not upgraded", tpl.CurrentVersion)
	}

	published, err := q.GetPublishedWorkflowTemplateVersion(ctx, db.GetPublishedWorkflowTemplateVersionParams{
		WorkspaceID: workspaceID, TemplateID: templateID,
	})
	if err != nil {
		t.Fatalf("get published version after upgrade: %v", err)
	}
	if published.Version != 2 {
		t.Fatalf("published version = %d, want 2", published.Version)
	}
	if published.PublishedByType.String != "system" {
		t.Errorf("published_by_type = %q, want system", published.PublishedByType.String)
	}
	upgraded, err := workflow.ParseDefinition(published.Definition)
	if err != nil {
		t.Fatalf("upgraded definition does not parse: %v", err)
	}
	if upgraded.EntryNode != "intake" {
		t.Errorf("upgraded entry_node = %q, want intake", upgraded.EntryNode)
	}
	intake, ok := upgraded.EntryInputNode()
	if !ok {
		t.Fatal("the upgraded published version has no entry input node")
	}
	if len(intake.InputFields) != 2 {
		t.Errorf("upgraded intake declares %d fields, want 2", len(intake.InputFields))
	}

	// IMMUTABILITY: the v1 row must still exist, still be published-or-not exactly
	// as it was, and carry byte-identical bytes. An in-flight Run pinned to v1
	// resolves its node semantics through this row.
	v1After := readVersionRow(t, pool, v1ID)
	if v1After.version != 1 {
		t.Errorf("the v1 row's version changed to %d", v1After.version)
	}
	if v1After.status != "published" {
		t.Errorf("the v1 row's status = %q, want published (it was published before the upgrade)", v1After.status)
	}
	if string(storedDefinitionBytes(t, pool, v1ID)) != string(v1BytesBefore) {
		t.Error("the v1 version row's definition changed; a published version is immutable and in-flight Runs pin its bytes")
	}
	// And the old row must still be the OLD graph, not silently rewritten to the
	// new one by an UPDATE that happened to preserve the row.
	oldStored, err := workflow.ParseDefinition(v1BytesBefore)
	if err != nil {
		t.Fatalf("stored v1 does not parse: %v", err)
	}
	if oldStored.EntryNode != "analyze" || len(oldStored.Nodes) != 5 {
		t.Errorf("the v1 row now says entry=%q nodes=%d, want analyze/5", oldStored.EntryNode, len(oldStored.Nodes))
	}
}

// TestEnsureBuiltinWorkflowTemplatesSeedsThriceAndUpgradesOnce is the idempotence
// of the UPGRADE, as opposed to of the create. A version-number or node-count
// heuristic would keep finding the template "not current" and publish v3, v4, v5,
// one per page load.
func TestEnsureBuiltinWorkflowTemplatesSeedsThriceAndUpgradesOnce(t *testing.T) {
	ctx := context.Background()
	pool, workspaceID := setupBuiltinWorkflowTestWorkspace(t)
	q := db.New(pool)

	v1 := readBuiltinWorkflowRevision(t, "revisions/bug_fix.v1.json")
	templateID, _ := seedLegacyBugFix(t, pool, workspaceID, "system", v1)

	for i := 1; i <= 3; i++ {
		if err := EnsureBuiltinWorkflowTemplates(ctx, q, workspaceID); err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
	}

	versions, err := q.ListWorkflowTemplateVersions(ctx, db.ListWorkflowTemplateVersionsParams{
		WorkspaceID: workspaceID, TemplateID: templateID,
	})
	if err != nil {
		t.Fatalf("list versions: %v", err)
	}
	if len(versions) != 2 {
		got := make([]string, 0, len(versions))
		for _, v := range versions {
			got = append(got, fmt.Sprintf("v%d/%s", v.Version, v.Status))
		}
		t.Fatalf("version count after three seeds = %d (%s), want 2; the upgrade published more than once",
			len(versions), strings.Join(got, ", "))
	}
	tpl, err := q.GetWorkflowTemplateByKey(ctx, db.GetWorkflowTemplateByKeyParams{
		WorkspaceID: workspaceID, Key: "bug_fix",
	})
	if err != nil {
		t.Fatalf("get template: %v", err)
	}
	if !tpl.CurrentVersion.Valid || tpl.CurrentVersion.Int32 != 2 {
		t.Errorf("current_version = %+v, want 2 after three seeds", tpl.CurrentVersion)
	}
}

// TestEnsureBuiltinWorkflowTemplatesLeavesAnEditedBuiltinAlone is the constraint
// the whole fingerprint mechanism exists to satisfy. The edit here is ONE WORD in
// one instruction: node count, entry node, version number, and created_by_type are
// all still exactly what a pristine v1 has, so any pristineness test that is not
// content-sensitive passes this workspace through and destroys the user's work.
func TestEnsureBuiltinWorkflowTemplatesLeavesAnEditedBuiltinAlone(t *testing.T) {
	ctx := context.Background()
	pool, workspaceID := setupBuiltinWorkflowTestWorkspace(t)
	q := db.New(pool)

	v1 := readBuiltinWorkflowRevision(t, "revisions/bug_fix.v1.json")
	edited := strings.Replace(string(v1),
		"Reproduce the reported defect",
		"Reproduce the reported defect in a scratch workspace first",
		1)
	if edited == string(v1) {
		t.Fatal("the instruction this test edits is no longer in the v1 fixture; the edit is a no-op and the test cannot fail")
	}
	// The edit must still be a legal graph, or the test would be proving that the
	// seeder refuses to upgrade invalid definitions instead of edited ones.
	editedDef, err := workflow.ParseDefinition([]byte(edited))
	if err != nil {
		t.Fatalf("edited fixture does not parse: %v", err)
	}
	if err := workflow.Validate(editedDef, workflow.DefaultWorkspacePolicy, workflow.DefaultSchemaRegistry); err != nil {
		t.Fatalf("edited fixture does not validate: %v", err)
	}
	if len(editedDef.Nodes) != 5 || editedDef.EntryNode != "analyze" {
		t.Fatalf("edited fixture shape changed (%d nodes, entry %q); the edit must be invisible to a shape heuristic",
			len(editedDef.Nodes), editedDef.EntryNode)
	}

	templateID, editedID := seedLegacyBugFix(t, pool, workspaceID, "system", []byte(edited))
	before := storedDefinitionBytes(t, pool, editedID)

	if err := EnsureBuiltinWorkflowTemplates(ctx, q, workspaceID); err != nil {
		t.Fatalf("seed over an edited workspace: %v", err)
	}

	versions, err := q.ListWorkflowTemplateVersions(ctx, db.ListWorkflowTemplateVersionsParams{
		WorkspaceID: workspaceID, TemplateID: templateID,
	})
	if err != nil {
		t.Fatalf("list versions: %v", err)
	}
	if len(versions) != 1 {
		t.Fatalf("version count = %d, want 1; the seeder published over a user's edited built-in", len(versions))
	}
	tpl, err := q.GetWorkflowTemplateByKey(ctx, db.GetWorkflowTemplateByKeyParams{
		WorkspaceID: workspaceID, Key: "bug_fix",
	})
	if err != nil {
		t.Fatalf("get template: %v", err)
	}
	if !tpl.CurrentVersion.Valid || tpl.CurrentVersion.Int32 != 1 {
		t.Errorf("current_version = %+v, want 1; the edited copy must stay current", tpl.CurrentVersion)
	}
	if string(storedDefinitionBytes(t, pool, editedID)) != string(before) {
		t.Error("the user's edited definition was modified")
	}
	// The user's words must still be there. Comparing bytes alone would pass even
	// if the row had been replaced with something else the same length.
	stillEdited, err := workflow.ParseDefinition(storedDefinitionBytes(t, pool, editedID))
	if err != nil {
		t.Fatalf("stored definition does not parse: %v", err)
	}
	analyze, _ := stillEdited.NodeByKey("analyze")
	if analyze == nil || !strings.Contains(analyze.Instruction, "scratch workspace first") {
		t.Errorf("the user's edit is gone from the analyze instruction: %+v", analyze)
	}
}

// TestEnsureBuiltinWorkflowTemplatesLeavesABuiltinWithAUserDraftAlone covers the
// case content comparison of the PUBLISHED row alone cannot see: the published
// version is pristine v1, byte for byte, and the human's work is in a draft. A
// check that only looked at the published row would publish v3 here, burying a
// draft the author is still editing behind a newer current_version.
func TestEnsureBuiltinWorkflowTemplatesLeavesABuiltinWithAUserDraftAlone(t *testing.T) {
	ctx := context.Background()
	pool, workspaceID := setupBuiltinWorkflowTestWorkspace(t)
	q := db.New(pool)

	v1 := readBuiltinWorkflowRevision(t, "revisions/bug_fix.v1.json")
	templateID, _ := seedLegacyBugFix(t, pool, workspaceID, "system", v1)

	// The draft is the user's in-progress edit of the built-in, exactly the shape
	// the handler's save path produces: a new draft row above the published one.
	draft := strings.Replace(string(v1),
		"Reproduce the reported defect",
		"Reproduce the reported defect on the staging cluster",
		1)
	if draft == string(v1) {
		t.Fatal("the instruction this test edits is no longer in the v1 fixture; the draft would be identical to v1 and the test could not fail")
	}
	var draftID pgtype.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO workflow_template_version (workspace_id, template_id, version, definition, schema_version, status)
		 VALUES ($1, $2, 2, $3::jsonb, 1, 'draft') RETURNING id`,
		workspaceID, templateID, draft).Scan(&draftID); err != nil {
		t.Fatalf("insert user draft: %v", err)
	}
	draftBefore := storedDefinitionBytes(t, pool, draftID)

	if err := EnsureBuiltinWorkflowTemplates(ctx, q, workspaceID); err != nil {
		t.Fatalf("seed over a workspace with a user draft: %v", err)
	}

	versions, err := q.ListWorkflowTemplateVersions(ctx, db.ListWorkflowTemplateVersionsParams{
		WorkspaceID: workspaceID, TemplateID: templateID,
	})
	if err != nil {
		t.Fatalf("list versions: %v", err)
	}
	if len(versions) != 2 {
		t.Fatalf("version count = %d, want 2 (published v1 + the user's draft v2); the seeder wrote a version under an author",
			len(versions))
	}
	tpl, err := q.GetWorkflowTemplateByKey(ctx, db.GetWorkflowTemplateByKeyParams{
		WorkspaceID: workspaceID, Key: "bug_fix",
	})
	if err != nil {
		t.Fatalf("get template: %v", err)
	}
	if !tpl.CurrentVersion.Valid || tpl.CurrentVersion.Int32 != 1 {
		t.Errorf("current_version = %+v, want 1; publishing here would strand the author's draft", tpl.CurrentVersion)
	}
	after := readVersionRow(t, pool, draftID)
	if after.status != "draft" {
		t.Errorf("the user's draft status = %q, want draft; the seeder published someone else's work", after.status)
	}
	if string(storedDefinitionBytes(t, pool, draftID)) != string(draftBefore) {
		t.Error("the user's draft definition was modified")
	}
}

// TestEnsureBuiltinWorkflowTemplatesLeavesAMemberAuthoredTemplateAlone is the
// provenance half of the rule. Content alone would say pristine here — the
// definition IS v1 — but a member created this template, so it is theirs. This is
// the "is_builtin derived from a forgeable key string" bug from an earlier round:
// the key is attacker-supplied, created_by_type is not.
func TestEnsureBuiltinWorkflowTemplatesLeavesAMemberAuthoredTemplateAlone(t *testing.T) {
	ctx := context.Background()
	pool, workspaceID := setupBuiltinWorkflowTestWorkspace(t)
	q := db.New(pool)

	v1 := readBuiltinWorkflowRevision(t, "revisions/bug_fix.v1.json")
	templateID, _ := seedLegacyBugFix(t, pool, workspaceID, "member", v1)

	if err := EnsureBuiltinWorkflowTemplates(ctx, q, workspaceID); err != nil {
		t.Fatalf("seed over a member-authored bug_fix: %v", err)
	}

	versions, err := q.ListWorkflowTemplateVersions(ctx, db.ListWorkflowTemplateVersionsParams{
		WorkspaceID: workspaceID, TemplateID: templateID,
	})
	if err != nil {
		t.Fatalf("list versions: %v", err)
	}
	if len(versions) != 1 {
		t.Fatalf("version count = %d, want 1; the seeder revised a member-authored template", len(versions))
	}
}

// --- fixtures ---------------------------------------------------------------

// readBuiltinWorkflowRevision reads a shipped revision out of the SAME embedded
// FS the seeder fingerprints from. Reading it here rather than declaring a literal
// is the point: a hand-typed "previous revision" would not fingerprint as one, so
// every upgrade test would silently exercise the refuse-to-upgrade path instead.
func readBuiltinWorkflowRevision(t *testing.T, rel string) []byte {
	t.Helper()
	raw, err := fs.ReadFile(builtinWorkflowsFS, path.Join(builtinWorkflowsRoot, rel))
	if err != nil {
		t.Fatalf("read embedded revision %s: %v", rel, err)
	}
	return raw
}

func builtinMetaByKey(key string) (struct {
	File               string
	PriorRevisionFiles []string
	Key                string
	Name               string
	Description        string
}, bool) {
	for _, meta := range builtinWorkflowNames {
		if meta.Key == key {
			return meta, true
		}
	}
	return builtinWorkflowNames[0], false
}

// seedLegacyBugFix writes a bug_fix template the way an OLDER binary's seeder
// would have: one published version carrying `definition`, current_version = 1.
// Raw SQL rather than the seeder itself, because the whole point is to reproduce
// a state today's code no longer produces.
func seedLegacyBugFix(t *testing.T, pool *pgxpool.Pool, workspaceID pgtype.UUID, createdByType string, definition []byte) (templateID, versionID pgtype.UUID) {
	t.Helper()
	ctx := context.Background()
	if err := pool.QueryRow(ctx,
		`INSERT INTO workflow_template
		   (workspace_id, key, name, description, status, current_version, created_by_type, created_by_id)
		 VALUES ($1, 'bug_fix', 'Bug Fix', 'seeded by an older binary', 'published', 1, $2, gen_random_uuid())
		 RETURNING id`,
		workspaceID, createdByType).Scan(&templateID); err != nil {
		t.Fatalf("insert legacy template: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO workflow_template_version
		   (workspace_id, template_id, version, definition, schema_version, status,
		    published_by_type, published_by_id, published_at)
		 VALUES ($1, $2, 1, $3::jsonb, 1, 'published', 'system', gen_random_uuid(), now())
		 RETURNING id`,
		workspaceID, templateID, definition).Scan(&versionID); err != nil {
		t.Fatalf("insert legacy version: %v", err)
	}
	return templateID, versionID
}

func storedDefinitionBytes(t *testing.T, pool *pgxpool.Pool, versionID pgtype.UUID) []byte {
	t.Helper()
	var raw []byte
	if err := pool.QueryRow(context.Background(),
		`SELECT definition FROM workflow_template_version WHERE id = $1`, versionID).Scan(&raw); err != nil {
		t.Fatalf("read stored definition: %v", err)
	}
	return raw
}

type versionRow struct {
	version int32
	status  string
}

func readVersionRow(t *testing.T, pool *pgxpool.Pool, versionID pgtype.UUID) versionRow {
	t.Helper()
	var out versionRow
	if err := pool.QueryRow(context.Background(),
		`SELECT version, status FROM workflow_template_version WHERE id = $1`, versionID).
		Scan(&out.version, &out.status); err != nil {
		t.Fatalf("read version row: %v", err)
	}
	return out
}

// setupBuiltinWorkflowTestWorkspace creates a throwaway workspace, skipping the
// test when no migrated database is reachable. Same posture as
// internal/workflow/engine_integration_test.go: a checkout that has not run
// migrations must not fail this package.
func setupBuiltinWorkflowTestWorkspace(t *testing.T) (*pgxpool.Pool, pgtype.UUID) {
	t.Helper()
	ctx := context.Background()

	url := os.Getenv("DATABASE_URL")
	if url == "" {
		url = "postgres://multica:multica@localhost:5432/multica?sslmode=disable"
	}
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Skipf("skipping: cannot connect to database: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("skipping: database not reachable: %v", err)
	}
	var hasTable bool
	if err := pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_name='workflow_template')`).Scan(&hasTable); err != nil || !hasTable {
		pool.Close()
		t.Skip("skipping: workflow tables not migrated; run `make migrate-up`")
	}
	t.Cleanup(pool.Close)

	var workspaceID pgtype.UUID
	slug := fmt.Sprintf("bwf-%d", time.Now().UnixNano())
	if err := pool.QueryRow(ctx,
		`INSERT INTO workspace (name, slug) VALUES ($1, $2) RETURNING id`,
		"Builtin Workflow Test", slug).Scan(&workspaceID); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		// The workflow tables carry no foreign keys by design (plan section 4),
		// so nothing cascades and cleanup must be explicit.
		for _, stmt := range []string{
			`DELETE FROM workflow_template_version WHERE workspace_id = $1`,
			`DELETE FROM workflow_template WHERE workspace_id = $1`,
			`DELETE FROM workspace WHERE id = $1`,
		} {
			if _, err := pool.Exec(bg, stmt, workspaceID); err != nil {
				t.Logf("cleanup %q: %v", stmt, err)
			}
		}
	})
	return pool, workspaceID
}

func builtinByKey(key string) (BuiltinWorkflowTemplate, bool) {
	for _, tpl := range BuiltinWorkflowTemplates() {
		if tpl.Key == key {
			return tpl, true
		}
	}
	return BuiltinWorkflowTemplate{}, false
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
