-- Run -> Step -> Task trace (plan section 1, success criterion): read a Run's
-- event history in order.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_workflow_event_run
    ON workflow_event (run_id, created_at);
