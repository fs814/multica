CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS work_sync_operation_identity ON work_sync_receipt (workspace_id, operation_id);
