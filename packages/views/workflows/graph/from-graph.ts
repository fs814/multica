/**
 * xyflow graph -> `WorkflowDefinition`. The exact inverse of `to-graph.ts`.
 *
 * This file exists for one property: **opening a template and saving it without
 * editing must be a no-op.** A lossy collapse here would not fail loudly - it
 * would silently drop whatever the canvas does not draw (`submission_schema`,
 * `acceptance_criteria`, `max_attempts`, `join_policy`, `join_sources`,
 * `fan_out_max`, `instruction`, `limits`, and any forward-compatible key a newer
 * server added) the first time a user opened a graph to move one card. The
 * damage would land in a *published* version and be indistinguishable from an
 * author's intent.
 *
 * The rule that guarantees it: **spread, then override only what the graph
 * owns.** The graph owns exactly `next`, `branches`, `rework_targets`, the node
 * set, and `entry_node`. Everything else comes from the node object the canvas
 * has been carrying around untouched since `definitionToGraph`.
 *
 * Positions are deliberately NOT written back. A definition is the engine's
 * contract; pixel coordinates are not part of it, and storing them would make a
 * pure pan produce a new template version.
 */

import type {
  WorkflowBranch,
  WorkflowDefinition,
  WorkflowNode,
} from "@multica/core/workflows";
import type { FlowEdge, FlowNode } from "./types";

/**
 * Collapses the canvas back into a definition.
 *
 * `base` supplies everything outside the graph - `schema_version`, `limits`, and
 * any key a newer server added to the envelope - plus the declaration order of
 * `nodes`.
 *
 * Node order follows `base.nodes` rather than the canvas array, with newly added
 * nodes appended in canvas order. xyflow reorders its own node array (selection
 * raises a node above its siblings so its edges are not painted over), and
 * without this the JSON would churn on nothing more than a click - which would
 * defeat the no-op property above and make every diff unreadable.
 */
export function graphToDefinition(
  nodes: readonly FlowNode[],
  edges: readonly FlowEdge[],
  base: WorkflowDefinition,
): WorkflowDefinition {
  const ordered = orderNodes(nodes, base);

  // Edges address nodes by xyflow id, which is the node key at the time the
  // edge was created. A rename in flight can make id and `data.node.key`
  // disagree for a render, so every endpoint is translated through the live
  // id -> key map. An id with no matching node is passed through verbatim: that
  // is a dangling edge, and the author needs the validator to name the target
  // they typed, not to have it quietly disappear.
  const keyById = new Map<string, string>();
  for (const flowNode of ordered) {
    keyById.set(flowNode.id, flowNode.data.node.key);
  }
  const resolve = (id: string) => keyById.get(id) ?? id;

  const outgoing = groupEdgesBySource(edges);

  const definitionNodes = ordered.map((flowNode) => {
    const source = flowNode.data.node;
    const own = outgoing.get(flowNode.id);
    const definitionNode = {
      // Spread first, override only the three graph-owned fields: this is the
      // whole reason the round trip is lossless. Note `key` comes from the
      // spread - the key the properties panel has committed - and never from the
      // (possibly stale) xyflow id.
      ...source,
      next: (own?.next ?? []).map((edge) => resolve(edge.target)),
      branches: (own?.branch ?? []).map((edge) => toBranch(edge, resolve)),
      rework_targets: (own?.rework ?? []).map((edge) => resolve(edge.target)),
    } satisfies WorkflowNode;

    // input_mode is a known, type-owned field rather than a forward-compatible
    // extension: the server rejects it on every non-input node. Older clients
    // accidentally injected "text" while parsing every node, so an existing
    // draft may already carry the pollution. Clean it only at the outgoing
    // boundary, leaving `saved` untouched; the editor then becomes dirty and
    // offers the author the repair save that must happen before publish.
    if (definitionNode.type !== "input") {
      delete definitionNode.input_mode;
    }

    return definitionNode;
  });

  return {
    ...base,
    entry_node: resolveEntryNode(ordered, base),
    nodes: definitionNodes,
  };
}

/**
 * Reconstructs one branch.
 *
 * `data.branch` is spread so a branch's forward-compatible extra keys survive -
 * the wire schema is `.loose()`, so a newer server may put fields on a branch
 * that this build has no reason to understand but no right to delete.
 */
function toBranch(
  edge: FlowEdge,
  resolve: (id: string) => string,
): WorkflowBranch {
  return {
    ...(edge.data?.branch ?? {}),
    when_verdict: edge.data?.verdict ?? "",
    target: resolve(edge.target),
  };
}

type OutgoingEdges = {
  next: FlowEdge[];
  branch: FlowEdge[];
  rework: FlowEdge[];
};

/**
 * Buckets edges by source and kind, preserving array order within each bucket.
 *
 * Order is the only thing that carries a branch's position in `branches[]`, so
 * this must be a stable partition and not, say, a sort by target.
 */
function groupEdgesBySource(
  edges: readonly FlowEdge[],
): Map<string, OutgoingEdges> {
  const bySource = new Map<string, OutgoingEdges>();
  for (const edge of edges) {
    let bucket = bySource.get(edge.source);
    if (!bucket) {
      bucket = { next: [], branch: [], rework: [] };
      bySource.set(edge.source, bucket);
    }
    // An untagged edge is treated as a forward `next` edge: xyflow creates edges
    // from a user drag before any of our code sees them, and `next` is the only
    // kind that is meaningful for every node type. Guessing `rework` instead
    // would invent a cycle the author never declared.
    bucket[edge.data?.kind ?? "next"].push(edge);
  }
  return bySource;
}

/**
 * Emits nodes in `base.nodes` order, then canvas-only nodes in canvas order.
 * Nodes absent from the canvas were deleted and are dropped.
 */
function orderNodes(
  nodes: readonly FlowNode[],
  base: WorkflowDefinition,
): FlowNode[] {
  const remaining = new Map<string, FlowNode>();
  for (const flowNode of nodes) {
    // First occurrence wins, mirroring `definitionToGraph`'s duplicate-id rule
    // so the two directions describe the same graph.
    if (!remaining.has(flowNode.id)) remaining.set(flowNode.id, flowNode);
  }

  const ordered: FlowNode[] = [];
  for (const baseNode of base.nodes) {
    const flowNode = remaining.get(baseNode.key);
    if (!flowNode) continue;
    remaining.delete(baseNode.key);
    ordered.push(flowNode);
  }
  // Whatever is left was added on the canvas; `remaining` is insertion-ordered.
  for (const flowNode of remaining.values()) ordered.push(flowNode);
  return ordered;
}

/**
 * Entry is a graph property the canvas models (`data.isEntry`), so it is read
 * back from the nodes.
 *
 * Falling back to `base.entry_node` matters for a graph whose entry names a node
 * that does not exist: no node can be flagged, and clearing the field would swap
 * the validator's precise "entry_node %q is not a declared node" for a vaguer
 * "entry_node is empty", hiding the typo the author needs to see.
 */
function resolveEntryNode(
  ordered: readonly FlowNode[],
  base: WorkflowDefinition,
): string {
  for (const flowNode of ordered) {
    if (flowNode.data.isEntry) return flowNode.data.node.key;
  }
  return base.entry_node;
}
