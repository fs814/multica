package daemon

import (
	"encoding/json"
	"github.com/multica-ai/multica/server/pkg/agent"
	"strings"
	"testing"
)

func TestWorkflowStructuredOutputScopedToCodexSteps(t *testing.T) {
	for _, tc := range []struct {
		name, provider string
		task           Task
		constrained    bool
	}{
		{"workflow", "codex", Task{WorkflowPrompt: "brief", WorkflowStepInstanceID: "step-a"}, true},
		{"ordinary task", "codex", Task{}, false},
		{"incomplete workflow", "codex", Task{WorkflowPrompt: "brief"}, false},
		{"other runtime", "claude", Task{WorkflowPrompt: "brief", WorkflowStepInstanceID: "step-a"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var opts agent.ExecOptions
			prompt := configureWorkflowOutput(tc.task, tc.provider, "original prompt", &opts)
			if (len(opts.OutputSchema) > 0) != tc.constrained {
				t.Fatal("wrong output constraint scope")
			}
			if tc.constrained {
				if !json.Valid(opts.OutputSchema) || !strings.Contains(string(opts.OutputSchema), `"step-a"`) {
					t.Fatal("missing bound schema")
				}
				if !strings.Contains(prompt, "return verdict blocked") || !strings.HasPrefix(prompt, "original prompt") {
					t.Fatal("clarification or original instructions lost")
				}
			} else if prompt != "original prompt" {
				t.Fatal("ordinary prompt changed")
			}
		})
	}
}
