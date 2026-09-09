CREATE INDEX CONCURRENTLY idx_workflow_input_instance_template ON workflow_input_instance (workspace_id, template_id, updated_at DESC);
