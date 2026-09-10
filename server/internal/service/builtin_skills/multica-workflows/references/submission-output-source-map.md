# Workflow submission output

- server/internal/workflow/submission_output.go: schema bound to the current step; whole-object JSON or delimited payload extraction, followed by existing submission validation.
- server/internal/daemon/workflow_output.go: enables structured final output only for Codex workflow steps; clarification remains a blocked verdict.
- server/pkg/agent/codex.go: sends the schema object in turn/start.outputSchema.
- server/internal/workflow/task_context.go: valid blocked example and explicit clarification instructions for every runtime.
- server/internal/handler/workflow_run.go: exposes the already bounded raw_result in authorized run details.
- packages/core/workflows/schemas.ts and packages/views/workflows/runs/components/workflow-run-detail-page.tsx: optional original reply display; rejected output shown as escaped text.
- submission_output_test.go, workflow_output_test.go, codex_output_schema_test.go and workflow_run_e2e_test.go: strict verdict/identity validation, provider scope, wire shape, no prose-to-pass conversion and preserved clarification.
