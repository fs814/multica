# Input instance source map

- `server/internal/handler/workflow_input_instance.go`: member access, request
  validation, CRUD response shape and revision conflict handling.
- `server/pkg/db/queries/workflow_input_instance.sql`: template/workspace scope.
- `server/migrations/491_workflow_input_instance.up.sql`: persisted input values.
- `server/migrations/495_workflow_input_instance_version.up.sql`: version and creator metadata.
- `server/internal/handler/workflow_run.go`: scoped published version header and version-aware retry fingerprint.
- `packages/core/workflows/input-instance-schemas.ts`: wire response validation.
- `packages/core/workflows/input-instances.ts`: workspace-scoped cache and writes.
- `packages/core/workflows/queries.ts`: published run definition cache, separate from mutable editor drafts.
- `packages/views/workflows/runs/components/workflow-input-instances.tsx`: named
  selection, save, update and delete controls.
- `packages/views/workflows/runs/components/workflow-run-dialog.tsx`: input
  loading, graph validation, fresh run keys and stable submission retries.