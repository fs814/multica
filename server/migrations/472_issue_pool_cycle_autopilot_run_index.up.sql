CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_issue_pool_cycle_autopilot_run
    ON issue_pool_cycle(autopilot_run_id) WHERE autopilot_run_id IS NOT NULL;
