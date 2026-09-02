CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_issue_pool_item_workflow_run
    ON issue_pool_item(workflow_run_id) WHERE workflow_run_id IS NOT NULL;
