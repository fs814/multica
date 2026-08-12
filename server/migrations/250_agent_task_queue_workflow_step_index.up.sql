-- Reverse lookup for the reconciler's 'terminal Task not consumed by Step'
-- repair (plan section 7): given recently-finished workflow tasks, find the ones
-- whose Step never advanced. Partial so the index covers only workflow-created
-- tasks, leaving the large legacy task population out of it.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_agent_task_queue_workflow_step
    ON agent_task_queue (workflow_step_instance_id)
    WHERE workflow_step_instance_id IS NOT NULL;
