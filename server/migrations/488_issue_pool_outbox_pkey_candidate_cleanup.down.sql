CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS issue_pool_notification_outbox_pkey_candidate
    ON issue_pool_notification_outbox (id);
