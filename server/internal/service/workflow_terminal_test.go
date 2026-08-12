package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/workflow"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// Tests for the TaskService -> workflow observer seam.
//
// The seam's whole value is conditional behaviour: a workflow task must advance
// its Run, and a non-workflow task must cost nothing. Getting the second half
// wrong is the expensive mistake - every issue, chat, autopilot, and quick-create
// completion in the system flows through these call sites, so a hook that fires
// for all of them adds a query per completion forever.
//
// These run without a database: the fixture is a fake observer and hand-built
// task rows, because what is under test is the guard, not the engine behind it.

// recordingObserver captures the calls the seam makes.
type recordingObserver struct {
	calls []observedTerminal
	err   error
}

type observedTerminal struct {
	taskID        pgtype.UUID
	taskStatus    string
	result        string
	failureReason string
	errorDetail   string
}

func (o *recordingObserver) OnAgentTaskTerminal(_ context.Context, taskID pgtype.UUID, taskStatus, result, failureReason, errorDetail string) error {
	o.calls = append(o.calls, observedTerminal{
		taskID:        taskID,
		taskStatus:    taskStatus,
		result:        result,
		failureReason: failureReason,
		errorDetail:   errorDetail,
	})
	return o.err
}

func uuidWithByte(b byte) pgtype.UUID {
	var raw [16]byte
	raw[15] = b
	return pgtype.UUID{Bytes: raw, Valid: true}
}

// TestWorkflowTerminalObserverSkipsNonWorkflowTask is the important one: the
// hook must be free for the overwhelming majority of tasks.
//
// The guard is workflow_step_instance_id being NULL, checked before any query. If
// this regresses, every task completion in the system pays a lookup - and a
// GetAgent call - to discover it is not a workflow task.
func TestWorkflowTerminalObserverSkipsNonWorkflowTask(t *testing.T) {
	obs := &recordingObserver{}
	// Deliberately NO Queries: if the seam tries to resolve a workspace for a
	// non-workflow task it will nil-panic, which is exactly the regression this
	// test should catch rather than let pass quietly.
	svc := &TaskService{WorkflowTerminal: obs}

	svc.notifyWorkflowTaskTerminal(context.Background(), db.AgentTaskQueue{
		ID:      uuidWithByte(1),
		IssueID: uuidWithByte(2),
	}, "completed", "some output", "", "")

	if len(obs.calls) != 0 {
		t.Fatalf("observer was called %d times for a non-workflow task; it must be a no-op", len(obs.calls))
	}
}

// TestWorkflowTerminalObserverNilIsNoOp: a deployment that has not wired the
// engine must behave exactly as it did before the seam existed - including not
// panicking.
func TestWorkflowTerminalObserverNilIsNoOp(t *testing.T) {
	svc := &TaskService{}
	svc.notifyWorkflowTaskTerminal(context.Background(), db.AgentTaskQueue{
		ID:                     uuidWithByte(1),
		WorkflowStepInstanceID: uuidWithByte(9),
	}, "completed", "", "", "")
	// Reaching here without a panic is the assertion.
}

// TestWorkflowTerminalObserverSwallowsError: the Task is already terminal and
// committed. Propagating a workflow-side error would report a task that genuinely
// finished as failed, which is a worse lie than a Run that stalls visibly.
func TestWorkflowTerminalObserverSwallowsError(t *testing.T) {
	obs := &recordingObserver{err: errors.New("engine exploded")}
	svc := &TaskService{WorkflowTerminal: obs}

	svc.notifyWorkflowTaskTerminal(context.Background(), db.AgentTaskQueue{
		ID:                     uuidWithByte(1),
		WorkflowStepInstanceID: uuidWithByte(9),
	}, "failed", "", "agent_error", "boom")

	if len(obs.calls) != 1 {
		t.Fatalf("observer calls = %d, want 1", len(obs.calls))
	}
	// No panic, no propagation. The failure is logged and the caller continues.
}

// TestWorkflowTerminalObserverForwardsTerminalDetail: the engine's decision
// depends on every field. A failure that arrives with no reason lands as a
// generic agent_task_failed instead of the taskfailure classification, and a
// completion that arrives with no result cannot become a Submission.
func TestWorkflowTerminalObserverForwardsTerminalDetail(t *testing.T) {
	obs := &recordingObserver{}
	svc := &TaskService{WorkflowTerminal: obs}
	task := db.AgentTaskQueue{
		ID:                     uuidWithByte(7),
		WorkflowStepInstanceID: uuidWithByte(9),
	}

	svc.notifyWorkflowTaskTerminal(context.Background(), task, "completed", "the agent output", "", "")
	svc.notifyWorkflowTaskTerminal(context.Background(), task, "failed", "", "timeout", "no heartbeat")
	svc.notifyWorkflowTaskTerminal(context.Background(), task, "cancelled", "", "", "")

	if len(obs.calls) != 3 {
		t.Fatalf("observer calls = %d, want 3", len(obs.calls))
	}
	if obs.calls[0].taskStatus != "completed" || obs.calls[0].result != "the agent output" {
		t.Errorf("completion call = %+v; the result must reach the engine to become a submission", obs.calls[0])
	}
	if obs.calls[1].failureReason != "timeout" || obs.calls[1].errorDetail != "no heartbeat" {
		t.Errorf("failure call = %+v; the classification must survive the hop", obs.calls[1])
	}
	if obs.calls[2].taskStatus != "cancelled" {
		t.Errorf("cancel call = %+v, want status cancelled", obs.calls[2])
	}
	for i, c := range obs.calls {
		if c.taskID != task.ID {
			t.Errorf("call %d carried task %v, want %v", i, c.taskID, task.ID)
		}
	}
}

// TestEngineSatisfiesTerminalObserver is the compile-time link between the two
// halves of the seam.
//
// The interface is declared in package service and satisfied structurally by
// *workflow.Engine, which means nothing forces them to agree. Without this
// assertion a signature change on either side would only fail in
// cmd/server/router.go - far from the change - and the symptom would be "workflow
// runs stopped advancing", not a type error.
func TestEngineSatisfiesTerminalObserver(t *testing.T) {
	var _ WorkflowTaskTerminalObserver = (*workflow.Engine)(nil)
}

// TestWorkflowNotifierToleratesMissingCollaborators: the notifier's two
// dependencies are both optional, and neither absence may panic. The engine calls
// these after commit, so a nil-panic here would surface as a crashed request on
// an operation that already succeeded.
func TestWorkflowNotifierToleratesMissingCollaborators(t *testing.T) {
	n := NewWorkflowNotifier(nil, nil)
	n.WorkflowChanged(context.Background(), "ws", "run")
	n.IssueChanged(context.Background(), db.Issue{}, "todo")
	n.TaskEnqueued(context.Background(), db.AgentTaskQueue{ID: uuidWithByte(1)})

	// A notifier with no workspace id must also not publish: the bus routes by
	// workspace, so an empty one would be a message nobody receives on a topic
	// nobody subscribes to.
	n2 := NewWorkflowNotifier(nil, &TaskService{})
	n2.WorkflowChanged(context.Background(), "", "run")
}

func TestWorkflowNotifierPublishesIssueStatusProjection(t *testing.T) {
	bus := events.New()
	var got []events.Event
	bus.SubscribeAll(func(e events.Event) { got = append(got, e) })
	n := NewWorkflowNotifier(bus, nil)
	issue := db.Issue{
		ID:          uuidWithByte(1),
		WorkspaceID: uuidWithByte(2),
		Status:      "done",
	}

	n.IssueChanged(context.Background(), issue, "in_progress")

	if len(got) != 1 {
		t.Fatalf("published events = %d, want 1", len(got))
	}
	if got[0].Type != protocol.EventIssueUpdated {
		t.Fatalf("event type = %q, want %q", got[0].Type, protocol.EventIssueUpdated)
	}
	payload, ok := got[0].Payload.(map[string]any)
	if !ok {
		t.Fatalf("payload type = %T, want map[string]any", got[0].Payload)
	}
	if payload["status_changed"] != true || payload["prev_status"] != "in_progress" {
		t.Fatalf("status payload = %#v", payload)
	}
	issuePayload, ok := payload["issue"].(map[string]any)
	if !ok || issuePayload["status"] != "done" {
		t.Fatalf("issue payload = %#v, want status done", payload["issue"])
	}
}

// TestNotifierSatisfiesEngineSeam mirrors TestEngineSatisfiesTerminalObserver for
// the other direction of the wiring.
func TestNotifierSatisfiesEngineSeam(t *testing.T) {
	var _ workflow.Notifier = (*WorkflowNotifier)(nil)
}

// TestWorkflowAgentOutputUnwrapsTheEnvelope is the regression test for a bug that
// would have blocked EVERY step of EVERY run while looking like an agent problem.
//
// agent_task_queue.result stores a protocol.TaskCompletedPayload envelope, and
// encoding/json escapes `<` as `<` by default. So the raw blob does NOT
// contain the literal `<<<MULTICA_SUBMISSION>>>` delimiters even when the agent
// emitted them perfectly: ExtractDelimitedSubmission finds no block, the step
// blocks with submission_contract_invalid, and the evidence stored for the human
// is a wall of `<` escapes. Handing the engine `string(result)` is therefore
// a total failure that no unit test of either half would catch.
func TestWorkflowAgentOutputUnwrapsTheEnvelope(t *testing.T) {
	const submitted = "Here is my analysis.\n" +
		"<<<MULTICA_SUBMISSION>>>\n" +
		`{"verdict":"pass","artifact":{"type":"analysis","summary":"root cause found"}}` + "\n" +
		"<<<END_MULTICA_SUBMISSION>>>"

	envelope, err := json.Marshal(protocol.TaskCompletedPayload{
		TaskID: "11111111-1111-1111-1111-111111111111",
		Output: submitted,
	})
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}

	// Establish the hazard is real rather than hypothetical: the stored blob does
	// not contain the delimiter.
	if strings.Contains(string(envelope), "<<<MULTICA_SUBMISSION>>>") {
		t.Fatal("precondition failed: the envelope was expected to escape the delimiters")
	}

	got := workflowAgentOutput(envelope)
	if _, ok := workflow.ExtractDelimitedSubmission(got); !ok {
		t.Fatalf("the unwrapped output has no extractable submission block: %q", got)
	}

	// The output must come back EXACTLY as the agent wrote it. This is the second
	// half of the bug: the unwrapper used to run util.UnescapeBackslashEscapes over
	// the decoded string, but json.Unmarshal has already turned every escape into
	// its real character, so a second pass corrupts any legitimate backslash.
	//
	// The fixtures below are the shapes that unescaping destroys and that the
	// original single fixture ("root cause found" - no backslash, no newline) could
	// never have caught: a Windows path, a regex, and a real newline inside the
	// summary. Each is something an agent plausibly writes when describing a fix.
	for _, tc := range []struct {
		name    string
		summary string
	}{
		{"windows path", `fixed the loader in C:\Users\dev\app\main.go`},
		{"regex", `tightened the guard to \d+\.\d+ so betas are excluded`},
		{"real newline", "two findings:\n- the retry never fires\n- the timeout is ignored"},
		{"trailing backslash", `escaped the literal separator \\ in the parser`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			block, err := json.Marshal(map[string]any{
				"verdict":  "pass",
				"artifact": map[string]any{"type": "analysis", "summary": tc.summary},
			})
			if err != nil {
				t.Fatalf("marshal submission: %v", err)
			}
			raw := "Here is my analysis.\n" +
				"<<<MULTICA_SUBMISSION>>>\n" + string(block) + "\n" +
				"<<<END_MULTICA_SUBMISSION>>>"

			env, err := json.Marshal(protocol.TaskCompletedPayload{Output: raw})
			if err != nil {
				t.Fatalf("marshal envelope: %v", err)
			}

			extracted, ok := workflow.ExtractDelimitedSubmission(workflowAgentOutput(env))
			if !ok {
				t.Fatalf("no extractable block for %s", tc.name)
			}
			sub, problems, err := workflow.ParseSubmission([]byte(extracted), "")
			if err != nil {
				t.Fatalf("%s: a valid submission was rejected: %v (%v)", tc.name, err, problems)
			}
			// The summary must survive byte-for-byte: a mangled summary is what a
			// human reads as the record of the work.
			if sub.Artifact.Summary != tc.summary {
				t.Errorf("summary round trip\n got: %q\nwant: %q", sub.Artifact.Summary, tc.summary)
			}
		})
	}

	// Degenerate inputs must not panic and must not fabricate a submission.
	if got := workflowAgentOutput(nil); got != "" {
		t.Errorf("workflowAgentOutput(nil) = %q, want empty", got)
	}
	if got := workflowAgentOutput([]byte(`{"task_id":"x"}`)); got != "" {
		t.Errorf("an envelope with no output = %q, want empty", got)
	}
	// A non-envelope blob is passed through so the engine's contract validation
	// can record what actually arrived, rather than reporting silence.
	if got := workflowAgentOutput([]byte("not json at all")); got != "not json at all" {
		t.Errorf("a malformed blob = %q, want it passed through as evidence", got)
	}
}
