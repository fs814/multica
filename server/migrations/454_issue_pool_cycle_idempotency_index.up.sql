CREATE UNIQUE INDEX CONCURRENTLY issue_pool_cycle_idempotency_key ON issue_pool_cycle (autopilot_id, idempotency_key);
