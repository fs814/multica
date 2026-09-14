import type { ScriptPipelineConfig } from "./schemas";

export const scriptPipelineSteps = ["clone", "build", "run"] as const;
export const scriptPipelineInputKeys = ["script_directory", "script_platform", "script_steps", "clone_script", "build_script", "run_script", "script_timeout_seconds"];

export function scriptPipelineConfig(value?: Partial<ScriptPipelineConfig>): ScriptPipelineConfig {
  return { directory: "", platform: "auto", steps: [...scriptPipelineSteps], scripts: {}, timeout_seconds: 3600, ...value };
}
export function scriptPipelineInput(value: ScriptPipelineConfig): Record<string, string> {
  return {
    script_directory: value.directory,
    script_platform: value.platform,
    script_steps: JSON.stringify(value.steps),
    clone_script: value.scripts.clone ?? "",
    build_script: value.scripts.build ?? "",
    run_script: value.scripts.run ?? "",
    script_timeout_seconds: String(value.timeout_seconds),
  };
}
export function scriptPipelineFromInput(defaults: ScriptPipelineConfig | undefined, input: Readonly<Record<string, string>>): ScriptPipelineConfig {
  const base = scriptPipelineConfig(defaults);
  let steps = base.steps;
  if (input.script_steps !== undefined) {
    try {
      const decoded: unknown = JSON.parse(input.script_steps);
      steps = Array.isArray(decoded) && decoded.every((step) => typeof step === "string") ? decoded : [];
    } catch { steps = []; }
  }
  return {
    directory: input.script_directory ?? base.directory,
    platform: input.script_platform ?? base.platform,
    steps,
    scripts: Object.fromEntries(scriptPipelineSteps.map((step) => [step, input[`${step}_script`] ?? base.scripts[step] ?? ""])),
    timeout_seconds: input.script_timeout_seconds === undefined ? base.timeout_seconds : Number(input.script_timeout_seconds),
  };
}
export function scriptPipelineReady(value: ScriptPipelineConfig): boolean {
  return Boolean(value.directory.trim()) && value.steps.length > 0 && value.steps.length <= 3 &&
    new Set(value.steps).size === value.steps.length &&
    value.steps.every((step) => scriptPipelineSteps.includes(step as typeof scriptPipelineSteps[number])) &&
    ["auto", "windows", "darwin", "linux"].includes(value.platform) &&
    Number.isInteger(value.timeout_seconds) && value.timeout_seconds > 0 && value.timeout_seconds <= 86400;
}