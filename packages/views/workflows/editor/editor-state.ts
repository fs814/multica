/**
 * The graph editor's state machine.
 *
 * Everything here is **editor state, not server state**: an unsaved graph, a
 * selection, an undo stack and a set of canvas positions are all things that
 * exist only while a person has this page open. React Query owns the fetched
 * template (plan section 4); this reducer owns the working copy, and the two are
 * joined at exactly one point - {@link hydrate}.
 *
 * ## Why a reducer rather than a pile of useState
 *
 * Undo is the reason. Undo means "the previous *whole* state", so the states have
 * to be produced in one place by named transitions - with six independent
 * setters there is no moment at which a snapshot is known to be consistent, and
 * an undo stack assembled from effects would capture halves of edits.
 *
 * ## The three invariants
 *
 * **1. The definition is reconstructed, never mirrored.** State holds
 * `{nodes, edges, base}`; the working definition is
 * `graphToDefinition(nodes, edges, base)`, computed on read. A second stored copy
 * would be a cache that has to be invalidated on every keystroke, and the first
 * time it drifted the editor would save a graph that does not match the canvas.
 * `base` carries the parts of the envelope the canvas does not model
 * (`schema_version`, `limits`, forward-compatible keys) so those survive.
 *
 * **2. History records definition changes only.** Dragging a card produces a new
 * `nodes` array but an identical definition (positions are deliberately not part
 * of the contract - see graph/from-graph.ts). If drags pushed history entries,
 * "undo" after moving a node around would spend a dozen presses walking back
 * through pixels before it reached the edit the user actually wanted to revert.
 * So {@link workflowEditorReducer} compares the resulting definition and only
 * pushes a past entry when it differs.
 *
 * **3. `dirty` is derived, never set.** It is
 * `serialize(working) !== serialize(saved)`, which makes the graph model's
 * round-trip guarantee do the work: opening a template and saving it unedited is
 * provably a no-op (graph.test.ts, "is byte-equivalent, not merely deep-equal"),
 * so an untouched template cannot report dirty and a flag can never be left
 * stale by a transition that forgot to clear it.
 */

import {
  COL_GAP,
  autoLayout,
  blankWorkflowNode,
  definitionToGraph,
  editorNodeId,
  graphToDefinition,
  nodeEdges,
  type FlowEdge,
  type FlowNode,
  type WorkflowNodeType,
} from "../graph";
import {
  defaultOutputPorts,
  renameWorkflowPort,
} from "@multica/core/workflows";
import type { WorkflowDefinition, WorkflowNode } from "@multica/core/workflows";

/** One point in time: the canvas plus the envelope the canvas does not model. */
export type EditorSnapshot = {
  nodes: FlowNode[];
  edges: FlowEdge[];
  /**
   * Supplies `schema_version`, `limits`, node declaration order and any key a
   * newer server added. Only the JSON view replaces it.
   */
  base: WorkflowDefinition;
};

export type WorkflowEditorState = {
  /**
   * The template this state was seeded from, `null` before the first hydrate.
   * Compared on every hydrate so switching templates re-seeds while a background
   * refetch of the *same* template does not - see {@link hydrate}.
   */
  templateId: string | null;
  present: EditorSnapshot;
  /** Most recent first, so undo is a shift and the cap trims the tail. */
  past: EditorSnapshot[];
  future: EditorSnapshot[];
  /**
   * The graph as last written to the server (or as fetched, before any save).
   * `dirty` is measured against this rather than against a boolean flag.
   */
  saved: WorkflowDefinition;
  selectedNodeId: string | null;
  jsonOpen: boolean;
};

export type WorkflowEditorAction =
  | {
      type: "rename_port";
      nodeKey: string;
      direction: "input_ports" | "output_ports";
      previous: string;
      next: string;
    }
  /** Seed from the server. Ignored if this template is already seeded. */
  | { type: "hydrate"; templateId: string; definition: WorkflowDefinition }
  /** Explicitly discard the working copy after the author accepts a conflict reload. */
  | {
      type: "reload_from_server";
      templateId: string;
      definition: WorkflowDefinition;
    }
  /** The canvas reports an applied node/edge array. */
  | { type: "set_graph"; nodes: FlowNode[]; edges: FlowEdge[] }
  /** Add-node toolbar. */
  | { type: "add_node"; nodeType: WorkflowNodeType }
  | { type: "delete_node"; nodeId: string }
  | { type: "set_entry"; nodeId: string }
  /** The properties panel edited the selected node. */
  | { type: "patch_node"; node: WorkflowNode }
  /** The JSON view applied a wholesale replacement. */
  | { type: "apply_definition"; definition: WorkflowDefinition }
  | { type: "auto_layout" }
  | { type: "undo" }
  | { type: "redo" }
  /** A save landed: the working graph becomes the new baseline. */
  | { type: "mark_saved"; definition: WorkflowDefinition }
  | { type: "select"; nodeId: string | null }
  | { type: "toggle_json" };

/**
 * History cap.
 *
 * A snapshot is two arrays of references plus a definition object, so the memory
 * cost is small - the cap exists because an unbounded stack on a long editing
 * session is a leak, not because 50 undos is a product decision.
 */
const HISTORY_LIMIT = 50;

/** The empty graph the reducer holds before the first fetch resolves. */
const EMPTY_DEFINITION: WorkflowDefinition = {
  schema_version: 1,
  entry_node: "",
  nodes: [],
  limits: {
    max_attempts_per_node: 0,
    max_rework_rounds: 0,
    max_fan_out: 0,
    max_duration_seconds: 0,
    max_total_steps: 0,
    max_cost_cents: 0,
  },
};

export function initialWorkflowEditorState(): WorkflowEditorState {
  return {
    templateId: null,
    present: { nodes: [], edges: [], base: EMPTY_DEFINITION },
    past: [],
    future: [],
    saved: EMPTY_DEFINITION,
    selectedNodeId: null,
    jsonOpen: false,
  };
}

/** The graph a save would send. Cheap enough to call on every render. */
export function workingDefinition(
  state: WorkflowEditorState,
): WorkflowDefinition {
  return graphToDefinition(
    state.present.nodes,
    state.present.edges,
    state.present.base,
  );
}

/**
 * Canonical serialization used for both the dirty check and history's
 * "did the definition actually change?" test.
 *
 * `JSON.stringify` rather than a deep-equal walk because key *order* matters
 * here for the same reason it matters to the server: a save mints an immutable
 * version, and `graphToDefinition` is byte-stable by construction (it spreads
 * the source node), so string equality is both the strictest and the cheapest
 * test available.
 */
function serialize(definition: WorkflowDefinition): string {
  return JSON.stringify(definition);
}

/** True when the working graph differs from what the server last accepted. */
export function isDirty(state: WorkflowEditorState): boolean {
  return serialize(workingDefinition(state)) !== serialize(state.saved);
}

export function canUndo(state: WorkflowEditorState): boolean {
  return state.past.length > 0;
}

export function canRedo(state: WorkflowEditorState): boolean {
  return state.future.length > 0;
}

/** The selected node's source object, for the properties panel. */
export function selectedWorkflowNode(
  state: WorkflowEditorState,
): WorkflowNode | null {
  if (state.selectedNodeId === null) return null;
  const found = state.present.nodes.find(
    (node) => node.id === state.selectedNodeId,
  );
  return found ? found.data.node : null;
}

/**
 * Commits a new present.
 *
 * The definition comparison is invariant 2: a transition that leaves the
 * definition byte-identical (a drag, a pan-induced position write) replaces the
 * present without touching history, so undo stays a list of *semantic* edits.
 * Anything that does change the definition also clears `future`, because a new
 * edit after an undo makes the redo branch unreachable - keeping it would let a
 * later redo splice an abandoned graph back in.
 */
function commit(
  state: WorkflowEditorState,
  next: EditorSnapshot,
): WorkflowEditorState {
  const changed =
    serialize(graphToDefinition(next.nodes, next.edges, next.base)) !==
    serialize(workingDefinition(state));

  if (!changed) return { ...state, present: next };

  return {
    ...state,
    present: next,
    past: [state.present, ...state.past].slice(0, HISTORY_LIMIT),
    future: [],
  };
}

/**
 * Rewrites one node's payload in place.
 *
 * The xyflow id is left alone because it *is* the node key (graph/types.ts) and
 * the panel does not offer renaming; if it ever does, the id and every incident
 * edge endpoint must move with it.
 */
function replaceNode(nodes: FlowNode[], node: WorkflowNode): FlowNode[] {
  const id = editorNodeId(node.key);
  return nodes.map((flowNode) =>
    flowNode.id === id
      ? { ...flowNode, data: { ...flowNode.data, node } }
      : flowNode,
  );
}

/**
 * Re-derives one node's outgoing edges after the panel rewrote its payload.
 *
 * `graphToDefinition` reads `next`, `branches` and `rework_targets` off the EDGE
 * list, not off the node object, while the properties panel writes them onto the
 * node. Without this, a panel edit to a condition's branches or to any node's
 * rework targets was discarded on save - and silently, because `isDirty` compares
 * definitions, so Save stayed disabled while the panel still displayed the change.
 * An acceptance node could therefore never be given the rework target the
 * validator demands.
 *
 * Only the edited node's outgoing edges are replaced; edges INTO it and every
 * other node's edges are untouched, so this cannot disturb a part of the graph the
 * author was not editing. The rebuild goes through `nodeEdges` - the same function
 * `definitionToGraph` uses - so ids, ordering and `data` stay identical to a fresh
 * expansion and the round trip keeps closing.
 */
function resyncNodeEdges(edges: FlowEdge[], node: WorkflowNode): FlowEdge[] {
  const id = editorNodeId(node.key);
  const untouched = edges
    .filter((edge) => edge.source !== id || edge.data?.kind === "data")
    .map((edge) => {
      if (edge.source !== id || edge.data?.kind !== "data") return edge;
      const port = node.output_ports?.find(
        (p) => `out:${p.id}` === edge.sourceHandle,
      );
      return port
        ? { ...edge, data: { ...edge.data, dataType: port.type } }
        : edge;
    });
  const rebuilt = nodeEdges(node);
  // Preserve the grouped-by-source ordering `definitionEdges` produces: splice
  // the rebuilt run back in where this node's edges used to start, so a save does
  // not reorder unrelated nodes' lists.
  const at = edges.findIndex((edge) => edge.source === id);
  if (at < 0) return [...untouched, ...rebuilt];
  const before = untouched.filter(
    (edge) => edges.findIndex((original) => original.id === edge.id) < at,
  );
  const after = untouched.filter(
    (edge) => edges.findIndex((original) => original.id === edge.id) >= at,
  );
  return [...before, ...rebuilt, ...after];
}

/**
 * A key no existing node uses.
 *
 * Keys are the graph's addressing scheme - every edge, rework target, join
 * source and the entry node itself refer to a node by key - so a collision would
 * not create a second node, it would silently re-point somebody else's edges.
 * The `type_n` shape matches the built-in graphs' `step_1` / `condition_1`
 * convention so a hand-written and a toolbar-added node read the same.
 */
function freshNodeKey(
  nodes: readonly FlowNode[],
  type: WorkflowNodeType,
): string {
  const taken = new Set(nodes.map((node) => node.data.node.key));
  const stem = type === "agent" ? "step" : type;
  for (let index = 1; ; index++) {
    const candidate = `${stem}_${index}`;
    if (!taken.has(candidate)) return candidate;
  }
}

/**
 * Where a new card lands: one column right of the rightmost node, on its row.
 *
 * Not at the origin, and not auto-laid-out: layout ranks by edges, and a node
 * with no edges yet has rank 0 - so auto-layout would drop every new node on top
 * of the entry node. Placing it past the end of the graph means the author can
 * see what they just added and drag an edge to it.
 */
function nextNodePosition(nodes: readonly FlowNode[]): {
  x: number;
  y: number;
} {
  if (nodes.length === 0) return { x: 0, y: 0 };
  let x = -Infinity;
  let y = 0;
  for (const node of nodes) {
    if (node.position.x > x) {
      x = node.position.x;
      y = node.position.y;
    }
  }
  return { x: x + COL_GAP, y };
}

export function workflowEditorReducer(
  state: WorkflowEditorState,
  action: WorkflowEditorAction,
): WorkflowEditorState {
  switch (action.type) {
    case "hydrate": {
      // THE REFETCH-CLOBBER GUARD. React Query refetches on window focus, on
      // reconnect, and after every mutation settles, and each of those would
      // otherwise re-seed this reducer from the server and throw away whatever
      // the author has been typing. Seeding is therefore keyed on the template
      // id: a different template is a different document and must re-seed; the
      // same template must not, no matter how many times the query resolves.
      //
      // The consequence is deliberate and is the safe direction: if someone else
      // saves this template while it is open, this editor keeps showing the
      // author's own graph rather than silently replacing it. The page detects
      // that case by comparing the fetched definition with `saved` and says so,
      // which leaves the choice with the person who would lose the work.
      if (state.templateId === action.templateId) return state;

      const { nodes, edges } = definitionToGraph(action.definition);
      return {
        templateId: action.templateId,
        present: { nodes, edges, base: action.definition },
        past: [],
        future: [],
        saved: action.definition,
        selectedNodeId: null,
        jsonOpen: false,
      };
    }

    case "reload_from_server": {
      const { nodes, edges } = definitionToGraph(action.definition);
      return {
        templateId: action.templateId,
        present: { nodes, edges, base: action.definition },
        past: [],
        future: [],
        saved: action.definition,
        selectedNodeId: null,
        jsonOpen: state.jsonOpen,
      };
    }

    case "set_graph": {
      if (action.edges === state.present.edges) {
        return commit(state, { ...state.present, nodes: action.nodes });
      }
      // Inline and panel editors spread node payloads. Keep their outgoing
      // references current so typing after a connection edit cannot undo it.
      const definition = graphToDefinition(
        action.nodes,
        action.edges,
        state.present.base,
      );
      const byKey = new Map(definition.nodes.map((node) => [node.key, node]));
      return commit(state, {
        ...state.present,
        nodes: action.nodes.map((node) => ({
          ...node,
          data: { ...node.data, node: byKey.get(node.data.node.key)! },
        })),
        edges: action.edges,
      });
    }

    case "add_node": {
      const key = freshNodeKey(state.present.nodes, action.nodeType);
      const node = blankWorkflowNode(key, action.nodeType);
      if (state.present.base.schema_version === 2) {
        node.next_ids = [];
        node.input_ports = [];
        node.output_ports = defaultOutputPorts(action.nodeType);
        node.on_failure = "fail";
      }
      const id = editorNodeId(key);
      const flowNode: FlowNode = {
        id,
        type: action.nodeType,
        position: nextNodePosition(state.present.nodes),
        data: {
          id,
          nodeKey: key,
          type: action.nodeType,
          isEntry: state.present.nodes.length === 0,
          schemaVersion: state.present.base.schema_version,
          node,
        },
      };
      // Selected immediately: a blank node is invalid until it is configured
      // (an agent node needs routing, an acceptance node needs rework targets),
      // so the properties panel is where the author has to go next anyway.
      return {
        ...commit(state, {
          ...state.present,
          nodes: [...state.present.nodes, flowNode],
          base:
            state.present.nodes.length === 0
              ? { ...state.present.base, entry_node: key }
              : state.present.base,
        }),
        selectedNodeId: id,
      };
    }

    case "delete_node": {
      const removed = state.present.nodes.find(
        (node) => node.id === action.nodeId,
      );
      if (!removed) return state;
      const nodes = state.present.nodes.filter(
        (node) => node.id !== action.nodeId,
      );
      const edges = state.present.edges.filter(
        (edge) =>
          edge.source !== action.nodeId && edge.target !== action.nodeId,
      );
      // Edges own next/branches/rework. Normalize the remaining payloads too,
      // or the next property edit could resurrect a deleted connection.
      const entryNode = workingDefinition(state).entry_node;
      const definition = graphToDefinition(nodes, edges, {
        ...state.present.base,
        entry_node: entryNode === removed.data.node.key ? "" : entryNode,
      });
      definition.nodes = definition.nodes.map((node) => ({
        ...node,
        join_sources: node.join_sources.filter(
          (key) => key !== removed.data.node.key,
        ),
        ...(node.routing?.from_node === removed.data.node.key
          ? { routing: { ...node.routing, from_node: "" } }
          : {}),
      }));
      const byKey = new Map(definition.nodes.map((node) => [node.key, node]));
      const next = commit(state, {
        base: definition,
        edges,
        nodes: nodes.map((node) => ({
          ...node,
          data: {
            ...node.data,
            node: byKey.get(node.data.node.key)!,
            isEntry: node.data.node.key === definition.entry_node,
          },
        })),
      });
      return {
        ...next,
        selectedNodeId:
          state.selectedNodeId === action.nodeId ? null : state.selectedNodeId,
      };
    }

    case "set_entry": {
      const selected = state.present.nodes.find(
        (node) => node.id === action.nodeId,
      );
      if (!selected) return state;
      return commit(state, {
        ...state.present,
        base: { ...state.present.base, entry_node: selected.data.node.key },
        nodes: state.present.nodes.map((node) => ({
          ...node,
          data: { ...node.data, isEntry: node.id === selected.id },
        })),
      });
    }
    case "rename_port": {
      const before = workingDefinition(state);
      const after = renameWorkflowPort(
        before,
        action.nodeKey,
        action.direction,
        action.previous,
        action.next,
      );
      if (before === after) return state;
      const graph = definitionToGraph(after);
      return commit(state, {
        base: after,
        edges: graph.edges,
        nodes: graph.nodes.map((node) => ({
          ...node,
          position:
            state.present.nodes.find((old) => old.id === node.id)?.position ??
            node.position,
        })),
      });
    }
    case "patch_node": {
      const edges = resyncNodeEdges(state.present.edges, action.node).filter(
        (edge) => {
          if (edge.data?.kind !== "data") return true;
          if (
            edge.source === action.node.key &&
            !action.node.output_ports?.some(
              (p) => `out:${p.id}` === edge.sourceHandle,
            )
          )
            return false;
          if (
            edge.target === action.node.key &&
            !action.node.input_ports?.some(
              (p) => `in:${p.id}` === edge.targetHandle,
            )
          )
            return false;
          return true;
        },
      );
      return commit(state, {
        ...state.present,
        nodes: replaceNode(state.present.nodes, action.node),
        edges,
      });
    }

    case "apply_definition": {
      // A wholesale replacement, so the graph is re-expanded from scratch: the
      // pasted JSON may have added, removed or re-typed nodes, and merging it
      // into the existing canvas would leave stale flow nodes behind. `base`
      // becomes the pasted definition, which is how an edit to `limits` (a field
      // no control renders) takes effect.
      const { nodes, edges } = definitionToGraph(action.definition);
      const next = commit(state, { nodes, edges, base: action.definition });
      // Selection is dropped when the selected key no longer exists, rather than
      // always: keeping it lets an author fix a node in JSON and carry on
      // editing the same node in the panel.
      const stillThere = nodes.some((node) => node.id === state.selectedNodeId);
      return stillThere ? next : { ...next, selectedNodeId: null };
    }

    case "auto_layout":
      // Goes through `commit`, which will find the definition unchanged and so
      // will NOT push a history entry - correct, because layout moves pixels and
      // pixels are not part of the definition. Pressing undo after auto-layout
      // therefore reverts the last real edit, not the layout.
      return commit(state, {
        ...state.present,
        nodes: autoLayout(state.present.nodes, state.present.edges),
      });

    case "undo": {
      const [previous, ...rest] = state.past;
      if (!previous) return state;
      return {
        ...state,
        present: previous,
        selectedNodeId: previous.nodes.some(
          (node) => node.id === state.selectedNodeId,
        )
          ? state.selectedNodeId
          : null,
        past: rest,
        future: [state.present, ...state.future].slice(0, HISTORY_LIMIT),
      };
    }

    case "redo": {
      const [next, ...rest] = state.future;
      if (!next) return state;
      return {
        ...state,
        present: next,
        selectedNodeId: next.nodes.some(
          (node) => node.id === state.selectedNodeId,
        )
          ? state.selectedNodeId
          : null,
        past: [state.present, ...state.past].slice(0, HISTORY_LIMIT),
        future: rest,
      };
    }

    case "mark_saved":
      // Only the baseline moves. `present` is untouched on purpose: the PATCH
      // response's `definition` is the template's *effective* graph, which after
      // saving over a published template is still the published bytes (the draft
      // is invisible to Runs until publish - see api/client.ts). Re-seeding the
      // canvas from it would make the author's own save appear to vanish. So the
      // page passes the graph it *sent*, and history survives the save.
      return { ...state, saved: action.definition };

    case "select":
      return { ...state, selectedNodeId: action.nodeId };

    case "toggle_json":
      return { ...state, jsonOpen: !state.jsonOpen };
  }
}
