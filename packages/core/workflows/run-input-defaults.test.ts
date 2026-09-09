import { describe, expect, it } from "vitest";
import { WorkflowDefinitionSchema } from "./schemas";
import { workflowRunInputDefaults } from "./run-input-defaults";

describe("workflowRunInputDefaults", () => {
  it("uses only the entry input node's authored content", () => {
    const definition = WorkflowDefinitionSchema.parse({ entry_node: "input", nodes: [
      { key: "other", type: "input", instruction: "Ignore this node" },
      { key: "input", type: "input", name: "Machine", instruction: "Background\nGoals" },
    ] });
    expect(workflowRunInputDefaults(definition, "Workflow")).toEqual({ title: "Machine", description: "Background\nGoals" });
  });
  it("uses the template name when the entry has no name", () => {
    const definition = WorkflowDefinitionSchema.parse({ entry_node: "input", nodes: [
      { key: "input", type: "input", name: "  ", instruction: "Details" },
    ] });
    expect(workflowRunInputDefaults(definition, "Workflow")).toEqual({ title: "Workflow", description: "Details" });
  });
  it("keeps workflows without an entry input on their existing input flow", () => {
    const definition = WorkflowDefinitionSchema.parse({ entry_node: "agent", nodes: [
      { key: "agent", type: "agent", name: "Agent", instruction: "Generic agent instruction" },
    ] });
    expect(workflowRunInputDefaults(definition, "Workflow")).toEqual({});
    expect(workflowRunInputDefaults(undefined, "Workflow")).toEqual({});
  });
});