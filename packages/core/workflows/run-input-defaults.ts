import type { WorkflowDefinition } from "./schemas";

/** Reuse authored intake content as run values, independently of the run's schema. */
export function workflowRunInputDefaults(
  definition: WorkflowDefinition | undefined,
  templateName: string,
): Record<string, string> {
  const entry = definition?.nodes.find((node) => node.key === definition.entry_node);
  if (entry?.type !== "input") return {};
  return {
    title: entry.name.trim() || templateName,
    description: entry.instruction,
  };
}