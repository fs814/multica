package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/workflow"
	"github.com/multica-ai/multica/server/pkg/scriptpipeline"
)

func (d *Daemon) runScriptPipelineTask(ctx context.Context, task Task, logger *slog.Logger) (TaskResult, error) {
	startCtx, cancelStart := context.WithTimeout(ctx, 10*time.Second)
	err := d.client.StartTask(startCtx, task.ID)
	cancelStart()
	if err != nil {
		return TaskResult{}, fmt.Errorf("start task failed: %w", err)
	}
	d.runningTasks.Add(1)
	defer d.runningTasks.Add(-1)
	taskPhaseRecorderFromContext(ctx).Mark(taskPhaseRuntimeStarted)
	seq := 0
	emit := func(text string) {
		if ctx.Err() != nil {
			return
		}
		seq++
		sendCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		if err := d.client.ReportTaskMessages(sendCtx, task.ID, []TaskMessageData{{Seq: seq, Type: "text", Content: text, CreatedAt: time.Now()}}); err != nil {
			logger.Warn("script pipeline progress delivery failed", "error", err)
		}
	}
	var environment map[string]string
	if task.Agent != nil {
		environment = task.Agent.CustomEnv
	}
	result := scriptpipeline.Run(ctx, task.WorkflowScriptPipeline, emit, environment)
	if ctx.Err() != nil {
		return TaskResult{WorkDir: result.Directory}, ctx.Err()
	}
	var summary strings.Builder
	fmt.Fprintf(&summary, "Script directory: %s\n", result.Directory)
	for _, step := range result.Steps {
		fmt.Fprintf(&summary, "%s: exit %d, %d ms — %s\n", step.Name, step.ExitCode, step.DurationMS, step.Path)
		output := step.Output
		if len(output) > 4096 {
			output = output[:4096]
		}
		if output != "" {
			fmt.Fprintf(&summary, "%s\n", output)
		}
		if step.Truncated || len(step.Output) > 4096 {
			summary.WriteString("[output preview truncated]\n")
		}
	}
	verdict := workflow.VerdictPass
	rationale := "All selected scripts exited successfully in clone/build/run order."
	if result.Error != "" {
		verdict = workflow.VerdictFail
		rationale = result.Error
		summary.WriteString("Stopped: " + result.Error)
	}
	submission := workflow.Submission{SchemaVersion: 1, StepInstanceID: task.WorkflowStepInstanceID, Verdict: verdict, Artifact: workflow.Artifact{Type: "script_pipeline", Summary: summary.String(), References: []string{}}, Rationale: rationale}
	body, err := json.Marshal(submission)
	if err != nil {
		return TaskResult{}, err
	}
	// A completed executor can report a failed business verdict. The workflow
	// engine applies on_failure and records the structured evidence atomically.
	return TaskResult{Status: "completed", Comment: string(body), WorkDir: result.Directory}, nil
}
