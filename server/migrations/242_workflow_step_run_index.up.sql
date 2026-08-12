-- Run detail / timeline view: all steps of a Run in creation order.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_workflow_step_run
    ON workflow_step_instance (run_id, created_at);
