import type { WorkflowDefinition, WorkflowNode } from "@multica/core/workflows";

/** Forward edges are the execution graph; rework edges are deliberately absent. */
function forwardTargets(node: WorkflowNode): string[] {
  const targets = [...node.next];
  for (const branch of node.branches) {
    if (branch.target !== "") targets.push(branch.target);
  }
  return targets;
}

function reachableFrom(
  byKey: ReadonlyMap<string, WorkflowNode>,
  start: string,
): Set<string> {
  const seen = new Set<string>();
  const stack = [start];
  while (stack.length > 0) {
    const key = stack.pop()!;
    if (seen.has(key)) continue;
    seen.add(key);
    const node = byKey.get(key);
    if (!node) continue;
    for (const target of forwardTargets(node)) {
      if (!seen.has(target)) stack.push(target);
    }
  }
  return seen;
}

/**
 * Legal rework destinations for `sourceKey`, in declaration order.
 *
 * A destination must be on a real forward path from entry -> target -> source.
 * This is stricter than merely "declared and not self": returning work to a
 * downstream or disconnected box would activate a step that did not produce
 * the result the source is rejecting. Input is excluded independently because
 * the engine cannot re-prompt the human who started the run.
 */
export function legalReworkTargetNodes(
  definition: WorkflowDefinition,
  sourceKey: string,
): WorkflowNode[] {
  const byKey = new Map<string, WorkflowNode>();
  for (const node of definition.nodes) {
    if (node.key !== "" && !byKey.has(node.key)) byKey.set(node.key, node);
  }
  if (!byKey.has(sourceKey) || !byKey.has(definition.entry_node)) return [];

  const reachableFromEntry = reachableFrom(byKey, definition.entry_node);
  const reverse = new Map<string, string[]>();
  for (const node of definition.nodes) {
    for (const target of forwardTargets(node)) {
      const sources = reverse.get(target) ?? [];
      sources.push(node.key);
      reverse.set(target, sources);
    }
  }
  const upstream = new Set<string>();
  const stack = [sourceKey];
  while (stack.length > 0) {
    const key = stack.pop()!;
    if (upstream.has(key)) continue;
    upstream.add(key);
    for (const source of reverse.get(key) ?? []) {
      if (!upstream.has(source)) stack.push(source);
    }
  }

  return definition.nodes.filter(
    (candidate) =>
      candidate.key !== "" &&
      candidate.key !== sourceKey &&
      candidate.type !== "input" &&
      reachableFromEntry.has(candidate.key) &&
      upstream.has(candidate.key),
  );
}
