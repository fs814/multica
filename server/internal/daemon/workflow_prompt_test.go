package daemon

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestWorkflowStepPromptSurvivesTheWireAndWins is the regression test for a
// critical defect: the server rendered a complete workflow brief and the daemon
// silently threw it away.
//
// The Task struct had no workflow fields, so json.Unmarshal discarded
// workflow_prompt without error, and BuildPrompt fell through every branch to the
// legacy assignment prompt. The agent was therefore never told about the step,
// never given the run input, and - fatally - never told to emit the delimited
// submission block, so EVERY step of EVERY run would have blocked with
// submission_contract_invalid while the work itself was done correctly.
//
// This test decodes a real server payload into the real daemon.Task and calls the
// real BuildPrompt. Three earlier suites missed the bug because they asserted on
// the server's own response struct, or on a hand-rolled mirror of it declared
// inside the test file: those prove the server SENDS the field and are
// structurally incapable of noticing that the consumer DROPS it.
func TestWorkflowStepPromptSurvivesTheWireAndWins(t *testing.T) {
	// Shaped exactly like handler.AgentTaskResponse on the claim path. A workflow
	// run normally HAS an issue and may carry a trigger comment, which is precisely
	// why the workflow branch has to win over both.
	const wire = `{
	  "id": "task-1",
	  "issue_id": "issue-1",
	  "trigger_comment_id": "comment-1",
	  "workflow_prompt": "You are executing step 'analyze' of the Bug Fix workflow.\n\nEmit the block delimited by <<<MULTICA_SUBMISSION>>>.",
	  "workflow_run_id": "run-1",
	  "workflow_step_instance_id": "step-1",
	  "workflow_node_key": "analyze"
	}`

	var task Task
	if err := json.Unmarshal([]byte(wire), &task); err != nil {
		t.Fatalf("decode claim payload: %v", err)
	}

	// The field must exist on the struct at all. Without this check the rest of the
	// test would pass vacuously on an empty prompt.
	if task.WorkflowPrompt == "" {
		t.Fatal("daemon.Task dropped workflow_prompt: the agent would receive no brief")
	}
	if task.WorkflowRunID != "run-1" || task.WorkflowStepInstanceID != "step-1" || task.WorkflowNodeKey != "analyze" {
		t.Errorf("workflow ids did not survive the wire: run=%q step=%q node=%q",
			task.WorkflowRunID, task.WorkflowStepInstanceID, task.WorkflowNodeKey)
	}

	out := BuildPrompt(task, "claude")

	// Returned verbatim: the server owns the whole brief, so this binary - which is
	// versioned separately - cannot omit part of it.
	if out != task.WorkflowPrompt {
		t.Errorf("BuildPrompt must return the server brief verbatim\n got: %q\nwant: %q", out, task.WorkflowPrompt)
	}
	// The submission contract is the field whose absence breaks every run.
	if !strings.Contains(out, "<<<MULTICA_SUBMISSION>>>") {
		t.Error("the delivered prompt does not tell the agent to emit a submission block")
	}
	// The legacy paths must not have claimed it, even though the task carries both
	// an issue id and a trigger comment id.
	if strings.Contains(out, "multica issue get") {
		t.Error("the assignment prompt captured a workflow step task")
	}
	if strings.Contains(out, "DISTINCT threads") {
		t.Error("the comment prompt captured a workflow step task")
	}
}
