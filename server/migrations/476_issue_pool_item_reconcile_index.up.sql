CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_issue_pool_item_reconcile
    ON issue_pool_item(updated_at, id)
    WHERE status IN ('approved', 'dispatching', 'running', 'waiting_acceptance', 'blocked');
