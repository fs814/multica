-- 'Each Agent Step attempt has at most one active Task' (plan section 4).
-- A partial unique index on task_id enforces the converse direction too: one
-- Agent Task can never be claimed by two Step attempts, which is what would
-- let a crash-replay double-count a submission.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_workflow_step_task
    ON workflow_step_instance (task_id)
    WHERE task_id IS NOT NULL;
