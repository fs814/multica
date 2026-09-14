import type {
  WorkflowDefinition,
  WorkflowPort,
  WorkflowDiagnostic,
} from "./schemas";

export function validateGraphV2(
  def: WorkflowDefinition,
  complete = true,
): string[] {
  return diagnoseGraphV2(def, complete).map((item) => item.message);
}
export function diagnoseGraphV2(
  def: WorkflowDefinition,
  complete = true,
): WorkflowDiagnostic[] {
  const errors: WorkflowDiagnostic[] = [];
  let fieldPath = "definition";
  let nodeKey: string | undefined;
  let edgeId: string | undefined;
  const add = (message: string) =>
    errors.push({
      code: "workflow_invalid_definition",
      message,
      fieldPath,
      nodeKey,
      edgeId,
    });
  const byKey = new Map(def.nodes.map((n) => [n.key, n]));
  const adjacency = new Map<string, string[]>();
  const ids = new Set<string>();
  const combinations = def.nodes
    .filter((n) => n.type === "condition")
    .reduce((count, n) => count * Math.max(1, n.branches.length), 1);
  if (combinations > 4096)
    add("Condition combinations exceed the static validation limit of 4096.");
  const claim = (id: string | undefined) => {
    if (!id || ids.has(id)) add("Edge IDs must be nonempty and unique.");
    if (id) ids.add(id);
  };
  const types = new Set([
    "string",
    "number",
    "boolean",
    "object",
    "array",
    "any",
  ]);
  for (const [nodeIndex, node] of def.nodes.entries()) {
    fieldPath = `nodes[${nodeIndex}]`;
    nodeKey = node.key;
    if (node.max_attempts < 0 || node.max_attempts > 10)
      add(
        `${node.key}: maximum attempts must be between 1 and 10 (0 uses the graph default).`,
      );
    adjacency.set(node.key, [
      ...node.next,
      ...node.branches.map((b) => b.target),
    ]);
    if (
      node.rework_targets.length ||
      (node.on_failure && node.on_failure !== "fail")
    )
      add(
        `${node.key}: schema 2 propagates final failure and rejects rework cycles.`,
      );
    if (node.join_policy && node.join_policy !== "fail_fast")
      add(`${node.key}: schema 2 waits for all activated predecessors.`);
    if ((node.next_ids?.length ?? 0) !== node.next.length)
      add(`${node.key}: each flow edge needs a stable ID.`);
    node.next_ids?.forEach(claim);
    if (
      complete &&
      node.routing?.from_node &&
      !def.nodes.some(
        (source) =>
          source.key === node.routing?.from_node &&
          source.next.includes(node.key),
      )
    )
      add(
        `${node.key}: routing source must be a direct ordinary control predecessor.`,
      );
    if (new Set(node.next).size !== node.next.length)
      add(`${node.key}: duplicate flow edge.`);
    node.branches.forEach((b) => claim(b.id));
    if (
      complete &&
      node.type === "condition" &&
      (node.next.length ||
        node.branches.filter((b) => !b.predicate && !b.when_verdict).length !==
          1)
    )
      add(
        `${node.key}: condition needs exactly one default branch and no ordinary flow exits.`,
      );
    for (const ports of [node.input_ports ?? [], node.output_ports ?? []]) {
      const seen = new Set<string>();
      for (const port of ports) {
        if (!port.id || seen.has(port.id) || !types.has(port.type))
          add(`${node.key}: ports need unique IDs and supported types.`);
        seen.add(port.id);
      }
    }
    for (const branch of node.branches)
      if (branch.predicate) {
        const port = node.input_ports?.find(
          (p) => p.id === branch.predicate?.input_port,
        );
        if (!port || !matches(port.type, branch.predicate.equals))
          add(
            `${node.key}: predicate needs a declared input and compatible value.`,
          );
      }
  }
  const incoming = new Map<
    string,
    NonNullable<WorkflowDefinition["data_edges"]>
  >();
  for (const [edgeIndex, edge] of (def.data_edges ?? []).entries()) {
    fieldPath = `data_edges[${edgeIndex}]`;
    nodeKey = edge.target;
    edgeId = edge.id;
    claim(edge.id);
    const source = byKey.get(edge.source),
      target = byKey.get(edge.target);
    const out = source?.output_ports?.find((p) => p.id === edge.source_port);
    const input = target?.input_ports?.find((p) => p.id === edge.target_port);
    if (!out || !input) {
      add("Connect a declared output port to a declared input port.");
      continue;
    }
    if (out.type !== input.type && input.type !== "any")
      add(`Incompatible port types: ${out.type} → ${input.type}.`);
    const key = `${edge.target}/${edge.target_port}`;
    const prior = incoming.get(key) ?? [];
    if (
      prior.some(
        (p) =>
          !input.multiple ||
          p.order === edge.order ||
          (p.source === edge.source && p.source_port === edge.source_port),
      )
    )
      add(
        `${key}: input already connected, duplicate source or ambiguous collection order.`,
      );
    incoming.set(key, [...prior, edge]);
    adjacency.get(edge.source)?.push(edge.target);
    if (edge.order < 0 || !Number.isInteger(edge.order))
      add("Collection order must be a nonnegative integer.");
    if (
      complete &&
      input.required &&
      reachableWithout(def, edge.target, edge.source)
    )
      add(`${key}: source may be skipped; use an explicit merge.`);
  }
  edgeId = undefined;
  if (complete)
    for (const [nodeIndex, n] of def.nodes.entries())
      for (const [portIndex, p] of (n.input_ports ?? []).entries()) {
        fieldPath = `nodes[${nodeIndex}].input_ports[${portIndex}]`;
        nodeKey = n.key;
        if (p.required && !incoming.has(`${n.key}/${p.id}`))
          add(`${n.key}/${p.id}: required input has no source.`);
      }
  fieldPath = "data_edges";
  nodeKey = undefined;
  const visiting = new Set<string>(),
    visited = new Set<string>();
  const visit = (key: string) => {
    if (visiting.has(key)) {
      add("Combined flow and data dependencies contain a cycle.");
      return;
    }
    if (visited.has(key)) return;
    visiting.add(key);
    for (const next of adjacency.get(key) ?? []) visit(next);
    visiting.delete(key);
    visited.add(key);
  };
  for (const key of byKey.keys()) visit(key);
  return errors;
}
function reachableWithout(
  def: WorkflowDefinition,
  target: string,
  source: string,
): boolean {
  const conditions = def.nodes.filter((n) => n.type === "condition"),
    choice = new Map<string, string>();
  let budget = 4096;
  const check = (index: number): boolean => {
    if (budget <= 0) return false;
    if (index < conditions.length) {
      const n = conditions[index]!;
      for (const b of n.branches) {
        choice.set(n.key, b.target);
        if (check(index + 1)) return true;
      }
      return false;
    }
    budget--;
    const seen = new Set<string>(),
      queue = [def.entry_node];
    while (queue.length) {
      const key = queue.shift()!;
      if (seen.has(key)) continue;
      seen.add(key);
      const n = def.nodes.find((n) => n.key === key);
      if (n)
        queue.push(
          ...(n.type === "condition" ? [choice.get(key) ?? ""] : n.next),
        );
    }
    return seen.has(target) && !seen.has(source);
  };
  return check(0);
}
function matches(type: string, value: unknown): boolean {
  if (value == null) return false;
  return (
    type === "any" ||
    (type === "array"
      ? Array.isArray(value)
      : type === "object"
        ? typeof value === "object" && !Array.isArray(value)
        : typeof value === type)
  );
}
export function defaultOutputPorts(type: string): WorkflowPort[] {
  if (type === "input")
    return [
      { id: "title", type: "string" },
      { id: "description", type: "string" },
    ];
  if (type === "agent")
    return [
      { id: "summary", type: "string" },
      { id: "references", type: "array" },
    ];
  return [];
}
