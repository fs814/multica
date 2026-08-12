-- Case-insensitive template key uniqueness per workspace (plan section 5).
-- Keep this the migration's only statement: PostgreSQL rejects CREATE INDEX
-- CONCURRENTLY inside a transaction or multi-command string.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_workflow_template_ws_key
    ON workflow_template (workspace_id, LOWER(key));
