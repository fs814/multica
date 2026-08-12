-- One row per (run, node, attempt) — plan section 5. This is the replay fence
-- for step activation: a duplicated ActivateStep command collides here instead
-- of creating a second attempt (and therefore a second Agent Task).
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_workflow_step_run_node_attempt
    ON workflow_step_instance (run_id, node_key, attempt);
