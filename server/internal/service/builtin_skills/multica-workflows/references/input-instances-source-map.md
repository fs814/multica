# Input instance source map

- server/internal/handler/workflow_input_instance.go: creation/update, schema binding, creator, reference validation and idempotency.
- server/internal/handler/workflow_instance_management.go: workspace-scoped browsing, detail, version reads, readiness, archive/restore, runs.
- server/internal/workflow/input_instance.go: authoritative snapshot resolution under row lock inside StartRun; saved/temporary/history modes.
- server/internal/workflow/engine.go: atomic issue/run/step creation and immutable input provenance.
- server/pkg/db/queries/workflow_input_instance.sql: revision CAS, archive, idempotency, paginated queries.
- server/migrations/496_workflow_instance_management.up.sql through 499_workflow_instance_list.up.sql: additive migration; old IDs and nullable version bindings preserved.
- packages/core/workflows/input-instance-schemas.ts and input-instances.ts: validated wire contract and workspace-scoped caches.
- packages/views/workflows/instances/: standalone create, detail and list; instance-specific images, explicit upgrades and run history.
- packages/core/paths/ and packages/views/layout/tab-presentation.tsx: stable web/desktop identity and tab titles.
- server/internal/handler/workflow_instance_management_test.go: lifecycle, retries, conflicts, input isolation, archive/history and access boundaries.
- packages/views/workflows/instances/instances.test.tsx: create-without-run, unsaved values, pending binding, temporary runs and conflict preservation.

- packages/views/layout/app-sidebar.tsx: workspace Workflow instances entry directly below Workflows.
- packages/core/workflows/input-instances.ts: groupWorkflowInstances groups by parent ID and resolves current names without merging same-name workflows.
- packages/views/workflows/instances/workflow-instances-page.tsx: grouped workspace list with parent/detail links and existing filters/pagination.
- packages/views/layout/app-sidebar.test.tsx and instances/instances.test.tsx: sidebar order, active state, grouped membership and navigation coverage.
