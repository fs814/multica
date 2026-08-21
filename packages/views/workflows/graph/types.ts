/**
 * Editor-side graph types: the seam between the stored `WorkflowDefinition`
 * (which the server owns and the engine executes) and the xyflow canvas (which
 * only the editor owns).
 *
 * Two invariants make this seam safe, and every type below exists to protect
 * one of them:
 *
 *  1. **The definition is the only source of truth.** A flow node's `data.node`
 *     carries the *whole* source `WorkflowNode`, not a projection of it. The
 *     canvas models a handful of fields (edges, entry, position); everything
 *     else - `submission_schema`, `acceptance_criteria`, `max_attempts`,
 *     `join_policy`, `join_sources`, `fan_out_max`, `instruction` - rides along
 *     untouched so that saving a graph the user only *panned* cannot delete
 *     semantics the canvas never rendered. See `from-graph.ts`.
 *
 *  2. **Rework edges are the only legal cycles.** The server validator rejects
 *     any cycle not made exclusively of declared `rework_targets`
 *     (`validateAcyclic` in server/internal/workflow/validate.go). So an edge's
 *     kind is not decoration: layout must ignore rework edges when ranking or
 *     it would rank a cycle, and the canvas must draw them differently or an
 *     author cannot tell a bounded loop from a mistake. `EdgeKind` is therefore
 *     part of the model, not of the stylesheet.
 *
 * The wire types are imported, never redeclared - `@multica/core/workflows` is
 * the single definition of the contract.
 */

import type { Edge, Node } from "@xyflow/react";
import type {
  WorkflowBranch,
  WorkflowInputField,
  WorkflowNode,
  WorkflowRouting,
} from "@multica/core/workflows";

// ---------------------------------------------------------------------------
// Node types
// ---------------------------------------------------------------------------

/**
 * The node vocabulary this build knows how to render, mirroring
 * `validNodeTypes` in server/internal/workflow/definition.go — which in turn
 * mirrors the CHECK on `workflow_step_instance.node_type` (migration 251).
 * All three must be widened together: a kind accepted here but rejected by the
 * CHECK would publish and then fail its first Run's INSERT.
 */
export const WORKFLOW_NODE_TYPES = [
  "input",
  "agent",
  "condition",
  "fan_out",
  "join",
  "acceptance",
  "end",
] as const;

export type WorkflowNodeType = (typeof WORKFLOW_NODE_TYPES)[number];

/**
 * Sentinel for a node kind a *newer server* published and this build has never
 * heard of. The wire schema keeps `type` a `z.string()` on purpose (see the
 * header of packages/core/workflows/schemas.ts): an installed desktop build
 * outlives any given server, and a strict enum here would turn "the server
 * added a node kind" into a blank canvas. Coercing the unknown kind to some
 * known one instead would be worse than a blank canvas - it would render a lie
 * and then let the user save it.
 *
 * The canvas registers a read-only renderer under this key; the original string
 * is always available as `data.node.type`, and `from-graph.ts` writes that
 * original string back, so an unknown node survives an edit to its neighbours.
 */
export const UNKNOWN_NODE_TYPE = "unknown";

/** Renderer key for an xyflow node: a known kind, or the unknown sentinel. */
export type EditorNodeType = WorkflowNodeType | typeof UNKNOWN_NODE_TYPE;

/** True when `type` is a node kind this build can edit. */
export function isWorkflowNodeType(type: string): type is WorkflowNodeType {
  return (WORKFLOW_NODE_TYPES as readonly string[]).includes(type);
}

/** Narrows a server-sent node type, degrading to {@link UNKNOWN_NODE_TYPE}. */
export function toEditorNodeType(type: string): EditorNodeType {
  return isWorkflowNodeType(type) ? type : UNKNOWN_NODE_TYPE;
}

/**
 * Payload on every xyflow node.
 *
 * `id` duplicates the xyflow node id and `nodeKey` duplicates `node.key`
 * deliberately: a node renderer receives only `data` plus `id`, and the
 * properties panel edits `node.key` while a rename is in flight, so the two can
 * disagree for exactly one render. `nodeKey` is the *committed* key that edges
 * address; `node.key` is what the panel is currently editing.
 */
export type EditorNode = {
  /** Always equal to the xyflow node id. See {@link editorNodeId}. */
  id: string;
  /** The key every edge, rework target and join source addresses. */
  nodeKey: string;
  /** Renderer key. `node.type` keeps the server's original string. */
  type: EditorNodeType;
  /**
   * Whether this node is the graph's `entry_node`. Entry is a *graph* property
   * (the canvas shows and can move it), so it round-trips through here rather
   * than being read back off the base definition.
   */
  isEntry: boolean;
  /**
   * The complete source node, verbatim. Renderers read badge/key/name/routing
   * from here, the properties panel edits it, and `graphToDefinition` spreads
   * it - which is what keeps unmodelled fields from being dropped on save.
   */
  node: WorkflowNode;
};

/**
 * xyflow node id == the workflow node key.
 *
 * Node keys are already unique (the validator rejects duplicates) and every
 * edge, rework target and join source addresses nodes by key, so a synthetic id
 * would only add a lookup table. A rename must rewrite the incident edges
 * anyway, so it does not save the editor any work either.
 */
export function editorNodeId(nodeKey: string): string {
  return nodeKey;
}

export type FlowNode = Node<EditorNode, EditorNodeType>;

// ---------------------------------------------------------------------------
// Edge kinds
// ---------------------------------------------------------------------------

export const EDGE_KINDS = ["next", "branch", "rework"] as const;

/**
 * - `next`   - a node's `next[]`: the forward spine.
 * - `branch` - a condition node's `branches[]`, carrying a verdict.
 * - `rework` - a node's `rework_targets[]`: the ONLY edges permitted to run
 *   backwards, and the only ones excluded from rank computation.
 */
export type EdgeKind = (typeof EDGE_KINDS)[number];

/** True for the edge kinds the server's acyclicity check treats as forward. */
export function isForwardEdgeKind(kind: EdgeKind): boolean {
  return kind !== "rework";
}

export type EditorEdge = {
  kind: EdgeKind;
  /** Source node key (== xyflow `source`), repeated for renderer convenience. */
  sourceKey: string;
  /** Target node key (== xyflow `target`). */
  targetKey: string;
  /**
   * For `branch` edges: the verdict this branch fires on (`pass` | `fail` |
   * `blocked`), or `""` for the condition's single default branch.
   */
  verdict?: string;
  /** True for the one branch with an empty verdict. */
  isDefaultBranch?: boolean;
  /**
   * The original `WorkflowBranch`, kept so a branch's forward-compatible extra
   * keys (the wire schema is `.loose()`) survive a round trip.
   */
  branch?: WorkflowBranch;
};

export type FlowEdge = Edge<EditorEdge, EdgeKind>;

/**
 * Deterministic edge id. The index is part of it because a *malformed* graph can
 * legitimately reach the canvas - two branches to the same target, or a repeated
 * `next` entry - and xyflow silently drops duplicate ids, which would hide the
 * very edge the validator is complaining about.
 */
export function editorEdgeId(
  kind: EdgeKind,
  sourceKey: string,
  index: number,
  targetKey: string,
): string {
  return `${kind}:${sourceKey}:${index}:${targetKey}`;
}

/**
 * i18n key suffix for an edge's pill label, inside the `workflows` namespace.
 *
 * The model deliberately does not set xyflow's `label`: a verdict token is data,
 * not display text, and every user-visible string in this repo goes through
 * `useT("workflows")`. Returning the key rather than the text keeps the mapping
 * in one place while leaving translation to the canvas.
 */
export function edgeLabelKey(edge: EditorEdge): string {
  if (edge.kind === "rework") return "graph.edge.rework";
  if (edge.kind === "branch") {
    if (!edge.verdict) return "graph.edge.default";
    return `graph.edge.verdict.${edge.verdict}`;
  }
  return "graph.edge.next";
}

// ---------------------------------------------------------------------------
// Graph shape
// ---------------------------------------------------------------------------

/** What `definitionToGraph` produces and `graphToDefinition` consumes. */
export type EditorGraph = {
  nodes: FlowNode[];
  edges: FlowEdge[];
};

/**
 * Empty routing, for a node whose strategy the author has not chosen yet. The
 * wire type has no optional members, so the panel needs a complete zero value
 * rather than a partial one.
 */
export function emptyRouting(): WorkflowRouting {
  return {
    strategy: "",
    agent_id: "",
    from_node: "",
    capability: "",
    fallback_agent_id: "",
  };
}

/**
 * The field kinds an input node's declaration may use, mirroring
 * `validInputFieldTypes` in server/internal/workflow/definition.go. The server
 * rejects anything else at publish time, so the panel must not offer more.
 */
export const WORKFLOW_INPUT_FIELD_TYPES = [
  "text",
  "textarea",
  "select",
] as const;

export type WorkflowInputFieldType =
  (typeof WORKFLOW_INPUT_FIELD_TYPES)[number];

/**
 * Narrows a server-sent field kind, degrading to `"text"`.
 *
 * A text input is the honest fallback for a kind this build has never heard of:
 * it accepts any string, so the human can still supply the value and the run can
 * still start. Refusing to render the field would make the run unstartable
 * against a template the server considers valid, and guessing `select` would
 * show an empty dropdown. An empty `type` is the server's own default for text.
 */
export function toWorkflowInputFieldType(type: string): WorkflowInputFieldType {
  return (WORKFLOW_INPUT_FIELD_TYPES as readonly string[]).includes(type)
    ? (type as WorkflowInputFieldType)
    : "text";
}

/**
 * A blank declared field, for the properties panel's "add field" action.
 *
 * `key` is empty so the author must name it — the validator rejects an empty key,
 * and inventing `field_1` would produce a graph that publishes with a key nobody
 * chose and that every downstream prompt then labels meaninglessly. `required`
 * defaults to false so adding a field cannot retroactively break the run dialog
 * of a template mid-edit.
 */
export function blankWorkflowInputField(): WorkflowInputField {
  return {
    key: "",
    label: "",
    type: "text",
    required: false,
    options: [],
    placeholder: "",
  };
}

/**
 * A blank node of `type`, for the "+ 输入 / + Issue / + 验收 / + 条件 / + 结束"
 * toolbar.
 *
 * Defaults are chosen so a freshly added node is as close to valid as its type
 * allows: an agent node gets routing (the validator rejects an agent without
 * it) and an acceptance node gets `on_failure` left empty, meaning the server's
 * default of `block`. The remaining rules - exactly one outgoing edge, and
 * rework targets for acceptance - cannot be satisfied without edges, so they
 * are left to the author and reported by {@link clientValidateGraph}.
 *
 * An input node gets an EMPTY `input_fields` list rather than a starter field,
 * and that is deliberate: an empty declaration is legal (it means "documents
 * where work enters, collects nothing typed", and the Run dialog falls back to
 * the freeform pair), whereas a starter field would have an empty `key` and so
 * would make a newly added node immediately invalid. The node must also be made
 * the entry node by the caller — the validator requires it, and only the editor
 * knows whether the author is replacing an existing entry.
 */
export function blankWorkflowNode(
  key: string,
  type: WorkflowNodeType,
): WorkflowNode {
  return {
    key,
    type,
    name: "",
    instruction: "",
    next: [],
    ...(type === "agent" ? { routing: emptyRouting() } : {}),
    submission_schema: "",
    acceptance_criteria: [],
    on_failure: "",
    rework_targets: [],
    max_attempts: 0,
    branches: [],
    join_policy: "",
    join_sources: [],
    fan_out_max: 0,
    ...(type === "input" ? { input_mode: "text" } : {}),
    image_attachment_id: "",
    input_fields: [],
  };
}
