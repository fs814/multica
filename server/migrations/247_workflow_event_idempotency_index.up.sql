-- Event replay fence (plan section 4): 'State and Workflow Event commit
-- atomically; replay cannot advance twice.' The engine writes its command's
-- idempotency key here inside the state-change transaction, so a duplicate
-- command violates this index and the whole transaction rolls back.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_workflow_event_ws_idempotency
    ON workflow_event (workspace_id, idempotency_key);
