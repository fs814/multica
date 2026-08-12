-- Idempotency fence for run creation (plan section 4): replaying an intake
-- event or retrying StartRun must collide here rather than start a second Run.
-- The engine relies on this index's violation as its dedup signal.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_workflow_run_ws_idempotency
    ON workflow_run (workspace_id, idempotency_key);
