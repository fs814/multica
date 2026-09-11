CREATE INDEX CONCURRENTLY idx_workflow_callback_delivery_claim ON workflow_callback_delivery (available_at, created_at) WHERE status = 'queued';
