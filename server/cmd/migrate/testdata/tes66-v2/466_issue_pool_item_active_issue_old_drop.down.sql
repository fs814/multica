CREATE UNIQUE INDEX CONCURRENTLY issue_pool_item_active_issue_key ON issue_pool_item (issue_id) WHERE status IN ('claimed', 'approved');
