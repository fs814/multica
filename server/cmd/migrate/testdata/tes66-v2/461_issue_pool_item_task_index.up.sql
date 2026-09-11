CREATE UNIQUE INDEX CONCURRENTLY issue_pool_item_task_key ON issue_pool_item (task_id) WHERE task_id IS NOT NULL;
