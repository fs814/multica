CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_work_sync_grant_identity ON work_sync_grant (workspace_id, node_id);
