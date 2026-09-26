CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS work_sync_change_position ON work_sync_change (workspace_id, sequence);
