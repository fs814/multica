-- TES-66: finish the forward-only transition from the superseded 459-469
-- direct-task prototype without inventing WorkflowRun history. The outbox
-- primary key is added separately in 485-488 so its unique index can be built
-- concurrently before the constraint is attached.

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema=current_schema() AND table_name='issue_pool_item' AND column_name='task_id'
    ) THEN
        EXECUTE $normalize$
            UPDATE issue_pool_item
            SET dispatched_at = COALESCE(dispatched_at, dispatch_started_at),
                waiting_acceptance_at = COALESCE(waiting_acceptance_at, awaiting_acceptance_at),
                failure_code = COALESCE(failure_code, NULLIF(failure_reason, '')),
                failure_detail = CASE
                    WHEN failure_reason IS NULL THEN failure_detail
                    ELSE COALESCE(failure_detail, '{}'::jsonb) || jsonb_build_object('legacy_failure_reason', failure_reason)
                END,
                updated_at = now()
            WHERE dispatch_started_at IS NOT NULL
               OR awaiting_acceptance_at IS NOT NULL
               OR failure_reason IS NOT NULL
        $normalize$;
    END IF;
END $$;

UPDATE issue_pool_item
SET failure_code = 'legacy_execution_unmappable',
    failure_detail = COALESCE(failure_detail, '{}'::jsonb) ||
        jsonb_build_object('legacy_execution', issue_snapshot->'legacy_execution'),
    completed_at = COALESCE(completed_at, updated_at),
    updated_at = now()
WHERE workflow_run_id IS NULL
  AND status = 'blocked'
  AND issue_snapshot ? 'legacy_execution';

UPDATE issue_pool_cycle cycle
SET status = CASE
        WHEN counts.blocked_count > 0 THEN 'blocked'
        WHEN counts.active_count > 0 THEN 'running'
        WHEN counts.terminal_count = counts.total_count THEN 'completed'
        ELSE cycle.status
    END,
    blocked_count = counts.blocked_count,
    completed_count = counts.completed_count,
    failed_count = counts.failed_count,
    deferred_count = counts.deferred_count,
    updated_at = now()
FROM (
    SELECT cycle_id, count(*)::int AS total_count,
           count(*) FILTER (WHERE status IN ('approved','dispatching','running','waiting_acceptance'))::int AS active_count,
           count(*) FILTER (WHERE status = 'blocked')::int AS blocked_count,
           count(*) FILTER (WHERE status = 'completed')::int AS completed_count,
           count(*) FILTER (WHERE status = 'failed')::int AS failed_count,
           count(*) FILTER (WHERE status = 'deferred')::int AS deferred_count,
           count(*) FILTER (WHERE status IN ('completed','failed','cancelled','deferred','rejected'))::int AS terminal_count
    FROM issue_pool_item GROUP BY cycle_id
) counts
WHERE cycle.id = counts.cycle_id;

DO $$
BEGIN
    IF to_regclass(current_schema() || '.issue_pool_notification') IS NOT NULL THEN
        EXECUTE $copy$
            INSERT INTO issue_pool_notification_outbox (
                id, workspace_id, autopilot_id, cycle_id, recipient_id, event_type,
                payload, attempts, available_at, delivered_at, created_at, updated_at
            )
            SELECT legacy.id, legacy.workspace_id, legacy.autopilot_id, legacy.cycle_id,
                   legacy.recipient_id,
                   CASE legacy.kind WHEN 'review_requested' THEN 'candidate_review' ELSE 'cycle_terminal' END,
                   jsonb_build_object(
                       'legacy_notification_id', legacy.id,
                       'legacy_inbox_item_id', legacy.inbox_item_id,
                       'migrated_from', 'issue_pool_notification'
                   ),
                   0, legacy.created_at, legacy.created_at, legacy.created_at, legacy.created_at
            FROM issue_pool_notification legacy
            ON CONFLICT DO NOTHING
        $copy$;
        DROP TABLE issue_pool_notification;
    END IF;
END $$;
