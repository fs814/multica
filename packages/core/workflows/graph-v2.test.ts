import { describe, it, expect } from "vitest";
import { WorkflowDefinitionSchema } from "./schemas";
import { validateGraphV2 } from "./graph-v2";
const fixture = () =>
  WorkflowDefinitionSchema.parse({
    schema_version: 2,
    entry_node: "start",
    nodes: [
      { key: "start", type: "input", next: ["a", "b"], next_ids: ["sa", "sb"] },
      {
        key: "a",
        type: "agent",
        next: ["join"],
        next_ids: ["aj"],
        output_ports: [{ id: "summary", type: "string" }],
      },
      {
        key: "b",
        type: "agent",
        next: ["join"],
        next_ids: ["bj"],
        output_ports: [{ id: "summary", type: "string" }],
      },
      {
        key: "join",
        type: "join",
        next: ["end"],
        next_ids: ["je"],
        input_ports: [{ id: "value", type: "string", required: true }],
      },
      { key: "end", type: "end" },
    ],
    data_edges: [
      {
        id: "binding",
        source: "a",
        source_port: "summary",
        target: "join",
        target_port: "value",
        order: 0,
      },
    ],
  });
describe("v2 authoring contract", () => {
  it("allows required data from a parallel branch", () =>
    expect(validateGraphV2(fixture())).toEqual([]));
  it("rejects mismatched types, cycles and duplicate IDs", () => {
    const typed = fixture();
    typed.nodes[3]!.input_ports![0]!.type = "number";
    expect(validateGraphV2(typed).join()).toMatch(/Incompatible/);
    const cycle = fixture();
    cycle.nodes[3]!.next.push("a");
    cycle.nodes[3]!.next_ids!.push("ja");
    expect(validateGraphV2(cycle).join()).toMatch(/cycle/);
    const id = fixture();
    id.data_edges![0]!.id = "sa";
    expect(validateGraphV2(id).join()).toMatch(/unique/);
  });
  it("rejects a required value only available on an exclusive branch", () => {
    const d = fixture();
    d.nodes[0]!.type = "condition";
    d.nodes[0]!.next = [];
    d.nodes[0]!.next_ids = [];
    d.nodes[0]!.branches = [
      { id: "choose-a", target: "a", when_verdict: "pass" },
      { id: "default-b", target: "b", when_verdict: "" },
    ];
    expect(validateGraphV2(d).join()).toMatch(/source may be skipped/);
  });
  it("requires explicit collection cardinality and stable distinct order", () => {
    const d = fixture();
    d.data_edges!.push({
      id: "other",
      source: "b",
      source_port: "summary",
      target: "join",
      target_port: "value",
      order: 1,
    });
    expect(validateGraphV2(d).join()).toMatch(/already connected/);
    d.nodes[3]!.input_ports![0]!.multiple = true;
    expect(validateGraphV2(d)).toEqual([]);
    d.data_edges![1]!.order = 0;
    expect(validateGraphV2(d).join()).toMatch(/order/);
  });
  it("allows an incomplete draft but rejects missing inputs at publish", () => {
    const d = fixture();
    d.data_edges = [];
    expect(validateGraphV2(d, false)).toEqual([]);
    expect(validateGraphV2(d).join()).toMatch(/required input/);
  });
});
