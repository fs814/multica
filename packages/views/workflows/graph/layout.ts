/**
 * Deterministic layered left-to-right layout, dependency-free.
 *
 * Two properties are non-negotiable, and everything below follows from them:
 *
 *  1. **Rework edges must not influence ranks.** They are the only edges the
 *     server permits to run backwards (`validateAcyclic` in
 *     server/internal/workflow/validate.go excludes `rework_targets` from its
 *     cycle check for exactly this reason). Ranking them would make the rank
 *     relation cyclic - `implement -> analyze` alongside `analyze -> implement` -
 *     and the longest-path relaxation would never settle. So they are dropped
 *     before ranking and only ever drawn.
 *
 *  2. **Same input must give byte-identical output.** A user pressing 自动布局
 *     twice must see nothing move, and an unedited template must open in the
 *     same place every time. That rules out anything that iterates a `Set`/`Map`
 *     keyed on something the caller can reorder, or that breaks ties on
 *     floating-point work. Every ordering below is derived from the input node
 *     array's index, which is derived from `nodes[]` declaration order.
 *
 * The algorithm is longest-path layering: rank(n) = 1 + max(rank(pred)) over
 * forward edges, which puts each node as far right as its deepest dependency
 * requires. Iterative relaxation bounded by the node count is used rather than a
 * topological sort because a *forward* cycle is illegal but perfectly
 * constructible on the canvas - the author has to be able to see the mistake
 * they just made, and it is `clientValidateGraph`'s job, not layout's, to
 * complain about it.
 */

import type { FlowEdge, FlowNode } from "./types";

/** Horizontal distance between layers. Wide enough for a node card + label. */
export const COL_GAP = 320;
/** Vertical distance between siblings within a layer. */
export const ROW_GAP = 160;

/**
 * Assigns positions to every node, returning new node objects.
 *
 * Returns new objects rather than mutating: xyflow compares node identity to
 * decide what to re-render, and React state must not be mutated in place.
 */
export function autoLayout(
  nodes: readonly FlowNode[],
  edges: readonly FlowEdge[],
): FlowNode[] {
  if (nodes.length === 0) return [];

  const indexById = new Map<string, number>();
  nodes.forEach((node, index) => {
    // First occurrence wins, matching `definitionToGraph`: a duplicate id is
    // invalid, and the alternative is two cards stacked at the same point.
    if (!indexById.has(node.id)) indexById.set(node.id, index);
  });

  const ranks = computeRanks(nodes, edges, indexById);
  const byRank = groupByRank(nodes, ranks);

  // Center each layer vertically about y = 0 so a graph with a wide fan-out
  // stays visually balanced around its spine instead of hanging below it.
  const positioned = new Map<string, { x: number; y: number }>();
  for (const [rank, members] of byRank) {
    const height = (members.length - 1) * ROW_GAP;
    members.forEach((node, slot) => {
      positioned.set(node.id, {
        x: rank * COL_GAP,
        y: slot * ROW_GAP - height / 2,
      });
    });
  }

  return nodes.map((node) => {
    const position = positioned.get(node.id);
    // A duplicate id has no slot of its own; leaving its position untouched is
    // better than throwing, because the canvas has to keep rendering.
    return position ? { ...node, position } : node;
  });
}

/**
 * Longest-path ranks over forward edges only.
 *
 * The relaxation runs at most `nodes.length` passes. On a DAG that is strictly
 * more than enough (the longest path has fewer edges than there are nodes). On a
 * graph with an illegal forward cycle it is what makes this terminate at all:
 * the members of the cycle simply stop being pushed right, which draws them
 * stacked in adjacent layers - visibly wrong, which is the point.
 */
function computeRanks(
  nodes: readonly FlowNode[],
  edges: readonly FlowEdge[],
  indexById: ReadonlyMap<string, number>,
): Map<string, number> {
  const forward: Array<{ source: string; target: string }> = [];
  for (const edge of edges) {
    // Untagged edges count as forward, matching `groupEdgesBySource` in
    // from-graph.ts: an edge the user just dragged has no `data` yet, and it
    // would be worse to have a new edge not move its target right.
    if (edge.data?.kind === "rework") continue;
    // Skip edges with an endpoint that is not on the canvas: a dangling edge
    // must not be able to invent a rank for a node that does not exist.
    if (!indexById.has(edge.source) || !indexById.has(edge.target)) continue;
    // A self-edge is illegal and would only ever push a node right of itself.
    if (edge.source === edge.target) continue;
    forward.push({ source: edge.source, target: edge.target });
  }

  const ranks = new Map<string, number>();
  for (const node of nodes) ranks.set(node.id, 0);

  for (let pass = 0; pass < nodes.length; pass++) {
    let changed = false;
    // Edge iteration order does not affect the fixpoint (max is commutative),
    // but iterating the array keeps each pass allocation-free.
    for (const { source, target } of forward) {
      const candidate = (ranks.get(source) ?? 0) + 1;
      if (candidate > (ranks.get(target) ?? 0)) {
        ranks.set(target, candidate);
        changed = true;
      }
    }
    if (!changed) break;
  }

  return ranks;
}

/**
 * Buckets nodes into layers, ordering both the layers and the members within
 * each layer deterministically.
 *
 * Within a layer, members are ordered by their index in the input array, i.e. by
 * `nodes[]` declaration order. That is a *stable* key the caller controls, unlike
 * insertion order into a `Map` that xyflow is free to reshuffle when a node is
 * selected.
 */
function groupByRank(
  nodes: readonly FlowNode[],
  ranks: ReadonlyMap<string, number>,
): Array<[number, FlowNode[]]> {
  const byRank = new Map<number, FlowNode[]>();
  const seen = new Set<string>();
  for (const node of nodes) {
    if (seen.has(node.id)) continue;
    seen.add(node.id);
    const rank = ranks.get(node.id) ?? 0;
    const members = byRank.get(rank);
    if (members) members.push(node);
    else byRank.set(rank, [node]);
  }
  // Sorting by rank (rather than trusting Map insertion order) is what makes the
  // x coordinates independent of which node happened to be declared first.
  return [...byRank.entries()].sort((a, b) => a[0] - b[0]);
}
