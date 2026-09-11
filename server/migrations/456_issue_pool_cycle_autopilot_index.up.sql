CREATE INDEX CONCURRENTLY issue_pool_cycle_autopilot_created_index ON issue_pool_cycle (autopilot_id, created_at DESC);
