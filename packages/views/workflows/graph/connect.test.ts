// @vitest-environment jsdom
import { describe, it, expect } from "vitest";
import { WorkflowDefinitionSchema } from "@multica/core/workflows";
import { definitionToGraph } from "./to-graph";
import { graphToDefinition } from "./from-graph";
import { connectWorkflow } from "./connect";
import {
  initialWorkflowEditorState,
  workflowEditorReducer,
  workingDefinition,
} from "../editor/editor-state";
const fixture = () =>
  WorkflowDefinitionSchema.parse({
    schema_version: 2,
    entry_node: "a",
    nodes: [
      {
        key: "a",
        type: "agent",
        next: ["b", "c"],
        next_ids: ["ab", "ac"],
        output_ports: [{ id: "summary", type: "string" }],
      },
      {
        key: "b",
        type: "agent",
        next: ["end"],
        next_ids: ["be"],
        input_ports: [{ id: "brief", type: "string" }],
      },
      {
        key: "c",
        type: "agent",
        next: ["end"],
        next_ids: ["ce"],
        input_ports: [
          { id: "brief", type: "string" },
          { id: "count", type: "number" },
        ],
      },
      { key: "end", type: "end", next_ids: [] },
    ],
    data_edges: [],
  });
describe("typed workflow connections", () => {
  it("preserves omitted empty fields returned by the server", () => {
    const def = fixture();
    delete def.data_edges;
    delete def.nodes[3]!.next_ids;
    const graph = definitionToGraph(def);
    expect(graphToDefinition(graph.nodes, graph.edges, def)).toEqual(def);
  });
  it("adds, reconnects and round trips stable port/edge identity", () => {
    const def = fixture(),
      graph = definitionToGraph(def);
    const added = connectWorkflow(
      graph.nodes,
      graph.edges,
      def,
      "a",
      "b",
      "out:summary",
      "in:brief",
    );
    expect(added.error).toBeUndefined();
    const edge = added.edges.at(-1)!;
    const reconnected = connectWorkflow(
      graph.nodes,
      added.edges,
      def,
      "a",
      "c",
      "out:summary",
      "in:brief",
      edge.id,
    );
    expect(reconnected.error).toBeUndefined();
    const saved = graphToDefinition(graph.nodes, reconnected.edges, def);
    expect(saved.data_edges).toEqual([
      {
        id: edge.id,
        source: "a",
        target: "c",
        source_port: "summary",
        target_port: "brief",
        order: 0,
      },
    ]);
    const loaded = definitionToGraph(WorkflowDefinitionSchema.parse(saved));
    expect(graphToDefinition(loaded.nodes, loaded.edges, saved)).toEqual(saved);
  });
  it("rejects invalid direction, types, occupied ports and cycles without losing the original", () => {
    const def = fixture(),
      graph = definitionToGraph(def);
    const added = connectWorkflow(
      graph.nodes,
      graph.edges,
      def,
      "a",
      "b",
      "out:summary",
      "in:brief",
    );
    const edge = added.edges.at(-1)!;
    for (const [source, target, sh, th] of [
      ["a", "c", "out:summary", "in:count"],
      ["b", "a", "in:brief", "out:summary"],
      ["a", "a", "out:summary", "in:brief"],
    ]) {
      const result = connectWorkflow(
        graph.nodes,
        added.edges,
        def,
        source!,
        target!,
        sh,
        th,
        edge.id,
      );
      expect(result.error).toBeTruthy();
      expect(result.edges).toBe(added.edges);
    }
    expect(
      connectWorkflow(
        graph.nodes,
        added.edges,
        def,
        "a",
        "b",
        "out:summary",
        "in:brief",
      ).error,
    ).toBeTruthy();
    expect(
      connectWorkflow(graph.nodes, graph.edges, def, "b", "a").error,
    ).toMatch(/cycle/);
  });
  it("retains bindings on property edits and deletes node references in one undoable action", () => {
    const def = fixture();
    let state = workflowEditorReducer(initialWorkflowEditorState(), {
      type: "hydrate",
      templateId: "test",
      definition: def,
    });
    const added = connectWorkflow(
      state.present.nodes,
      state.present.edges,
      def,
      "a",
      "b",
      "out:summary",
      "in:brief",
    );
    state = workflowEditorReducer(state, {
      type: "set_graph",
      nodes: state.present.nodes,
      edges: added.edges,
    });
    state = workflowEditorReducer(state, {
      type: "patch_node",
      node: { ...state.present.nodes[0]!.data.node, name: "Edited" },
    });
    expect(workingDefinition(state).data_edges).toHaveLength(1);
    state = workflowEditorReducer(state, { type: "delete_node", nodeId: "b" });
    expect(workingDefinition(state).data_edges).toHaveLength(0);
    expect(workingDefinition(state).nodes[0]?.next).toEqual(["c"]);
    state = workflowEditorReducer(state, { type: "undo" });
    expect(workingDefinition(state).data_edges).toHaveLength(1);
  });
});
