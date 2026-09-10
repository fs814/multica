"use client";

/**
 * The `edgeTypes` map handed to `<ReactFlow />`.
 *
 * Keyed on {@link EdgeKind}, because `definitionToGraph` writes the kind string
 * straight into `edge.type`. xyflow falls back to its own bezier edge for a type
 * with no entry here, which would silently drop the pass/fail colour and the
 * rework dash - i.e. it would render a *legal bounded loop* and an *illegal
 * forward cycle* identically. `Record<EdgeKind, ...>` makes a new edge kind added
 * to the model a compile error here rather than a graph that lies.
 *
 * Module scope, never inline: xyflow re-instantiates the whole edge layer when
 * this object's identity changes.
 */

import type { EdgeTypes } from "@xyflow/react";
import type { EdgeKind } from "../graph";
import { WorkflowEdge } from "./workflow-edge";

// One component for all three kinds - it reads the kind out of `data` and picks
// its stroke from a table. See workflow-edge.tsx on why that is one file.
export const workflowEdgeTypes = {
  data: WorkflowEdge,
  next: WorkflowEdge,
  branch: WorkflowEdge,
  rework: WorkflowEdge,
} satisfies Record<EdgeKind, unknown> as EdgeTypes;
