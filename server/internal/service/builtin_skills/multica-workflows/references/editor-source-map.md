# Workflow graph editing source map

- `packages/views/workflows/editor/editor-state.ts`: atomic node deletion,
  structured reference cleanup, entry selection, dirty state and undo/redo.
- `packages/views/workflows/canvas/workflow-canvas.tsx`: canvas-scoped deletion
  keys, input protection, node and edge focus and selection.
- `packages/views/workflows/editor/properties-panel.tsx`: visible node deletion
  and entry controls, read-only handling.
- `packages/views/workflows/components/workflow-template-detail-page.tsx`: editing
  permission gates, action dispatch and draft persistence.
- Adjacent tests cover deleting newly added nodes, restoring complete graphs,
  removing incident edges, preserving deletions on save/reopen, selecting an entry,
  and avoiding deletion from text inputs or read-only views.
- `packages/views/workflows/canvas/nodes/input-node-editor.tsx`: direct input name
  and instruction editing, multiline display and expandable field/image controls.
- `packages/views/workflows/canvas/input-node.test.tsx`: real xyflow controls with
  reducer state, typing focus, key protection, undo/redo and serialized content.
- `set_graph` normalizes outgoing node references so a later inline edit cannot
  restore a deleted connection or remove a newly connected edge.
- `packages/core/workflows/run-input-defaults.ts`: maps the entry input name and
  instruction to run title/description; unnamed inputs use the template name.
- `packages/views/workflows/runs/components/workflow-run-dialog.tsx`: applies node
  defaults beneath user/instance overrides and includes them in the actual request.
- The detail page supplies current draft intake text separately from the published
  field schema. Its integration test submits fresh inline edits without a save.