CREATE UNIQUE INDEX CONCURRENTLY issue_pool_notification_dedup_key ON issue_pool_notification (cycle_id, recipient_id, kind);
