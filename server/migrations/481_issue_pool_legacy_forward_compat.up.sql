-- TES-66: finish the append-only transition from the superseded 459-469
-- direct-task prototype. WorkflowRun remains the only execution truth.
ALTER TABLE issue_pool_cycle
    ADD COLUMN IF NOT EXISTS dispatched_count INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS awaiting_acceptance_count INTEGER NOT NULL DEFAULT 0;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema=current_schema() AND table_name='issue_pool_item' AND column_name='task_id'
    ) THEN
        EXECUTE $convert$
            UPDATE issue_pool_item
            SET waiting_acceptance_at = COALESCE(waiting_acceptance_at, awaiting_acceptance_at),
                failure_code = COALESCE(failure_code, 'legacy_direct_task'),
                failure_detail = COALESCE(failure_detail, '{}'::jsonb) || jsonb_build_object(
                    'message', COALESCE(failure_reason, 'legacy direct-task item requires Workflow review'),
                    'legacy_execution', COALESCE(issue_snapshot->'legacy_execution', jsonb_strip_nulls(jsonb_build_object(
                        'prior_status', status,
                        'task_id', task_id,
                        'failure_reason', failure_reason
                    )))
                ),
                completed_at = COALESCE(completed_at, now()),
                status = 'deferred',
                updated_at = now()
            WHERE workflow_run_id IS NULL
              AND (task_id IS NOT NULL OR issue_snapshot ? 'legacy_execution')
        $convert$;
    END IF;
END $$;

WITH counts AS (
    SELECT cycle_id,
           count(*) FILTER (WHERE status = 'approved')::int AS approved_count,
           count(*) FILTER (WHERE status = 'rejected')::int AS rejected_count,
           count(*) FILTER (WHERE workflow_run_id IS NOT NULL)::int AS dispatched_count,
           count(*) FILTER (WHERE status = 'waiting_acceptance')::int AS waiting_count,
           count(*) FILTER (WHERE status = 'completed')::int AS completed_count,
           count(*) FILTER (WHERE status = 'blocked')::int AS blocked_count,
           count(*) FILTER (WHERE status = 'failed')::int AS failed_count,
           count(*) FILTER (WHERE status = 'deferred')::int AS deferred_count,
           count(*) FILTER (WHERE status = 'claimed')::int AS claimed_count,
           count(*) FILTER (WHERE status IN ('approved','dispatching','running'))::int AS running_count,
           count(*)::int AS total_count
    FROM issue_pool_item GROUP BY cycle_id
)
UPDATE issue_pool_cycle cycle
SET approved_count=counts.approved_count,
    rejected_count=counts.rejected_count,
    dispatched_count=counts.dispatched_count,
    awaiting_acceptance_count=counts.waiting_count,
    completed_count=counts.completed_count,
    blocked_count=counts.blocked_count,
    failed_count=counts.failed_count,
    deferred_count=counts.deferred_count,
    status=CASE
        WHEN counts.claimed_count > 0 THEN 'awaiting_review'
        WHEN counts.blocked_count > 0 THEN 'blocked'
        WHEN counts.waiting_count > 0 THEN 'waiting_acceptance'
        WHEN counts.running_count > 0 THEN 'running'
        WHEN counts.failed_count = counts.total_count AND counts.total_count > 0 THEN 'failed'
        WHEN counts.failed_count + counts.deferred_count > 0 THEN 'partial'
        WHEN counts.total_count = 0 OR counts.completed_count + counts.rejected_count = counts.total_count THEN 'completed'
        ELSE 'partial'
    END,
    completed_at=CASE WHEN counts.claimed_count+counts.blocked_count+counts.waiting_count+counts.running_count=0
        THEN COALESCE(cycle.completed_at,now()) ELSE NULL END,
    updated_at=now()
FROM counts WHERE cycle.id=counts.cycle_id;

DO $$
BEGIN
    IF to_regclass(current_schema() || '.issue_pool_notification') IS NOT NULL THEN
        INSERT INTO issue_pool_notification_outbox (
            id,workspace_id,autopilot_id,cycle_id,recipient_id,event_type,payload,delivered_at,created_at,updated_at
        )
        SELECT id,workspace_id,autopilot_id,cycle_id,recipient_id,
               CASE kind WHEN 'review_requested' THEN 'candidate_review' ELSE 'cycle_terminal' END,
               jsonb_build_object('migrated_from','issue_pool_notification','legacy_inbox_item_id',inbox_item_id,'legacy_kind',kind),
               now(),created_at,now()
        FROM issue_pool_notification
        ON CONFLICT (id) DO NOTHING;
    END IF;
END $$;
