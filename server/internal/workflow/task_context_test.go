// Tests for the Agent Task brief a Step activation writes.
//
// This is the delivery mechanism for the fix to the engine's central defect: a
// Step used to enqueue a task carrying only ids, so the claiming agent had no
// work description at all. It then replied in prose, the prose failed the
// submission contract, and the Step blocked - a symptom several hops from its
// cause. Every test here defends a property the agent cannot recover from if it
// is missing.
package workflow

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// loadTaskContext reads back the brief the engine wrote onto a Step's task,
// asserting it is parseable as a workflow brief. An unparseable context is the
// original bug in a different costume, so this fails rather than skips.
func (env *testEnv) loadTaskContext(t *testing.T, taskID db.AgentTaskQueue) TaskContext {
	t.Helper()
	tc, ok := ParseTaskContext(taskID.Context)
	if !ok {
		t.Fatalf("task context is not a workflow brief: %s", string(taskID.Context))
	}
	return tc
}

func (env *testEnv) loadTask(t *testing.T, step db.WorkflowStepInstance) db.AgentTaskQueue {
	t.Helper()
	if !step.TaskID.Valid {
		t.Fatalf("step %s has no task bound", step.NodeKey)
	}
	task, err := env.q.GetAgentTask(context.Background(), step.TaskID)
	if err != nil {
		t.Fatalf("load task for step %s: %v", step.NodeKey, err)
	}
	return task
}

// startRunWithInput starts a Run carrying the freeform input a human supplied in
// the Run dialog. Separate from startRun because most engine tests do not care
// about the input, whereas every brief test does: the run input is the difference
// between "analyze the defect" and an actionable task.
func (env *testEnv) startRunWithInput(t *testing.T, key string, input map[string]any) db.WorkflowRun {
	t.Helper()
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("marshal run input: %v", err)
	}
	res, err := env.engine.StartRun(context.Background(), StartRunInput{
		WorkspaceID:       env.workspaceID,
		TemplateID:        env.templateID,
		Source:            "manual",
		IdempotencyKey:    key,
		AccountableUserID: env.userID,
		Input:             raw,
		ActorType:         "member",
		ActorID:           env.userID,
	})
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	return res.Run
}

// TestActivatedStepTaskCarriesTheWork is the regression test for the central bug.
//
// A dispatched workflow task MUST carry the node instruction and the submission
// contract. Without the instruction the agent has no task; without the contract
// it answers in prose and every step of every run blocks with
// submission_contract_invalid.
func TestActivatedStepTaskCarriesTheWork(t *testing.T) {
	env := setupTestEnv(t)
	def := linearDefinition()
	def.Nodes[0].Name = "Analyze"
	def.Nodes[0].Instruction = "Reproduce the defect and name the root cause."
	env.publishTemplate(t, def)

	run := env.startRunWithInput(t, "run-brief-1", map[string]any{
		"title":       "Login button does nothing on Safari",
		"description": "Clicking Sign in on Safari 17 produces no network request.",
	})

	step := env.stepByNode(t, run.ID, "analyze")
	task := env.loadTask(t, step)
	if len(task.Context) == 0 {
		t.Fatal("the dispatched task has an EMPTY context: the agent would be asked to do nothing")
	}
	tc := env.loadTaskContext(t, task)

	if tc.Instruction != "Reproduce the defect and name the root cause." {
		t.Errorf("instruction = %q, want the node's instruction", tc.Instruction)
	}
	if tc.NodeKey != "analyze" || tc.NodeName != "Analyze" {
		t.Errorf("node = %q/%q, want analyze/Analyze", tc.NodeKey, tc.NodeName)
	}
	if tc.StepInstanceID != uuidString(step.ID) {
		t.Errorf("step_instance_id = %q, want %q; the submission must be able to name its own step",
			tc.StepInstanceID, uuidString(step.ID))
	}
	if tc.RunID != uuidString(run.ID) {
		t.Errorf("run_id = %q, want %q", tc.RunID, uuidString(run.ID))
	}
	if tc.WorkspaceID != uuidString(env.workspaceID) {
		t.Errorf("workspace_id = %q, want %q; the daemon has no other authority for a run with no issue",
			tc.WorkspaceID, uuidString(env.workspaceID))
	}

	// The run input, not just the generic instruction. "Reproduce the defect" is
	// unactionable without knowing which defect.
	if tc.RunTitle != "Login button does nothing on Safari" {
		t.Errorf("run_title = %q, want the run's title", tc.RunTitle)
	}
	if !strings.Contains(tc.RunDescription, "Safari 17") {
		t.Errorf("run_description = %q, want the human's description", tc.RunDescription)
	}

	// The contract. This is the field whose absence breaks every run.
	if tc.SubmissionContract == "" {
		t.Fatal("the brief carries NO submission contract; the agent will reply in prose and the step will block")
	}
	if !strings.Contains(tc.SubmissionContract, submissionOpen) ||
		!strings.Contains(tc.SubmissionContract, submissionClose) {
		t.Error("the contract must show the literal delimiters ExtractDelimitedSubmission searches for")
	}
	if !strings.Contains(tc.SubmissionContract, uuidString(step.ID)) {
		t.Error("the contract must tell the agent which step id to echo; ParseSubmission rejects a mismatch")
	}
}

// TestActivatedStepPromptRoundTripsThroughSubmissionParser is the end-to-end
// proof that the instructions the agent is given actually satisfy the parser
// that judges it.
//
// The contract text and the parser are two pieces of code that must agree. If
// they drift - a renamed field, a changed delimiter - every run blocks and the
// unit tests for each half still pass. So build the example payload the prompt
// shows and feed it to the real ParseSubmission.
func TestActivatedStepPromptRoundTripsThroughSubmissionParser(t *testing.T) {
	env := setupTestEnv(t)
	env.publishTemplate(t, linearDefinition())
	run := env.startRun(t, "run-brief-roundtrip")
	step := env.stepByNode(t, run.ID, "analyze")
	tc := env.loadTaskContext(t, env.loadTask(t, step))

	prompt := tc.RenderPrompt()

	// An agent following the prompt emits a block between the delimiters it shows.
	// Reconstruct exactly that shape with real values and check the parser accepts
	// it.
	answered := submissionOpen + "\n" + `{
  "schema_version": 1,
  "step_instance_id": "` + tc.StepInstanceID + `",
  "verdict": "pass",
  "artifact": {"type": "analysis", "summary": "root cause is a stale event listener"},
  "rationale": "reproduced and bisected",
  "confidence": 0.8
}` + "\n" + submissionClose

	payload, ok := ExtractDelimitedSubmission(answered)
	if !ok {
		t.Fatal("a submission written to the prompt's own format was not extractable")
	}
	sub, problems, err := ParseSubmission([]byte(payload), tc.StepInstanceID)
	if err != nil {
		t.Fatalf("a submission in the format the prompt teaches failed validation: %v (%v)", err, problems)
	}
	if sub.Verdict != VerdictPass {
		t.Errorf("verdict = %q, want pass", sub.Verdict)
	}

	// The prompt must actually contain the work, not just the contract.
	for _, want := range []string{"Workflow step", "How to report your result"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("rendered prompt is missing the %q section", want)
		}
	}
}

// TestDownstreamStepReceivesUpstreamArtifact: the second agent must see what the
// first produced. Without it, `implement` re-derives the analysis - wasting the
// analysis step entirely and, worse, possibly reaching a different conclusion
// than the one the run's audit trail records.
func TestDownstreamStepReceivesUpstreamArtifact(t *testing.T) {
	env := setupTestEnv(t)
	env.publishTemplate(t, linearDefinition())
	run := env.startRun(t, "run-brief-upstream")

	analyze := env.stepByNode(t, run.ID, "analyze")
	if _, err := env.engine.SubmitResult(context.Background(), SubmitResultInput{
		WorkspaceID: env.workspaceID,
		StepID:      analyze.ID,
		RawOutput: submissionOpen + "\n" + `{"verdict":"pass","artifact":{"type":"analysis",` +
			`"summary":"the click handler is bound before hydration","references":["src/login.tsx:42"]},` +
			`"rationale":"bisected"}` + "\n" + submissionClose,
		ActorType: "agent",
	}); err != nil {
		t.Fatalf("SubmitResult: %v", err)
	}

	implement := env.stepByNode(t, run.ID, "implement")
	tc := env.loadTaskContext(t, env.loadTask(t, implement))

	if tc.UpstreamNodeKey != "analyze" {
		t.Errorf("upstream_node_key = %q, want analyze", tc.UpstreamNodeKey)
	}
	if tc.UpstreamVerdict != string(VerdictPass) {
		t.Errorf("upstream_verdict = %q, want pass", tc.UpstreamVerdict)
	}
	if !strings.Contains(tc.UpstreamSummary, "bound before hydration") {
		t.Fatalf("upstream_summary = %q; the implementer cannot act on an analysis it was not given", tc.UpstreamSummary)
	}
	if len(tc.UpstreamReferences) != 1 || tc.UpstreamReferences[0] != "src/login.tsx:42" {
		t.Errorf("upstream_references = %v, want the analysis's references", tc.UpstreamReferences)
	}

	// And it must survive into the rendered prompt, which is what the agent reads.
	if !strings.Contains(tc.RenderPrompt(), "bound before hydration") {
		t.Error("the upstream summary did not reach the rendered prompt")
	}
}

// TestReworkAttemptTaskCarriesTheRejectionReason: an agent re-running a node
// without being told what was rejected will reproduce the rejected work, burn the
// next attempt, and eventually exhaust the node's budget - turning a recoverable
// rework into a blocked run.
func TestReworkAttemptTaskCarriesTheRejectionReason(t *testing.T) {
	env := setupTestEnv(t)
	def := linearDefinition()
	// implement fails back to analyze.
	def.Nodes[1].OnFailure = FailurePolicyRework
	def.Nodes[1].ReworkTargets = []string{"analyze"}
	env.publishTemplate(t, def)
	run := env.startRun(t, "run-brief-rework")
	ctx := context.Background()

	analyze := env.stepByNode(t, run.ID, "analyze")
	if _, err := env.engine.SubmitResult(ctx, SubmitResultInput{
		WorkspaceID: env.workspaceID,
		StepID:      analyze.ID,
		RawOutput:   passPayload("first analysis"),
		ActorType:   "agent",
	}); err != nil {
		t.Fatalf("SubmitResult analyze: %v", err)
	}

	implement := env.stepByNode(t, run.ID, "implement")
	if _, err := env.engine.SubmitResult(ctx, SubmitResultInput{
		WorkspaceID: env.workspaceID,
		StepID:      implement.ID,
		RawOutput: submissionOpen + "\n" + `{"verdict":"fail","artifact":{"type":"code_change","summary":""},` +
			`"rationale":"the named root cause does not reproduce","root_cause":"analysis was wrong"}` +
			"\n" + submissionClose,
		ActorType: "agent",
	}); err != nil {
		t.Fatalf("SubmitResult implement: %v", err)
	}

	retry := env.stepByNode(t, run.ID, "analyze")
	if retry.Attempt != 2 {
		t.Fatalf("expected a second analyze attempt, got attempt %d", retry.Attempt)
	}
	tc := env.loadTaskContext(t, env.loadTask(t, retry))

	if tc.Attempt != 2 {
		t.Errorf("attempt = %d, want 2; the agent must know it is retrying", tc.Attempt)
	}
	if tc.ReworkFromNode != "implement" {
		t.Errorf("rework_from_node = %q, want implement", tc.ReworkFromNode)
	}
	if tc.ReworkReason == "" {
		t.Fatal("the rework attempt carries no reason; the agent will reproduce the rejected work")
	}
	prompt := tc.RenderPrompt()
	if !strings.Contains(prompt, "running this step again") {
		t.Error("the rendered prompt does not tell the agent why it is running again")
	}
	if !strings.Contains(prompt, "attempt 2") {
		t.Error("the rendered prompt does not surface the attempt number")
	}
}

// TestAcceptanceCriteriaReachTheAgent: an agent that does not know what the
// reviewer will check optimises for the wrong thing, and the rejection that
// follows costs a whole rework round.
func TestAcceptanceCriteriaReachTheAgent(t *testing.T) {
	env := setupTestEnv(t)
	def := linearDefinition()
	def.Nodes[0].AcceptanceCriteria = []string{"root cause named", "reproduction steps included"}
	env.publishTemplate(t, def)
	run := env.startRun(t, "run-brief-criteria")

	step := env.stepByNode(t, run.ID, "analyze")
	tc := env.loadTaskContext(t, env.loadTask(t, step))
	if len(tc.AcceptanceCriteria) != 2 {
		t.Fatalf("acceptance_criteria = %v, want both criteria", tc.AcceptanceCriteria)
	}
	if !strings.Contains(tc.RenderPrompt(), "reproduction steps included") {
		t.Error("the criteria did not reach the rendered prompt")
	}
}

// TestStepInputMirrorsTheBrief: the Step row is the durable audit record of what
// the Step was asked to do. If it disagrees with what the agent was actually
// sent, an investigation into a bad result reads the wrong thing.
func TestStepInputMirrorsTheBrief(t *testing.T) {
	env := setupTestEnv(t)
	env.publishTemplate(t, linearDefinition())
	run := env.startRunWithInput(t, "run-brief-mirror", map[string]any{
		"title":       "Widget crashes",
		"description": "on save",
	})

	step := env.stepByNode(t, run.ID, "analyze")
	var input map[string]any
	if err := json.Unmarshal(step.Input, &input); err != nil {
		t.Fatalf("step input is not JSON: %v", err)
	}
	runInput, ok := input["run_input"].(map[string]any)
	if !ok {
		t.Fatal("step input carries no run_input; the row does not record what the agent was told")
	}
	if runInput["title"] != "Widget crashes" {
		t.Errorf("step input run_input.title = %v, want the run's title", runInput["title"])
	}

	tc := env.loadTaskContext(t, env.loadTask(t, step))
	if tc.RunTitle != "Widget crashes" {
		t.Errorf("brief and step input disagree: brief title = %q", tc.RunTitle)
	}
}

// TestTaskContextIsNotMistakenForQuickCreate: the two "job description lives in
// context" paths share a column, so the discriminator has to actually
// discriminate. A workflow brief read as a quick-create prompt would make the
// agent file an issue instead of doing the step.
func TestTaskContextIsNotMistakenForQuickCreate(t *testing.T) {
	env := setupTestEnv(t)
	env.publishTemplate(t, linearDefinition())
	run := env.startRun(t, "run-brief-discriminator")
	task := env.loadTask(t, env.stepByNode(t, run.ID, "analyze"))

	var probe struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(task.Context, &probe); err != nil {
		t.Fatalf("context is not JSON: %v", err)
	}
	if probe.Type != TaskContextType {
		t.Fatalf("context type = %q, want %q", probe.Type, TaskContextType)
	}
	if probe.Type == "quick_create" {
		t.Fatal("a workflow brief must not be readable as a quick-create prompt")
	}

	// And the reverse: a quick-create context must not parse as a workflow brief.
	if _, ok := ParseTaskContext([]byte(`{"type":"quick_create","prompt":"file a bug"}`)); ok {
		t.Fatal("a quick-create context was accepted as a workflow brief")
	}
	if _, ok := ParseTaskContext(nil); ok {
		t.Fatal("an empty context was accepted as a workflow brief")
	}
	if _, ok := ParseTaskContext([]byte(`{"head_sha":"abc123"}`)); ok {
		t.Fatal("a head_sha context was accepted as a workflow brief")
	}
}

// TestWorkflowTaskIsLinkedAtInsert: the task must never be observable as an
// unlinked, promptless row. It is created with workflow_step_instance_id already
// set, in the same statement, so there is no window in which a claim could see a
// workflow task that does not know which Step it serves.
func TestWorkflowTaskIsLinkedAtInsert(t *testing.T) {
	env := setupTestEnv(t)
	env.publishTemplate(t, linearDefinition())
	run := env.startRun(t, "run-brief-linked")
	step := env.stepByNode(t, run.ID, "analyze")
	task := env.loadTask(t, step)

	if uuidString(task.WorkflowStepInstanceID) != uuidString(step.ID) {
		t.Fatalf("task links to step %s, want %s",
			uuidString(task.WorkflowStepInstanceID), uuidString(step.ID))
	}
	// The reverse link too: together they are the one-active-task fence.
	if uuidString(step.TaskID) != uuidString(task.ID) {
		t.Fatalf("step links to task %s, want %s", uuidString(step.TaskID), uuidString(task.ID))
	}
	// Attribution: originator and accountable must agree, or the row violates
	// agent_task_queue_accountable_matches_originator.
	if uuidString(task.OriginatorUserID) != uuidString(task.AccountableUserID) {
		t.Errorf("originator %s != accountable %s",
			uuidString(task.OriginatorUserID), uuidString(task.AccountableUserID))
	}
	if uuidString(task.AccountableUserID) != uuidString(env.userID) {
		t.Errorf("accountable = %s, want the run's accountable user %s",
			uuidString(task.AccountableUserID), uuidString(env.userID))
	}
	if task.TriggerEvidenceKind.String != "workflow_step" {
		t.Errorf("trigger_evidence_kind = %q, want workflow_step so an isolated task is traceable",
			task.TriggerEvidenceKind.String)
	}
}

// TestRenderPromptWithoutInstructionTellsTheAgentToBlock: a graph with no
// instruction on an Agent node cannot pass Validate, so this is unreachable in
// production - but if it ever happens the agent must be told to report `blocked`
// rather than left to invent a task. Inventing one produces confident wrong work.
func TestRenderPromptWithoutInstructionTellsTheAgentToBlock(t *testing.T) {
	tc := TaskContext{
		Type:               TaskContextType,
		NodeKey:            "mystery",
		StepInstanceID:     "11111111-1111-1111-1111-111111111111",
		SubmissionContract: SubmissionContractInstructions("11111111-1111-1111-1111-111111111111"),
	}
	prompt := tc.RenderPrompt()
	if !strings.Contains(prompt, "blocked") {
		t.Error("a brief with no instruction must direct the agent to report blocked")
	}
}

// TestSubmissionContractRefusesProseAsPass restates the invariant the contract
// exists to protect, from the parser's side: whatever an agent says in prose,
// only a valid block is a verdict. If this ever stops holding, the contract text
// in the prompt is pointless.
func TestSubmissionContractRefusesProseAsPass(t *testing.T) {
	if _, ok := ExtractDelimitedSubmission(
		"I fixed the bug and all the tests pass. Everything looks good!"); ok {
		t.Fatal("prose was extracted as a submission; a confident sentence is not a verdict")
	}
}

// TestParseRunInputToleratesGarbage: the run input is advisory prompt context.
// Refusing to dispatch a Step because an optional description did not parse would
// turn a cosmetic problem into a blocked run.
func TestParseRunInputToleratesGarbage(t *testing.T) {
	for _, raw := range []string{"", "{}", "not json", `{"title":123}`, `[]`} {
		got := ParseRunInput([]byte(raw))
		if got.Title != "" && raw != `{"title":123}` {
			t.Errorf("ParseRunInput(%q) = %+v, want zero value", raw, got)
		}
	}
	got := ParseRunInput([]byte(`{"title":"t","description":"d","future_typed_field":"ignored"}`))
	if got.Title != "t" || got.Description != "d" {
		t.Errorf("ParseRunInput = %+v, want title/description read", got)
	}
}

// ---------------------------------------------------------------------------
// Declared input fields
// ---------------------------------------------------------------------------

// intakeNode is the declaration an input node carries. Built here rather than
// reused from the validator's fixture because these tests are about the read and
// check paths, which take a *Node and never see a whole graph.
func intakeNode() *Node {
	return &Node{
		Key:  "intake",
		Type: NodeTypeInput,
		Next: []string{"analyze"},
		InputFields: []InputField{
			{Key: "title", Label: "Title", Type: InputFieldText, Required: true},
			{Key: "description", Label: "Bug description", Type: InputFieldTextarea, Required: true},
			{Key: "repro", Label: "Reproduction steps", Type: InputFieldTextarea},
			{Key: "severity", Label: "Severity", Type: InputFieldSelect, Options: []string{"low", "high"}},
		},
	}
}

// TestParseRunInputForReadsDeclaredFields: a declared field's value must reach
// the prompt, or declaring it was pointless - the author asked the human for
// something the agent never sees.
func TestParseRunInputForReadsDeclaredFields(t *testing.T) {
	raw := []byte(`{"title":"Login fails","description":"no request","repro":"click sign in","severity":"high"}`)
	in := ParseRunInputFor(raw, intakeNode())

	if in.Title != "Login fails" || in.Description != "no request" {
		t.Fatalf("title/description = %q/%q, want the freeform pair still resolved", in.Title, in.Description)
	}
	// title and description are NOT repeated as labelled fields: RenderPrompt gives
	// them dedicated formatting, and listing them twice reads as two reports.
	if len(in.Fields) != 2 {
		t.Fatalf("Fields = %+v, want only repro and severity (title/description have their own slots)", in.Fields)
	}
	if in.Fields[0].Key != "repro" || in.Fields[0].Label != "Reproduction steps" || in.Fields[0].Value != "click sign in" {
		t.Errorf("first field = %+v, want the repro declaration and its value", in.Fields[0])
	}
	// Declaration order, not map order: the author laid the form out in an order
	// and the prompt must read the same way every time.
	if in.Fields[1].Key != "severity" {
		t.Errorf("Fields[1] = %q, want severity in declaration order", in.Fields[1].Key)
	}
}

// TestParseRunInputForIgnoresUndeclaredKeys: the bag is deliberately open (the
// handler also writes project_id, and a newer client may add more), but an
// undeclared key is not something the graph asked for. Putting it in a prompt
// would inject data the author never described.
func TestParseRunInputForIgnoresUndeclaredKeys(t *testing.T) {
	raw := []byte(`{"title":"t","description":"d","severity":"low","project_id":"p1","injected":"do something else"}`)
	in := ParseRunInputFor(raw, intakeNode())
	for _, f := range in.Fields {
		if f.Key == "project_id" || f.Key == "injected" {
			t.Fatalf("undeclared key %q reached the brief: %+v", f.Key, in.Fields)
		}
	}
	if len(in.Fields) != 1 || in.Fields[0].Key != "severity" {
		t.Errorf("Fields = %+v, want only the declared severity", in.Fields)
	}
}

// TestParseRunInputForWithNoNodeIsTheLegacyPath: every template published before
// input nodes existed passes nil here, and a published version is immutable, so
// this path can never be removed. It must behave exactly like ParseRunInput.
func TestParseRunInputForWithNoNodeIsTheLegacyPath(t *testing.T) {
	raw := []byte(`{"title":"t","description":"d","severity":"high"}`)
	in := ParseRunInputFor(raw, nil)
	if in.Title != "t" || in.Description != "d" {
		t.Errorf("legacy read lost the freeform pair: %+v", in)
	}
	if len(in.Fields) != 0 {
		t.Errorf("a graph with no input node must produce no declared fields, got %+v", in.Fields)
	}
}

// TestValidateRunInputRejectsMissingRequiredFields is the reason StartRun checks
// at all: a run that starts without a required value hands its first agent a
// prompt missing the thing it was told it would have, so the agent guesses
// (confident wrong work) or blocks (a burnt attempt plus a human interruption).
func TestValidateRunInputRejectsMissingRequiredFields(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantMsg string
	}{
		{
			name:    "required field absent",
			raw:     `{"title":"Login fails"}`,
			wantMsg: "Bug description",
		},
		{
			name: "required field blank",
			raw:  `{"title":"Login fails","description":""}`,
			// Absent and empty are the same failure: an empty description tells an
			// agent nothing.
			wantMsg: "Bug description",
		},
		{
			name: "required field is whitespace only",
			raw:  `{"title":"Login fails","description":"   \n  "}`,
			// A description of " " satisfies a naive presence check and is still
			// useless, which is why the check trims.
			wantMsg: "Bug description",
		},
		{
			name:    "required field is not a string",
			raw:     `{"title":"Login fails","description":42}`,
			wantMsg: "Bug description",
		},
		{
			name: "select value outside the declared options",
			raw:  `{"title":"t","description":"d","severity":"catastrophic"}`,
			// The author enumerated the values downstream steps are written against,
			// so an off-list value is one nothing knows how to act on.
			wantMsg: "must be one of low, high",
		},
		{
			name:    "input is not an object",
			raw:     `[]`,
			wantMsg: "not a JSON object",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateRunInput([]byte(tt.raw), intakeNode())
			if err == nil {
				t.Fatalf("expected %q to be rejected, but it was accepted", tt.raw)
			}
			if !strings.Contains(err.Error(), tt.wantMsg) {
				t.Fatalf("error = %q, want it to mention %q", err.Error(), tt.wantMsg)
			}
			// The code is what the handler maps to a status: a 422 saying "your
			// values are wrong", not a 500 and not "this template is broken".
			engErr, ok := err.(*EngineError)
			if !ok {
				t.Fatalf("expected *EngineError so the handler can map a status, got %T", err)
			}
			if engErr.Code != ErrCodeInvalidSubmission {
				t.Errorf("code = %q, want %q", engErr.Code, ErrCodeInvalidSubmission)
			}
		})
	}
}

// TestValidateRunInputAcceptsSatisfiedAndLegacyInput covers both directions of
// the fallback: a complete form passes, an optional field may be omitted, and a
// template with NO input node accepts whatever it always did.
func TestValidateRunInputAcceptsSatisfiedAndLegacyInput(t *testing.T) {
	// repro is optional, so omitting it is fine.
	if err := ValidateRunInput([]byte(`{"title":"t","description":"d","severity":"low"}`), intakeNode()); err != nil {
		t.Fatalf("a satisfied form must be accepted, got: %v", err)
	}
	// severity is optional too.
	if err := ValidateRunInput([]byte(`{"title":"t","description":"d"}`), intakeNode()); err != nil {
		t.Fatalf("omitting every optional field must be accepted, got: %v", err)
	}
	// The legacy path: nil node means nothing is declared, so nothing can be
	// missing. This must hold for the empty bag too - most existing runs.
	for _, raw := range []string{"", "{}", `{"title":"t"}`, "not json"} {
		if err := ValidateRunInput([]byte(raw), nil); err != nil {
			t.Errorf("a template with no input node must accept %q, got: %v", raw, err)
		}
	}
	// An input node with an empty declaration is legal, so it also cannot reject.
	empty := &Node{Key: "intake", Type: NodeTypeInput}
	if err := ValidateRunInput([]byte(`{}`), empty); err != nil {
		t.Errorf("an input node with no declared fields must accept any input, got: %v", err)
	}
}

// TestRenderPromptWithoutRunFieldsIsByteIdentical is the backward-compatibility
// assertion for the prompt itself.
//
// The section that renders declared fields lives inside the existing "What this
// run is about" block, so a bug there could add a stray blank line or heading to
// EVERY brief - including the millions belonging to templates that have no input
// node and, because published versions are immutable, never will. Compare the two
// renders byte for byte rather than asserting on substrings: a substring check
// would not notice added whitespace, and whitespace is what changes when a writer
// loop is added in the wrong place.
func TestRenderPromptWithoutRunFieldsIsByteIdentical(t *testing.T) {
	base := TaskContext{
		Type:               TaskContextType,
		NodeKey:            "analyze",
		NodeName:           "Analyze",
		StepInstanceID:     "11111111-1111-1111-1111-111111111111",
		Instruction:        "Reproduce the defect.",
		RunTitle:           "Login fails",
		RunDescription:     "No network request on submit.",
		SubmissionContract: SubmissionContractInstructions("11111111-1111-1111-1111-111111111111"),
	}
	// A nil RunFields and an explicitly-empty one must both render the legacy text:
	// the engine produces nil (truncateRunFields returns nil for empty), but a
	// hand-built or JSON-decoded context could carry `[]`.
	withNil := base
	withEmpty := base
	withEmpty.RunFields = []RunInputValue{}

	if got, want := withEmpty.RenderPrompt(), withNil.RenderPrompt(); got != want {
		t.Fatalf("an empty RunFields changed the prompt:\n--- with []\n%s\n--- with nil\n%s", got, want)
	}
	// And the section itself must be intact and unchanged in shape.
	prompt := withNil.RenderPrompt()
	if !strings.Contains(prompt, "**Login fails**\n\nNo network request on submit.\n\n") {
		t.Errorf("the legacy run-input section changed shape:\n%s", prompt)
	}
}

// TestRenderPromptLabelsDeclaredFields: the value has to be labelled. A bare
// string under a raw key is a value the agent has to guess the meaning of, which
// is the same defect as sending no value at all - it just fails less visibly.
func TestRenderPromptLabelsDeclaredFields(t *testing.T) {
	tc := TaskContext{
		Type:               TaskContextType,
		NodeKey:            "analyze",
		StepInstanceID:     "11111111-1111-1111-1111-111111111111",
		Instruction:        "Reproduce the defect.",
		RunTitle:           "Login fails",
		RunDescription:     "No network request.",
		SubmissionContract: SubmissionContractInstructions("11111111-1111-1111-1111-111111111111"),
		RunFields: []RunInputValue{
			{Key: "repro", Label: "Reproduction steps", Value: "1. click sign in"},
			{Key: "severity", Label: "Severity", Value: "high"},
		},
	}
	prompt := tc.RenderPrompt()
	for _, want := range []string{
		"**Reproduction steps:** 1. click sign in",
		"**Severity:** high",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt is missing %q:\n%s", want, prompt)
		}
	}
	// Inside the run-input section, not appended after the submission contract:
	// the contract must be the last thing in view when the agent writes its
	// answer.
	if strings.Index(prompt, "**Severity:** high") > strings.Index(prompt, "How to report your result") {
		t.Error("declared fields must render before the reporting contract, not after it")
	}
}

// TestRenderPromptShowsFieldsEvenWithNoFreeformInput: a workflow whose intake
// declares only typed fields (no title/description at all) must still get a "what
// this run is about" section. Gating that section on the freeform pair alone would
// silently drop every declared value for such a template.
func TestRenderPromptShowsFieldsEvenWithNoFreeformInput(t *testing.T) {
	tc := TaskContext{
		Type:               TaskContextType,
		NodeKey:            "analyze",
		StepInstanceID:     "11111111-1111-1111-1111-111111111111",
		Instruction:        "Do the thing.",
		SubmissionContract: SubmissionContractInstructions("11111111-1111-1111-1111-111111111111"),
		RunFields: []RunInputValue{
			{Key: "target", Label: "Target service", Value: "checkout-api"},
		},
	}
	prompt := tc.RenderPrompt()
	if !strings.Contains(prompt, "What this run is about") {
		t.Fatalf("a run with only declared fields lost its run-input section:\n%s", prompt)
	}
	if !strings.Contains(prompt, "**Target service:** checkout-api") {
		t.Errorf("the declared value did not reach the prompt:\n%s", prompt)
	}
}

// TestRunFieldsAreNotSerializedBackIntoTheInputBag: RunInput.Fields is a READ
// model. The handler marshals a RunInput to build workflow_run.input, so emitting
// a nested `fields` array would write a second copy of values that already live at
// the top level of the flat bag - and two copies are free to disagree.
func TestRunFieldsAreNotSerializedBackIntoTheInputBag(t *testing.T) {
	in := ParseRunInputFor([]byte(`{"title":"t","description":"d","severity":"high"}`), intakeNode())
	if len(in.Fields) == 0 {
		t.Fatal("fixture did not populate Fields, so this test cannot fail for its reason")
	}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), "severity") || strings.Contains(string(raw), "fields") {
		t.Fatalf("RunInput must not serialize its read model back into the bag; got: %s", raw)
	}
}

func TestRenderPromptUsesSafeImageDownloadReference(t *testing.T) {
	tc := TaskContext{
		Type:        TaskContextType,
		NodeKey:     "analyze",
		Instruction: "Inspect the submitted image.",
		ImageAttachment: &ImageAttachmentRef{
			ID:          "11111111-1111-1111-1111-111111111111",
			Filename:    "report.png",
			ContentType: "image/png",
		},
	}
	prompt := tc.RenderPrompt()
	if !strings.Contains(prompt, "multica attachment download 11111111-1111-1111-1111-111111111111") {
		t.Fatalf("image prompt is missing the authorized download command: %s", prompt)
	}
	for _, forbidden := range []string{"https://", "data:image", "base64"} {
		if strings.Contains(prompt, forbidden) {
			t.Errorf("image prompt leaked %q: %s", forbidden, prompt)
		}
	}
}
