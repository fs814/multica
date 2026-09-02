CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_issue_pool_outbox_id
    ON issue_pool_notification_outbox(id);
