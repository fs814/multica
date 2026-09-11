CREATE INDEX CONCURRENTLY issue_pool_item_cycle_index ON issue_pool_item (cycle_id, score DESC, issue_id);
