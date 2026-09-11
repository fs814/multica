CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_issue_pool_item_active_issue_v3
    ON issue_pool_item(issue_id)
    WHERE status IN ('claimed', 'approved', 'dispatching', 'running', 'waiting_acceptance', 'blocked');
