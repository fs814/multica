package workflow

import (
	"context"
	"encoding/json"
	"github.com/multica-ai/multica/server/pkg/scriptpipeline"
	"testing"
)

func TestScriptPipelineDispatchAndSubsequentReview(t *testing.T) {
	env := setupTestEnv(t)
	def := intakeLinearDefinition()
	def.Nodes[0].InputMode = InputModeScripts
	def.Nodes[0].InputFields = nil
	def.Nodes[0].ScriptPipeline = &scriptpipeline.Config{Directory: t.TempDir(), Platform: "auto", Steps: []string{"clone", "build", "run"}, TimeoutSeconds: 60}
	env.publishTemplate(t, def)
	run := env.startRunWithInput(t, "scripts-instance", map[string]any{"title": "Run only", "description": "Saved instance", "script_steps": `["run"]`})
	step := env.stepByNode(t, run.ID, "analyze")
	tc := env.loadTaskContext(t, env.loadTask(t, step))
	if tc.ScriptPipeline == nil || len(tc.ScriptPipeline.Steps) != 1 || tc.ScriptPipeline.Steps[0] != "run" {
		t.Fatalf("lost instance override: %+v", tc.ScriptPipeline)
	}
	if _, err := env.engine.SubmitResult(context.Background(), SubmitResultInput{WorkspaceID: env.workspaceID, StepID: step.ID, RawOutput: passPayload("script executed"), ActorType: "agent"}); err != nil {
		t.Fatal(err)
	}
	next := env.stepByNode(t, run.ID, "implement")
	if env.loadTaskContext(t, env.loadTask(t, next)).ScriptPipeline != nil {
		t.Fatal("review node must not rerun scripts")
	}
}

func TestScriptPipelineInputValidation(t *testing.T) {
	def := intakeLinearDefinition()
	def.Nodes[0].InputMode = InputModeScripts
	def.Nodes[0].InputFields = nil
	for _, value := range []string{`{"script_directory":"C:/scripts","script_steps":"[]"}`, `{"script_steps":"[\"run\"]"}`} {
		if _, err := ScriptPipelineForNode(def, &def.Nodes[1], json.RawMessage(value)); err == nil {
			t.Fatalf("accepted invalid input: %s", value)
		}
	}
}

func TestScriptPipelineSingleStepSnapshot(t *testing.T) {
	def := intakeLinearDefinition()
	def.Nodes[0].InputMode = InputModeScripts
	definition, _ := json.Marshal(def)
	originalSteps, _ := json.Marshal([]string{"clone", "build", "run"})
	original, _ := json.Marshal(map[string]string{"title": "A", "script_directory": "C:/scripts", "script_steps": string(originalSteps)})
	for _, step := range []string{"clone", "build", "run"} {
		result, err := singleScriptStepInput(definition, original, step)
		if err != nil {
			t.Fatal(err)
		}
		var values map[string]string
		if err := json.Unmarshal(result, &values); err != nil {
			t.Fatal(err)
		}
		expected, _ := json.Marshal([]string{step})
		if values["script_steps"] != string(expected) || values["script_directory"] != "C:/scripts" || values["title"] != "A" {
			t.Fatalf("bad snapshot: %s", result)
		}
	}
	for _, step := range []string{"delete", "clone,build", "RUN"} {
		if _, err := singleScriptStepInput(definition, original, step); err == nil {
			t.Fatalf("accepted %s", step)
		}
	}
	def.Nodes[0].InputMode = InputModeText
	definition, _ = json.Marshal(def)
	if _, err := singleScriptStepInput(definition, original, "run"); err == nil {
		t.Fatal("accepted text input")
	}
}
