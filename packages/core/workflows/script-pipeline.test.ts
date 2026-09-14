import { describe, expect, it } from "vitest";
import { scriptPipelineConfig, scriptPipelineInput, scriptPipelineFromInput, scriptPipelineReady } from "./script-pipeline";
import { workflowRunInputDefaults } from "./run-input-defaults";
import { WorkflowDefinitionSchema } from "./schemas";

describe("script pipeline inputs", () => {
  it("round trips a saved instance and preserves explicit clears", () => {
    const defaults = scriptPipelineConfig({ directory: "C:/winbuild/project", scripts: { build: "build.ps1" } });
    const instance = scriptPipelineFromInput(defaults, { script_directory: "D:/another project", script_steps: '["run"]', build_script: "" });
    expect(scriptPipelineFromInput(defaults, scriptPipelineInput(instance))).toEqual(instance);
    expect(instance.scripts.build).toBe("");
    expect(instance.steps).toEqual(["run"]);
    expect(scriptPipelineReady(instance)).toBe(true);
  });
  it("reuses authored values without requesting duplicate input", () => {
    const config = scriptPipelineConfig({ directory: "C:/winbuild/project", steps: ["build"] });
    const definition = WorkflowDefinitionSchema.parse({ entry_node: "input", nodes: [{ key: "input", type: "input", input_mode: "scripts", script_pipeline: config }] });
    expect(workflowRunInputDefaults(definition, "Build project")).toMatchObject({ title: "Build project", description: "build", script_directory: config.directory, script_steps: '["build"]' });
  });
  it.each(['[]', '["unknown"]', '["build","build"]', 'invalid'])("rejects invalid step selection %s", (steps) => {
    expect(scriptPipelineReady(scriptPipelineFromInput(scriptPipelineConfig({ directory: "C:/scripts" }), { script_steps: steps }))).toBe(false);
  });
  it("keeps the graph readable when optional script settings are malformed", () => {
    const definition = WorkflowDefinitionSchema.parse({ entry_node: "input", nodes: [{ key: "input", type: "input", script_pipeline: "bad" }] });
    expect(definition.nodes).toHaveLength(1);
    expect(definition.nodes[0]?.script_pipeline).toBeUndefined();
  });
});
