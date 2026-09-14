CREATE UNIQUE INDEX CONCURRENTLY idx_workflow_debug_stop_claim ON workflow_debug_stop_request (run_id, task_id, claim_id);
