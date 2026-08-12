/**
 * Linearization of a pinned workflow graph for reading.
 *
 * A canvas is deliberately NOT part of this release (plan section 9: the
 * visual editor waits until fan-out / join semantics settle), so the detail
 * page renders the graph as an ordered step sequence. That is only honest if
 * the ordering is derived from the graph itself rather than from the order
 * nodes happen to sit in the JSON array — an author reordering `nodes[]` must
 * not change what a reader sees.
 *
 * The walk follows the FIRST outgoing edge of each node, which is the spine
 * of the graph: agent / acceptance / join / fan_out nodes have exactly one
 * successor in the first release, so the spine is the whole story for them.
 * A condition node's remaining branches are surfaced on the node card itself,
 * not by forking the sequence — a reader needs one authoritative reading
 * order, and branch targets are all present later in the list anyway.
 *
 * Rework edges are excluded from the walk on purpose: they are the only
 * cycles the graph permits and they run BACKWARDS. Rendering them inline
 * would turn a bounded cycle into an infinite sequence; they are shown as
 * explicit "on failure -> target" lines instead.
 *
 * Anything the walk never reaches is still returned, flagged `onChain: false`,
 * so a graph with an orphaned node shows the orphan rather than silently
 * hiding it. The server validator rejects unreachable nodes, but the UI must
 * survive a graph published by a newer or buggier server.
 */

/** Minimal shape the walk needs — deliberately structural, not the API type. */
export interface LinearizableNode {
  key: string;
  next?: readonly string[] | null;
}

export interface OrderedGraphNode<N extends LinearizableNode> {
  node: N;
  /** Position in the reading order, 1-based (what the UI numbers steps by). */
  index: number;
  /** False for nodes the entry walk never reached. */
  onChain: boolean;
}

export function linearizeGraph<N extends LinearizableNode>(
  nodes: readonly N[],
  entryNode: string | null | undefined,
): OrderedGraphNode<N>[] {
  const byKey = new Map<string, N>();
  for (const node of nodes) {
    // First declaration wins: a duplicate key is invalid, and picking the
    // first keeps the reading order stable instead of depending on which
    // duplicate the walk happens to land on.
    if (!byKey.has(node.key)) byKey.set(node.key, node);
  }

  const ordered: OrderedGraphNode<N>[] = [];
  const seen = new Set<string>();

  let cursor = entryNode ?? null;
  while (cursor) {
    // `seen` is what bounds the walk. A condition branch pointing back at an
    // earlier node is legal JSON, so without this the loop never terminates.
    if (seen.has(cursor)) break;
    const node = byKey.get(cursor);
    if (!node) break;
    seen.add(cursor);
    ordered.push({ node, index: ordered.length + 1, onChain: true });
    cursor = node.next?.[0] ?? null;
  }

  for (const node of nodes) {
    // `seen` doubles as the dedupe set here: a duplicate key is invalid (edges
    // address nodes by key, so the shadowed declaration is unreachable by
    // definition) and emitting one card per key beats showing two steps a
    // reader cannot tell apart.
    if (seen.has(node.key)) continue;
    seen.add(node.key);
    ordered.push({ node, index: ordered.length + 1, onChain: false });
  }

  return ordered;
}
