-- Acceptance panel lookup: the pending decision for a Run. Partial unique so a
-- Run cannot accumulate two competing pending acceptances — the reviewer-race
-- fence from plan section 8.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_workflow_acceptance_pending_step
    ON workflow_acceptance (step_id)
    WHERE status = 'pending';
