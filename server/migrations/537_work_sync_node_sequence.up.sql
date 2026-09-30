CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS work_sync_node_sequence ON work_sync_receipt (workspace_id, node_id, incarnation, sequence);
