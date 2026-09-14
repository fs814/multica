import type { WorkflowDefinition, WorkflowNode } from "./schemas";
import { scriptPipelineInputKeys } from "./script-pipeline";

/** Output IDs name produced values, not labels. Without an explicit producer
 * mapping, output/coupled input renames lose data. Conditions also consume the
 * implicit verdict key. Neither endpoint may move across these contracts. */
export function canRenameWorkflowPort(
  node: WorkflowNode,
  direction: "input_ports" | "output_ports",
  previous: string,
  next: string = previous,
): boolean {
  return (
    direction === "input_ports" &&
    !node.output_ports?.some(
      (port) => port.id === previous || port.id === next,
    ) &&
    !(
      node.type === "condition" &&
      (previous === "verdict" || next === "verdict")
    )
  );
}

/** A confirmed safe input rename preserves binding identity and owned predicates. */
export function renameWorkflowPort(
  definition: WorkflowDefinition,
  nodeKey: string,
  direction: "input_ports" | "output_ports",
  previous: string,
  next: string,
): WorkflowDefinition {
  const node = definition.nodes.find((n) => n.key === nodeKey);
  if (
    definition.schema_version !== 2 ||
    !node ||
    !canRenameWorkflowPort(node, direction, previous, next) ||
    !next.trim() ||
    next !== next.trim() ||
    previous === next ||
    !node[direction]?.some((p) => p.id === previous) ||
    node[direction]?.some((p) => p.id === next)
  )
    return definition;
  return {
    ...definition,
    nodes: definition.nodes.map((n) =>
      n !== node
        ? n
        : {
            ...n,
            [direction]: n[direction]?.map((p) =>
              p.id === previous ? { ...p, id: next } : p,
            ),
            branches:
              direction === "input_ports"
                ? n.branches.map((b) =>
                    b.predicate?.input_port === previous
                      ? {
                          ...b,
                          predicate: { ...b.predicate, input_port: next },
                        }
                      : b,
                  )
                : n.branches,
          },
    ),
    data_edges: definition.data_edges?.map((edge) => {
      if (
        direction === "input_ports" &&
        edge.target === nodeKey &&
        edge.target_port === previous
      )
        return { ...edge, target_port: next };
      if (
        direction === "output_ports" &&
        edge.source === nodeKey &&
        edge.source_port === previous
      )
        return { ...edge, source_port: next };
      return edge;
    }),
  };
}

export type InputFieldChange = {
  key: string;
  before?: WorkflowNode["input_fields"][number];
  after?: WorkflowNode["input_fields"][number];
  changes: ("added" | "removed" | "type" | "options" | "required" | "label")[];
};
export function compareInputFields(
  before?: WorkflowNode | null,
  after?: WorkflowNode | null,
): InputFieldChange[] {
  const oldFields = before?.input_fields ?? [],
    newFields = after?.input_fields ?? [];
  return [...new Set([...oldFields, ...newFields].map((f) => f.key))]
    .map((key) => {
      const before = oldFields.find((f) => f.key === key),
        after = newFields.find((f) => f.key === key);
      const changes: InputFieldChange["changes"] = [];
      if (!before) changes.push("added");
      else if (!after) changes.push("removed");
      else {
        if ((before.type || "text") !== (after.type || "text"))
          changes.push("type");
        if (JSON.stringify(before.options) !== JSON.stringify(after.options))
          changes.push("options");
        if (before.required !== after.required) changes.push("required");
        if (before.label !== after.label) changes.push("label");
      }
      return { key, before, after, changes };
    })
    .filter((item) => item.changes.length > 0);
}
/** Title/description remain valid built-in inputs when omitted from declarations. */
export function unknownInputKeys(
  input: Record<string, string>,
  node: WorkflowNode | null,
): string[] {
  const known = new Set([
    "title",
    "description",
    ...(node?.input_fields ?? []).map((f) => f.key),
    ...(node?.input_mode === "scripts" ? scriptPipelineInputKeys : []),
  ]);
  return Object.keys(input).filter((key) => !known.has(key));
}
export function compareWorkflowVersions(
  before: WorkflowDefinition,
  after: WorkflowDefinition,
) {
  const oldNodes = new Map(before.nodes.map((n) => [n.key, n]));
  const newNodes = new Map(after.nodes.map((n) => [n.key, n]));
  return {
    added: after.nodes.filter((n) => !oldNodes.has(n.key)).map((n) => n.key),
    removed: before.nodes.filter((n) => !newNodes.has(n.key)).map((n) => n.key),
    changed: after.nodes
      .filter(
        (n) =>
          oldNodes.has(n.key) &&
          JSON.stringify(oldNodes.get(n.key)) !== JSON.stringify(n),
      )
      .map((n) => n.key),
    schemaChanged: before.schema_version !== after.schema_version,
    entryChanged: before.entry_node !== after.entry_node,
    limitsChanged:
      JSON.stringify(before.limits) !== JSON.stringify(after.limits),
    dataChanged:
      JSON.stringify(before.data_edges ?? []) !==
      JSON.stringify(after.data_edges ?? []),
  };
}
