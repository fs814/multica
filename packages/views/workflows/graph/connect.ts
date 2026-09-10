import {
  validateGraphV2,
  type WorkflowDefinition,
} from "@multica/core/workflows";
import { graphToDefinition } from "./from-graph";
import type { FlowEdge, FlowNode } from "./types";

/** One atomic add/reconnect. Invalid or cancelled drags leave the original edge. */
export function connectWorkflow(
  nodes: FlowNode[],
  edges: FlowEdge[],
  base: WorkflowDefinition,
  source: string,
  target: string,
  sourceHandle?: string | null,
  targetHandle?: string | null,
  replacing?: string,
): { edges: FlowEdge[]; error?: string } {
  const from = nodes.find((n) => n.id === source),
    to = nodes.find((n) => n.id === target);
  const reject = (error: string) => ({ edges, error });
  if (!from || !to || source === target)
    return reject("Connection needs two different existing nodes.");
  replacing ??= edges.find(
    (e) =>
      e.source === source &&
      e.sourceHandle === sourceHandle &&
      e.data?.kind === "branch" &&
      !e.target,
  )?.id;
  const old = edges.find((e) => e.id === replacing);
  const remaining = edges.filter((e) => e.id !== replacing);
  const data =
    sourceHandle?.startsWith("out:") || targetHandle?.startsWith("in:");
  const id = old?.id ?? crypto.randomUUID();
  let edge: FlowEdge;
  if (data) {
    if (
      base.schema_version !== 2 ||
      !sourceHandle?.startsWith("out:") ||
      !targetHandle?.startsWith("in:")
    )
      return reject("Connect a data output to a data input.");
    const port = from.data.node.output_ports?.find(
      (p) => p.id === sourceHandle.slice(4),
    );
    edge = {
      id,
      source,
      target,
      sourceHandle,
      targetHandle,
      type: "data",
      data: {
        kind: "data",
        sourceKey: source,
        targetKey: target,
        dataType: port?.type ?? "any",
        order:
          old?.data?.order ??
          Math.max(
            -1,
            ...remaining
              .filter((e) => e.data?.kind === "data")
              .map((e) => e.data?.order ?? 0),
          ) + 1,
      },
    };
  } else {
    if (from.data.type === "end" || to.data.type === "input")
      return reject(
        "End nodes have no output; input nodes have no incoming flow.",
      );
    const branchIndex = sourceHandle?.startsWith("branch-")
      ? Number(sourceHandle.slice(7))
      : -1;
    const branch = from.data.node.branches[branchIndex];
    if (from.data.type === "condition" && !branch)
      return reject("Configure a condition branch before connecting its port.");
    const kind = branch
      ? "branch"
      : old?.data?.kind === "rework"
        ? "rework"
        : "next";
    if (
      remaining.some(
        (e) =>
          e.source === source &&
          e.target === target &&
          e.data?.kind === kind &&
          e.sourceHandle === sourceHandle,
      )
    )
      return reject("This connection already exists.");
    if (
      kind === "branch" &&
      remaining.some(
        (e) => e.source === source && e.sourceHandle === sourceHandle,
      )
    )
      return reject(
        "This branch already has a target; reconnect its existing edge.",
      );
    edge = {
      id,
      source,
      target,
      sourceHandle,
      targetHandle,
      type: kind,
      data: {
        kind,
        sourceKey: source,
        targetKey: target,
        ...(branch
          ? {
              branch: {
                ...branch,
                ...(base.schema_version === 2 ? { id } : {}),
              },
              verdict: branch.when_verdict,
              isDefaultBranch: !branch.when_verdict && !branch.predicate,
            }
          : {}),
      },
    };
  }
  const next = replacing
    ? edges.map((e) => (e.id === replacing ? edge : e))
    : [...edges, edge];
  if (base.schema_version === 2) {
    const errors = validateGraphV2(graphToDefinition(nodes, next, base), false);
    if (errors.length) return reject(errors[0]!);
  }
  return { edges: next };
}
