CREATE INDEX CONCURRENTLY workflow_instance_list_idx ON workflow_input_instance (workspace_id, updated_at DESC, id);
