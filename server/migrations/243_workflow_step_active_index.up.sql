-- Reconciler: find non-terminal Steps (ready past activation timeout, agent step
-- without a task, running step whose task already ended). Partial to stay
-- proportional to in-flight work.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_workflow_step_active
    ON workflow_step_instance (status, activation_timeout_at)
    WHERE status IN ('pending', 'ready', 'queued', 'running', 'submitted', 'waiting_acceptance');
