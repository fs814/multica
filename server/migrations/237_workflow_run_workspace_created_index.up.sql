-- Run list view: newest first within a workspace.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_workflow_run_ws_created
    ON workflow_run (workspace_id, created_at DESC);
