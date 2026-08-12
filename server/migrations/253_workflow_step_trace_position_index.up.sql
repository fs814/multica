-- Run-detail lookups read the durable activation history in trace order.
-- This is separate from migration 252 because CREATE INDEX CONCURRENTLY cannot
-- share a transaction with the column/backfill statements there.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_workflow_step_run_trace_position
    ON workflow_step_instance (run_id, trace_position);