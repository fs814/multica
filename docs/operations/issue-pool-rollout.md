# Issue-pool Autopilot rollout and recovery

This runbook covers the grey release of the Workflow-backed issue-pool mode.
The workflow_run table is the execution and acceptance source of truth. Pool
rows are selection, review, dispatch and projection records; operators must not
repair a pool item by creating an agent_task_queue row or by editing a terminal
Workflow Run.

## Preconditions

- Migrate with the normal server migrator. Confirm every migration through the
  latest issue-pool migration is present in schema_migrations.
- Verify the selected Autopilot has an agent assignee, a fixed published
  Workflow template/version, and a saved policy whose project matches the
  Autopilot project.
- Keep the Autopilot paused while reviewing the dry-run preview. Confirm
  selection reasons, exclusion counts, batch limit, max-in-flight capacity and
  input mapping against representative issues.
- Confirm /metrics exposes multica_issue_pool_* series without workspace,
  policy, issue, template, cycle, item or run identifiers in labels.

There is no global MVP feature flag. The per-Autopilot status is the kill
switch, and issue-pool mode is enabled only for explicitly configured
Autopilots.

## Grey rollout

1. Start with one non-critical workspace, one project and a batch limit of 1.
2. Activate the Autopilot and trigger one cycle. Review the candidate in the
   Autopilot detail page; reject a bad candidate with a reason or approve it.
3. Follow the item link to the original issue and the Workflow Run link for
   execution/Acceptance. Do not infer success from the pool row alone.
4. Observe one complete cycle, including inbox delivery, before increasing the
   batch limit. Increase one dimension at a time: workspaces, then batch size,
   then trigger frequency.

## Accounting checks

Run these read-only queries against the target database.

~~~sql
SELECT status, count(*) FROM issue_pool_cycle GROUP BY status ORDER BY status;
SELECT status, count(*) FROM issue_pool_item GROUP BY status ORDER BY status;

SELECT count(*) AS active_items_without_active_workflow
FROM issue_pool_item item
LEFT JOIN workflow_run run ON run.id = item.workflow_run_id
WHERE item.status IN ('running','waiting_acceptance','blocked')
  AND (run.id IS NULL OR run.status NOT IN ('pending','running','waiting_acceptance','blocked'));

SELECT count(*) AS duplicate_active_issues
FROM (
  SELECT issue_id
  FROM issue_pool_item
  WHERE status IN ('claimed','approved','dispatching','running','waiting_acceptance','blocked')
  GROUP BY issue_id HAVING count(*) > 1
) duplicates;

SELECT count(*) AS undelivered_outbox,
       max(now() - created_at) AS oldest_age
FROM issue_pool_notification_outbox
WHERE delivered_at IS NULL;
~~~

Expected invariants are zero active items without a matching active Workflow
Run after a reconciler pass, zero duplicate active issues, and an outbox backlog
that drains after retry backoff. A claimed item intentionally has no Workflow
Run until manual approval.

## Crash recovery and reconciliation

The server reconciler runs every 15 seconds. It:

- expires stale manual-review claims as deferred;
- retries stale dispatching rows with the same Workflow idempotency key;
- projects canonical Workflow Run state into item/cycle/Autopilot Run views;
- persists an inbox row before broadcasting and replays undelivered outbox
  records with bounded backoff.

After a restart, wait for two reconciler intervals, then repeat the accounting
queries. For a delivery crash between inbox commit and delivered_at, the outbox
replay must reuse the persisted notification ID and create no duplicate inbox
row.

## Alerts and rollback

Alert on sustained growth of multica_issue_pool_current_items with
state=blocked, non-zero growth in reconciliation/query errors, notification
persist errors, and increasing claim-to-dispatch or cycle duration histograms.
Dashboard labels must remain the documented finite values only.

To stop new work, pause the affected Autopilot(s). Do not delete pool,
Workflow, outbox or inbox rows: they are the recovery/audit trail. Allow active
Workflow Runs to finish or cancel them through the Workflow Run API/UI, then
wait for projection. If application rollback is required, deploy the previous
binary while leaving forward migrations in place; the migrations are additive
and are not down-migrated during incident response.

Before broader rollout, an operator must sign off the browser review flow,
Linux race test, isolated-database workspace-delete manifest, and the
accounting queries above.
