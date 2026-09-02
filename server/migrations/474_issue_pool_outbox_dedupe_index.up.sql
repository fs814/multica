CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_issue_pool_outbox_dedupe
    ON issue_pool_notification_outbox(event_type, cycle_id, COALESCE(item_id, '00000000-0000-0000-0000-000000000000'::uuid), recipient_id);
