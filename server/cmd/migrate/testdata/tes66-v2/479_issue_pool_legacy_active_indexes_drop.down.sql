CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS issue_pool_item_active_issue_v2_key
    ON issue_pool_item(issue_id)
    WHERE status IN ('claimed', 'approved', 'queued', 'running', 'awaiting_acceptance');
