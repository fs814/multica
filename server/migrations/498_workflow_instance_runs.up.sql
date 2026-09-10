CREATE INDEX CONCURRENTLY workflow_instance_runs_idx ON workflow_run (workspace_id, input_instance_id, created_at DESC) WHERE input_instance_id IS NOT NULL;
