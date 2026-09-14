CREATE INDEX CONCURRENTLY idx_workflow_debug_run_deadline ON workflow_run (debug_deadline_at) WHERE execution_mode = 'draft_test' AND status IN ('pending','running','blocked','waiting_acceptance');
