CREATE UNIQUE INDEX CONCURRENTLY workflow_instance_idempotency_idx ON workflow_input_instance (workspace_id, idempotency_key) WHERE idempotency_key IS NOT NULL;
