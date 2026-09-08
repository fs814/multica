CREATE INDEX CONCURRENTLY IF NOT EXISTS issue_pool_item_reconcile_index
ON issue_pool_item (status,updated_at)
WHERE status IN ('approved','queued','running','awaiting_acceptance');
