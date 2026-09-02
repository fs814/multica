UPDATE issue_pool_item
SET status = CASE
        WHEN status = 'claimed' THEN 'claimed'
        WHEN status IN ('approved', 'queued', 'running', 'awaiting_acceptance') THEN 'approved'
        ELSE 'rejected'
    END,
    review_reason = CASE
        WHEN status IN ('claimed', 'approved', 'queued', 'running', 'awaiting_acceptance') THEN review_reason
        ELSE COALESCE(review_reason, failure_reason, 'execution ended before acceptance')
    END;

ALTER TABLE issue_pool_item
    DROP COLUMN completed_at,
    DROP COLUMN awaiting_acceptance_at,
    DROP COLUMN dispatch_started_at,
    DROP COLUMN failure_reason,
    DROP COLUMN task_id;
ALTER TABLE issue_pool_item DROP CONSTRAINT issue_pool_item_status_check;
ALTER TABLE issue_pool_item ADD CONSTRAINT issue_pool_item_status_check
    CHECK (status IN ('claimed', 'approved', 'rejected'));

UPDATE issue_pool_cycle
SET status = CASE
        WHEN status = 'failed' THEN 'failed'
        WHEN status IN ('completed', 'partial') THEN 'reviewed'
        ELSE 'awaiting_review'
    END;

ALTER TABLE issue_pool_cycle
    DROP COLUMN completed_at,
    DROP COLUMN deferred_count,
    DROP COLUMN failed_count,
    DROP COLUMN completed_count,
    DROP COLUMN awaiting_acceptance_count,
    DROP COLUMN dispatched_count,
    DROP COLUMN rejected_count,
    DROP COLUMN approved_count,
    DROP COLUMN autopilot_run_id;
ALTER TABLE issue_pool_cycle DROP CONSTRAINT issue_pool_cycle_status_check;
ALTER TABLE issue_pool_cycle ADD CONSTRAINT issue_pool_cycle_status_check
    CHECK (status IN ('scanning', 'awaiting_review', 'reviewed', 'failed'));

UPDATE autopilot SET execution_mode = 'run_only' WHERE execution_mode = 'issue_pool';
ALTER TABLE autopilot DROP CONSTRAINT autopilot_execution_mode_check;
ALTER TABLE autopilot ADD CONSTRAINT autopilot_execution_mode_check
    CHECK (execution_mode IN ('create_issue', 'run_only'));
