CREATE INDEX CONCURRENTLY idx_workflow_debug_run_cleanup ON workflow_run (debug_purge_after) WHERE execution_mode = 'draft_test' AND purge_completed_at IS NULL;
