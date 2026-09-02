-- TES-66 MVP execution lifecycle. This is append-only because migrations
-- 449-458 may already have run in local deployments.
ALTER TABLE autopilot DROP CONSTRAINT autopilot_execution_mode_check;
ALTER TABLE autopilot ADD CONSTRAINT autopilot_execution_mode_check
    CHECK (execution_mode IN ('create_issue', 'run_only', 'issue_pool'));

ALTER TABLE issue_pool_cycle
    ADD COLUMN autopilot_run_id UUID,
    ADD COLUMN approved_count INTEGER NOT NULL DEFAULT 0 CHECK (approved_count >= 0),
    ADD COLUMN rejected_count INTEGER NOT NULL DEFAULT 0 CHECK (rejected_count >= 0),
    ADD COLUMN dispatched_count INTEGER NOT NULL DEFAULT 0 CHECK (dispatched_count >= 0),
    ADD COLUMN awaiting_acceptance_count INTEGER NOT NULL DEFAULT 0 CHECK (awaiting_acceptance_count >= 0),
    ADD COLUMN completed_count INTEGER NOT NULL DEFAULT 0 CHECK (completed_count >= 0),
    ADD COLUMN failed_count INTEGER NOT NULL DEFAULT 0 CHECK (failed_count >= 0),
    ADD COLUMN deferred_count INTEGER NOT NULL DEFAULT 0 CHECK (deferred_count >= 0),
    ADD COLUMN completed_at TIMESTAMPTZ;

UPDATE issue_pool_cycle cycle
SET approved_count = counts.approved_count,
    rejected_count = counts.rejected_count
FROM (
    SELECT cycle_id,
           COUNT(*) FILTER (WHERE status = 'approved')::INTEGER AS approved_count,
           COUNT(*) FILTER (WHERE status = 'rejected')::INTEGER AS rejected_count
    FROM issue_pool_item
    GROUP BY cycle_id
) counts
WHERE cycle.id = counts.cycle_id;
UPDATE issue_pool_cycle
SET status = CASE WHEN approved_count > 0 THEN 'running' ELSE 'completed' END,
    completed_at = CASE WHEN approved_count = 0 THEN COALESCE(reviewed_at, updated_at) ELSE NULL END
WHERE status = 'reviewed';
ALTER TABLE issue_pool_cycle DROP CONSTRAINT issue_pool_cycle_status_check;
ALTER TABLE issue_pool_cycle ADD CONSTRAINT issue_pool_cycle_status_check
    CHECK (status IN ('scanning', 'awaiting_review', 'running', 'completed', 'partial', 'failed'));

ALTER TABLE issue_pool_item DROP CONSTRAINT issue_pool_item_status_check;
ALTER TABLE issue_pool_item ADD CONSTRAINT issue_pool_item_status_check
    CHECK (status IN (
        'claimed', 'approved', 'rejected', 'queued', 'running',
        'awaiting_acceptance', 'completed', 'failed', 'blocked',
        'cancelled', 'deferred'
    ));
ALTER TABLE issue_pool_item
    ADD COLUMN task_id UUID,
    ADD COLUMN failure_reason TEXT,
    ADD COLUMN dispatch_started_at TIMESTAMPTZ,
    ADD COLUMN awaiting_acceptance_at TIMESTAMPTZ,
    ADD COLUMN completed_at TIMESTAMPTZ;
