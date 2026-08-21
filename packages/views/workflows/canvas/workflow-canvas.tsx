"use client";

/**
 * The graph canvas: `<ReactFlow />` wired to the editor's state.
 *
 * The component is **controlled and stateless**. Every prop in the frozen
 * signature is either data to draw or a callback, and it keeps no copy of the
 * graph. That is not stylistic - the page owns an undo stack, and a canvas with
 * its own node array would mean two graphs that disagree the moment undo fires,
 * with the visible one being the stale one. So `onNodesChange` / `onEdgesChange`
 * hand the page a *fully applied* array (xyflow's changes are already folded in
 * via `applyNodeChanges`) rather than raw change objects: the page must be able
 * to push a graph onto its history without knowing what a `NodeChange` is.
 *
 * Three details are load-bearing:
 *
 *  1. **Selection is derived from `selectedNodeId`, not stored here.** The
 *     properties panel edits the node the page thinks is selected, so if the
 *     canvas kept its own selection the panel could edit a different node than
 *     the one with the ring around it. `nodes` is re-projected each render with
 *     `selected` set from the prop.
 *
 *  2. **`readOnly` disables editing, never viewing.** Pan, zoom, fit-view,
 *     minimap and selecting a node to inspect it all keep working: a built-in
 *     template is exactly the graph a user most wants to read. Only the
 *     mutating interactions - drag, connect, delete - are switched off, because
 *     the server answers PATCH on a built-in or published template with 409 and
 *     an edit that cannot be saved is worse than no edit.
 *
 *  3. **Positions are canvas-only.** `graphToDefinition` never writes them (see
 *     graph/from-graph.ts), so dragging a node is a UI-state change the page may
 *     keep without it counting as a definition edit. The canvas still reports the
 *     drag, because it has no way to know whether the page wants to remember it.
 */

import { useCallback, useEffect, useMemo } from "react";
import {
  Background,
  BackgroundVariant,
  Controls,
  MiniMap,
  ReactFlow,
  ReactFlowProvider,
  applyEdgeChanges,
  applyNodeChanges,
  type Connection,
  type EdgeChange,
  type NodeChange,
  type NodeMouseHandler,
} from "@xyflow/react";
import { COL_GAP, ROW_GAP, type FlowEdge, type FlowNode } from "../graph";
import { useT } from "../../i18n";
import { workflowEdgeTypes } from "./edge-types";
import { NODE_ACCENT } from "./node-accent";
import { workflowNodeTypes } from "./node-types";
import {
  WorkflowEdgeActionsProvider,
  WorkflowEdgeMarkers,
} from "./workflow-edge";

// xyflow's own stylesheet. `base.css` rather than `style.css`: base carries only
// the layout and transform rules the library cannot work without, while
// style.css adds its default light/dark palette - which would fight the theme
// bridge in workflow-canvas.css and win on specificity for some rules.
import "@xyflow/react/dist/base.css";
import "./workflow-canvas.css";

/** Dotted-grid pitch. Half a column / row so the grid reads as a subdivision of
 *  the layout rather than as an unrelated second rhythm. */
const GRID_GAP: [number, number] = [COL_GAP / 8, ROW_GAP / 8];

/** Leaves a node card clear of the toolbar row when a graph opens zoomed to fit. */
const FIT_VIEW_PADDING = 0.2;

export function WorkflowCanvas(props: {
  nodes: FlowNode[];
  edges: FlowEdge[];
  selectedNodeId: string | null;
  readOnly: boolean;
  onNodesChange(next: FlowNode[]): void;
  onEdgesChange(next: FlowEdge[]): void;
  onSelectNode(id: string | null): void;
  onConnect(source: string, target: string): void;
}) {
  // ReactFlowProvider so `<MiniMap />` and `<Controls />` can reach the store.
  // Mounted here rather than at the page level so the page cannot accidentally
  // share one flow store between the canvas and a future second graph view.
  return (
    <ReactFlowProvider>
      <CanvasInner {...props} />
    </ReactFlowProvider>
  );
}

function CanvasInner({
  nodes,
  edges,
  selectedNodeId,
  readOnly,
  onNodesChange,
  onEdgesChange,
  onSelectNode,
  onConnect,
}: {
  nodes: FlowNode[];
  edges: FlowEdge[];
  selectedNodeId: string | null;
  readOnly: boolean;
  onNodesChange(next: FlowNode[]): void;
  onEdgesChange(next: FlowEdge[]): void;
  onSelectNode(id: string | null): void;
  onConnect(source: string, target: string): void;
}) {
  const { t } = useT("workflows");
  // Selection projected from the prop. New objects only for the nodes whose
  // selected flag actually changes, so xyflow's identity-based re-render check
  // still skips the rest of the layer.
  const projected = useMemo(
    () =>
      nodes.map((node) => {
        const selected = node.id === selectedNodeId;
        return node.selected === selected ? node : { ...node, selected };
      }),
    [nodes, selectedNodeId],
  );

  const handleNodesChange = useCallback(
    (changes: NodeChange<FlowNode>[]) => {
      // Selection changes are routed to `onSelectNode` and NOT folded into the
      // node array: selection is the page's single source of truth (see rule 1),
      // and writing `selected` into the array here would make the projection
      // above fight whatever the page decided.
      const structural = changes.filter((change) => change.type !== "select");
      for (const change of changes) {
        if (change.type === "select" && change.selected) onSelectNode(change.id);
      }
      if (structural.length === 0) return;
      onNodesChange(applyNodeChanges(structural, nodes));
    },
    [nodes, onNodesChange, onSelectNode],
  );

  const handleEdgesChange = useCallback(
    (changes: EdgeChange<FlowEdge>[]) => {
      onEdgesChange(applyEdgeChanges(changes, edges));
    },
    [edges, onEdgesChange],
  );

  const handleDeleteEdge = useCallback(
    (edgeId: string) => {
      if (readOnly) return;
      onEdgesChange(edges.filter((edge) => edge.id !== edgeId));
    },
    [edges, onEdgesChange, readOnly],
  );

  useEffect(() => {
    if (readOnly) return;
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.repeat || (event.key !== "Delete" && event.key !== "Backspace")) {
        return;
      }
      const target = event.target;
      if (
        target instanceof HTMLElement &&
        (target.isContentEditable ||
          target.closest("input, textarea, select, [role='textbox']") !== null)
      ) {
        return;
      }
      const selected = new Set(
        edges.filter((edge) => edge.selected).map((edge) => edge.id),
      );
      if (selected.size === 0) return;
      event.preventDefault();
      onEdgesChange(edges.filter((edge) => !selected.has(edge.id)));
    };
    window.addEventListener("keydown", handleKeyDown);
    return () => window.removeEventListener("keydown", handleKeyDown);
  }, [edges, onEdgesChange, readOnly]);

  const handleConnect = useCallback(
    (connection: Connection) => {
      // A connection with no target is a drag released on empty pane. xyflow
      // does not call onConnect for those, but the types allow null endpoints,
      // and inventing an edge to nowhere would produce a dangling edge the
      // author never drew.
      if (!connection.source || !connection.target) return;
      onConnect(connection.source, connection.target);
    },
    [onConnect],
  );

  // Clicking empty pane clears the selection, which is what puts the properties
  // panel back into its "nothing selected" state. Without it a panel would keep
  // showing a node the user has visibly deselected.
  const handlePaneClick = useCallback(() => onSelectNode(null), [onSelectNode]);

  const handleNodeClick = useCallback<NodeMouseHandler<FlowNode>>(
    (_event, node) => onSelectNode(node.id),
    [onSelectNode],
  );

  return (
    <div
      className="workflow-canvas relative min-h-0 min-w-0 flex-1"
      data-read-only={readOnly ? "true" : "false"}
    >
      <WorkflowEdgeActionsProvider
        readOnly={readOnly}
        onDeleteEdge={handleDeleteEdge}
      >
        <WorkflowEdgeMarkers />
        <ReactFlow<FlowNode, FlowEdge>
        nodes={projected}
        edges={edges}
        nodeTypes={workflowNodeTypes}
        edgeTypes={workflowEdgeTypes}
        onNodesChange={handleNodesChange}
        onEdgesChange={handleEdgesChange}
        onConnect={handleConnect}
        onNodeClick={handleNodeClick}
        onPaneClick={handlePaneClick}
        nodesDraggable={!readOnly}
        nodesConnectable={!readOnly}
        edgesReconnectable={!readOnly}
        elementsSelectable
        // Keep xyflow's broad deletion disabled: it deletes selected nodes and
        // their incident edges together. The scoped listener above accepts the
        // same keys only when one or more EDGES are selected.
        deleteKeyCode={null}
        fitView
        fitViewOptions={{ padding: FIT_VIEW_PADDING }}
        // A graph deeper than the viewport must still be readable end to end;
        // the default 0.5 floor cuts off around six columns.
        minZoom={0.2}
        maxZoom={2}
        // The library's attribution panel also defaults to bottom-right, so it
        // renders its link text on top of the minimap. Moved rather than hidden:
        // hiding it is a paid-tier option we have no licence for, and the
        // attribution is the library's asking price.
        attributionPosition="bottom-left"
      >
        <Background variant={BackgroundVariant.Dots} gap={GRID_GAP} size={1} />
        <Controls showInteractive={false} />
        <MiniMap<FlowNode>
          pannable
          zoomable
          ariaLabel={t(($) => $.canvas.minimap_aria)}
          // Node colour in the minimap comes from the same per-type table as the
          // card's left border, so the minimap is a legible thumbnail of the
          // graph's structure rather than a field of identical grey rectangles.
          nodeClassName={(node) => NODE_ACCENT[node.data.type].minimap}
        />
        </ReactFlow>
      </WorkflowEdgeActionsProvider>
    </div>
  );
}
