CREATE INDEX CONCURRENTLY idx_workflow_debug_run_quota ON workflow_run (workspace_id, accountable_user_id, created_at) WHERE execution_mode = 'draft_test';
