// @vitest-environment jsdom

/**
 * The edge renderer, exercised directly.
 *
 * Not through `<WorkflowCanvas />`: jsdom has no layout, so xyflow measures every
 * node as 0x0 and never computes edge geometry - the edge layer comes out empty
 * whether or not the registry is right. Rendering the component with the geometry
 * the library would have supplied is the only way to assert the three things that
 * are *semantics* rather than styling:
 *
 *  1. a rework edge is visually distinct from a forward edge, because the server
 *     permits cycles ONLY through declared rework_targets and an author who cannot
 *     tell a bounded loop from an accidental forward cycle cannot fix either;
 *  2. the pill reads out the verdict, translated, with the raw token surviving for
 *     a verdict a newer server invented;
 *  3. each stroke's arrowhead matches its stroke.
 */

import { describe, expect, it } from "vitest";
import { render } from "@testing-library/react";
import { Position, ReactFlowProvider } from "@xyflow/react";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../locales/en/common.json";
import enWorkflows from "../../locales/en/workflows.json";
import type { EdgeKind, EditorEdge } from "../graph";
import { workflowEdgeTypes } from "./edge-types";
import { WorkflowEdge } from "./workflow-edge";

const TEST_RESOURCES = { en: { common: enCommon, workflows: enWorkflows } };

function renderEdge(data: EditorEdge | undefined) {
  const view = render(
    <I18nProvider locale="en" resources={TEST_RESOURCES}>
      {/* The provider is required because EdgeLabelRenderer portals into the
          flow's own DOM node; with no flow mounted it renders nothing, which is
          why the label is read out of the returned tree instead. */}
      <ReactFlowProvider>
        <svg>
          <WorkflowEdge
            id="e1"
            source={data?.sourceKey ?? "a"}
            target={data?.targetKey ?? "b"}
            sourceX={0}
            sourceY={0}
            targetX={200}
            targetY={0}
            sourcePosition={Position.Right}
            targetPosition={Position.Left}
            data={data}
          />
        </svg>
      </ReactFlowProvider>
    </I18nProvider>,
  );
  const path = view.container.querySelector("path.react-flow__edge-path");
  return { view, path };
}

function edge(patch: Partial<EditorEdge> & { kind: EdgeKind }): EditorEdge {
  return { sourceKey: "a", targetKey: "b", ...patch };
}

describe("WorkflowEdge", () => {
  it("draws a rework edge dashed, so a declared loop is not mistaken for a bug", () => {
    const { path } = renderEdge(edge({ kind: "rework" }));
    // The dash is the load-bearing part: colour alone cannot carry it, because a
    // rework edge and a failed branch are both failure paths and share the hue.
    expect(path?.getAttribute("style")).toContain("dasharray");
    expect(path?.getAttribute("style")).toContain("--wf-edge-rework");
  });

  it("draws the forward spine solid and in the success colour", () => {
    const { path } = renderEdge(edge({ kind: "next" }));
    expect(path?.getAttribute("style")).not.toContain("dasharray");
    expect(path?.getAttribute("style")).toContain("--wf-edge-pass");
  });

  it("colours a branch by its verdict", () => {
    expect(
      renderEdge(edge({ kind: "branch", verdict: "pass" })).path?.getAttribute(
        "style",
      ),
    ).toContain("--wf-edge-pass");
    expect(
      renderEdge(edge({ kind: "branch", verdict: "fail" })).path?.getAttribute(
        "style",
      ),
    ).toContain("--wf-edge-fail");
    expect(
      renderEdge(
        edge({ kind: "branch", verdict: "blocked" }),
      ).path?.getAttribute("style"),
    ).toContain("--wf-edge-blocked");
  });

  it("gives an unknown verdict the neutral stroke rather than guessing", () => {
    // `when_verdict` is a lenient `z.string()` on the wire, so a verdict from a
    // newer server reaches this build. Painting it emerald would claim it is the
    // success path - an assertion about routing this build cannot make.
    const { path } = renderEdge(
      edge({ kind: "branch", verdict: "needs_review" }),
    );
    expect(path?.getAttribute("style")).toContain("--wf-edge-default");
  });

  it("matches each arrowhead to its stroke", () => {
    // A rose edge ending in a green point would misreport which path it is.
    expect(
      renderEdge(edge({ kind: "rework" })).path?.getAttribute("marker-end"),
    ).toBe("url(#wf-arrow-fail)");
    expect(
      renderEdge(edge({ kind: "next" })).path?.getAttribute("marker-end"),
    ).toBe("url(#wf-arrow-pass)");
    expect(
      renderEdge(
        edge({ kind: "branch", verdict: "blocked" }),
      ).path?.getAttribute("marker-end"),
    ).toBe("url(#wf-arrow-blocked)");
  });

  it("treats an untagged edge as forward, matching the model's collapse rule", () => {
    // xyflow creates an edge from a user drag before any of our code sees it, and
    // `groupEdgesBySource` in graph/from-graph.ts buckets an untagged edge as
    // `next`. Drawing it as rework here would show a cycle nobody declared.
    const { path } = renderEdge(undefined);
    expect(path?.getAttribute("style")).toContain("--wf-edge-pass");
    expect(path?.getAttribute("style")).not.toContain("dasharray");
  });

  it("registers one renderer under every edge kind the model emits", () => {
    // xyflow falls back to its own bezier edge for an unregistered type, which
    // would silently drop the pass/fail colour AND the rework dash - i.e. render a
    // legal bounded loop and an illegal forward cycle identically.
    expect(Object.keys(workflowEdgeTypes).sort()).toEqual([
      "branch",
      "next",
      "rework",
    ]);
    for (const component of Object.values(workflowEdgeTypes)) {
      expect(component).toBe(WorkflowEdge);
    }
  });
});
