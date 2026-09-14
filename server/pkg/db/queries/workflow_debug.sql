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

-- name: GetWorkflowDebugExecution :one
SELECT * FROM workflow_debug_task_execution WHERE id = $1;

-- name: LockWorkflowDebugTasksForRun :many
SELECT t.* FROM agent_task_queue t JOIN workflow_step_instance s ON s.id=t.workflow_step_instance_id
WHERE s.run_id=$1 AND s.workspace_id=$2 ORDER BY t.id FOR UPDATE OF t;

-- name: MarkWorkflowDebugExecutionStop :exec
UPDATE workflow_debug_task_execution SET stop_requested_at=COALESCE(stop_requested_at,clock_timestamp())
WHERE id=$1 AND workspace_id=$2 AND delivery_drained_at IS NULL;

-- name: SetWorkflowDebugWaitingStop :exec
UPDATE workflow_run SET debug_cleanup_state='waiting_stop'
WHERE id=$1 AND workspace_id=$2 AND execution_mode='draft_test' AND details_purged_at IS NULL;

-- name: ListWorkflowStepsForUpdate :many
SELECT * FROM workflow_step_instance WHERE run_id=$1 AND workspace_id=$2 ORDER BY id FOR UPDATE;

-- name: CountWorkflowDebugPendingUploads :one
SELECT count(*)::bigint FROM workflow_debug_upload WHERE claim_id=$1 AND state='pending';

-- name: BeginWorkflowDebugUpload :one
INSERT INTO workflow_debug_upload (workspace_id,run_id,claim_id,task_id) VALUES ($1,$2,$3,$4) RETURNING *;

-- name: SettleWorkflowDebugUpload :execrows
UPDATE workflow_debug_upload SET state=$3, attachment_id=sqlc.narg('attachment_id'), settled_at=clock_timestamp()
WHERE id=$1 AND workspace_id=$2 AND state='pending';

-- name: QueueWorkflowDebugAttachmentCleanup :exec
INSERT INTO workflow_debug_cleanup_object (workspace_id,run_id,object_id,object_kind)
SELECT $2,$1,a.id,'attachment' FROM attachment a JOIN agent_task_queue t ON t.id=a.task_id
JOIN workflow_step_instance s ON s.id=t.workflow_step_instance_id
WHERE s.run_id=$1 AND s.workspace_id=$2 AND a.issue_id IS NULL AND a.comment_id IS NULL
 AND a.chat_session_id IS NULL AND a.chat_message_id IS NULL AND a.source_context_id IS NULL
ON CONFLICT (run_id,object_kind,object_id) DO NOTHING;

-- name: UnlinkWorkflowDebugSharedAttachments :exec
UPDATE attachment a SET task_id=NULL FROM agent_task_queue t,workflow_step_instance s
WHERE a.task_id=t.id AND s.id=t.workflow_step_instance_id AND s.run_id=$1 AND s.workspace_id=$2
 AND (a.issue_id IS NOT NULL OR a.comment_id IS NOT NULL OR a.chat_session_id IS NOT NULL
 OR a.chat_message_id IS NOT NULL OR a.source_context_id IS NOT NULL);

-- name: PurgeWorkflowDebugSnapshot :exec
UPDATE workflow_execution_snapshot SET definition=NULL, environment_snapshot=NULL,purged_at=clock_timestamp()
WHERE id=$1 AND workspace_id=$2 AND purged_at IS NULL;

-- name: PurgeWorkflowDebugSteps :exec
UPDATE workflow_step_instance SET input='{}',output=NULL,failure_detail=NULL,routing_reason=NULL,
 node_key='expired:node:'||encode(sha256(convert_to(node_key,'UTF8')),'hex'),
 expansion_key=CASE WHEN expansion_key IS NULL THEN NULL ELSE 'expired:expansion:'||encode(sha256(convert_to(expansion_key,'UTF8')),'hex') END,
 failure_reason=CASE WHEN failure_reason IS NULL THEN NULL ELSE 'details_expired' END
WHERE run_id=$1 AND workspace_id=$2;

-- name: PurgeWorkflowDebugTasks :exec
UPDATE agent_task_queue t SET context='{}',result=NULL,error=NULL,trigger_summary=NULL,wait_reason=NULL,
 session_id=NULL,retired_session_id=NULL,work_dir=NULL,durable_work_dir=NULL,branch_name=NULL,cancelled_by_name=NULL,
 handoff_note=NULL,runtime_mcp_overlay=NULL,runtime_connected_apps=NULL,
 failure_reason=CASE WHEN t.failure_reason IS NULL THEN NULL ELSE 'details_expired' END,
 originator_source=CASE WHEN originator_source IN ('direct_human','delegation','comment_source','rule_owner','owner_fallback','backfill','unattributed') THEN originator_source ELSE NULL END,
 trigger_evidence_kind='workflow_step'
FROM workflow_step_instance s WHERE s.id=t.workflow_step_instance_id AND s.run_id=$1 AND s.workspace_id=$2;

-- name: PurgeWorkflowDebugSubmissions :exec
UPDATE workflow_submission SET artifact='{}',rationale='',root_cause=NULL,raw_result=NULL,validation_errors=NULL,confidence=NULL
WHERE run_id=$1 AND workspace_id=$2;

-- name: PurgeWorkflowDebugAcceptances :exec
UPDATE workflow_acceptance SET context='{}',rework_target_node_key=NULL,reason='details_expired'
WHERE run_id=$1 AND workspace_id=$2;

-- name: PurgeWorkflowDebugEvents :exec
UPDATE workflow_event SET payload='{}',idempotency_key='expired:event:'||encode(sha256(convert_to(idempotency_key,'UTF8')),'hex'),
 event_type=CASE WHEN event_type IN ('run.started','run.completed','run.failed','run.blocked','run.cancelled',
 'step.activated','step.queued','step.submitted','step.passed','step.failed','step.blocked','step.skipped',
 'acceptance.requested','acceptance.decided','rework.requested') THEN event_type ELSE 'details_expired' END
WHERE run_id=$1 AND workspace_id=$2;

-- name: PurgeWorkflowDebugMessages :exec
DELETE FROM task_message m USING agent_task_queue t,workflow_step_instance s
WHERE m.task_id=t.id AND t.workflow_step_instance_id=s.id AND s.run_id=$1 AND s.workspace_id=$2;

-- name: MarkWorkflowDebugDatabasePurged :one
UPDATE workflow_run SET input='{}',context='{}',failure_detail=NULL,source_event_id=NULL,
 policy=COALESCE((SELECT jsonb_object_agg(key,value) FROM jsonb_each(policy) WHERE jsonb_typeof(value)='number'
 AND key IN ('max_total_steps','max_attempts_per_node','max_rework_rounds','max_fan_out','max_duration_seconds','max_cost_cents')),'{}'),
 blocked_reason=CASE WHEN blocked_reason IS NULL THEN NULL ELSE 'details_expired' END,
 failure_reason=CASE WHEN failure_reason IN ('debug_deadline_exceeded','cancelled') THEN failure_reason WHEN failure_reason IS NULL THEN NULL ELSE 'details_expired' END,
 debug_cleanup_state='purging',details_purged_at=clock_timestamp(),bytes_released_at=clock_timestamp()
WHERE id=$1 AND workspace_id=$2 AND execution_mode='draft_test' AND details_purged_at IS NULL
 AND status IN ('completed','failed','cancelled') AND debug_purge_after<=clock_timestamp() RETURNING *;

-- name: ListWorkflowDebugCleanupObjects :many
SELECT * FROM workflow_debug_cleanup_object WHERE run_id=$1 AND workspace_id=$2 AND completed_at IS NULL
 AND next_attempt_at<=clock_timestamp() ORDER BY id;

-- name: CompleteWorkflowDebugCleanupObject :exec
UPDATE workflow_debug_cleanup_object SET completed_at=clock_timestamp(),error_code=NULL WHERE id=$1 AND workspace_id=$2;

-- name: RetryWorkflowDebugCleanupObject :exec
UPDATE workflow_debug_cleanup_object SET attempts=attempts+1,error_code='delete_failed',
 next_attempt_at=clock_timestamp()+LEAST(300,power(2,LEAST(attempts,8)))*interval '1 second'
WHERE id=$1 AND workspace_id=$2;

-- name: CompleteWorkflowDebugPurge :execrows
UPDATE workflow_run r SET debug_cleanup_state='purged',purge_completed_at=clock_timestamp()
WHERE r.id=$1 AND r.workspace_id=$2 AND r.execution_mode='draft_test' AND r.debug_cleanup_state='purging'
 AND NOT EXISTS (SELECT 1 FROM workflow_debug_cleanup_object o WHERE o.run_id=r.id AND o.completed_at IS NULL);

-- name: GetWorkflowDebugQuota :one
SELECT * FROM workflow_debug_quota WHERE workspace_id=$1;

-- name: ListWorkflowDebugClaimCandidates :many
SELECT t.* FROM agent_task_queue t JOIN workflow_step_instance s ON s.id=t.workflow_step_instance_id
JOIN workflow_run r ON r.id=s.run_id
WHERE t.runtime_id=$1 AND t.status='queued' AND r.execution_mode='draft_test'
 AND r.status IN ('pending','running','waiting_acceptance','blocked') AND r.debug_stop_requested_at IS NULL
 AND r.details_purged_at IS NULL AND r.debug_deadline_at>clock_timestamp()
ORDER BY t.priority DESC,t.created_at,t.id LIMIT 20;

-- name: DispatchWorkflowDebugTask :one
UPDATE agent_task_queue SET status='dispatched',dispatched_at=clock_timestamp(),
 prepare_lease_expires_at=clock_timestamp()+interval '5 minutes'
WHERE id=$1 AND runtime_id=$2 AND status='queued' AND debug_never_dispatched_at IS NULL RETURNING *;

-- name: StartWorkflowDebugTask :execrows
UPDATE agent_task_queue SET status='running',started_at=COALESCE(started_at,clock_timestamp()),prepare_lease_expires_at=NULL
WHERE id=$1 AND status IN ('dispatched','waiting_local_directory');

-- name: CompleteWorkflowDebugTask :execrows
UPDATE agent_task_queue SET status=$2,completed_at=clock_timestamp(),result=sqlc.narg('result')::jsonb,
 error=sqlc.narg('error')::text,failure_reason=sqlc.narg('failure_reason')::text,
 session_id=sqlc.narg('session_id')::text,work_dir=sqlc.narg('work_dir')::text,
 durable_work_dir=sqlc.narg('durable_work_dir')::text,branch_name=sqlc.narg('branch_name')::text,
 retired_session_id=sqlc.narg('retired_session_id')::text,prepare_lease_expires_at=NULL
WHERE id=$1 AND status NOT IN ('completed','failed','cancelled');

-- name: SetWorkflowDebugTaskSession :exec
UPDATE agent_task_queue SET session_id=sqlc.narg('session_id')::text,work_dir=sqlc.narg('work_dir')::text,
 durable_work_dir=COALESCE(sqlc.narg('durable_work_dir')::text,durable_work_dir),
 branch_name=COALESCE(sqlc.narg('branch_name')::text,branch_name)
WHERE id=$1;

-- name: SetWorkflowDebugTaskCancelAck :exec
UPDATE agent_task_queue SET branch_name=COALESCE(branch_name,sqlc.narg('branch_name')::text),
 durable_work_dir=COALESCE(durable_work_dir,sqlc.narg('durable_work_dir')::text),error=COALESCE(error,sqlc.narg('error')::text)
WHERE id=$1 AND status='cancelled';

-- name: SetWorkflowDebugTaskWaiting :execrows
UPDATE agent_task_queue SET status='waiting_local_directory',wait_reason=$2 WHERE id=$1 AND status='dispatched';

-- name: InsertWorkflowDebugTaskMessage :exec
INSERT INTO task_message (task_id,seq,type,tool,content,input,output,created_at,output_truncated)
SELECT $1,$2,$3,sqlc.narg('tool')::text,sqlc.narg('content')::text,sqlc.narg('input')::jsonb,sqlc.narg('output')::text,$4,sqlc.narg('output_truncated')::boolean
WHERE NOT EXISTS (SELECT 1 FROM task_message WHERE task_id=$1 AND seq=$2);

-- name: GetWorkflowDebugCleanupAttachment :one
SELECT * FROM attachment WHERE id=$1 AND workspace_id=$2;

-- name: SetWorkflowDebugStopConfirmed :exec
UPDATE workflow_run SET debug_cleanup_state='retained'
WHERE id=$1 AND workspace_id=$2 AND debug_cleanup_state='waiting_stop' AND details_purged_at IS NULL;
