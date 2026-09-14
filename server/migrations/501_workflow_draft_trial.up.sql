-- Draft trial storage is inert until all server/worker and daemon gates are ready.
-- Relationships are validated in application transactions, without foreign keys.
CREATE TABLE workflow_execution_snapshot (
 id uuid NOT NULL DEFAULT gen_random_uuid(), workspace_id uuid NOT NULL, template_id uuid NOT NULL,
 base_revision bigint NOT NULL CHECK (base_revision > 0), base_draft_version_id uuid,
 definition jsonb, graph_schema_version integer NOT NULL CHECK (graph_schema_version IN (1,2)),
 definition_hash text NOT NULL, environment_snapshot jsonb, created_by uuid NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(), purged_at timestamptz,
 CHECK ((purged_at IS NULL AND definition IS NOT NULL AND environment_snapshot IS NOT NULL)
     OR (purged_at IS NOT NULL AND definition IS NULL AND environment_snapshot IS NULL))
);
CREATE TABLE workflow_debug_quota (
 workspace_id uuid NOT NULL, payload_bytes bigint NOT NULL DEFAULT 0 CHECK (payload_bytes >= 0)
);
CREATE TABLE workflow_debug_policy (
 workspace_id uuid NOT NULL, revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
 enabled boolean NOT NULL DEFAULT false,
 user_active_runs integer NOT NULL DEFAULT 2 CHECK (user_active_runs BETWEEN 1 AND 20),
 workspace_active_runs integer NOT NULL DEFAULT 5 CHECK (workspace_active_runs BETWEEN 1 AND 100),
 user_starts_per_hour integer NOT NULL DEFAULT 20 CHECK (user_starts_per_hour BETWEEN 1 AND 1000),
 max_duration_seconds integer NOT NULL DEFAULT 1800 CHECK (max_duration_seconds BETWEEN 60 AND 86400),
 retention_seconds integer NOT NULL DEFAULT 2592000 CHECK (retention_seconds BETWEEN 86400 AND 7776000),
 payload_capacity_bytes bigint NOT NULL DEFAULT 104857600 CHECK (payload_capacity_bytes BETWEEN 1048576 AND 1073741824),
 updated_by uuid, updated_at timestamptz NOT NULL DEFAULT now(),
 CHECK (user_active_runs <= workspace_active_runs)
);
ALTER TABLE workflow_run ALTER COLUMN template_version_id DROP NOT NULL;
ALTER TABLE workflow_run
 ADD COLUMN execution_mode text NOT NULL DEFAULT 'published',
 ADD COLUMN execution_snapshot_id uuid,
 ADD COLUMN debug_deadline_at timestamptz,
 ADD COLUMN debug_request_hash text,
 ADD COLUMN debug_policy_revision bigint,
 ADD COLUMN debug_retention_seconds integer,
 ADD COLUMN debug_purge_after timestamptz,
 ADD COLUMN debug_payload_bytes bigint CHECK (debug_payload_bytes >= 0),
 ADD COLUMN debug_stop_requested_at timestamptz,
 ADD COLUMN debug_cleanup_state text CHECK (debug_cleanup_state IN ('retained','waiting_stop','purging','purged')),
 ADD COLUMN details_purged_at timestamptz,
 ADD COLUMN purge_completed_at timestamptz,
 ADD COLUMN bytes_released_at timestamptz,
 ADD CONSTRAINT workflow_run_execution_source CHECK (
   (execution_mode = 'published' AND template_version_id IS NOT NULL AND execution_snapshot_id IS NULL AND debug_deadline_at IS NULL)
   OR (execution_mode = 'draft_test' AND template_version_id IS NULL AND execution_snapshot_id IS NOT NULL
       AND debug_deadline_at IS NOT NULL AND issue_id IS NULL AND input_instance_id IS NULL
       AND callback_destination_id IS NULL AND source = 'manual' AND accountable_user_id IS NOT NULL
       AND debug_request_hash IS NOT NULL AND debug_policy_revision > 0 AND debug_policy_revision IS NOT NULL
       AND debug_retention_seconds > 0 AND debug_retention_seconds IS NOT NULL
       AND debug_payload_bytes IS NOT NULL AND debug_cleanup_state IS NOT NULL)),
 ADD CONSTRAINT workflow_run_debug_cleanup CHECK (
   (details_purged_at IS NULL AND bytes_released_at IS NULL AND purge_completed_at IS NULL
     AND (debug_cleanup_state IS NULL OR debug_cleanup_state IN ('retained','waiting_stop')))
   OR (details_purged_at IS NOT NULL AND bytes_released_at IS NOT NULL AND debug_cleanup_state IN ('purging','purged')
     AND (debug_cleanup_state <> 'purged' OR purge_completed_at IS NOT NULL)));
ALTER TABLE agent_task_queue ADD COLUMN debug_never_dispatched_at timestamptz;
CREATE TABLE workflow_debug_task_execution (
 id uuid NOT NULL DEFAULT gen_random_uuid(), workspace_id uuid NOT NULL, run_id uuid NOT NULL,
 step_id uuid NOT NULL, task_id uuid NOT NULL, task_attempt integer NOT NULL CHECK (task_attempt > 0),
 runtime_id uuid NOT NULL, daemon_incarnation_id uuid NOT NULL,
 claim_generation bigint NOT NULL CHECK (claim_generation > 0), claimed_at timestamptz NOT NULL DEFAULT now(),
 stop_requested_at timestamptz, receipt_kind text CHECK (receipt_kind IN ('complete','fail','cancel_ack')),
 receipt_id uuid, receipt_hash text, receipt_received_at timestamptz, process_stopped_at timestamptz,
 final_message_seq integer CHECK (final_message_seq >= 0), delivery_drained_at timestamptz,
 CHECK ((receipt_id IS NULL AND receipt_kind IS NULL AND receipt_hash IS NULL AND receipt_received_at IS NULL
     AND process_stopped_at IS NULL AND final_message_seq IS NULL AND delivery_drained_at IS NULL)
   OR (receipt_id IS NOT NULL AND receipt_kind IS NOT NULL AND receipt_hash IS NOT NULL AND receipt_received_at IS NOT NULL
     AND process_stopped_at IS NOT NULL AND final_message_seq IS NOT NULL AND delivery_drained_at IS NOT NULL))
);
CREATE TABLE workflow_debug_stop_request (
 id uuid NOT NULL DEFAULT gen_random_uuid(), workspace_id uuid NOT NULL, run_id uuid NOT NULL,
 task_id uuid NOT NULL, claim_id uuid NOT NULL, attempts integer NOT NULL DEFAULT 0,
 next_attempt_at timestamptz NOT NULL DEFAULT now(), error_code text CHECK (error_code IN ('offline','delivery_failed','retry_pending')),
 resolved_at timestamptz
);
CREATE TABLE workflow_debug_cleanup_object (
 id uuid NOT NULL DEFAULT gen_random_uuid(), workspace_id uuid NOT NULL, run_id uuid NOT NULL,
 object_id uuid NOT NULL, object_kind text NOT NULL CHECK (object_kind IN ('attachment','transcript','search','cache')),
 attempts integer NOT NULL DEFAULT 0, next_attempt_at timestamptz NOT NULL DEFAULT now(),
 error_code text CHECK (error_code IN ('delete_failed','capability_pending','retry_pending')), completed_at timestamptz
);
