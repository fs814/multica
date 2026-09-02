CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_inbox_issue_pool_notification_dedupe
    ON inbox_item ((details->>'notification_id'))
    WHERE type='issue_pool' AND details ? 'notification_id';
