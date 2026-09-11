package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/daemon"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/workflow"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// The input node, end to end, against a real Postgres.
//
// The complaint this feature answers is that the seeded Bug Fix graph had no node
// representing where the bug report enters: the Run dialog collected a Title and a
// Description that were hardcoded in the dialog and invisible on the canvas, so an
// author reading the template could not see the workflow took an input at all.
// The fix is a real `input` node type, a new bug_fix revision whose entry is an
// intake node declaring those fields, and a seeder that upgrades a workspace's
// copy only when it can prove nobody edited it.
//
// This file walks that whole path. It is a sibling of workflow_run_e2e_test.go and
// reuses its fixture (workflowE2EEnv, claimNextTask, e2eTaskResult) rather than
// duplicating them, because the assertions that matter here are about the same
// seams that file already reaches through: the real run endpoint, the real daemon
// claim endpoint, the real TaskService completion hook.
//
// THE ONE PLACE IT GOES FURTHER, and the reason it exists as its own file: the
// central assertion decodes the claim response into daemon.Task and calls
// daemon.BuildPrompt. workflow_run_e2e_test.go asserts on e2eClaimedTask, a struct
// declared inside that test file. That is enough to prove the server SENDS a
// field, and structurally incapable of noticing that the CONSUMER drops it - which
// is precisely the bug that shipped once already: the daemon's Task struct had no
// workflow fields, json.Unmarshal discarded workflow_prompt without error, and
// BuildPrompt fell through to the legacy assignment prompt. Three green suites
// missed it. So the delivery assertion below goes through the real consumer type
// and the real consumer function.

// ---------------------------------------------------------------------------
// 1. A FRESH workspace gets the new revision
// ---------------------------------------------------------------------------

// TestInputNodeFreshWorkspaceSeedsTheIntakeRevision is hop 1: a workspace that has
// never been seeded gets the graph with the intake node, and the bytes a Run would
// pin are legal.
//
// Validate is re-run on the STORED bytes rather than trusted from load time. A
// published version is immutable, so it can never be re-validated later; if the
// row were invalid, every Run pinned to it would be unrunnable and no amount of
// fixing the embedded file afterwards would repair the workspace.
func TestInputNodeFreshWorkspaceSeedsTheIntakeRevision(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	workspaceID := newInputNodeWorkspace(t, "fresh")
	wsUUID := parseUUID(workspaceID)

	if err := service.EnsureBuiltinWorkflowTemplates(ctx, testHandler.Queries, wsUUID); err != nil {
		t.Fatalf("EnsureBuiltinWorkflowTemplates: %v", err)
	}

	tpl, err := testHandler.Queries.GetWorkflowTemplateByKey(ctx, db.GetWorkflowTemplateByKeyParams{
		WorkspaceID: wsUUID, Key: "bug_fix",
	})
	if err != nil {
		t.Fatalf("bug_fix absent after seeding a fresh workspace: %v", err)
	}
	if !tpl.CurrentVersion.Valid {
		t.Fatal("current_version is null; a template with no published version cannot start a Run")
	}
	version, err := testHandler.Queries.GetPublishedWorkflowTemplateVersion(ctx, db.GetPublishedWorkflowTemplateVersionParams{
		TemplateID: tpl.ID, WorkspaceID: wsUUID,
	})
	if err != nil {
		t.Fatalf("no published version for the seeded built-in: %v", err)
	}

	def, err := workflow.ParseDefinition(version.Definition)
	if err != nil {
		t.Fatalf("the pinned definition does not parse: %v", err)
	}
	if err := workflow.Validate(def, workflow.DefaultWorkspacePolicy, workflow.DefaultSchemaRegistry); err != nil {
		t.Fatalf("the pinned definition does not validate; every Run pinned to it would be unrunnable: %v", err)
	}

	if len(def.Nodes) != 6 {
		keys := make([]string, 0, len(def.Nodes))
		for _, n := range def.Nodes {
			keys = append(keys, n.Key)
		}
		t.Fatalf("node count = %d (%s), want 6 (intake, analyze, implement, validate, acceptance, end)",
			len(def.Nodes), strings.Join(keys, ", "))
	}
	if def.EntryNode != "intake" {
		t.Fatalf("entry_node = %q, want intake; the graph must declare where the bug report enters", def.EntryNode)
	}

	// Read through EntryInputNode, not by scanning for a node of type input: that
	// is the accessor every caller uses (the engine, the Run dialog), and it
	// answers false unless the input node IS the entry. A graph whose input node
	// sat elsewhere would satisfy a scan and still fall back to hardcoded fields.
	intake, ok := def.EntryInputNode()
	if !ok {
		t.Fatal("the seeded graph has no ENTRY input node; the Run dialog would fall back to hardcoded fields")
	}
	if intake.Type != workflow.NodeTypeInput {
		t.Fatalf("intake node type = %q, want input", intake.Type)
	}
	if len(intake.InputFields) != 2 {
		t.Fatalf("intake declares %d fields, want 2", len(intake.InputFields))
	}
	// Assert the KEYS, not just the count. These two keys are what
	// ParseRunInputFor maps onto RunInput.Title/Description and what the run
	// endpoint's validated values are written under; renaming either would leave
	// the reporter's text reaching the prompt through neither path while the count
	// assertion stayed green.
	for i, want := range []struct {
		key      string
		kind     workflow.InputFieldType
		required bool
	}{
		{"title", workflow.InputFieldText, true},
		{"description", workflow.InputFieldTextarea, true},
	} {
		got := intake.InputFields[i]
		if got.Key != want.key {
			t.Errorf("intake field %d key = %q, want %q", i, got.Key, want.key)
		}
		if got.EffectiveType() != want.kind {
			t.Errorf("intake field %q type = %q, want %q", got.Key, got.EffectiveType(), want.kind)
		}
		if got.Required != want.required {
			t.Errorf("intake field %q required = %v, want %v; a blank value must be a typed rejection",
				got.Key, got.Required, want.required)
		}
		if got.DisplayLabel() == "" {
			t.Errorf("intake field %q has no label; the dialog and the prompt would name it by its raw key", got.Key)
		}
	}
	// It points at the first agent, and it is a passthrough by declaration: no
	// routing, no submission schema, nothing to send back to.
	if len(intake.Next) != 1 || intake.Next[0] != "analyze" {
		t.Errorf("intake next = %v, want [analyze]", intake.Next)
	}
	if intake.Routing != nil {
		t.Errorf("intake declares routing (%+v); intake is filled in by a human and never dispatches an Agent Task", intake.Routing)
	}
	if intake.SubmissionSchema != "" {
		t.Errorf("intake declares submission_schema %q; an input step never produces a submission", intake.SubmissionSchema)
	}
	if len(intake.ReworkTargets) != 0 {
		t.Errorf("intake declares rework_targets %v; nothing can be sent back to intake", intake.ReworkTargets)
	}

	// The rest of the Bug Fix spine survived the revision. A revision that added
	// intake and lost validate would pass every assertion above.
	for _, key := range []string{"analyze", "implement", "validate", "acceptance", "end"} {
		if _, ok := def.NodeByKey(key); !ok {
			t.Errorf("the new revision dropped the %q node", key)
		}
	}
}

// ---------------------------------------------------------------------------
// 2. The upgrade, both directions, against the real DB
// ---------------------------------------------------------------------------

// TestInputNodeSeederUpgradesPristineAndSparesEdited is hop 2 (a), (b) and (c) in
// one test, because they are one decision seen from three sides and running them
// together is what proves the decision discriminates rather than being uniformly
// permissive or uniformly refusing.
//
// The v1 fixture is service.BuiltinWorkflowShippedDefinition - the REAL bytes the
// previous binary seeded, read out of the same embed.FS the seeder fingerprints
// from. A hand-typed "previous revision" would fingerprint as something we never
// shipped, the seeder would correctly refuse to upgrade it, and (a) would then be
// asserting the refuse path while looking like it asserted the upgrade path:
// green, and proving the opposite of what it claims.
func TestInputNodeSeederUpgradesPristineAndSparesEdited(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()

	v1, ok := service.BuiltinWorkflowShippedDefinition("bug_fix", 0)
	if !ok {
		t.Fatal("bug_fix has no shipped revision 0; the seeder's shipping history is empty and no upgrade can be tested")
	}
	// Self-check the fixture. If revisions/bug_fix.v1.json were ever replaced with
	// the new graph, every assertion below would still pass while testing nothing.
	v1Def, err := workflow.ParseDefinition(v1)
	if err != nil {
		t.Fatalf("the shipped v1 bytes do not parse: %v", err)
	}
	if v1Def.EntryNode != "analyze" || len(v1Def.Nodes) != 5 {
		t.Fatalf("shipped revision 0 is entry=%q nodes=%d, want analyze/5; it is not the pre-input-node graph",
			v1Def.EntryNode, len(v1Def.Nodes))
	}
	if _, ok := v1Def.EntryInputNode(); ok {
		t.Fatal("shipped revision 0 already has an entry input node; there is nothing to upgrade to")
	}

	// --- (a) a PRISTINE v1 workspace is upgraded.
	t.Run("pristine v1 is upgraded and v1 stays byte-identical", func(t *testing.T) {
		workspaceID := newInputNodeWorkspace(t, "upgrade")
		wsUUID := parseUUID(workspaceID)
		templateID, v1ID := seedLegacyBugFixTemplate(t, wsUUID, "system", v1)
		v1Before := storedWorkflowDefinition(t, v1ID)

		if err := service.EnsureBuiltinWorkflowTemplates(ctx, testHandler.Queries, wsUUID); err != nil {
			t.Fatalf("seed over a v1 workspace: %v", err)
		}

		tpl, err := testHandler.Queries.GetWorkflowTemplateByKey(ctx, db.GetWorkflowTemplateByKeyParams{
			WorkspaceID: wsUUID, Key: "bug_fix",
		})
		if err != nil {
			t.Fatalf("get template: %v", err)
		}
		if !tpl.CurrentVersion.Valid || tpl.CurrentVersion.Int32 != 2 {
			t.Fatalf("current_version = %+v, want 2; the untouched built-in was not upgraded", tpl.CurrentVersion)
		}
		published, err := testHandler.Queries.GetPublishedWorkflowTemplateVersion(ctx, db.GetPublishedWorkflowTemplateVersionParams{
			WorkspaceID: wsUUID, TemplateID: templateID,
		})
		if err != nil {
			t.Fatalf("get published version after the upgrade: %v", err)
		}
		if published.Version != 2 {
			t.Fatalf("published version = %d, want 2", published.Version)
		}
		upgraded, err := workflow.ParseDefinition(published.Definition)
		if err != nil {
			t.Fatalf("the upgraded definition does not parse: %v", err)
		}
		intake, ok := upgraded.EntryInputNode()
		if !ok {
			t.Fatalf("the upgraded published version has no entry input node (entry=%q)", upgraded.EntryNode)
		}
		if len(intake.InputFields) != 2 {
			t.Errorf("upgraded intake declares %d fields, want 2", len(intake.InputFields))
		}
		// The upgraded bytes must be runnable, not merely present.
		if err := workflow.Validate(upgraded, workflow.DefaultWorkspacePolicy, workflow.DefaultSchemaRegistry); err != nil {
			t.Fatalf("the upgraded published version does not validate: %v", err)
		}

		// IMMUTABILITY. An in-flight Run pinned to v1 resolves its node semantics
		// through this exact row.
		after := readWorkflowVersionRow(t, v1ID)
		if after.version != 1 {
			t.Errorf("the v1 row's version changed to %d", after.version)
		}
		if after.status != "published" {
			t.Errorf("the v1 row's status = %q, want published", after.status)
		}
		if got := storedWorkflowDefinition(t, v1ID); string(got) != string(v1Before) {
			t.Error("the v1 row's definition changed; a published version is immutable and in-flight Runs pin its bytes")
		}
		// And it is still the OLD graph - an UPDATE that rewrote the row in place
		// while preserving its id would pass the version/status checks above.
		stillOld, err := workflow.ParseDefinition(storedWorkflowDefinition(t, v1ID))
		if err != nil {
			t.Fatalf("the stored v1 no longer parses: %v", err)
		}
		if stillOld.EntryNode != "analyze" || len(stillOld.Nodes) != 5 {
			t.Errorf("the v1 row now says entry=%q nodes=%d, want analyze/5", stillOld.EntryNode, len(stillOld.Nodes))
		}

		// --- (c) two more seeds do not publish a v3.
		for i := 2; i <= 3; i++ {
			if err := service.EnsureBuiltinWorkflowTemplates(ctx, testHandler.Queries, wsUUID); err != nil {
				t.Fatalf("seed %d: %v", i, err)
			}
		}
		versions, err := testHandler.Queries.ListWorkflowTemplateVersions(ctx, db.ListWorkflowTemplateVersionsParams{
			WorkspaceID: wsUUID, TemplateID: templateID,
		})
		if err != nil {
			t.Fatalf("list versions: %v", err)
		}
		if len(versions) != 2 {
			got := make([]string, 0, len(versions))
			for _, v := range versions {
				got = append(got, fmt.Sprintf("v%d/%s", v.Version, v.Status))
			}
			t.Fatalf("version count after three seeds = %d (%s), want 2; the upgrade is not idempotent and would publish one version per page load",
				len(versions), strings.Join(got, ", "))
		}
		reread, err := testHandler.Queries.GetWorkflowTemplateByKey(ctx, db.GetWorkflowTemplateByKeyParams{
			WorkspaceID: wsUUID, Key: "bug_fix",
		})
		if err != nil {
			t.Fatalf("get template after three seeds: %v", err)
		}
		if !reread.CurrentVersion.Valid || reread.CurrentVersion.Int32 != 2 {
			t.Errorf("current_version = %+v after three seeds, want 2", reread.CurrentVersion)
		}
	})

	// --- (b) an EDITED copy wins.
	//
	// The edit is ONE PHRASE in one instruction. Node count, entry node, version
	// number, status, and created_by_type all stay exactly what a pristine v1 has,
	// so any pristineness check that is not content-sensitive waves this workspace
	// through and silently destroys the user's process.
	t.Run("an edited built-in is left completely alone", func(t *testing.T) {
		workspaceID := newInputNodeWorkspace(t, "edited")
		wsUUID := parseUUID(workspaceID)

		const original = "Reproduce the reported defect"
		edited := strings.Replace(string(v1), original, original+" in a scratch workspace first", 1)
		if edited == string(v1) {
			t.Fatalf("the phrase %q is no longer in the shipped v1; the edit is a no-op and this test cannot fail", original)
		}
		// The edit must still be a LEGAL graph, or this test would be proving the
		// seeder refuses to upgrade invalid definitions rather than edited ones.
		editedDef, err := workflow.ParseDefinition([]byte(edited))
		if err != nil {
			t.Fatalf("the edited fixture does not parse: %v", err)
		}
		if err := workflow.Validate(editedDef, workflow.DefaultWorkspacePolicy, workflow.DefaultSchemaRegistry); err != nil {
			t.Fatalf("the edited fixture does not validate: %v", err)
		}
		if len(editedDef.Nodes) != 5 || editedDef.EntryNode != "analyze" {
			t.Fatalf("the edited fixture's shape changed (%d nodes, entry %q); the edit must be invisible to a shape heuristic",
				len(editedDef.Nodes), editedDef.EntryNode)
		}

		templateID, editedID := seedLegacyBugFixTemplate(t, wsUUID, "system", []byte(edited))
		before := storedWorkflowDefinition(t, editedID)

		if err := service.EnsureBuiltinWorkflowTemplates(ctx, testHandler.Queries, wsUUID); err != nil {
			t.Fatalf("seed over an edited workspace: %v", err)
		}

		versions, err := testHandler.Queries.ListWorkflowTemplateVersions(ctx, db.ListWorkflowTemplateVersionsParams{
			WorkspaceID: wsUUID, TemplateID: templateID,
		})
		if err != nil {
			t.Fatalf("list versions: %v", err)
		}
		if len(versions) != 1 {
			t.Fatalf("version count = %d, want 1; the seeder published over a user's edited built-in", len(versions))
		}
		tpl, err := testHandler.Queries.GetWorkflowTemplateByKey(ctx, db.GetWorkflowTemplateByKeyParams{
			WorkspaceID: wsUUID, Key: "bug_fix",
		})
		if err != nil {
			t.Fatalf("get template: %v", err)
		}
		if !tpl.CurrentVersion.Valid || tpl.CurrentVersion.Int32 != 1 {
			t.Errorf("current_version = %+v, want 1; the edited copy must stay current", tpl.CurrentVersion)
		}
		if got := storedWorkflowDefinition(t, editedID); string(got) != string(before) {
			t.Error("the user's edited definition was modified")
		}
		// The user's WORDS must still be there. A byte-length comparison would pass
		// even if the row had been replaced with something else the same size.
		stillEdited, err := workflow.ParseDefinition(storedWorkflowDefinition(t, editedID))
		if err != nil {
			t.Fatalf("the stored edited definition does not parse: %v", err)
		}
		analyze, _ := stillEdited.NodeByKey("analyze")
		if analyze == nil || !strings.Contains(analyze.Instruction, "scratch workspace first") {
			t.Errorf("the user's edit is gone from the analyze instruction: %+v", analyze)
		}
		if _, ok := stillEdited.EntryInputNode(); ok {
			t.Error("the edited copy grew an entry input node; the seeder rewrote a graph it was told to leave alone")
		}
	})
}

// ---------------------------------------------------------------------------
// 3, 4, 5. A real Run through the intake node
// ---------------------------------------------------------------------------

// TestInputNodeRunDeliversTypedIntakeToTheAnalyzeAgent is hops 3, 4 and 5: start a
// Run through the real endpoint supplying the intake fields, then prove the intake
// step passed through without dispatching work, that analyze queued behind it, and
// that the text the human typed reaches the agent THROUGH THE REAL CONSUMER PATH.
func TestInputNodeRunDeliversTypedIntakeToTheAnalyzeAgent(t *testing.T) {
	withWorkflowEngineForTest(t)
	env := newWorkflowE2EEnv(t, "intake")
	ctx := context.Background()

	// The pinned graph must be the intake revision, or every assertion below is
	// about the legacy path. newWorkflowE2EEnv seeds a FRESH workspace, so this is
	// the create path, not the upgrade path.
	pinned := env.pinnedPublishedDefinition(t)
	intake, ok := pinned.EntryInputNode()
	if !ok {
		t.Fatalf("the seeded template's entry node %q is not an input node; this test is about the typed intake path", pinned.EntryNode)
	}
	if len(intake.InputFields) != 2 {
		t.Fatalf("intake declares %d fields, want 2", len(intake.InputFields))
	}

	const (
		title = "Comment editor drops the edit when Save is pressed twice"
		// Distinctive enough that finding it in a prompt cannot be a coincidence,
		// and phrased as a real report so nothing in the chain can be "helpfully"
		// substituting the node instruction for it.
		description = "Open a comment, edit it, and press Save twice quickly. The editor closes, no PATCH is sent " +
			"for the second press, and the original text is back after a reload. Console shows " +
			"'detached node' from the editor teardown."
	)

	w := env.startRun(t, title, description)
	if w.Code != http.StatusCreated {
		t.Fatalf("RunWorkflowTemplate: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	run := decodeWorkflowRunDetail(t, w, "RunWorkflowTemplate")
	if run.Status != "running" {
		t.Fatalf("run status = %q, want running", run.Status)
	}

	// --- 3. The intake step exists, PASSED, and dispatched nothing.
	step, ok := findWorkflowStep(run.Steps, "intake")
	if !ok {
		keys := make([]string, 0, len(run.Steps))
		for _, s := range run.Steps {
			keys = append(keys, s.NodeKey+"/"+s.Status)
		}
		t.Fatalf("no intake step in the trace (%s); the Run's history does not record that a human supplied anything",
			strings.Join(keys, ", "))
	}
	if step.NodeType != string(workflow.NodeTypeInput) {
		t.Errorf("intake step node_type = %q, want %q", step.NodeType, workflow.NodeTypeInput)
	}
	if step.Status != "passed" {
		t.Fatalf("intake step status = %q, want passed (failure_reason=%v); an intake node is a passthrough, not work",
			step.Status, step.FailureReason)
	}
	// task_id ASSERTED NULL rather than assumed. The DB CHECK
	// workflow_step_instance_task_only_on_agent forbids a task on a non-agent node,
	// so a dispatch here would have failed the INSERT - but the engine could also
	// have created the task and then not linked it, which the CHECK cannot see.
	if step.TaskID != nil {
		t.Errorf("the intake step carries agent task %v; the human already filled the form in and an agent must not be asked to", *step.TaskID)
	}
	if step.AgentID != nil {
		t.Errorf("the intake step routed to agent %v; an input node declares no routing", *step.AgentID)
	}
	if step.RoutingReason != nil {
		t.Errorf("the intake step has a routing reason (%q); nothing was routed", *step.RoutingReason)
	}
	if step.Submission != nil {
		t.Errorf("the intake step recorded a submission (%+v); nothing submits at an intake node", step.Submission)
	}
	// Belt and braces straight at the table: the response shape could omit a task
	// link the row actually carries.
	assertNoTaskRowForStep(t, step.ID)

	// The intake step must be the FIRST thing in the trace, not a row written
	// alongside analyze. Ordering is what makes the trace a history.
	if idx := workflowStepIndex(run.Steps, "intake"); idx != 0 {
		t.Errorf("intake is at trace position %d, want 0; the entry node must be the first step", idx)
	}

	// --- 3 (continued). analyze queued immediately behind it, in the SAME response.
	// Not on a later poll: the intake passthrough advances inside StartRun's
	// transaction, so a Run whose first agent queues only after some later tick
	// would be a stall on the happy path.
	analyze, ok := findWorkflowStep(run.Steps, "analyze")
	if !ok {
		t.Fatalf("intake passed but analyze never activated; the run stalled at intake: %+v", run.Steps)
	}
	if analyze.Status != "queued" {
		t.Fatalf("analyze status = %q, want queued (failure_reason=%v routing_reason=%v)",
			analyze.Status, analyze.FailureReason, analyze.RoutingReason)
	}
	if analyze.TaskID == nil {
		t.Fatal("analyze queued no agent task; no agent was asked to do anything")
	}
	if analyze.AgentID == nil || *analyze.AgentID != env.analystID {
		t.Fatalf("analyze routed to %v, want the bug_analysis specialist %s", analyze.AgentID, env.analystID)
	}

	// The run's stored input bag carries the typed values under the keys the
	// declaration named. This is what ParseRunInputFor reads, and it is the seam
	// where a handler that modelled only {title, description} used to drop
	// everything else.
	var bag map[string]any
	if err := json.Unmarshal(run.Input, &bag); err != nil {
		t.Fatalf("the run's input is not a JSON object: %v (%s)", err, string(run.Input))
	}
	if bag["title"] != title {
		t.Errorf("run input title = %v, want the submitted title", bag["title"])
	}
	if bag["description"] != description {
		t.Errorf("run input description = %v, want the submitted description", bag["description"])
	}

	// --- 4. THE CENTRAL ASSERTION: the typed bug description reaches the analyze
	// agent, verified through the REAL consumer.
	wire, claimedID := env.claimNextTaskWire(t)
	if claimedID != *analyze.TaskID {
		t.Fatalf("claimed task %s, want the analyze task %s", claimedID, *analyze.TaskID)
	}
	// Read the node key off the same bytes: an intake node that had queued a task
	// would be the first thing claimable and would surface here.
	var nodeCheck struct {
		WorkflowNodeKey string `json:"workflow_node_key"`
	}
	if err := json.Unmarshal(wire, &nodeCheck); err != nil {
		t.Fatalf("decode the claimed task: %v", err)
	}
	if nodeCheck.WorkflowNodeKey != "analyze" {
		t.Fatalf("claim workflow_node_key = %q, want analyze; the intake node must not produce a claimable task",
			nodeCheck.WorkflowNodeKey)
	}
	delivered := deliveredWorkflowPrompt(t, wire)

	for _, want := range []struct {
		text string
		why  string
	}{
		{description, "the bug description the human typed at intake - without it the agent is told to reproduce a defect nobody named"},
		{title, "the title the human typed at intake"},
		{"identify the root cause", "this step's own instruction"},
		{"<<<MULTICA_SUBMISSION>>>", "the submission contract; without it correct work is rejected as submission_contract_invalid"},
		{analyze.ID, "the step id the agent must echo back, or its submission is refused as cross-talk"},
	} {
		if !strings.Contains(delivered, want.text) {
			t.Fatalf("the prompt daemon.BuildPrompt hands the agent is missing %s (%q).\n--- prompt ---\n%s\n--- end ---",
				want.why, want.text, delivered)
		}
	}
	t.Logf("=== prompt delivered to the analyze agent (via daemon.Task + daemon.BuildPrompt) ===\n%s\n=== end ===", delivered)

	// The intake node's own instruction is prose FOR THE AUTHOR reading the canvas,
	// not a task for the analyze agent. Leaking it would tell the agent to collect
	// the report it was just given.
	if strings.Contains(delivered, intake.Instruction) && intake.Instruction != "" {
		t.Errorf("the analyze prompt carries the intake node's instruction (%q); that text describes the human's job, not the agent's",
			intake.Instruction)
	}

	// The run still finishes. The intake node is new machinery on the entry path,
	// so "it delivers a prompt" is not enough - the Run has to still advance.
	if _, err := testHandler.TaskService.StartTask(ctx, parseUUID(claimedID)); err != nil {
		t.Fatalf("StartTask(analyze): %v", err)
	}
	brief, ok := workflow.ParseTaskContext(taskContextBytes(t, claimedID))
	if !ok {
		t.Fatal("the analyze task carries no workflow brief")
	}
	if _, err := testHandler.TaskService.CompleteTask(ctx, parseUUID(claimedID),
		e2eTaskResult(t, claimedID, brief.StepInstanceID, "analysis",
			"the Save handler is bound to a detached node after the editor re-renders"),
		"", "", "", false, "", ""); err != nil {
		t.Fatalf("CompleteTask(analyze): %v", err)
	}
	after := env.getRun(t, run.ID)
	if done, _ := findWorkflowStep(after.Steps, "analyze"); done.Status != "passed" {
		t.Fatalf("analyze status = %q after completion, want passed", done.Status)
	}
	if _, ok := findWorkflowStep(after.Steps, "implement"); !ok {
		t.Fatalf("the run did not advance past analyze: %+v", after.Steps)
	}
	// The intake step must not have been re-activated or rewritten by the advance.
	stillPassed, _ := findWorkflowStep(after.Steps, "intake")
	if stillPassed.Status != "passed" || stillPassed.TaskID != nil {
		t.Errorf("the intake step changed as the run advanced: status=%q task=%v", stillPassed.Status, stillPassed.TaskID)
	}
}

// TestInputNodeBlankRequiredFieldIsRejectedAndNoRunIsCreated is hop 5: a required
// declared field left blank is a typed rejection, and nothing durable is written.
//
// It runs against TWO templates on purpose, and the reason is a real blind spot
// found by mutating the production code. bug_fix's intake declares exactly title
// and description, and the HANDLER guards both itself (400) before StartRun is
// called - so deleting the required-field check from workflow.ValidateRunInput
// entirely leaves a bug_fix-only version of this test completely green. PART TWO
// therefore uses a graph declaring a required `severity`, a field the handler knows
// nothing about, where the declaration-driven check is the only thing that can
// refuse a blank value.
//
// The "no run" half is worth the DB round trip. The endpoint creates the Issue
// BEFORE calling StartRun, so a rejection that arrived after the Run row existed -
// or after the Issue was committed and not rolled back - would leave a submitter
// looking at an issue for work that never started, with nothing saying why.
func TestInputNodeBlankRequiredFieldIsRejectedAndNoRunIsCreated(t *testing.T) {
	withWorkflowEngineForTest(t)
	env := newWorkflowE2EEnv(t, "blank")
	ctx := context.Background()

	pinned := env.pinnedPublishedDefinition(t)
	intake, ok := pinned.EntryInputNode()
	if !ok {
		t.Fatalf("the seeded template's entry node %q is not an input node", pinned.EntryNode)
	}
	// The declaration must actually require something, or PART ONE below would be
	// asserting nothing about required-ness at all.
	required := 0
	for _, f := range intake.InputFields {
		if f.Required {
			required++
		}
	}
	if required == 0 {
		t.Fatal("the intake node declares no required field; this test cannot distinguish a declared-field rejection from a handler guard")
	}

	runsBefore := countWorkflowRuns(t, env.workspaceID)
	issuesBefore := countWorkspaceIssues(t, env.workspaceID)

	// PART ONE: the two first-class fields. bug_fix's intake declares exactly title
	// and description, and the handler guards both itself before the engine is
	// reached, so these cases assert the ENDPOINT's contract - a typed 400, never a
	// 500 and never a 201.
	for _, tc := range []struct {
		name string
		body map[string]any
	}{
		// Absent, empty, and whitespace-only are three different shapes of the same
		// mistake, and a presence check that only tested one of them would let " "
		// through - a description of a single space satisfies "not empty" and tells
		// an agent nothing.
		{"description absent", map[string]any{"title": "Blank description"}},
		{"description empty", map[string]any{"title": "Blank description", "description": ""}},
		{"description whitespace", map[string]any{"title": "Blank description", "description": "   \n\t "}},
		{"title absent", map[string]any{"description": "A report with no title at all."}},
		{"title whitespace", map[string]any{"title": "  ", "description": "A report whose title is spaces."}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := withURLParam(
				env.request("POST", "/api/workflow-templates/"+env.templateID+"/run", tc.body),
				"id", env.templateID,
			)
			testHandler.RunWorkflowTemplate(w, req)

			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", w.Code, w.Body.String())
			}
			var body struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatalf("the rejection body is not JSON: %v (%s)", err, w.Body.String())
			}
			if strings.TrimSpace(body.Error) == "" {
				t.Fatalf("the rejection carries no message; the submitter cannot tell which field to fix: %s", w.Body.String())
			}
		})
	}

	// PART TWO: a required field the handler knows NOTHING about, so
	// workflow.ValidateRunInput is the only thing that can refuse it.
	//
	// This half is the reason bug_fix alone is not enough here. Its intake declares
	// only title and description, and the handler rejects a blank one of those with
	// its own 400 before StartRun is ever called - so with the required-field check
	// deleted from ValidateRunInput entirely, every case in PART ONE still passes.
	// Verified by mutation: removing `if f.Required && blank` left this test green
	// until these cases existed. A required `severity` is invisible to the handler,
	// so only the declaration-driven check stands between a blank value and a Run
	// whose first agent is missing something the graph promised it.
	typedID := env.publishTemplate(t, "blank_typed_intake", "Blank Typed Intake", typedIntakeDefinition())
	for _, tc := range []struct {
		name string
		body map[string]any
		want string
	}{
		{
			"required declared field absent",
			map[string]any{"title": "No severity", "description": "The required severity is not in the body at all."},
			"How bad is it",
		},
		{
			"required declared field empty",
			map[string]any{"title": "Empty severity", "description": "The required severity is an empty string.", "severity": ""},
			"How bad is it",
		},
		{
			"required declared field whitespace",
			map[string]any{"title": "Blank severity", "description": "The required severity is spaces.", "severity": "   "},
			"How bad is it",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			testHandler.RunWorkflowTemplate(w, withURLParam(
				env.request("POST", "/api/workflow-templates/"+typedID+"/run", tc.body), "id", typedID))

			// 422 specifically, and the code specifically. This is a payload failing
			// its DECLARED contract, which is what ErrCodeInvalidSubmission means; a
			// 400 here would say the request was malformed and a 500 would say the
			// server broke over a human leaving a field blank.
			if w.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422; a blank required declared field must be a typed rejection: %s",
					w.Code, w.Body.String())
			}
			var body struct {
				Error string `json:"error"`
				Code  string `json:"code"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatalf("the rejection body is not JSON: %v (%s)", err, w.Body.String())
			}
			if body.Code != workflow.ErrCodeInvalidSubmission {
				t.Errorf("code = %q, want %q", body.Code, workflow.ErrCodeInvalidSubmission)
			}
			// The message must NAME the field, by its declared label. "invalid input"
			// leaves a submitter guessing which of four controls to fix.
			if !strings.Contains(body.Error, tc.want) {
				t.Errorf("the rejection does not name the field that failed (want %q): %s", tc.want, body.Error)
			}
		})
	}
	// And the control for PART TWO: the same template accepts the same body with
	// severity supplied. Without this, an endpoint that refused every run on this
	// template would satisfy all three cases above.
	t.Run("the same template accepts a complete body", func(t *testing.T) {
		w := httptest.NewRecorder()
		testHandler.RunWorkflowTemplate(w, withURLParam(
			env.request("POST", "/api/workflow-templates/"+typedID+"/run", map[string]any{
				"title":       "Severity supplied",
				"description": "The required severity is present, so this run must start.",
				"severity":    "minor",
			}), "id", typedID))
		if w.Code != http.StatusCreated {
			t.Fatalf("a complete typed submission was refused with %d: %s", w.Code, w.Body.String())
		}
	})

	// NOTHING durable was written by any of the REJECTED submissions. The accepted
	// control above created one Run on the typed template, so that is the expected
	// delta - a rejection that also created one would push this past it.
	if got := countWorkflowRuns(t, env.workspaceID); got != runsBefore+1 {
		t.Fatalf("workflow_run count = %d, want %d (the one accepted control run); a rejected submission created a Run",
			got, runsBefore+1)
	}
	if got := countWorkspaceIssues(t, env.workspaceID); got != issuesBefore+1 {
		t.Errorf("issue count = %d, want %d; a rejected submission left an orphan Issue for work that never started",
			got, issuesBefore+1)
	}

	// The control for PART ONE: the SAME endpoint, same bug_fix template, with both
	// fields filled in, starts a Run.
	ok2 := env.startRun(t, "Both intake fields supplied",
		"Filling in the declared fields must start the run; this is the control for the rejections above.")
	if ok2.Code != http.StatusCreated {
		t.Fatalf("a complete submission was refused with %d: %s", ok2.Code, ok2.Body.String())
	}
	if got := countWorkflowRuns(t, env.workspaceID); got != runsBefore+2 {
		t.Errorf("workflow_run count = %d after a valid submission, want %d", got, runsBefore+2)
	}
	_ = ctx
}

// TestInputNodeDeclaredFieldBeyondTitleAndDescriptionReachesThePrompt is the
// assertion the seeded bug_fix graph CANNOT make.
//
// bug_fix's intake declares exactly title and description, which
// ParseRunInputFor deliberately skips when building RunInput.Fields (they have
// first-class slots and dedicated prompt formatting, and listing them again would
// print the reporter's report twice in one prompt). So a test that only ever ran
// bug_fix would exercise none of the declared-field machinery: not the endpoint's
// passthrough of an unmodelled key, not ParseRunInputFor's field loop, not
// RenderPrompt's labelled-value block. It would pass with all three deleted.
//
// This test therefore publishes a template whose intake declares `severity` (a
// select) and `repro_steps` (a textarea) beyond the pair, and follows one value
// from the HTTP request body to the string daemon.BuildPrompt returns.
func TestInputNodeDeclaredFieldBeyondTitleAndDescriptionReachesThePrompt(t *testing.T) {
	withWorkflowEngineForTest(t)
	env := newWorkflowE2EEnv(t, "declared")
	ctx := context.Background()

	templateID := env.publishTemplate(t, "typed_intake", "Typed Intake", typedIntakeDefinition())

	const (
		title       = "Search returns results from another workspace"
		description = "Searching from workspace A shows three issues that belong to workspace B."
		repro       = "1. Sign in as a member of A only.\n2. Search for 'invoice'.\n3. Two of the five hits open 404s."
		severity    = "critical"
	)

	w := httptest.NewRecorder()
	testHandler.RunWorkflowTemplate(w, withURLParam(
		env.request("POST", "/api/workflow-templates/"+templateID+"/run", map[string]any{
			"title":       title,
			"description": description,
			// Flat, under the declared keys, which is the bag shape
			// workflow.ParseRunInputFor reads.
			"severity":    severity,
			"repro_steps": repro,
		}), "id", templateID))
	if w.Code != http.StatusCreated {
		t.Fatalf("RunWorkflowTemplate: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	run := decodeWorkflowRunDetail(t, w, "RunWorkflowTemplate")

	// (i) The endpoint carried the unmodelled keys into the Run's input bag. This
	// is the hop that used to be missing: RunWorkflowTemplateRequest is a struct of
	// {title, description, project_id}, and json.Unmarshal into a struct discards
	// every other key silently.
	var bag map[string]any
	if err := json.Unmarshal(run.Input, &bag); err != nil {
		t.Fatalf("the run's input is not a JSON object: %v (%s)", err, string(run.Input))
	}
	if bag["severity"] != severity {
		t.Fatalf("run input severity = %v, want %q; the endpoint dropped a declared field, so ValidateRunInput would refuse a value the submitter did send",
			bag["severity"], severity)
	}
	if bag["repro_steps"] != repro {
		t.Fatalf("run input repro_steps = %v, want the submitted steps", bag["repro_steps"])
	}
	// project_id must NOT have leaked into the bag: it addresses the Issue, not the
	// workflow, and a graph declaring a field of that name would receive it as if a
	// human had typed it.
	if _, present := bag["project_id"]; present {
		t.Errorf("project_id leaked into the run input bag: %v", bag["project_id"])
	}

	// (ii) The declared values reach the agent, with their declared LABELS. A raw
	// key like `repro_steps` in a prompt is a value the agent has to guess the
	// meaning of.
	first, ok := findWorkflowStep(run.Steps, "triage")
	if !ok || first.TaskID == nil {
		t.Fatalf("no queued triage step: %+v", run.Steps)
	}
	wire, claimedID := env.claimNextTaskWire(t)
	if claimedID != *first.TaskID {
		t.Fatalf("claimed %s, want the triage task %s", claimedID, *first.TaskID)
	}
	delivered := deliveredWorkflowPrompt(t, wire)
	for _, want := range []string{
		description,     // the freeform prose
		repro,           // a declared textarea's value
		severity,        // a declared select's value
		"Reproduction",  // repro_steps' declared label
		"How bad is it", // severity's declared label
	} {
		if !strings.Contains(delivered, want) {
			t.Fatalf("the delivered prompt is missing %q.\n--- prompt ---\n%s\n--- end ---", want, delivered)
		}
	}
	// The raw keys must not be what the agent is shown when a label exists.
	if strings.Contains(delivered, "repro_steps") {
		t.Errorf("the prompt names a declared field by its raw key rather than its label:\n%s", delivered)
	}
	t.Logf("=== prompt with declared fields ===\n%s\n=== end ===", delivered)

	// (iii) An off-list select value is refused, not passed through. The author
	// enumerated the values downstream steps are written against.
	bad := httptest.NewRecorder()
	testHandler.RunWorkflowTemplate(bad, withURLParam(
		env.request("POST", "/api/workflow-templates/"+templateID+"/run", map[string]any{
			"title":       "An off-list severity",
			"description": "The severity below is not one the graph declared.",
			"severity":    "apocalyptic",
			"repro_steps": "n/a",
		}), "id", templateID))
	if bad.Code != http.StatusUnprocessableEntity {
		t.Fatalf("an off-list select value returned %d, want 422: %s", bad.Code, bad.Body.String())
	}
	if !strings.Contains(bad.Body.String(), "critical") {
		t.Errorf("the refusal does not name the permitted values, so the submitter cannot correct it: %s", bad.Body.String())
	}
	_ = ctx
}

// ---------------------------------------------------------------------------
// 6. Backward compatibility: an agent entry node still works
// ---------------------------------------------------------------------------

// TestInputNodeAbsentEntryAgentGraphStillDeliversFreeformDescription is hop 6.
//
// The existing e2e suite covers this for the legacy SHAPE, but it now runs against
// the new bug_fix revision, so nothing there exercises a published graph whose
// entry node is an agent any more - and that is what every template published
// before this feature is, forever, because a published version is immutable and
// in-flight Runs pin it.
//
// So this publishes the REAL previous revision's bytes as a template and runs it.
// Not a hand-written "graph with no input node": the point is the graph a
// pre-upgrade workspace actually has.
func TestInputNodeAbsentEntryAgentGraphStillDeliversFreeformDescription(t *testing.T) {
	withWorkflowEngineForTest(t)
	env := newWorkflowE2EEnv(t, "legacy")

	v1, ok := service.BuiltinWorkflowShippedDefinition("bug_fix", 0)
	if !ok {
		t.Fatal("bug_fix has no shipped revision 0")
	}
	legacy, err := workflow.ParseDefinition(v1)
	if err != nil {
		t.Fatalf("the shipped v1 bytes do not parse: %v", err)
	}
	if _, hasInput := legacy.EntryInputNode(); hasInput {
		t.Fatal("shipped revision 0 has an entry input node; it cannot stand in for a pre-input-node template")
	}
	if legacy.EntryNode != "analyze" {
		t.Fatalf("shipped revision 0 entry = %q, want analyze", legacy.EntryNode)
	}

	// Published under a NON-builtin key: the built-in key is reserved by
	// CreateWorkflowTemplate (and would put this graph on the seeder's upgrade
	// path), and what is under test is the graph shape, not the key.
	var raw map[string]any
	if err := json.Unmarshal(v1, &raw); err != nil {
		t.Fatalf("re-decode v1: %v", err)
	}
	templateID := env.publishTemplate(t, "legacy_entry_agent", "Legacy Bug Fix", raw)

	const (
		title       = "Legacy graph must still run"
		description = "A template published before input nodes existed still has to deliver this text to its first agent."
	)
	w := httptest.NewRecorder()
	testHandler.RunWorkflowTemplate(w, withURLParam(
		env.request("POST", "/api/workflow-templates/"+templateID+"/run", map[string]any{
			"title": title, "description": description,
		}), "id", templateID))
	if w.Code != http.StatusCreated {
		t.Fatalf("a pre-input-node template was refused with %d: %s", w.Code, w.Body.String())
	}
	run := decodeWorkflowRunDetail(t, w, "RunWorkflowTemplate")
	if run.Status != "running" {
		t.Fatalf("run status = %q, want running", run.Status)
	}

	// No intake step, and analyze is the entry.
	if _, ok := findWorkflowStep(run.Steps, "intake"); ok {
		t.Error("a graph with no input node produced an intake step")
	}
	analyze, ok := findWorkflowStep(run.Steps, "analyze")
	if !ok {
		t.Fatalf("no analyze step: %+v", run.Steps)
	}
	if analyze.Status != "queued" || analyze.TaskID == nil {
		t.Fatalf("analyze status = %q task=%v, want a queued step with a task", analyze.Status, analyze.TaskID)
	}
	if workflowStepIndex(run.Steps, "analyze") != 0 {
		t.Errorf("analyze is not the first step of a graph whose entry it is")
	}

	// The freeform description still reaches the agent, through the real consumer.
	wire, claimedID := env.claimNextTaskWire(t)
	if claimedID != *analyze.TaskID {
		t.Fatalf("claimed %s, want %s", claimedID, *analyze.TaskID)
	}
	delivered := deliveredWorkflowPrompt(t, wire)
	for _, want := range []string{description, title, "<<<MULTICA_SUBMISSION>>>", analyze.ID} {
		if !strings.Contains(delivered, want) {
			t.Fatalf("the legacy graph's delivered prompt is missing %q.\n--- prompt ---\n%s\n--- end ---", want, delivered)
		}
	}
	// And the brief carries NO declared fields. `omitempty` on TaskContext.RunFields
	// is what keeps a legacy brief byte-identical to what it was before typed
	// inputs existed, and a stray empty array would break that.
	rawContext := taskContextBytes(t, claimedID)
	brief, ok := workflow.ParseTaskContext(rawContext)
	if !ok {
		t.Fatal("the analyze task carries no workflow brief")
	}
	if len(brief.RunFields) != 0 {
		t.Errorf("a template with no input node produced run_fields %+v", brief.RunFields)
	}
	if !strings.Contains(string(rawContext), `"run_description"`) {
		t.Error("the legacy brief does not carry run_description")
	}
	if strings.Contains(string(rawContext), `"run_fields"`) {
		t.Error("a legacy brief carries a run_fields key; omitempty must elide it so the stored brief is what it always was")
	}
}

// ---------------------------------------------------------------------------
// Fixture
// ---------------------------------------------------------------------------

// deliveredWorkflowPrompt is the assertion this file exists for: it takes the raw
// bytes of the claim response and produces the string the agent CLI is actually
// handed, using the real daemon types and the real daemon function.
//
// The distinction from e2eClaimedTask (in workflow_run_e2e_test.go) is not
// stylistic. That struct is declared in the test file, so it can only ever agree
// with itself: it proves the server serialized a field, and is structurally unable
// to notice that the consumer's own struct never declared it or that BuildPrompt
// took a different branch. Both of those shipped: daemon.Task had no workflow
// fields, so json.Unmarshal discarded workflow_prompt without error and BuildPrompt
// fell through to the legacy "go read your assigned issue" prompt - the agent was
// never told to emit a submission block and every step of every run would have
// blocked. Decoding into daemon.Task and calling daemon.BuildPrompt is the only
// assertion that can see that.
//
// `wire` must be the bytes the claim endpoint produced, not a value some Go type
// in this package has already interpreted - hence claimNextTaskWire below rather
// than a re-serialization of AgentTaskResponse. Package daemon imports nothing from
// package handler, so reading it here is not a cycle.
func deliveredWorkflowPrompt(t *testing.T, wire []byte) string {
	t.Helper()

	var task daemon.Task
	if err := json.Unmarshal(wire, &task); err != nil {
		t.Fatalf("decode the claim payload into daemon.Task: %v\n%s", err, string(wire))
	}
	if task.WorkflowPrompt == "" {
		t.Fatalf("daemon.Task dropped workflow_prompt: the agent receives no brief at all.\nwire bytes: %s", string(wire))
	}
	if task.WorkflowStepInstanceID == "" || task.WorkflowNodeKey == "" {
		t.Errorf("daemon.Task dropped the workflow ids: step=%q node=%q",
			task.WorkflowStepInstanceID, task.WorkflowNodeKey)
	}

	prompt := daemon.BuildPrompt(task, "claude")
	if prompt == "" {
		t.Fatal("daemon.BuildPrompt returned nothing for a workflow step task")
	}
	// The workflow branch must WIN. A workflow task normally carries an issue link,
	// so the legacy assignment prompt would happily claim it - and that prompt says
	// nothing about the step, the run input, or the submission contract.
	if prompt != task.WorkflowPrompt {
		t.Fatalf("daemon.BuildPrompt did not return the server's brief verbatim; a legacy branch captured the task.\n--- got ---\n%s\n--- want ---\n%s",
			prompt, task.WorkflowPrompt)
	}
	if strings.Contains(prompt, "multica issue get") {
		t.Error("the legacy assignment prompt captured a workflow step task")
	}
	return prompt
}

// claimNextTaskWire claims through the REAL daemon endpoint and returns both the
// untouched wire bytes of the task object and the id it carries.
//
// It returns the RAW JSON, not a struct, because the whole point of the assertion
// downstream is to decode into the consumer's own type. env.claimNextTask decodes
// into e2eClaimedTask and discards the bytes, so it cannot be reused here - a field
// e2eClaimedTask does not declare is exactly the class of bug being guarded
// against, and it would be gone before daemon.Task ever saw it.
func (env *workflowE2EEnv) claimNextTaskWire(t *testing.T) ([]byte, string) {
	t.Helper()
	w := httptest.NewRecorder()
	testHandler.ClaimTaskByRuntime(w, withURLParam(
		newDaemonTokenRequest("POST", "/api/daemon/runtimes/"+env.runtimeID+"/tasks/claim", nil,
			env.workspaceID, "workflow-wire-daemon"),
		"runtimeId", env.runtimeID))
	if w.Code != http.StatusOK {
		t.Fatalf("ClaimTaskByRuntime: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var body struct {
		Task json.RawMessage `json:"task"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode the claim envelope: %v (%s)", err, w.Body.String())
	}
	if len(body.Task) == 0 || string(body.Task) == "null" {
		t.Fatalf("no task was claimable on the runtime; the step queued no work: %s", w.Body.String())
	}
	// Read the id out of the same bytes rather than out of a second request, so the
	// caller's "is this the step's task" check is about the payload under test.
	var ident struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body.Task, &ident); err != nil || ident.ID == "" {
		t.Fatalf("the claimed task carries no id: %s", string(body.Task))
	}
	return body.Task, ident.ID
}

// pinnedPublishedDefinition parses the definition a Run started now would pin.
func (env *workflowE2EEnv) pinnedPublishedDefinition(t *testing.T) *workflow.Definition {
	t.Helper()
	ctx := context.Background()
	tpl, err := testHandler.Queries.GetWorkflowTemplateByKey(ctx, db.GetWorkflowTemplateByKeyParams{
		WorkspaceID: parseUUID(env.workspaceID), Key: "bug_fix",
	})
	if err != nil {
		t.Fatalf("get bug_fix template: %v", err)
	}
	version, err := testHandler.Queries.GetPublishedWorkflowTemplateVersion(ctx, db.GetPublishedWorkflowTemplateVersionParams{
		TemplateID: tpl.ID, WorkspaceID: parseUUID(env.workspaceID),
	})
	if err != nil {
		t.Fatalf("get published version: %v", err)
	}
	def, err := workflow.ParseDefinition(version.Definition)
	if err != nil {
		t.Fatalf("the pinned definition does not parse: %v", err)
	}
	return def
}

// publishTemplate creates and publishes a template in the env's workspace through
// the real endpoints, returning its id.
//
// Through the HTTP handlers rather than by inserting rows: publishing is where
// Validate runs, so a graph this test builds is proven to be one the server would
// actually accept. A row inserted directly could carry a definition the validator
// rejects, and every assertion after it would be about an unpublishable graph.
func (env *workflowE2EEnv) publishTemplate(t *testing.T, key, name string, definition map[string]any) string {
	t.Helper()

	cw := httptest.NewRecorder()
	testHandler.CreateWorkflowTemplate(cw, env.request("POST", "/api/workflow-templates", map[string]any{
		"key":        key,
		"name":       name,
		"definition": definition,
	}))
	if cw.Code != http.StatusCreated {
		t.Fatalf("CreateWorkflowTemplate(%s): expected 201, got %d: %s", key, cw.Code, cw.Body.String())
	}
	var created WorkflowTemplateDetailResponse
	if err := json.NewDecoder(cw.Body).Decode(&created); err != nil {
		t.Fatalf("decode the created template: %v", err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		testPool.Exec(bg, `DELETE FROM workflow_template_version WHERE template_id = $1`, created.ID)
		testPool.Exec(bg, `DELETE FROM workflow_template WHERE id = $1`, created.ID)
	})

	pw := httptest.NewRecorder()
	testHandler.PublishWorkflowTemplate(pw, withURLParam(
		env.request("POST", "/api/workflow-templates/"+created.ID+"/publish", nil), "id", created.ID))
	if pw.Code != http.StatusOK {
		t.Fatalf("PublishWorkflowTemplate(%s): expected 200, got %d: %s", key, pw.Code, pw.Body.String())
	}
	return created.ID
}

// typedIntakeDefinition is a graph whose intake declares fields BEYOND
// title/description.
//
// It has to be its own fixture rather than reusing bug_fix, because bug_fix
// declares exactly title and description - the two keys ParseRunInputFor
// deliberately skips when building RunInput.Fields. A test running only bug_fix
// therefore exercises none of the declared-field path and would stay green with
// that path deleted.
//
// `severity` is a select and `repro_steps` a textarea so both the enumerated and
// the free-text kinds are covered, and the labels are distinctive strings so
// finding them in a prompt cannot be a coincidence.
func typedIntakeDefinition() map[string]any {
	return map[string]any{
		"schema_version": 1,
		"entry_node":     "intake",
		"nodes": []map[string]any{
			{
				"key":         "intake",
				"type":        "input",
				"name":        "Report",
				"instruction": "What the reporter tells us.",
				"next":        []string{"triage"},
				"input_fields": []map[string]any{
					{"key": "title", "label": "Headline", "type": "text", "required": true},
					{"key": "description", "label": "What happened", "type": "textarea", "required": true},
					{
						"key": "severity", "label": "How bad is it", "type": "select", "required": true,
						"options": []string{"critical", "major", "minor"},
					},
					{"key": "repro_steps", "label": "Reproduction", "type": "textarea"},
				},
			},
			{
				"key":               "triage",
				"type":              "agent",
				"name":              "Triage",
				"instruction":       "Read the report and decide what has to change.",
				"next":              []string{"end"},
				"routing":           map[string]any{"strategy": "capability", "capability": "bug_analysis"},
				"submission_schema": "analysis",
				"on_failure":        "block",
			},
			{"key": "end", "type": "end", "name": "Done"},
		},
	}
}

// newInputNodeWorkspace creates a throwaway workspace with the test user as owner
// and registers explicit cleanup of every workflow table.
//
// A dedicated workspace per case, rather than the shared fixture: the seeder
// decisions under test are per-workspace and a leftover template from another case
// would make "pristine" unverifiable. The workflow tables carry no foreign keys by
// design, so dropping the workspace does NOT remove them.
func newInputNodeWorkspace(t *testing.T, name string) string {
	t.Helper()
	ctx := context.Background()
	slug := "wf-input-" + name

	cleanup := func() {
		bg := context.Background()
		for _, stmt := range []string{
			`DELETE FROM workflow_event WHERE workspace_id IN (SELECT id FROM workspace WHERE slug = $1)`,
			`DELETE FROM workflow_acceptance WHERE workspace_id IN (SELECT id FROM workspace WHERE slug = $1)`,
			`DELETE FROM workflow_submission WHERE workspace_id IN (SELECT id FROM workspace WHERE slug = $1)`,
			`DELETE FROM workflow_step_instance WHERE workspace_id IN (SELECT id FROM workspace WHERE slug = $1)`,
			`DELETE FROM workflow_run WHERE workspace_id IN (SELECT id FROM workspace WHERE slug = $1)`,
			`DELETE FROM workflow_template_version WHERE workspace_id IN (SELECT id FROM workspace WHERE slug = $1)`,
			`DELETE FROM workflow_template WHERE workspace_id IN (SELECT id FROM workspace WHERE slug = $1)`,
			`DELETE FROM workspace WHERE slug = $1`,
		} {
			testPool.Exec(bg, stmt, slug)
		}
	}
	// Residue from an interrupted run first: a leftover template row would survive
	// its workspace and make "freshly seeded" unverifiable.
	cleanup()
	t.Cleanup(cleanup)

	var workspaceID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO workspace (name, slug, description, issue_prefix)
		VALUES ($1, $2, $3, 'WFI') RETURNING id
	`, "Workflow Input "+name, slug, "input node test workspace").Scan(&workspaceID); err != nil {
		t.Fatalf("create workspace %s: %v", slug, err)
	}
	if _, err := testPool.Exec(ctx,
		`INSERT INTO member (workspace_id, user_id, role) VALUES ($1, $2, 'owner')`,
		workspaceID, testUserID); err != nil {
		t.Fatalf("add member to %s: %v", slug, err)
	}
	return workspaceID
}

// seedLegacyBugFixTemplate writes a bug_fix template the way an OLDER binary's
// seeder would have: one published version carrying `definition`, current_version
// = 1.
//
// Raw SQL rather than the seeder, because the whole point is to reproduce a state
// today's code no longer produces. createdByType is a parameter because provenance
// is half the pristineness rule: 'system' is ours to revise, 'member' is not.
func seedLegacyBugFixTemplate(t *testing.T, workspaceID pgtype.UUID, createdByType string, definition []byte) (templateID, versionID pgtype.UUID) {
	t.Helper()
	ctx := context.Background()
	if err := testPool.QueryRow(ctx, `
		INSERT INTO workflow_template
		  (workspace_id, key, name, description, status, current_version, created_by_type, created_by_id)
		VALUES ($1, 'bug_fix', 'Bug Fix', 'seeded by an older binary', 'published', 1, $2, gen_random_uuid())
		RETURNING id
	`, workspaceID, createdByType).Scan(&templateID); err != nil {
		t.Fatalf("insert the legacy template: %v", err)
	}
	if err := testPool.QueryRow(ctx, `
		INSERT INTO workflow_template_version
		  (workspace_id, template_id, version, definition, schema_version, status,
		   published_by_type, published_by_id, published_at)
		VALUES ($1, $2, 1, $3::jsonb, 1, 'published', 'system', gen_random_uuid(), now())
		RETURNING id
	`, workspaceID, templateID, definition).Scan(&versionID); err != nil {
		t.Fatalf("insert the legacy version: %v", err)
	}
	return templateID, versionID
}

func storedWorkflowDefinition(t *testing.T, versionID pgtype.UUID) []byte {
	t.Helper()
	var raw []byte
	if err := testPool.QueryRow(context.Background(),
		`SELECT definition FROM workflow_template_version WHERE id = $1`, versionID).Scan(&raw); err != nil {
		t.Fatalf("read the stored definition: %v", err)
	}
	return raw
}

type workflowVersionRow struct {
	version int32
	status  string
}

func readWorkflowVersionRow(t *testing.T, versionID pgtype.UUID) workflowVersionRow {
	t.Helper()
	var out workflowVersionRow
	if err := testPool.QueryRow(context.Background(),
		`SELECT version, status FROM workflow_template_version WHERE id = $1`, versionID).
		Scan(&out.version, &out.status); err != nil {
		t.Fatalf("read the version row: %v", err)
	}
	return out
}

// assertNoTaskRowForStep goes straight at agent_task_queue.
//
// The step response's task_id being null is the API's claim; this is the table's.
// They can disagree: the engine could create a task and fail to link it, which the
// workflow_step_instance_task_only_on_agent CHECK cannot see because it only
// constrains the step row.
func assertNoTaskRowForStep(t *testing.T, stepID string) {
	t.Helper()
	var count int
	if err := testPool.QueryRow(context.Background(),
		`SELECT count(*) FROM agent_task_queue WHERE workflow_step_instance_id = $1`, stepID).Scan(&count); err != nil {
		t.Fatalf("count tasks for step %s: %v", stepID, err)
	}
	if count != 0 {
		t.Errorf("the intake step has %d agent task row(s); an input node must never dispatch work", count)
	}
}

func taskContextBytes(t *testing.T, taskID string) []byte {
	t.Helper()
	var raw []byte
	if err := testPool.QueryRow(context.Background(),
		`SELECT context FROM agent_task_queue WHERE id = $1`, taskID).Scan(&raw); err != nil {
		t.Fatalf("load the task context: %v", err)
	}
	return raw
}

func countWorkflowRuns(t *testing.T, workspaceID string) int {
	t.Helper()
	var n int
	if err := testPool.QueryRow(context.Background(),
		`SELECT count(*) FROM workflow_run WHERE workspace_id = $1`, workspaceID).Scan(&n); err != nil {
		t.Fatalf("count runs: %v", err)
	}
	return n
}

func countWorkspaceIssues(t *testing.T, workspaceID string) int {
	t.Helper()
	var n int
	if err := testPool.QueryRow(context.Background(),
		`SELECT count(*) FROM issue WHERE workspace_id = $1`, workspaceID).Scan(&n); err != nil {
		t.Fatalf("count issues: %v", err)
	}
	return n
}

// workflowStepIndex is the position of a node's step in the trace, or -1.
// Ordering is asserted because it is what makes the trace a history rather than a
// set: an intake step written alongside analyze would say the human's input and
// the agent's work happened at the same moment.
func workflowStepIndex(steps []WorkflowStepResponse, nodeKey string) int {
	for i, s := range steps {
		if s.NodeKey == nodeKey {
			return i
		}
	}
	return -1
}
