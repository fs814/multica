/**
 * The graph model: the translation layer between the stored
 * `WorkflowDefinition` and the xyflow canvas.
 *
 * Every canvas component, the properties panel, and the editor toolbar go
 * through this barrel rather than reaching into individual files, so the
 * contract between the model and the UI is one reviewable surface.
 */

export {
  WORKFLOW_NODE_TYPES,
  WORKFLOW_INPUT_FIELD_TYPES,
  UNKNOWN_NODE_TYPE,
  EDGE_KINDS,
  isWorkflowNodeType,
  toEditorNodeType,
  toWorkflowInputFieldType,
  isForwardEdgeKind,
  editorNodeId,
  editorEdgeId,
  edgeLabelKey,
  emptyRouting,
  blankWorkflowNode,
  blankWorkflowInputField,
} from "./types";
export type {
  WorkflowNodeType,
  WorkflowInputFieldType,
  EditorNodeType,
  EditorNode,
  EditorEdge,
  EdgeKind,
  FlowNode,
  FlowEdge,
  EditorGraph,
} from "./types";

export { definitionToGraph, nodeEdges } from "./to-graph";
export { graphToDefinition } from "./from-graph";
export { autoLayout, COL_GAP, ROW_GAP } from "./layout";
export { clientValidateGraph } from "./validate-graph";
export { legalReworkTargetNodes } from "./rework-targets";
