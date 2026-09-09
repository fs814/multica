// @vitest-environment jsdom

/**
 * Canvas smoke test.
 *
 * tsc proves the `nodeTypes` map is exhaustive over the model's node types, but it
 * cannot prove that xyflow actually *finds* a renderer for the type strings the
 * model writes - a mismatch there drops the node silently, which is the one
 * failure mode of this component a reader would blame on their data. So this
 * renders the real `<ReactFlow />` with the real Bug Fix graph and asserts every
 * card is on screen with its badge and routing line.
 *
 * The EDGE layer is deliberately not asserted here. jsdom has no layout, so every
 * node measures 0x0 and xyflow never computes edge geometry - `react-flow__edges`
 * comes out empty regardless of whether the edge registry is correct, so an
 * assertion on it would pass or fail for reasons unrelated to this code. The edge
 * renderer is covered directly in workflow-edge.test.tsx, where it can be handed
 * the geometry the library would have measured.
 */

import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../locales/en/common.json";
import enWorkflows from "../../locales/en/workflows.json";
import { definitionToGraph } from "../graph";
import { bugFixDefinition } from "../graph/bug-fix.fixture";
import { WorkflowCanvas } from "./workflow-canvas";

const TEST_RESOURCES = { en: { common: enCommon, workflows: enWorkflows } };

function renderCanvas(
  options: { readOnly?: boolean; selectedEdgeIndex?: number; selectedNodeId?: string } = {},
) {
  const { nodes, edges } = definitionToGraph(bugFixDefinition());
  const selectedEdges = edges.map((edge, index) =>
    index === options.selectedEdgeIndex ? { ...edge, selected: true } : edge,
  );
  const onDeleteNode = vi.fn();
  const onNodesChange = vi.fn();
  const onEdgesChange = vi.fn();
  render(
    <I18nProvider locale="en" resources={TEST_RESOURCES}>
      <WorkflowCanvas
        nodes={nodes}
        edges={selectedEdges}
        selectedNodeId={options.selectedNodeId ?? null}
        onDeleteNode={onDeleteNode}
        readOnly={options.readOnly ?? false}
        onNodesChange={onNodesChange}
        onEdgesChange={onEdgesChange}
        onSelectNode={vi.fn()}
        onConnect={vi.fn()}
      />
    </I18nProvider>,
  );
  return { nodes, edges: selectedEdges, onNodesChange, onEdgesChange, onDeleteNode };
}

/** Node cards by xyflow's own per-node test id, so a dropped node is visible. */
function cardIds(): string[] {
  return Array.from(document.querySelectorAll("[data-id]"))
    .map((element) => element.getAttribute("data-id") ?? "")
    .filter((id) => id !== "");
}

describe("WorkflowCanvas", () => {
  it("draws a card for every node in the definition", async () => {
    renderCanvas();
    // Names from the fixture. A node whose `type` had no entry in the registry
    // would be dropped by xyflow, so this is the exhaustiveness check tsc cannot
    // make - it checks the map's keys, not that they match the model's output.
    for (const name of ["Analyze", "Implement", "Validate", "Done"]) {
      expect(await screen.findByText(name)).toBeInTheDocument();
    }
    expect(cardIds()).toEqual(
      expect.arrayContaining([
        "analyze",
        "implement",
        "validate",
        "acceptance",
        "end",
      ]),
    );
  });

  it("labels each card with its translated node type", async () => {
    renderCanvas();
    // Three agent steps, labelled "Issue" rather than "Agent" because that is the
    // product term the screenshot uses; `onAdd` still emits `agent`.
    expect((await screen.findAllByText("Issue")).length).toBe(3);
    // Two matches for the acceptance node: its badge and its name, which the
    // fixture also calls "Acceptance". Both are correct - asserting a unique
    // match here would be asserting the fixture's naming, not the renderer.
    expect(screen.getAllByText("Acceptance").length).toBe(2);
    expect(screen.getByText("End")).toBeInTheDocument();
  });

  it("marks the entry node, which has no other visual representation", async () => {
    renderCanvas();
    // entry_node is a graph-level field and the entry need not be the leftmost
    // card once rework edges exist, so the badge is the only signal on screen.
    expect(await screen.findByText("Entry")).toBeInTheDocument();
  });

  it("renders the routing field the chosen strategy actually makes meaningful", async () => {
    renderCanvas();
    // Bug Fix routes `validate` off `implement`'s agent; reading out `capability`
    // there would describe routing the engine will not perform.
    expect(await screen.findByText("Agent from implement")).toBeInTheDocument();
    expect(screen.getByText("Capability code_change")).toBeInTheDocument();
    expect(screen.getByText("Capability bug_analysis")).toBeInTheDocument();
  });

  it("keeps a read-only graph readable and flags it for the stylesheet", () => {
    renderCanvas({ readOnly: true });
    const pane = document.querySelector(".workflow-canvas");
    // `data-read-only` is what dims the ports (workflow-canvas.css): a port that
    // looks draggable but refuses to connect reads as a broken canvas rather than
    // as a locked one.
    expect(pane?.getAttribute("data-read-only")).toBe("true");
    // Still drawn, not hidden - a built-in template is exactly the graph a user
    // most wants to read.
    expect(screen.getByText("Analyze")).toBeInTheDocument();
  });

  it("declares the arrowhead markers the edge strokes reference", () => {
    renderCanvas();
    // Hand-written rather than xyflow's MarkerType, because xyflow bakes the
    // colour into the marker id and our colours are `var(--wf-edge-*)`, which
    // cannot appear in an id. If these ever go missing every edge loses its
    // arrowhead silently - `url(#missing)` is not an error.
    for (const id of [
      "wf-arrow-pass",
      "wf-arrow-fail",
      "wf-arrow-blocked",
      "wf-arrow-default",
    ]) {
      expect(document.getElementById(id)).not.toBeNull();
    }
  });

  it("deletes only the selected edge from a graph with multiple connections", () => {
    const rendered = renderCanvas({ selectedEdgeIndex: 1 });
    const removed = rendered.edges[1];
    expect(rendered.edges.length).toBeGreaterThan(1);

    fireEvent.keyDown(document.querySelector(".workflow-canvas")!, { key: "Delete" });

    expect(rendered.onEdgesChange).toHaveBeenCalledTimes(1);
    const next = rendered.onEdgesChange.mock.calls[0]?.[0];
    expect(next).toHaveLength(rendered.edges.length - 1);
    expect(next.map((edge: { id: string }) => edge.id)).not.toContain(removed?.id);
    expect(rendered.onNodesChange).not.toHaveBeenCalled();
    expect(rendered.nodes).toHaveLength(bugFixDefinition().nodes.length);
  });

  it("ignores keyboard deletion for a selected edge in read-only mode", () => {
    const rendered = renderCanvas({ readOnly: true, selectedEdgeIndex: 1 });

    fireEvent.keyDown(document.querySelector(".workflow-canvas")!, { key: "Backspace" });

    expect(rendered.onEdgesChange).not.toHaveBeenCalled();
    expect(rendered.onNodesChange).not.toHaveBeenCalled();
  });
});

describe("canvas node deletion shortcuts", () => {
  it.each(["Delete", "Backspace"])("deletes a selected node with %s as one editor action", (key) => {
    const rendered = renderCanvas({ selectedNodeId: "implement" });
    fireEvent.keyDown(document.querySelector(".workflow-canvas")!, { key });
    expect(rendered.onDeleteNode).toHaveBeenCalledExactlyOnceWith("implement");
    expect(rendered.onEdgesChange).not.toHaveBeenCalled();
    expect(rendered.onNodesChange).not.toHaveBeenCalled();
  });

  it("ignores keys outside the canvas and inside editable controls", () => {
    const rendered = renderCanvas({ selectedNodeId: "implement" });
    const canvas = document.querySelector(".workflow-canvas")!;
    fireEvent.keyDown(window, { key: "Delete" });
    for (const tag of ["input", "textarea", "select"]) {
      const field = document.createElement(tag);
      canvas.appendChild(field);
      fireEvent.keyDown(field, { key: "Backspace" });
      field.remove();
    }
    const editable = document.createElement("div");
    editable.setAttribute("contenteditable", "true");
    canvas.appendChild(editable);
    fireEvent.keyDown(editable, { key: "Delete" });
    editable.remove();
    fireEvent.keyDown(canvas, { key: "Delete", repeat: true });
    fireEvent.keyDown(canvas, { key: "Backspace", ctrlKey: true });
    expect(rendered.onDeleteNode).not.toHaveBeenCalled();
  });

  it("does not delete nodes from a read-only template", () => {
    const rendered = renderCanvas({ selectedNodeId: "implement", readOnly: true });
    fireEvent.keyDown(document.querySelector(".workflow-canvas")!, { key: "Delete" });
    expect(rendered.onDeleteNode).not.toHaveBeenCalled();
  });
});