"use client";

/**
 * The one edge renderer, plus the arrowhead definitions it references.
 *
 * An edge's appearance here is *semantics*, not decoration, and that is the only
 * reason this file exists instead of using xyflow's default edge:
 *
 *  1. **Colour encodes the path.** Emerald is the path the run takes when work
 *     succeeds; rose is the path it takes when work fails. A workflow author's
 *     first question about a graph is "what happens when this step fails?", and
 *     on a graph with a dozen steps that question is answered by following one
 *     colour rather than by opening a dozen properties panels.
 *
 *  2. **Rework is dashed.** The server permits a cycle ONLY through declared
 *     `rework_targets` (`validateAcyclic` in server/internal/workflow/validate.go).
 *     So a backward edge is either a bounded, legal loop or an illegal forward
 *     cycle the author just created by mistake - and those two look identical
 *     unless rework is drawn differently. Dashed says "this loop is declared and
 *     bounded"; a solid line that happens to run right-to-left says "you have a
 *     bug". Colour alone could not carry that, because a rework edge and a failed
 *     branch are both failure paths.
 *
 *  3. **The label is the verdict, translated.** `edgeLabelKey()` in the graph
 *     model returns a *runtime* key string, which cannot be handed to the
 *     selector-form `t($ => $.a.b)` this repo uses, so the mapping is re-expressed
 *     below as an explicit switch. That is deliberate duplication: the model owns
 *     which token an edge carries, this file owns how that token reads to a
 *     human, and the switch is what makes a missing translation a compile error.
 *
 * One component is registered under all three edge kinds rather than three
 * near-identical components: the kind only selects a row out of {@link EDGE_VISUAL},
 * and three files would let the label pill drift apart between them.
 */

import { memo } from "react";
import {
  BaseEdge,
  EdgeLabelRenderer,
  getBezierPath,
  type EdgeProps,
} from "@xyflow/react";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../../i18n";
import type { EdgeKind, EditorEdge, FlowEdge } from "../graph";

/**
 * The five visual roles an edge can play, keyed on what the edge *means*.
 *
 * Strokes are CSS custom properties defined in workflow-canvas.css and
 * redefined under `.dark`, so the palette follows the app theme from one place.
 * `marker` names an arrowhead from {@link WorkflowEdgeMarkers} - the arrowhead
 * has to match the stroke or a rose edge would end in a green point.
 */
const EDGE_VISUAL = {
  pass: { stroke: "var(--wf-edge-pass)", marker: "wf-arrow-pass", dashed: false },
  fail: { stroke: "var(--wf-edge-fail)", marker: "wf-arrow-fail", dashed: false },
  blocked: {
    stroke: "var(--wf-edge-blocked)",
    marker: "wf-arrow-blocked",
    dashed: false,
  },
  neutral: {
    stroke: "var(--wf-edge-default)",
    marker: "wf-arrow-default",
    dashed: false,
  },
  // Rose *and* dashed: see rule 2 in the file header. The dash is the part that
  // distinguishes a declared loop from an illegal one, so it must never be
  // dropped in favour of colour alone.
  rework: {
    stroke: "var(--wf-edge-rework)",
    marker: "wf-arrow-fail",
    dashed: true,
  },
} as const;

type EdgeVisualRole = keyof typeof EDGE_VISUAL;

/**
 * Arrowhead `<marker>` elements, referenced by `url(#id)` from every edge path.
 *
 * Hand-written rather than xyflow's `MarkerType.ArrowClosed` because xyflow bakes
 * the marker's colour into the generated marker id. Our colours are
 * `var(--wf-edge-*)`, and a `var(...)` token inside an element id would produce an
 * `url(#...)` reference the browser cannot resolve - the arrowheads would simply
 * not render. Declaring them ourselves keeps the colour a CSS variable, which is
 * what makes the arrows follow the theme with the strokes.
 *
 * Rendered once per canvas as a zero-sized SVG. Markers resolve across SVG roots
 * within a document, and keeping them out of the flow's own `<svg>` means xyflow
 * re-rendering the edge layer cannot remove them.
 */
export function WorkflowEdgeMarkers() {
  return (
    <svg
      aria-hidden="true"
      focusable="false"
      style={{ position: "absolute", width: 0, height: 0, overflow: "hidden" }}
    >
      <defs>
        {(
          [
            ["wf-arrow-pass", "var(--wf-edge-pass)"],
            ["wf-arrow-fail", "var(--wf-edge-fail)"],
            ["wf-arrow-blocked", "var(--wf-edge-blocked)"],
            ["wf-arrow-default", "var(--wf-edge-default)"],
          ] as const
        ).map(([id, fill]) => (
          <marker
            key={id}
            id={id}
            viewBox="0 0 10 10"
            refX="9"
            refY="5"
            markerWidth="6"
            markerHeight="6"
            orient="auto-start-reverse"
          >
            <path d="M 0 1 L 9 5 L 0 9 z" fill={fill} />
          </marker>
        ))}
      </defs>
    </svg>
  );
}

/** Which visual role an edge plays. Mirrors `edgeLabelKey()`'s branch structure. */
function visualRole(kind: EdgeKind, verdict: string | undefined): EdgeVisualRole {
  if (kind === "rework") return "rework";
  if (kind === "branch") {
    switch (verdict) {
      case "pass":
        return "pass";
      case "fail":
        return "fail";
      case "blocked":
        return "blocked";
      // Both the default branch (`""`) and a verdict a newer server invented get
      // the neutral role: claiming one of them is the success path would be a
      // guess about routing this build cannot make.
      default:
        return "neutral";
    }
  }
  // A `next` edge is the spine - the path taken when the step succeeds - so it
  // shares the success colour. An untagged edge (one the user just dragged,
  // before `data` exists) lands here too, matching `groupEdgesBySource`'s
  // treatment of it as a forward edge.
  return "pass";
}

/**
 * The pill text.
 *
 * A `default:` arm that returns the raw verdict is load-bearing: the wire schema
 * keeps `when_verdict` a `z.string()` so a newer server's verdict reaches this
 * build, and labelling it "pass" would describe routing that will not happen.
 */
function useEdgeLabel(data: EditorEdge | undefined): string {
  const { t } = useT("workflows");
  if (!data || data.kind === "next") return t(($) => $.graph.edge.next);
  if (data.kind === "rework") return t(($) => $.graph.edge.rework);
  if (!data.verdict) return t(($) => $.graph.edge.default);
  switch (data.verdict) {
    case "pass":
      return t(($) => $.graph.edge.verdict.pass);
    case "fail":
      return t(($) => $.graph.edge.verdict.fail);
    case "blocked":
      return t(($) => $.graph.edge.verdict.blocked);
    default:
      return data.verdict;
  }
}

/**
 * Registered under `next`, `branch` and `rework` - see canvas/edge-types.ts.
 *
 * `memo` because xyflow re-renders the edge layer on every viewport change; an
 * un-memoised edge would re-run `getBezierPath` on each frame of a pan.
 */
export const WorkflowEdge = memo(function WorkflowEdge({
  sourceX,
  sourceY,
  targetX,
  targetY,
  sourcePosition,
  targetPosition,
  data,
  selected,
}: EdgeProps<FlowEdge>) {
  const label = useEdgeLabel(data);
  const visual = EDGE_VISUAL[visualRole(data?.kind ?? "next", data?.verdict)];

  const [path, labelX, labelY] = getBezierPath({
    sourceX,
    sourceY,
    sourcePosition,
    targetX,
    targetY,
    targetPosition,
  });

  return (
    <>
      <BaseEdge
        path={path}
        markerEnd={`url(#${visual.marker})`}
        style={{
          stroke: visual.stroke,
          // A selected edge thickens rather than changing colour: the colour is
          // the edge's meaning, so selection must not be able to make a rework
          // edge read as a success path.
          strokeWidth: selected ? 2.5 : 1.5,
          strokeDasharray: visual.dashed ? "6 4" : undefined,
        }}
      />
      <EdgeLabelRenderer>
        <div
          // `nodrag nopan` is required: the label lives in an overlay above the
          // pane, so without it a click on the pill would start a canvas pan
          // instead of selecting the edge under it.
          className={cn(
            "nodrag nopan pointer-events-none absolute rounded-full border border-border bg-card px-1.5 py-0.5 text-[10px] leading-none font-medium text-card-foreground shadow-sm",
          )}
          style={{
            transform: `translate(-50%, -50%) translate(${labelX}px, ${labelY}px)`,
          }}
        >
          {label}
        </div>
      </EdgeLabelRenderer>
    </>
  );
});
