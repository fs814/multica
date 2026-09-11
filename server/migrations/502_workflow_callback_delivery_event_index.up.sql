CREATE UNIQUE INDEX CONCURRENTLY idx_workflow_callback_delivery_event ON workflow_callback_delivery (workspace_id, destination_id, event_key);
