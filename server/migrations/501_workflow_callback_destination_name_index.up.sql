CREATE UNIQUE INDEX CONCURRENTLY idx_workflow_callback_destination_workspace_name ON workflow_callback_destination (workspace_id, LOWER(name));
