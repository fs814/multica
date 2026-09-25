CREATE INDEX CONCURRENTLY IF NOT EXISTS work_sync_entity_history ON work_sync_change (workspace_id, kind, entity_id, sequence DESC);
