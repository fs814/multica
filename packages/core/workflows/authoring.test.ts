// @vitest-environment node
import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { WorkflowDefinitionSchema, WorkflowValidationResultSchema } from "./schemas";
import { canRenameWorkflowPort, renameWorkflowPort, compareInputFields, unknownInputKeys, compareWorkflowVersions } from "./authoring";
import { diagnoseGraphV2 } from "./graph-v2";
const graph = () => WorkflowDefinitionSchema.parse({ schema_version: 2, entry_node: "input", nodes: [
  { key: "input", type: "input", next: ["gate"], next_ids: ["flow"], output_ports: [{ id: "x", type: "string" }], input_fields: [{ key: "old", type: "select", options: ["a"] }] },
  { key: "gate", type: "condition", input_ports: [{ id: "x", type: "string", required: true }, { id: "other", type: "string" }], branches: [{ id: "yes", target: "end", predicate: { input_port: "x", equals: "yes" } }, { id: "no", target: "end" }] },
  { key: "end", type: "end" },
], data_edges: [{ id: "binding", source: "input", source_port: "x", target: "gate", target_port: "x", order: 0 }] });
describe("P2 authoring", () => {
  it("renames only the chosen port, preserving binding IDs and predicate values", () => {
    const before = graph();
    const input = renameWorkflowPort(before, "gate", "input_ports", "x", "request");
    expect(input.data_edges![0]).toEqual({ ...before.data_edges![0], target_port: "request" });
    expect(input.nodes[1]!.branches[0]!.predicate).toEqual({ input_port: "request", equals: "yes" });
    const output = renameWorkflowPort(input, "input", "output_ports", "x", "response");
    expect(output).toBe(input);
    expect(output.data_edges![0]!.source_port).toBe("x");
    expect(before.data_edges![0]!.target_port).toBe("x");
  });
  it("rejects collisions, missing IDs and v1 edits without mutation", () => {
    const before = graph();
    for (const next of ["other", "", " spaced "]) expect(renameWorkflowPort(before, "gate", "input_ports", "x", next)).toBe(before);
    expect(renameWorkflowPort(before, "gate", "input_ports", "missing", "new")).toBe(before);
    const v1 = { ...before, schema_version: 1 };
    expect(renameWorkflowPort(v1, "gate", "input_ports", "x", "new")).toBe(v1);
  });
  it("locates required inputs and incompatible edges using graph identity", () => {
    const before = graph(); before.data_edges = [];
    expect(diagnoseGraphV2(before)).toContainEqual(expect.objectContaining({ code: "workflow_invalid_definition", fieldPath: "nodes[1].input_ports[0]", nodeKey: "gate" }));
    const invalid = graph(); invalid.nodes[1]!.input_ports![0]!.type = "number";
    expect(diagnoseGraphV2(invalid)).toContainEqual(expect.objectContaining({ fieldPath: "data_edges[0]", nodeKey: "gate", edgeId: "binding" }));
  });
  it("accepts legacy and unknown diagnostic codes without losing the verdict", () => {
    expect(WorkflowValidationResultSchema.parse({ valid: false, messages: ["legacy"] }).diagnostics).toBeUndefined();
    const parsed = WorkflowValidationResultSchema.parse({ valid: false, messages: ["new rule"], diagnostics: [{ code: "future_code", message: "new rule", field_path: "nodes[1]", node_key: "gate" }] });
    expect(parsed.diagnostics![0]).toMatchObject({ code: "future_code", nodeKey: "gate", fieldPath: "nodes[1]" });
    expect(WorkflowValidationResultSchema.parse({ valid: false, messages: ["kept"], diagnostics: [{ field_path: 42 }] })).toMatchObject({ valid: false, messages: ["kept"], diagnostics: undefined });
  });
  it("reports added, removed, type and option changes without dropping input values", () => {
    const before = graph().nodes[0]!;
    const after = { ...before, input_fields: [{ ...before.input_fields[0]!, type: "text", options: ["b"], required: true }, { ...before.input_fields[0]!, key: "new" }] };
    expect(compareInputFields(before, after)).toEqual([expect.objectContaining({ key: "old", changes: ["type", "options", "required"] }), expect.objectContaining({ key: "new", changes: ["added"] })]);
    expect(compareInputFields(after, before)[1]!.changes).toEqual(["removed"]);
    const input = { title: "T", description: "D", stale: "keep me" };
    expect(unknownInputKeys(input, after)).toEqual(["stale"]);
    expect(input.stale).toBe("keep me");
  });
  it("summarizes graph and binding changes independently of input fields", () => {
    const before = graph(), after = graph(); after.nodes[2]!.name = "Changed"; after.data_edges = [];
    expect(compareWorkflowVersions(before, after)).toMatchObject({ changed: ["end"], dataChanged: true, schemaChanged: false });
  });
});

// Shared fixtures also run through the real Go planner in port_rename_value_flow_test.go.
describe("output producer contracts", () => {
  const cases = JSON.parse(
    readFileSync(
      new URL(
        "../../../server/internal/workflow/testdata/port-rename-value-flow.json",
        import.meta.url,
      ),
      "utf8",
    ),
  ) as Array<{
    name: string;
    before: unknown;
    after: unknown;
    node: string;
    direction: "input_ports" | "output_ports";
    previous: string;
    next: string;
  }>;
  it.each(cases)("preserves actual value-flow fixture $name", (fixture) => {
    const before = WorkflowDefinitionSchema.parse(fixture.before);
    const after = renameWorkflowPort(
      before,
      fixture.node,
      fixture.direction,
      fixture.previous,
      fixture.next,
    );
    expect(after).toBe(before);
    expect(after).toEqual(WorkflowDefinitionSchema.parse(fixture.after));
    expect(diagnoseGraphV2(after)).toEqual([]);
  });
});

describe("condition input execution contracts", () => {
  const cases = JSON.parse(readFileSync(new URL("../../../server/internal/workflow/testdata/condition-port-rename.json", import.meta.url), "utf8")) as Array<{name: string; before: unknown; after: unknown; previous: string; next: string; allowed: boolean}>;
  it.each(cases)("preserves branch fixture $name", (fixture) => {
    const before = WorkflowDefinitionSchema.parse(fixture.before);
    const node = before.nodes.find(n => n.key === "gate")!;
    expect(node.output_ports ?? []).toEqual([]);
    expect(canRenameWorkflowPort(node, "input_ports", fixture.previous, fixture.next)).toBe(fixture.allowed);
    const after = renameWorkflowPort(before, "gate", "input_ports", fixture.previous, fixture.next);
    expect(after).toEqual(WorkflowDefinitionSchema.parse(fixture.after));
    if (!fixture.allowed) expect(after).toBe(before);
    expect(diagnoseGraphV2(after)).toEqual([]);
  });
  it("does not introduce a coupled output ID through an input rename", () => {
    const before = graph();
    before.nodes[1]!.output_ports = [{id: "result", type: "string"}];
    expect(renameWorkflowPort(before, "gate", "input_ports", "x", "result")).toBe(before);
  });
});
