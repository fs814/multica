// Integration tests for the input-node contract at RUNTIME.
//
// The unit tests in validate_test.go and task_context_test.go prove the rules and
// the read model. These prove the two things only a real Run can show:
//
//  1. An intake step is written to workflow_step_instance and passes WITHOUT
//     creating an Agent Task, and the Run then advances to the first agent. Both
//     halves matter: no task (an intake node must never dispatch work) and it
//     advances (an entry node that stops is a stalled Run on the happy path).
//  2. A declared field's VALUE crosses every hop from the Run input bag to the
//     agent's rendered prompt. That path runs through activateNode ->
//     buildTaskContext -> agent_task_queue.context -> RenderPrompt, and the
//     recurring defect in this feature's history is a test that asserts on one
//     end of such a chain and is structurally blind to a drop in the middle. So
//     these read the value back off the REAL task row the engine wrote.
//
// The node type is also only executable because migration 251 widened the
// node_type CHECK; a run here would fail its INSERT against an unmigrated
// database, which is the point of testing it against a real one.
package workflow

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// intakeLinearDefinition is linearDefinition entered through a declared input
// node: intake -> analyze -> implement -> end.
//
// A fixture that can express the bug: it declares a field beyond
// title/description (`severity`), because a test whose only declared fields are
// title and description could not tell "declared fields reach the prompt" apart
// from "the freeform pair reaches the prompt", which already worked.
func intakeLinearDefinition() *Definition {
	d := linearDefinition()
	d.EntryNode = "intake"
	d.Nodes = append([]Node{{
		Key:  "intake",
		Type: NodeTypeInput,
		Name: "Bug report",
		Next: []string{"analyze"},
		InputFields: []InputField{
			{Key: "title", Label: "Title", Type: InputFieldText, Required: true},
			{Key: "description", Label: "Bug description", Type: InputFieldTextarea, Required: true},
			{Key: "repro", Label: "Reproduction steps", Type: InputFieldTextarea},
			{Key: "severity", Label: "Severity", Type: InputFieldSelect, Required: true, Options: []string{"low", "high"}},
		},
	}}, d.Nodes...)
	return d
}

// TestInputNodePassesThroughWithoutDispatchingATask is the core execution
// assertion.
//
// If the intake node ever reached dispatchAgentStep it would ask an agent to fill
// in the form the human already filled in - and the DB would refuse the row
// anyway (workflow_step_instance_task_only_on_agent), turning a design mistake
// into a 500 on StartRun. So assert the absence of the task, not just the
// presence of the next step.
func TestInputNodePassesThroughWithoutDispatchingATask(t *testing.T) {
	env := setupTestEnv(t)
	env.publishTemplate(t, intakeLinearDefinition())
	run := env.startRunWithInput(t, "run-intake-passthrough", map[string]any{
		"title":       "Login button does nothing on Safari",
		"description": "Clicking Sign in produces no network request.",
		"severity":    "high",
	})

	if run.Status != string(RunRunning) {
		t.Fatalf("run status = %q, want running", run.Status)
	}

	intake := env.stepByNode(t, run.ID, "intake")
	if intake.NodeType != string(NodeTypeInput) {
		t.Errorf("intake step node_type = %q, want input; the trace must record that intake happened", intake.NodeType)
	}
	if intake.Status != string(StepPassed) {
		t.Fatalf("intake step status = %q, want passed; an input node's work was done by the human before StartRun", intake.Status)
	}
	if intake.TaskID.Valid {
		t.Fatal("the intake step has an Agent Task bound: an input node must never dispatch work")
	}
	if intake.AgentID.Valid {
		t.Error("the intake step was routed to an agent; intake is a human's contribution and must not be routed")
	}
	if intake.RoutingReason.Valid {
		t.Error("the intake step recorded a routing reason, so it went through the router")
	}

	// And it ADVANCED: an entry node that passes without activating its successor
	// would leave a running Run whose only step is already terminal.
	analyze := env.stepByNode(t, run.ID, "analyze")
	if analyze.Status != string(StepQueued) {
		t.Fatalf("analyze step status = %q, want queued; intake must hand the run to the first agent", analyze.Status)
	}
	if !analyze.TaskID.Valid {
		t.Fatal("the first agent step has no Agent Task, so intake advanced without dispatching")
	}
}

// TestDeclaredInputFieldsReachTheFirstAgentsPrompt walks the value end to end.
//
// It reads the brief off the task row the engine actually wrote and renders the
// prompt with the real RenderPrompt, rather than asserting on a struct the test
// built. A hand-rolled mirror of the brief is the exact fixture that let an
// earlier round of this feature "prove" the server sends a field while the
// consumer dropped it.
func TestDeclaredInputFieldsReachTheFirstAgentsPrompt(t *testing.T) {
	env := setupTestEnv(t)
	env.publishTemplate(t, intakeLinearDefinition())
	run := env.startRunWithInput(t, "run-intake-fields", map[string]any{
		"title":       "Login button does nothing on Safari",
		"description": "Clicking Sign in produces no network request.",
		"repro":       "1. open Safari 17\n2. click Sign in",
		"severity":    "high",
		// Undeclared: must NOT reach the prompt. The bag is open by design (the
		// handler writes project_id), so an undeclared key in a prompt would be data
		// the graph never asked for.
		"project_id": "not-a-declared-field",
	})

	step := env.stepByNode(t, run.ID, "analyze")
	tc := env.loadTaskContext(t, env.loadTask(t, step))

	// The freeform pair still works - that path must not regress.
	if tc.RunTitle != "Login button does nothing on Safari" {
		t.Errorf("run_title = %q, want the run's title", tc.RunTitle)
	}
	if !strings.Contains(tc.RunDescription, "no network request") {
		t.Errorf("run_description = %q, want the human's description", tc.RunDescription)
	}

	// The declared extras, with their labels, in declaration order.
	if len(tc.RunFields) != 2 {
		t.Fatalf("run_fields = %+v, want repro and severity (title/description have their own slots)", tc.RunFields)
	}
	byKey := map[string]RunInputValue{}
	for _, f := range tc.RunFields {
		byKey[f.Key] = f
	}
	repro, ok := byKey["repro"]
	if !ok || !strings.Contains(repro.Value, "open Safari 17") {
		t.Fatalf("the repro field did not reach the brief: %+v", tc.RunFields)
	}
	if repro.Label != "Reproduction steps" {
		t.Errorf("repro label = %q; an unlabelled value is one the agent has to guess the meaning of", repro.Label)
	}
	if sev, ok := byKey["severity"]; !ok || sev.Value != "high" {
		t.Errorf("the severity field did not reach the brief: %+v", tc.RunFields)
	}
	if _, leaked := byKey["project_id"]; leaked {
		t.Error("an undeclared bag key reached the agent's brief")
	}

	// The prompt is what the agent actually reads. A brief carrying the field while
	// the renderer drops it is the same defect as never sending it.
	prompt := tc.RenderPrompt()
	for _, want := range []string{
		"**Reproduction steps:** ",
		"open Safari 17",
		"**Severity:** high",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("rendered prompt is missing %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "not-a-declared-field") {
		t.Error("an undeclared bag value reached the rendered prompt")
	}

	// The Step row must agree with the brief: it is the durable audit record of
	// what the Step was asked to do, and an investigation that reads a row
	// disagreeing with the prompt reads the wrong thing.
	var input map[string]any
	if err := json.Unmarshal(step.Input, &input); err != nil {
		t.Fatalf("step input is not JSON: %v", err)
	}
	runInput, ok := input["run_input"].(map[string]any)
	if !ok {
		t.Fatal("step input carries no run_input")
	}
	fields, ok := runInput["fields"].([]any)
	if !ok || len(fields) != 2 {
		t.Fatalf("step input run_input.fields = %v, want the two declared extras", runInput["fields"])
	}
}

// TestDeclaredFieldsSurviveToLaterSteps: the run input is echoed onto EVERY step,
// not just the entry's successor. A second agent that lost the severity would be
// working from less context than the first for no reason the graph states.
func TestDeclaredFieldsSurviveToLaterSteps(t *testing.T) {
	env := setupTestEnv(t)
	env.publishTemplate(t, intakeLinearDefinition())
	run := env.startRunWithInput(t, "run-intake-later-steps", map[string]any{
		"title":       "Widget crashes",
		"description": "on save",
		"severity":    "low",
	})

	analyze := env.stepByNode(t, run.ID, "analyze")
	if _, err := env.engine.SubmitResult(context.Background(), SubmitResultInput{
		WorkspaceID: env.workspaceID,
		StepID:      analyze.ID,
		RawOutput:   passPayload("root cause found"),
		ActorType:   "agent",
	}); err != nil {
		t.Fatalf("SubmitResult analyze: %v", err)
	}

	implement := env.stepByNode(t, run.ID, "implement")
	tc := env.loadTaskContext(t, env.loadTask(t, implement))
	if len(tc.RunFields) != 1 || tc.RunFields[0].Value != "low" {
		t.Fatalf("the second step lost the declared fields: %+v", tc.RunFields)
	}
	if !strings.Contains(tc.RenderPrompt(), "**Severity:** low") {
		t.Error("the declared field did not reach the second agent's prompt")
	}
}

// TestStartRunRejectsMissingRequiredInputField: the rejection must happen BEFORE
// any durable state exists. A Run that starts and then confuses an agent costs an
// attempt and a human interruption; a typed refusal costs the submitter one
// corrected form.
func TestStartRunRejectsMissingRequiredInputField(t *testing.T) {
	env := setupTestEnv(t)
	env.publishTemplate(t, intakeLinearDefinition())
	ctx := context.Background()

	raw, err := json.Marshal(map[string]any{
		"title":       "Login fails",
		"description": "no request",
		// severity is REQUIRED and absent.
	})
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}
	_, err = env.engine.StartRun(ctx, StartRunInput{
		WorkspaceID:       env.workspaceID,
		TemplateID:        env.templateID,
		Source:            "manual",
		IdempotencyKey:    "run-intake-missing-required",
		AccountableUserID: env.userID,
		Input:             raw,
		ActorType:         "member",
		ActorID:           env.userID,
	})
	if err == nil {
		t.Fatal("StartRun accepted a run missing a required declared field")
	}
	engErr, ok := err.(*EngineError)
	if !ok {
		t.Fatalf("expected a typed *EngineError the handler can map to 422, got %T: %v", err, err)
	}
	if engErr.Code != ErrCodeInvalidSubmission {
		t.Errorf("code = %q, want %q", engErr.Code, ErrCodeInvalidSubmission)
	}
	if !strings.Contains(engErr.Message, "Severity") {
		t.Errorf("message = %q, want it to name the missing field by its label", engErr.Message)
	}

	// Nothing durable may be left behind: no Run under the idempotency key, so the
	// submitter can retry the SAME key with a corrected form instead of being told
	// their key was already used.
	if _, err := env.q.GetWorkflowRunByIdempotencyKey(ctx, db.GetWorkflowRunByIdempotencyKeyParams{
		WorkspaceID:    env.workspaceID,
		IdempotencyKey: "run-intake-missing-required",
	}); err == nil {
		t.Fatal("a rejected StartRun left a Run row behind, so the submitter cannot retry the same key")
	} else if !errorsIsNoRows(err) {
		t.Fatalf("lookup after rejection: %v", err)
	}

	// The corrected retry must then work, on the same key.
	fixed, err := json.Marshal(map[string]any{
		"title": "Login fails", "description": "no request", "severity": "high",
	})
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}
	res, err := env.engine.StartRun(ctx, StartRunInput{
		WorkspaceID:       env.workspaceID,
		TemplateID:        env.templateID,
		Source:            "manual",
		IdempotencyKey:    "run-intake-missing-required",
		AccountableUserID: env.userID,
		Input:             fixed,
		ActorType:         "member",
		ActorID:           env.userID,
	})
	if err != nil {
		t.Fatalf("the corrected retry must succeed on the same idempotency key: %v", err)
	}
	if res.AlreadyExisted {
		t.Error("the corrected retry was treated as a replay, so the rejected attempt did leave state")
	}
}

// TestStartRunRejectsOffListSelectValue: an off-list select value is as unusable
// as a missing one - the author enumerated the values downstream steps are written
// against.
func TestStartRunRejectsOffListSelectValue(t *testing.T) {
	env := setupTestEnv(t)
	env.publishTemplate(t, intakeLinearDefinition())

	raw, err := json.Marshal(map[string]any{
		"title": "t", "description": "d", "severity": "catastrophic",
	})
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}
	if _, err := env.engine.StartRun(context.Background(), StartRunInput{
		WorkspaceID:       env.workspaceID,
		TemplateID:        env.templateID,
		Source:            "manual",
		IdempotencyKey:    "run-intake-offlist",
		AccountableUserID: env.userID,
		Input:             raw,
		ActorType:         "member",
		ActorID:           env.userID,
	}); err == nil {
		t.Fatal("StartRun accepted a select value the graph does not declare")
	} else if !strings.Contains(err.Error(), "must be one of") {
		t.Errorf("error = %q, want it to name the permitted values", err.Error())
	}
}

// TestRunWithNoInputNodeIsUnaffected is the backward-compatibility test, and it is
// the one that must never be deleted: every template published before input nodes
// existed is immutable, so this shape is permanent. A run of such a template must
// start with NO field checking (there is nothing declared to check), produce no
// run_fields on the brief, and render the prompt it always did.
func TestRunWithNoInputNodeIsUnaffected(t *testing.T) {
	env := setupTestEnv(t)
	env.publishTemplate(t, linearDefinition())
	run := env.startRunWithInput(t, "run-legacy-no-input-node", map[string]any{
		"title":       "Widget crashes",
		"description": "on save",
		// An extra key the graph never declared. Before input nodes existed the bag
		// was open and this was simply ignored; it must stay ignored rather than
		// becoming a rejection.
		"severity": "high",
	})

	step := env.stepByNode(t, run.ID, "analyze")
	tc := env.loadTaskContext(t, env.loadTask(t, step))
	if len(tc.RunFields) != 0 {
		t.Fatalf("a template with no input node produced run_fields %+v; nothing was declared", tc.RunFields)
	}
	if tc.RunTitle != "Widget crashes" || !strings.Contains(tc.RunDescription, "on save") {
		t.Errorf("the freeform pair regressed: %q / %q", tc.RunTitle, tc.RunDescription)
	}
	if strings.Contains(tc.RenderPrompt(), "high") {
		t.Error("an undeclared bag value reached the prompt of a template with no input node")
	}

	// The step row must carry exactly the two keys it always did.
	var input map[string]any
	if err := json.Unmarshal(step.Input, &input); err != nil {
		t.Fatalf("step input is not JSON: %v", err)
	}
	runInput, ok := input["run_input"].(map[string]any)
	if !ok {
		t.Fatal("step input carries no run_input")
	}
	if _, present := runInput["fields"]; present {
		t.Error("a template with no input node gained a run_input.fields key")
	}
}

// TestRunWithNoDeclaredFieldsStillPassesThrough: an input node with an EMPTY
// declaration is legal (it documents where work enters without collecting
// anything typed), so it must execute the same way and leave the freeform path
// intact.
func TestRunWithNoDeclaredFieldsStillPassesThrough(t *testing.T) {
	env := setupTestEnv(t)
	def := intakeLinearDefinition()
	def.Nodes[0].InputFields = nil
	env.publishTemplate(t, def)

	run := env.startRunWithInput(t, "run-intake-no-fields", map[string]any{
		"title":       "Widget crashes",
		"description": "on save",
	})

	intake := env.stepByNode(t, run.ID, "intake")
	if intake.Status != string(StepPassed) || intake.TaskID.Valid {
		t.Fatalf("intake step = %q with task %v, want passed with no task", intake.Status, intake.TaskID.Valid)
	}
	tc := env.loadTaskContext(t, env.loadTask(t, env.stepByNode(t, run.ID, "analyze")))
	if len(tc.RunFields) != 0 {
		t.Errorf("an empty declaration produced run_fields %+v", tc.RunFields)
	}
	if tc.RunTitle != "Widget crashes" {
		t.Errorf("the freeform pair must still reach the agent, got %q", tc.RunTitle)
	}
}

// TestIntakeRunCompletesThroughEnd: the whole graph still terminates. An entry
// node whose passthrough advanced but whose successor chain was broken would show
// up here and nowhere else.
func TestIntakeRunCompletesThroughEnd(t *testing.T) {
	env := setupTestEnv(t)
	env.publishTemplate(t, intakeLinearDefinition())
	ctx := context.Background()
	run := env.startRunWithInput(t, "run-intake-complete", map[string]any{
		"title": "t", "description": "d", "severity": "low",
	})

	for _, nodeKey := range []string{"analyze", "implement"} {
		step := env.stepByNode(t, run.ID, nodeKey)
		if _, err := env.engine.SubmitResult(ctx, SubmitResultInput{
			WorkspaceID: env.workspaceID,
			StepID:      step.ID,
			RawOutput:   passPayload("done: " + nodeKey),
			ActorType:   "agent",
		}); err != nil {
			t.Fatalf("SubmitResult %s: %v", nodeKey, err)
		}
	}

	final := env.reloadRun(t, run.ID)
	if final.Status != string(RunCompleted) {
		t.Fatalf("run status = %q, want completed; a graph entered through intake must still terminate", final.Status)
	}
	// The intake step remains in the trace as the record that a human supplied the
	// input, which is the whole point of representing it as a node.
	if got := env.stepByNode(t, run.ID, "intake").Status; got != string(StepPassed) {
		t.Errorf("intake step status = %q at completion, want passed", got)
	}
}

// TestIntakeStepEmitsItsActivationEvent: the run trace must show intake. Without
// the event, a reader of the audit log sees a Run that began at `analyze` and has
// no record that a human contributed anything.
func TestIntakeStepEmitsItsActivationEvent(t *testing.T) {
	env := setupTestEnv(t)
	env.publishTemplate(t, intakeLinearDefinition())
	run := env.startRunWithInput(t, "run-intake-events", map[string]any{
		"title": "t", "description": "d", "severity": "high",
	})

	events, err := env.q.ListWorkflowEventsForRun(context.Background(), db.ListWorkflowEventsForRunParams{
		RunID: run.ID, WorkspaceID: env.workspaceID,
	})
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	intake := env.stepByNode(t, run.ID, "intake")
	sawIntakeActivation := false
	for _, e := range events {
		if e.EventType == EventStepActivated && uuidString(e.StepID) == uuidString(intake.ID) {
			sawIntakeActivation = true
			// The payload must name the type, so a metric or a reader can tell an
			// intake activation from an agent one. Decoded rather than substring-
			// matched: the column is JSONB, so Postgres re-serializes it with its own
			// whitespace and key order and a text match would assert on formatting.
			var payload struct {
				NodeKey  string `json:"node_key"`
				NodeType string `json:"node_type"`
			}
			if err := json.Unmarshal(e.Payload, &payload); err != nil {
				t.Fatalf("activation payload is not JSON: %v", err)
			}
			if payload.NodeType != string(NodeTypeInput) {
				t.Errorf("intake activation payload node_type = %q, want input", payload.NodeType)
			}
		}
	}
	if !sawIntakeActivation {
		t.Error("the run trace has no activation event for the intake step")
	}
}

func TestImageInputValidatesAttachmentAndDeliversOnlyASafeReference(t *testing.T) {
	env := setupTestEnv(t)
	var attachmentID string
	if err := env.pool.QueryRow(context.Background(), `
		INSERT INTO attachment (workspace_id, uploader_type, uploader_id, filename, url, content_type, size_bytes)
		VALUES ($1, 'member', gen_random_uuid(), 'report.png', 'https://storage.example.test/signed-secret', 'image/png', 12)
		RETURNING id::text`, env.workspaceID).Scan(&attachmentID); err != nil {
		t.Fatalf("create image attachment: %v", err)
	}
	definition := intakeLinearDefinition()
	definition.Nodes[0].InputMode = InputModeImage
	definition.Nodes[0].ImageAttachmentID = attachmentID
	env.publishTemplate(t, definition)

	run := env.startRunWithInput(t, "run-image-input", map[string]any{
		"title":       "Screenshot regression",
		"description": "Inspect the supplied screenshot.",
		"severity":    "high",
	})
	analyze := env.stepByNode(t, run.ID, "analyze")
	task := env.loadTask(t, analyze)
	brief := env.loadTaskContext(t, task)
	if brief.ImageAttachment == nil || brief.ImageAttachment.ID != attachmentID {
		t.Fatalf("safe image reference = %+v, want id %s", brief.ImageAttachment, attachmentID)
	}
	if brief.ImageAttachment.Filename != "report.png" || brief.ImageAttachment.ContentType != "image/png" {
		t.Errorf("safe image reference = %+v", brief.ImageAttachment)
	}
	prompt := brief.RenderPrompt()
	if !strings.Contains(prompt, "multica attachment download "+attachmentID) {
		t.Fatalf("agent prompt does not tell the agent how to fetch the image: %s", prompt)
	}
	if strings.Contains(string(task.Context), "https://storage.example.test/signed-secret") || strings.Contains(string(analyze.Input), "https://storage.example.test/signed-secret") {
		t.Fatal("task or execution record leaked a storage URL")
	}
}

func TestImageInputRejectsUnavailableAndInvalidAttachmentsBeforeRunCreation(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		size        int64
		missing     bool
		want        string
	}{
		{name: "non-image", contentType: "text/plain", size: 1, want: "JPEG, PNG, WebP, or GIF"},
		{name: "oversize", contentType: "image/png", size: maxWorkflowImageBytes + 1, want: "100 MB"},
		{name: "missing", missing: true, want: "was not found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := setupTestEnv(t)
			attachmentID := "019ec09d-6222-722b-bdfa-427b105d80be"
			if !tt.missing {
				if err := env.pool.QueryRow(context.Background(), `
					INSERT INTO attachment (workspace_id, uploader_type, uploader_id, filename, url, content_type, size_bytes)
					VALUES ($1, 'member', $2, 'payload.bin', 'https://storage.example.test/object', $3, $4)
					RETURNING id::text`, env.workspaceID, env.userID, tt.contentType, tt.size).Scan(&attachmentID); err != nil {
					t.Fatalf("create attachment: %v", err)
				}
			}
			definition := intakeLinearDefinition()
			definition.Nodes[0].InputMode = InputModeImage
			definition.Nodes[0].ImageAttachmentID = attachmentID
			env.publishTemplate(t, definition)
			input, _ := json.Marshal(map[string]any{
				"title": "Bad image", "description": "This must fail.", "severity": "high",
			})
			_, err := env.engine.StartRun(context.Background(), StartRunInput{
				WorkspaceID: env.workspaceID, TemplateID: env.templateID, Source: "manual", IdempotencyKey: "image-invalid-" + tt.name,
				AccountableUserID: env.userID, Input: input, ActorType: "member", ActorID: env.userID,
			})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("StartRun error = %v, want %q", err, tt.want)
			}
			if engineErr, ok := err.(*EngineError); !ok || engineErr.Code != ErrCodeInvalidSubmission {
				t.Fatalf("error = %T %v, want invalid submission", err, err)
			}
		})
	}
}
