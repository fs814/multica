"use client";

/**
 * The `nodeTypes` map handed to `<ReactFlow />`.
 *
 * Keyed on {@link EditorNodeType}, which is what `definitionToGraph` writes into
 * `node.type` - including the `unknown` sentinel. xyflow silently drops a node
 * whose `type` has no entry here, so this map being exhaustive over
 * `EditorNodeType` is what guarantees the canvas cannot show a graph with fewer
 * steps than the definition has. `Record<EditorNodeType, ...>` makes a future
 * node kind added to the model a compile error here rather than a missing card at
 * runtime.
 *
 * Defined at module scope, never inline in the component: xyflow warns on (and
 * fully re-instantiates the node layer for) a `nodeTypes` object with a new
 * identity each render.
 */

import type { NodeTypes } from "@xyflow/react";
import { UNKNOWN_NODE_TYPE, type EditorNodeType } from "../graph";
import {
  AcceptanceNode,
  AgentNode,
  ConditionNode,
  EndNode,
  FanOutNode,
  InputNode,
  JoinNode,
  UnknownNode,
} from "./nodes/workflow-nodes";

// The `satisfies` keeps the exhaustiveness check while still handing xyflow the
// `NodeTypes` it wants: `NodeTypes` widens `data`/`type` to `any`, so typing the
// constant as `NodeTypes` directly would silently accept a missing kind.
export const workflowNodeTypes = {
  input: InputNode,
  agent: AgentNode,
  condition: ConditionNode,
  fan_out: FanOutNode,
  join: JoinNode,
  acceptance: AcceptanceNode,
  end: EndNode,
  [UNKNOWN_NODE_TYPE]: UnknownNode,
} satisfies Record<EditorNodeType, unknown> as NodeTypes;
