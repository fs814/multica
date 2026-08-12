/**
 * `WorkflowDefinition` -> xyflow graph.
 *
 * The definition stores edges *on the nodes* (`next`, `branches`,
 * `rework_targets`) while xyflow needs a flat edge list, so this file is the
 * expansion half of that impedance mismatch; `from-graph.ts` is the collapse
 * half and must stay its exact inverse.
 *
 * Three properties are load-bearing:
 *
 *  1. **Every edge is tagged with its kind.** The server permits cycles only
 *     through declared `rework_targets`, so a rework edge that renders like a
 *     `next` edge would show the author a graph that appears to contain an
 *     illegal forward cycle. The kind also gates layout ranking.
 *
 *  2. **`data.node` is the verbatim source node**, not a projection. See
 *     `types.ts` - it is what makes saving lossless.
 *
 *  3. **Dangling edges are still emitted.** A `next` pointing at a deleted node
 *     is the most common authoring mistake and the thing the author most needs
 *     to see. Dropping it would make the canvas disagree with the validator's
 *     complaint. xyflow tolerates an edge whose endpoint is missing (it renders
 *     nothing), and `clientValidateGraph` names it.
 */

import type { WorkflowDefinition, WorkflowNode } from "@multica/core/workflows";
import { autoLayout } from "./layout";
import {
  editorEdgeId,
  editorNodeId,
  toEditorNodeType,
  type EdgeKind,
  type EditorGraph,
  type FlowEdge,
  type FlowNode,
} from "./types";

/**
 * Expands a definition into positioned xyflow nodes plus a flat edge list.
 *
 * Positions come from {@link autoLayout} because the definition does not store
 * any: a definition is the engine's contract, and pixel coordinates are not part
 * of it. Laying out here rather than leaving every caller to remember means a
 * template always opens readable instead of as a pile of cards at the origin.
 * Layout is deterministic, so this is stable across reloads.
 */
export function definitionToGraph(def: WorkflowDefinition): EditorGraph {
  const nodes = definitionNodes(def);
  const edges = definitionEdges(def);
  return { nodes: autoLayout(nodes, edges), edges };
}

function definitionNodes(def: WorkflowDefinition): FlowNode[] {
  const seen = new Set<string>();
  const nodes: FlowNode[] = [];

  for (const node of def.nodes) {
    const id = editorNodeId(node.key);
    // A duplicate key is invalid (the validator rejects it) but can still
    // arrive from a hand-edited JSON draft. xyflow silently drops the second
    // node with a repeated id, so dropping it here explicitly keeps the canvas
    // and `clientValidateGraph` describing the same graph - and keeps the first
    // declaration winning, matching `linearizeGraph`.
    if (seen.has(id)) continue;
    seen.add(id);

    nodes.push({
      id,
      type: toEditorNodeType(node.type),
      // Overwritten by autoLayout; present because xyflow's Node type requires
      // a position and a node without one is dropped from the store.
      position: { x: 0, y: 0 },
      data: {
        id,
        nodeKey: node.key,
        type: toEditorNodeType(node.type),
        isEntry: node.key === def.entry_node && node.key !== "",
        node,
      },
    });
  }

  return nodes;
}

/**
 * Flattens the three edge-bearing fields into one list.
 *
 * The emission order is part of the contract: `from-graph.ts` reconstructs
 * `next` / `branches` / `rework_targets` from the order edges appear in this
 * array, which is how a round trip preserves list order without storing an
 * index anywhere. Grouping per node (all of a node's edges together, in
 * next -> branch -> rework order) rather than per kind keeps that reconstruction
 * a single pass.
 */
function definitionEdges(def: WorkflowDefinition): FlowEdge[] {
  const edges: FlowEdge[] = [];

  for (const node of def.nodes) {
    edges.push(...nodeEdges(node));
  }

  return edges;
}

/**
 * The edges one node declares, in the contract's next -> branch -> rework order.
 *
 * Exported because the properties panel edits `branches` and `rework_targets` on
 * the node object while `graphToDefinition` reads them back off the EDGE list. A
 * panel edit must therefore re-derive that node's edges, and it has to do so with
 * exactly this function: hand-rolling the expansion in the reducer is how the two
 * halves drift, and a drift here silently discards the author's edit on save.
 */
export function nodeEdges(node: WorkflowNode): FlowEdge[] {
  const edges: FlowEdge[] = [];

  node.next.forEach((target, index) => {
    edges.push(makeEdge("next", node, index, target));
  });

  node.branches.forEach((branch, index) => {
    const edge = makeEdge("branch", node, index, branch.target);
    edge.data = {
      ...edge.data!,
      verdict: branch.when_verdict,
      // An empty verdict is the condition's default branch - the validator
      // permits at most one, and the canvas labels it differently from a
      // verdict branch.
      isDefaultBranch: branch.when_verdict === "",
      branch,
    };
    edges.push(edge);
  });

  node.rework_targets.forEach((target, index) => {
    edges.push(makeEdge("rework", node, index, target));
  });

  return edges;
}

function makeEdge(
  kind: EdgeKind,
  source: WorkflowNode,
  index: number,
  target: string,
): FlowEdge {
  return {
    id: editorEdgeId(kind, source.key, index, target),
    source: editorNodeId(source.key),
    target: editorNodeId(target),
    type: kind,
    data: { kind, sourceKey: source.key, targetKey: target },
  };
}
