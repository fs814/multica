package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/workflow"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/scriptpipeline"
)

func TestWorkflowScriptPipelineClaimExecuteAndComplete(t *testing.T) {
	for _, selected := range []string{"clone", "build", "run"} {
		t.Run(selected, func(t *testing.T) { testWorkflowSingleScriptStep(t, selected) })
	}
}
func testWorkflowSingleScriptStep(t *testing.T, selected string) {
	shell, ext := "bash", ".sh"
	if runtime.GOOS == "windows" {
		shell, ext = "pwsh", ".ps1"
	}
	if _, err := exec.LookPath(shell); err != nil {
		t.Skip("shell unavailable")
	}
	withWorkflowEngineForTest(t)
	env := newWorkflowE2EEnv(t, "scripts")
	ctx := context.Background()
	dir := t.TempDir()
	body := "echo pipeline-result\n"
	if runtime.GOOS == "windows" {
		body = "Write-Output 'pipeline-result'\n"
	}
	if err := os.WriteFile(filepath.Join(dir, selected+ext), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	version, err := testHandler.Queries.GetPublishedWorkflowTemplateVersion(ctx, db.GetPublishedWorkflowTemplateVersionParams{TemplateID: parseUUID(env.templateID), WorkspaceID: parseUUID(env.workspaceID)})
	if err != nil {
		t.Fatal(err)
	}
	def, err := workflow.ParseDefinition(version.Definition)
	if err != nil {
		t.Fatal(err)
	}
	intake, _ := def.NodeByKey("intake")
	analyze, _ := def.NodeByKey("analyze")
	end, _ := def.NodeByKey("end")
	intake.InputMode = workflow.InputModeScripts
	intake.InputFields = nil
	intake.ScriptPipeline = &scriptpipeline.Config{Directory: dir, Platform: runtime.GOOS, Steps: []string{"clone", "build", "run"}, TimeoutSeconds: 30}
	analyze.Next = []string{"end"}
	def.Nodes = []workflow.Node{*intake, *analyze, *end}
	definition, _ := json.Marshal(def)
	if _, err = testPool.Exec(ctx, `UPDATE workflow_template_version SET definition=$1 WHERE id=$2`, definition, version.ID); err != nil {
		t.Fatal(err)
	}
	metadata, _ := json.Marshal(map[string]any{"os": runtime.GOOS, "capabilities": []string{scriptpipeline.Capability}})
	if _, err = testPool.Exec(ctx, `UPDATE agent_runtime SET metadata=$1 WHERE id=$2`, metadata, env.runtimeID); err != nil {
		t.Fatal(err)
	}
	saved := httptest.NewRecorder()
	testHandler.SaveWorkflowInputInstance(saved, withURLParam(env.request("POST", "/", map[string]any{
		"name": "Single stage", "template_version_id": uuidToString(version.ID),
		"input": map[string]string{"title": "Script integration", "description": "Execute selected script"},
	}), "id", env.templateID))
	if saved.Code != http.StatusCreated {
		t.Fatalf("instance rejected: %s", saved.Body.String())
	}
	var instance workflowInputInstanceResponse
	if err := json.Unmarshal(saved.Body.Bytes(), &instance); err != nil {
		t.Fatal(err)
	}
	started := httptest.NewRecorder()
	testHandler.RunWorkflowInstance(started, withURLParam(env.request("POST", "/", map[string]any{
		"revision": instance.Revision, "mode": "saved", "script_step": selected, "idempotency_key": "stage-" + selected,
	}), "instanceID", instance.ID))
	if started.Code != http.StatusCreated {
		t.Fatalf("run rejected: %d %s", started.Code, started.Body.String())
	}
	run := decodeWorkflowRunDetail(t, started, "scripts")
	w := httptest.NewRecorder()
	req := newDaemonTokenRequest("POST", "/api/daemon/runtimes/"+env.runtimeID+"/tasks/claim", nil, env.workspaceID, "workflow-e2e-daemon")
	req = withURLParam(req, "runtimeId", env.runtimeID)
	req.Header.Set("X-Client-Capabilities", scriptpipeline.Capability)
	testHandler.ClaimTaskByRuntime(w, req)
	var claim struct {
		Task *struct {
			ID     string                 `json:"id"`
			StepID string                 `json:"workflow_step_instance_id"`
			Config *scriptpipeline.Config `json:"workflow_script_pipeline"`
		} `json:"task"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &claim); err != nil || claim.Task == nil || claim.Task.Config == nil {
		t.Fatalf("missing script claim: %s", w.Body.String())
	}
	if _, err = testHandler.TaskService.StartTask(ctx, parseUUID(claim.Task.ID)); err != nil {
		t.Fatal(err)
	}
	active := env.getRun(t, run.ID)
	activeStep, _ := findWorkflowStep(active.Steps, "analyze")
	if activeStep.Status != "running" || activeStep.StartedAt == nil {
		t.Fatalf("task start not projected to workflow: %+v", activeStep)
	}
	if len(claim.Task.Config.Steps) != 1 || claim.Task.Config.Steps[0] != selected {
		t.Fatalf("wrong stage: %+v", claim.Task.Config)
	}
	// Unselected script files do not exist; they must neither be required nor run.
	result := scriptpipeline.Run(ctx, claim.Task.Config, nil)
	if result.Error != "" || len(result.Steps) != 1 || !strings.Contains(result.Steps[0].Output, "pipeline-result") {
		t.Fatalf("execution: %+v", result)
	}
	submission, _ := json.Marshal(workflow.Submission{SchemaVersion: 1, StepInstanceID: claim.Task.StepID, Verdict: workflow.VerdictPass, Artifact: workflow.Artifact{Type: "script_pipeline", Summary: result.Steps[0].Output}})
	output, _ := json.Marshal(map[string]any{"task_id": claim.Task.ID, "output": string(submission)})
	if _, err = testHandler.TaskService.CompleteTask(ctx, parseUUID(claim.Task.ID), output, "", "", dir, false, "", ""); err != nil {
		t.Fatal(err)
	}
	loaded, err := testHandler.Queries.GetWorkflowInputInstance(ctx, db.GetWorkflowInputInstanceParams{ID: parseUUID(instance.ID), WorkspaceID: parseUUID(env.workspaceID)})
	var beforeInput, afterInput map[string]any
	beforeErr := json.Unmarshal(instance.Input, &beforeInput)
	afterErr := json.Unmarshal(loaded.Input, &afterInput)
	if err != nil || beforeErr != nil || afterErr != nil || !reflect.DeepEqual(beforeInput, afterInput) || loaded.Revision != instance.Revision {
		t.Fatalf("single stage changed saved inputs: %v", err)
	}
	after := env.getRun(t, run.ID)
	step, _ := findWorkflowStep(after.Steps, "analyze")
	if after.Status != "completed" || step.Submission == nil || step.Submission.Verdict != "pass" {
		t.Fatalf("result not recorded: status=%s step=%+v", after.Status, step)
	}
}
