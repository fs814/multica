CREATE UNIQUE INDEX CONCURRENTLY issue_pool_cycle_autopilot_run_key ON issue_pool_cycle (autopilot_run_id) WHERE autopilot_run_id IS NOT NULL;
