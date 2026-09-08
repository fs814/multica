CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_issue_pool_outbox_pending
    ON issue_pool_notification_outbox(available_at, created_at) WHERE delivered_at IS NULL;
