-- Reconciler hot path (plan section 7): find non-terminal Runs to inspect every
-- 30 seconds. Partial so the index stays proportional to in-flight work rather
-- than to total history.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_workflow_run_active
    ON workflow_run (updated_at)
    WHERE status IN ('pending', 'running', 'waiting_acceptance', 'blocked');
