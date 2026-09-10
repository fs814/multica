package daemon

import (
	"github.com/multica-ai/multica/server/internal/workflow"
	"github.com/multica-ai/multica/server/pkg/agent"
)

func configureWorkflowOutput(task Task, provider string, prompt string, opts *agent.ExecOptions) string {
	if provider != "codex" || task.WorkflowPrompt == "" || task.WorkflowStepInstanceID == "" {
		return prompt
	}
	opts.OutputSchema = workflow.SubmissionOutputSchema(task.WorkflowStepInstanceID)
	return prompt + "\n\nStructured final output is enabled for this workflow step. Return the submission JSON object directly, without the delimiter lines or Markdown fences. The runtime's output schema replaces only the presentation format; all verdict, evidence and step identity rules above still apply. If clarification or access is required, return verdict blocked and put the specific question or missing prerequisite in rationale. Do not end with only a question or invent a pass.\n"
}
