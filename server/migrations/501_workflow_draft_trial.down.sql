-- Destructive rollback is forbidden while any draft trial identity exists.
DO $$ BEGIN
 IF EXISTS (SELECT 1 FROM workflow_run WHERE execution_mode = 'draft_test') THEN
  RAISE EXCEPTION 'draft trial records exist; retain the dual-source engine and schema';
 END IF;
END $$;
DROP TABLE workflow_debug_cleanup_object, workflow_debug_stop_request, workflow_debug_task_execution,
 workflow_debug_policy, workflow_debug_quota, workflow_execution_snapshot;
ALTER TABLE agent_task_queue DROP COLUMN debug_never_dispatched_at;
ALTER TABLE workflow_run DROP CONSTRAINT workflow_run_execution_source, DROP CONSTRAINT workflow_run_debug_cleanup,
 DROP COLUMN execution_mode, DROP COLUMN execution_snapshot_id, DROP COLUMN debug_deadline_at,
 DROP COLUMN debug_request_hash, DROP COLUMN debug_policy_revision, DROP COLUMN debug_retention_seconds,
 DROP COLUMN debug_purge_after, DROP COLUMN debug_payload_bytes, DROP COLUMN debug_stop_requested_at,
 DROP COLUMN debug_cleanup_state, DROP COLUMN details_purged_at, DROP COLUMN purge_completed_at, DROP COLUMN bytes_released_at;
ALTER TABLE workflow_run ALTER COLUMN template_version_id SET NOT NULL;
