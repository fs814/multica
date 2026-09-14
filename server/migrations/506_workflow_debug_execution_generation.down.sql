DO $$ BEGIN IF EXISTS (SELECT 1 FROM workflow_run WHERE execution_mode = 'draft_test') THEN RAISE EXCEPTION 'draft trial records exist; rollback is unsafe'; END IF; END $$;
DROP INDEX idx_workflow_debug_execution_generation;
