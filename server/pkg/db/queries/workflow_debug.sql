-- name: GetWorkflowExecutionSnapshot :one
SELECT * FROM workflow_execution_snapshot WHERE id = $1 AND workspace_id = $2;

-- name: CreateWorkflowExecutionSnapshot :one
INSERT INTO workflow_execution_snapshot (workspace_id, template_id, base_revision, base_draft_version_id,
 definition, graph_schema_version, definition_hash, environment_snapshot, created_by)
VALUES ($1,$2,$3,sqlc.narg('base_draft_version_id'),$4,$5,$6,$7,$8) RETURNING *;

-- name: EnsureWorkflowDebugQuota :exec
INSERT INTO workflow_debug_quota (workspace_id) VALUES ($1) ON CONFLICT (workspace_id) DO NOTHING;

-- name: LockWorkflowDebugQuota :one
SELECT * FROM workflow_debug_quota WHERE workspace_id = $1 FOR UPDATE;

-- name: EnsureWorkflowDebugPolicy :exec
INSERT INTO workflow_debug_policy (workspace_id) VALUES ($1) ON CONFLICT (workspace_id) DO NOTHING;

-- name: GetWorkflowDebugPolicy :one
SELECT * FROM workflow_debug_policy WHERE workspace_id = $1;

-- name: UpdateWorkflowDebugPolicy :one
UPDATE workflow_debug_policy SET enabled = $2, user_active_runs = $3, workspace_active_runs = $4,
 user_starts_per_hour = $5, max_duration_seconds = $6, retention_seconds = $7, payload_capacity_bytes = $8,
 updated_by = $9, updated_at = now(), revision = revision + 1
WHERE workspace_id = $1 AND revision = sqlc.arg('expected_revision') RETURNING *;

-- name: GetWorkflowDebugUsage :one
SELECT count(*) FILTER (WHERE status IN ('pending','running','blocked','waiting_acceptance'))::bigint AS workspace_active,
 count(*) FILTER (WHERE accountable_user_id = sqlc.arg('user_id') AND status IN ('pending','running','blocked','waiting_acceptance'))::bigint AS user_active,
 count(*) FILTER (WHERE accountable_user_id = sqlc.arg('user_id') AND created_at > clock_timestamp() - interval '1 hour')::bigint AS user_hourly,
 COALESCE(ceil(extract(epoch FROM (min(created_at) FILTER (WHERE accountable_user_id = sqlc.arg('user_id') AND created_at > clock_timestamp() - interval '1 hour') + interval '1 hour' - clock_timestamp()))),0)::bigint AS retry_after_seconds,
 count(*) FILTER (WHERE debug_stop_requested_at IS NOT NULL AND EXISTS (
  SELECT 1 FROM workflow_debug_task_execution x WHERE x.run_id = workflow_run.id AND x.delivery_drained_at IS NULL
 ))::bigint AS waiting_stop
FROM workflow_run WHERE workflow_run.workspace_id = $1 AND execution_mode = 'draft_test';

-- name: AddWorkflowDebugPayloadBytes :execrows
UPDATE workflow_debug_quota SET payload_bytes = payload_bytes + sqlc.arg('bytes')::bigint
WHERE workspace_id = $1 AND payload_bytes + sqlc.arg('bytes')::bigint >= 0;

-- name: CreateWorkflowDraftTestRun :one
INSERT INTO workflow_run (workspace_id, template_id, template_version_id, status, source, idempotency_key,
 accountable_user_id, input, context, policy, execution_mode, execution_snapshot_id, debug_deadline_at,
 debug_request_hash, debug_policy_revision, debug_retention_seconds, debug_payload_bytes, debug_cleanup_state, created_at)
VALUES ($1,$2,NULL,'pending','manual',$3,$4,$5,$6,$7,'draft_test',$8,$9,$10,$11,$12,$13,'retained',$14)
RETURNING *;

-- name: ListWorkflowDraftTestRuns :many
SELECT * FROM workflow_run WHERE workspace_id = $1 AND execution_mode = 'draft_test'
 AND (sqlc.narg('template_id')::uuid IS NULL OR template_id = sqlc.narg('template_id')::uuid)
ORDER BY created_at DESC LIMIT sqlc.arg('limit_count')::int OFFSET sqlc.arg('offset_count')::int;

-- name: CountWorkflowDraftTestRuns :one
SELECT count(*)::bigint FROM workflow_run WHERE workspace_id = $1 AND execution_mode = 'draft_test'
 AND (sqlc.narg('template_id')::uuid IS NULL OR template_id = sqlc.narg('template_id')::uuid);

-- name: MarkWorkflowDebugTerminal :exec
UPDATE workflow_run SET debug_stop_requested_at = COALESCE(debug_stop_requested_at, clock_timestamp()),
 debug_purge_after = COALESCE(debug_purge_after, completed_at + debug_retention_seconds * interval '1 second')
WHERE id = $1 AND workspace_id = $2 AND execution_mode = 'draft_test'
 AND status IN ('completed','failed','cancelled');

-- name: ListWorkflowDebugMaintenanceRuns :many
SELECT * FROM workflow_run WHERE execution_mode = 'draft_test' AND (
 (status IN ('pending','running','blocked','waiting_acceptance') AND debug_deadline_at <= clock_timestamp()) OR
 (status IN ('completed','failed','cancelled') AND purge_completed_at IS NULL))
ORDER BY created_at LIMIT $1;

-- name: ListWorkflowDebugTasksForRun :many
-- Follow every durable task binding, not the step's latest task pointer.
SELECT t.* FROM agent_task_queue t JOIN workflow_step_instance s ON s.id = t.workflow_step_instance_id
WHERE s.run_id = $1 AND s.workspace_id = $2 ORDER BY t.id;

-- name: GetWorkflowDebugTaskRun :one
SELECT r.* FROM workflow_run r JOIN workflow_step_instance s ON s.run_id = r.id
JOIN agent_task_queue t ON t.workflow_step_instance_id = s.id
WHERE t.id = $1 AND r.execution_mode = 'draft_test';

-- name: ListWorkflowDebugExecutions :many
SELECT * FROM workflow_debug_task_execution WHERE run_id = $1 AND workspace_id = $2 ORDER BY task_id, claim_generation;

-- name: GetWorkflowDebugExecutionForUpdate :one
SELECT * FROM workflow_debug_task_execution WHERE id = $1 AND workspace_id = $2 FOR UPDATE;

-- name: CreateWorkflowDebugExecution :one
INSERT INTO workflow_debug_task_execution (workspace_id,run_id,step_id,task_id,task_attempt,runtime_id,daemon_incarnation_id,claim_generation)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8) RETURNING *;

-- name: AcceptWorkflowDebugReceipt :one
UPDATE workflow_debug_task_execution SET receipt_kind = $3, receipt_id = $4, receipt_hash = $5,
 receipt_received_at = clock_timestamp(), process_stopped_at = $6, final_message_seq = $7, delivery_drained_at = clock_timestamp()
WHERE id = $1 AND workspace_id = $2 AND receipt_id IS NULL RETURNING *;

-- name: MarkWorkflowDebugNeverDispatched :execrows
UPDATE agent_task_queue t SET status = 'cancelled', completed_at = COALESCE(completed_at,clock_timestamp()),
 debug_never_dispatched_at = clock_timestamp()
WHERE t.id = $1 AND t.status IN ('queued','deferred','cancelled') AND t.started_at IS NULL
 AND NOT EXISTS (SELECT 1 FROM workflow_debug_task_execution x WHERE x.task_id = t.id)
 AND EXISTS (SELECT 1 FROM workflow_step_instance s JOIN workflow_run r ON r.id = s.run_id
 WHERE s.id = t.workflow_step_instance_id AND r.workspace_id = $2 AND r.execution_mode = 'draft_test'
 AND r.status IN ('completed','failed','cancelled'));

-- name: EnsureWorkflowDebugStopRequest :exec
INSERT INTO workflow_debug_stop_request (workspace_id,run_id,task_id,claim_id)
VALUES ($1,$2,$3,$4) ON CONFLICT (run_id,task_id,claim_id) DO NOTHING;

-- name: ListWorkflowDebugStopRequests :many
SELECT * FROM workflow_debug_stop_request WHERE run_id = $1 AND workspace_id = $2
 AND resolved_at IS NULL AND next_attempt_at <= clock_timestamp() ORDER BY id;

-- name: ResolveWorkflowDebugStopRequest :exec
UPDATE workflow_debug_stop_request SET resolved_at = clock_timestamp(), error_code = NULL
WHERE claim_id = $1 AND workspace_id = $2;

-- name: RetryWorkflowDebugStopRequest :exec
UPDATE workflow_debug_stop_request SET attempts = attempts + 1,
 next_attempt_at = clock_timestamp() + LEAST(300, power(2,LEAST(attempts,8))) * interval '1 second', error_code = $3
WHERE id = $1 AND workspace_id = $2 AND resolved_at IS NULL;

-- name: GetWorkflowDebugMessageDelivery :one
SELECT count(DISTINCT seq)::bigint AS message_count, COALESCE(min(seq),0)::int AS first_seq,
 COALESCE(max(seq),0)::int AS last_seq FROM task_message WHERE task_id = $1;

-- name: GetWorkflowDraftVersionForUpdate :one
SELECT * FROM workflow_template_version WHERE workspace_id = $1 AND template_id = $2 AND status = 'draft' FOR UPDATE;

-- name: WorkflowDebugDatabaseTime :one
SELECT clock_timestamp()::timestamptz AS accepted_at;
