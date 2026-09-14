CREATE UNIQUE INDEX CONCURRENTLY idx_workflow_debug_cleanup_object ON workflow_debug_cleanup_object (run_id, object_kind, object_id);
