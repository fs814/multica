-- Workflow control-plane queries (plan section 5 / U1).
--
-- Every query filters workspace_id (plan section 4). Where a row is addressed
-- by primary key the workspace still appears in the WHERE clause, so a leaked
-- or guessed UUID from another workspace returns zero rows instead of data.
--
-- The engine's transactional commands use the *ForUpdate lookups to lock Run
-- and Step rows in a stable order (Run first, then Step by id) before
-- validating a transition, which is what serializes concurrent commands
-- against the same Run.

-- ---------------------------------------------------------------------------
-- Template
-- ---------------------------------------------------------------------------

-- name: CreateWorkflowTemplate :one
INSERT INTO workflow_template (
    workspace_id, key, name, description, created_by_type, created_by_id
) VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: GetWorkflowTemplate :one
SELECT * FROM workflow_template
WHERE id = $1 AND workspace_id = $2;

-- name: GetWorkflowTemplateByKey :one
-- Case-insensitive to match idx_workflow_template_ws_key; external intake
-- resolves templates by key (plan section 9).
SELECT * FROM workflow_template
WHERE workspace_id = $1 AND LOWER(key) = LOWER(sqlc.arg('key')::text);

-- name: ListWorkflowTemplates :many
SELECT * FROM workflow_template
WHERE workspace_id = $1
  AND (sqlc.arg('include_archived')::bool OR status <> 'archived')
ORDER BY created_at DESC;

-- name: UpdateWorkflowTemplate :one
-- `key` is immutable: external callers and autopilots reference it, so a
-- rename would silently break their intake requests.
UPDATE workflow_template SET
    name = COALESCE(sqlc.narg('name'), name),
    description = COALESCE(sqlc.narg('description'), description),
    updated_at = now()
WHERE id = $1 AND workspace_id = $2
RETURNING *;

-- name: SetWorkflowTemplateCurrentVersion :one
-- Called inside the publish transaction, after the new version row flips to
-- published, so the template's advertised version and that row agree.
UPDATE workflow_template SET
    current_version = sqlc.arg('current_version')::int,
    status = 'published',
    updated_at = now()
WHERE id = $1 AND workspace_id = $2
RETURNING *;

-- name: ArchiveWorkflowTemplate :one
-- Archival is one-way and does not touch existing Runs: in-flight Runs hold a
-- pinned version and must be allowed to finish (plan section 12, rollback).
UPDATE workflow_template SET
    status = 'archived',
    archived_at = now(),
    updated_at = now()
WHERE id = $1 AND workspace_id = $2 AND status <> 'archived'
RETURNING *;

-- ---------------------------------------------------------------------------
-- Template version
-- ---------------------------------------------------------------------------

-- name: CreateWorkflowTemplateVersion :one
-- version = max+1 computed in-statement so two concurrent draft creations
-- cannot both claim the same number; idx_workflow_template_version_template_version
-- is the backstop if they race.
INSERT INTO workflow_template_version (
    workspace_id, template_id, version, definition, schema_version
)
SELECT sqlc.arg('workspace_id')::uuid,
       sqlc.arg('template_id')::uuid,
       COALESCE((
           SELECT MAX(version) FROM workflow_template_version
           WHERE template_id = sqlc.arg('template_id')::uuid
       ), 0) + 1,
       sqlc.arg('definition')::jsonb,
       sqlc.arg('schema_version')::int
RETURNING *;

-- name: GetWorkflowTemplateVersion :one
SELECT * FROM workflow_template_version
WHERE id = $1 AND workspace_id = $2;

-- name: GetPublishedWorkflowTemplateVersion :one
-- Resolves the version a new Run should pin: the template's current_version.
SELECT v.* FROM workflow_template_version v
JOIN workflow_template t
  ON t.id = v.template_id AND t.current_version = v.version
WHERE v.template_id = $1
  AND v.workspace_id = $2
  AND v.status = 'published';

-- name: ListWorkflowTemplateVersions :many
SELECT * FROM workflow_template_version
WHERE template_id = $1 AND workspace_id = $2
ORDER BY version DESC;

-- name: UpdateWorkflowTemplateVersionDefinition :one
-- Guarded on status='draft': published definitions are immutable (plan
-- section 4) because in-flight Runs resolve node semantics through them.
UPDATE workflow_template_version SET
    definition = sqlc.arg('definition')::jsonb,
    updated_at = now()
WHERE id = $1 AND workspace_id = $2 AND status = 'draft'
RETURNING *;

-- name: PublishWorkflowTemplateVersion :one
-- One-way draft -> published. The status guard makes a duplicate publish a
-- no-op (zero rows) rather than re-stamping the publisher.
UPDATE workflow_template_version SET
    status = 'published',
    published_by_type = sqlc.arg('published_by_type')::text,
    published_by_id = sqlc.arg('published_by_id')::uuid,
    published_at = now(),
    updated_at = now()
WHERE id = $1 AND workspace_id = $2 AND status = 'draft'
RETURNING *;

-- ---------------------------------------------------------------------------
-- Run
-- ---------------------------------------------------------------------------

-- name: NextWorkflowIssuePosition :one
SELECT CAST(COALESCE(MIN(position), 0) - 1 AS DOUBLE PRECISION) AS position
FROM issue
WHERE workspace_id = $1 AND status = $2;

-- name: CreateWorkflowRun :one
INSERT INTO workflow_run (
    workspace_id, issue_id, template_id, template_version_id, status, source,
    source_event_id, idempotency_key, accountable_user_id, input, context, policy,
    request_hash, callback_destination_id
) VALUES (
    $1, sqlc.narg('issue_id'), $2, $3, 'pending', $4,
    sqlc.narg('source_event_id'), sqlc.arg('idempotency_key')::text,
    sqlc.narg('accountable_user_id'),
    sqlc.arg('input')::jsonb, sqlc.arg('context')::jsonb, sqlc.arg('policy')::jsonb,
    sqlc.narg('request_hash'), sqlc.narg('callback_destination_id')
)
RETURNING *;

-- name: GetWorkflowRun :one
SELECT * FROM workflow_run
WHERE id = $1 AND workspace_id = $2;

-- name: GetWorkflowRunForUpdate :one
-- Locked first in every engine command, establishing the stable lock order
-- (Run before Step) that keeps concurrent commands from deadlocking.
SELECT * FROM workflow_run
WHERE id = $1 AND workspace_id = $2
FOR UPDATE;

-- name: GetWorkflowRunByIdempotencyKey :one
-- The dedup read path: lets a replayed intake return the original Run instead
-- of surfacing a unique-violation to the caller.
SELECT * FROM workflow_run
WHERE workspace_id = $1 AND idempotency_key = $2;

-- name: ListWorkflowRuns :many
SELECT * FROM workflow_run
WHERE workspace_id = $1
  AND (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status')::text)
  AND (sqlc.narg('template_id')::uuid IS NULL OR template_id = sqlc.narg('template_id')::uuid)
ORDER BY created_at DESC
LIMIT sqlc.arg('limit_count')::int OFFSET sqlc.arg('offset_count')::int;

-- name: CountWorkflowRuns :one
-- The `total` the runs list reports. It counts the whole filtered set, NOT the
-- page: a client that paginates needs to know how many rows exist beyond the
-- window, and len(page) would silently report "20 runs" forever. The predicates
-- are byte-identical to ListWorkflowRuns' so the count and the page can never
-- describe different sets.
SELECT count(*)::bigint FROM workflow_run
WHERE workspace_id = $1
  AND (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status')::text)
  AND (sqlc.narg('template_id')::uuid IS NULL OR template_id = sqlc.narg('template_id')::uuid);

-- name: ListWorkflowTemplatesByIDs :many
-- Batch template lookup for the runs list, which renders template_name/key on
-- every row. One query for the page instead of one per run: the list is the
-- most-visited workflow endpoint and a 20-row page would otherwise be 21 round
-- trips just to label the rows. No status filter - an ARCHIVED template's
-- in-flight Runs must still render with a name (plan section 12), and a run
-- whose template row was hard-deleted simply gets an empty label rather than
-- disappearing from the list.
SELECT * FROM workflow_template
WHERE workspace_id = sqlc.arg('workspace_id')::uuid
  AND id = ANY(sqlc.arg('ids')::uuid[]);

-- name: SummarizeWorkflowStepsForRuns :many
-- step_count + current_node_key per run, batched over a page of runs.
--
-- Batched for the same reason ListWorkflowTemplatesByIDs is: this feeds the runs
-- list, and a per-run query would make a 20-row page 20 extra round trips to
-- render two columns. The DETAIL endpoint calls it too - with a single-element
-- run_ids - even though it already holds every Step row, so that "which node is
-- this run on" has exactly ONE definition. Recomputing it in Go for detail would
-- let the list and the detail page disagree about the same run, which is the kind
-- of drift a reader blames on the run rather than on the API.
--
-- current_node_key prefers a non-terminal Step and then the newest, which is what
-- keeps the answer stable through rework: the same node key has several attempts
-- and only the latest is what a reader means by "where is this run". It falls
-- back to the newest terminal Step so a finished, failed, or blocked Run still
-- reports the node it ended on - precisely when a human most needs to know where
-- it stopped.
--
-- Runs with no Steps are absent from the result, so callers must default them to
-- step_count 0 / no current node: a Run that blocked at routing before its first
-- activation legitimately has none.
SELECT s.run_id,
       count(*)::bigint AS step_count,
       (SELECT c.node_key
        FROM workflow_step_instance c
        WHERE c.run_id = s.run_id AND c.workspace_id = s.workspace_id
        ORDER BY (c.status IN ('pending', 'ready', 'queued', 'running', 'submitted', 'waiting_acceptance')) DESC,
                 c.created_at DESC
        LIMIT 1) AS current_node_key
FROM workflow_step_instance s
WHERE s.workspace_id = sqlc.arg('workspace_id')::uuid
  AND s.run_id = ANY(sqlc.arg('run_ids')::uuid[])
GROUP BY s.run_id, s.workspace_id;

-- name: ListActiveWorkflowRunsForIssue :many
-- Issue cancellation cancels the active Run (plan section 6).
SELECT * FROM workflow_run
WHERE issue_id = $1 AND workspace_id = $2
  AND status IN ('pending', 'running', 'waiting_acceptance', 'blocked')
ORDER BY created_at DESC;

-- name: GetIssueForWorkflowStart :one
-- Locking the Issue serializes competing starts. The effective status closes
-- custom-status aliases that map to terminal built-in states.
SELECT i.*, issue_effective_status(i.workspace_id, i.status)::text AS effective_status
FROM issue i
WHERE i.id = $1 AND i.workspace_id = $2
FOR UPDATE OF i;

-- name: HasActiveAgentTaskForWorkflowIssue :one
SELECT EXISTS (
    SELECT 1 FROM agent_task_queue
    WHERE issue_id = $1
      AND status IN ('queued', 'dispatched', 'running', 'waiting_local_directory', 'deferred')
) AS has_active_task;

-- name: MarkWorkflowRunRunning :one
-- pending -> running, or running -> running when resuming after an acceptance
-- or a repaired block. Idempotent by design: replaying it changes nothing.
UPDATE workflow_run SET
    status = 'running',
    blocked_reason = NULL,
    started_at = COALESCE(started_at, now()),
    updated_at = now()
WHERE id = $1 AND workspace_id = $2
  AND status IN ('pending', 'running', 'waiting_acceptance', 'blocked')
RETURNING *;

-- name: MarkWorkflowRunWaitingAcceptance :one
UPDATE workflow_run SET
    status = 'waiting_acceptance',
    updated_at = now()
WHERE id = $1 AND workspace_id = $2 AND status = 'running'
RETURNING *;

-- name: MarkWorkflowRunBlocked :one
UPDATE workflow_run SET
    status = 'blocked',
    blocked_reason = sqlc.arg('blocked_reason')::text,
    failure_detail = sqlc.narg('failure_detail'),
    updated_at = now()
WHERE id = $1 AND workspace_id = $2
  AND status IN ('pending', 'running', 'waiting_acceptance')
RETURNING *;

-- name: CompleteWorkflowRun :one
-- Reachable only from the End executor: 'a Run completes only through End'
-- (plan section 4). The status guard prevents completing an already-terminal
-- Run, and completed_at satisfies workflow_run_completed_at_present.
UPDATE workflow_run SET
    status = 'completed',
    completed_at = now(),
    updated_at = now()
WHERE id = $1 AND workspace_id = $2 AND status = 'running'
RETURNING *;

-- name: FailWorkflowRun :one
UPDATE workflow_run SET
    status = 'failed',
    failure_reason = sqlc.arg('failure_reason')::text,
    failure_detail = sqlc.narg('failure_detail'),
    completed_at = now(),
    updated_at = now()
WHERE id = $1 AND workspace_id = $2
  AND status IN ('pending', 'running', 'waiting_acceptance', 'blocked')
RETURNING *;

-- name: CancelWorkflowRun :one
UPDATE workflow_run SET
    status = 'cancelled',
    completed_at = now(),
    updated_at = now()
WHERE id = $1 AND workspace_id = $2
  AND status IN ('pending', 'running', 'waiting_acceptance', 'blocked')
RETURNING *;

-- name: ListStaleActiveWorkflowRuns :many
-- Reconciler claim query (plan section 7). Bounded and ordered by staleness so
-- concurrent replicas converge on the same candidates; SKIP LOCKED lets a
-- second reconciler make progress instead of blocking on the first.
SELECT * FROM workflow_run
WHERE status IN ('pending', 'running', 'waiting_acceptance', 'blocked')
  AND updated_at < now() - make_interval(secs => sqlc.arg('stale_seconds')::float)
ORDER BY updated_at ASC
LIMIT sqlc.arg('limit_count')::int
FOR UPDATE SKIP LOCKED;

-- ---------------------------------------------------------------------------
-- Step instance
-- ---------------------------------------------------------------------------

-- name: CreateWorkflowStepInstance :one
-- attempt defaults to 1; rework passes attempt = N+1 explicitly. A duplicate
-- (run, node, attempt) violates idx_workflow_step_run_node_attempt, which is
-- the replay fence that stops a second Agent Task being created.
INSERT INTO workflow_step_instance (
    workspace_id, run_id, node_key, node_type, attempt, status,
    parent_step_id, expansion_key, agent_id, routing_reason, input,
    activation_timeout_at, ready_at, trace_position
) VALUES (
    $1, $2, $3, $4, sqlc.arg('attempt')::int, sqlc.arg('status')::text,
    sqlc.narg('parent_step_id'), sqlc.narg('expansion_key'),
    sqlc.narg('agent_id'), sqlc.narg('routing_reason'),
    sqlc.arg('input')::jsonb,
    sqlc.narg('activation_timeout_at'), sqlc.narg('ready_at'),
    (SELECT COALESCE(MAX(trace_position), 0) + 1
     FROM workflow_step_instance
     WHERE run_id = $2)
)
RETURNING *;

-- name: GetWorkflowStepInstance :one
SELECT * FROM workflow_step_instance
WHERE id = $1 AND workspace_id = $2;

-- name: GetWorkflowStepInstanceForUpdate :one
-- Locked after the Run row (stable order) inside engine commands.
SELECT * FROM workflow_step_instance
WHERE id = $1 AND workspace_id = $2
FOR UPDATE;

-- name: GetWorkflowStepInstanceByTask :one
-- Maps a terminal Agent Task back to its Step attempt so TaskService's
-- post-commit hook can invoke RecordTaskTerminal.
SELECT * FROM workflow_step_instance
WHERE task_id = $1;

-- name: GetLatestWorkflowStepAttempt :one
-- Used by rework to compute the next attempt number for a node.
SELECT * FROM workflow_step_instance
WHERE run_id = $1 AND node_key = $2
ORDER BY attempt DESC
LIMIT 1;

-- name: ListWorkflowStepInstances :many
SELECT * FROM workflow_step_instance
WHERE run_id = $1 AND workspace_id = $2
ORDER BY trace_position ASC;

-- name: CountWorkflowReworkRounds :one
-- Only the node directly activated by a rewind carries a top-level `rework`
-- input. Nodes replayed later on the normal forward path do not, so this counts
-- feedback-loop rounds rather than every repeated downstream attempt. The
-- durable Step input also makes the count correct for Runs started before the
-- max_rework_rounds enforcement code was deployed.
SELECT count(*)::bigint
FROM workflow_step_instance
WHERE run_id = $1
  AND workspace_id = $2
  AND parent_step_id IS NULL
  AND input ? 'rework';

-- name: ListActiveWorkflowStepInstances :many
-- 'Running Run with no active/ready Step' is a reconciler repair case, so the
-- engine needs a cheap non-terminal step count per Run.
SELECT * FROM workflow_step_instance
WHERE run_id = $1 AND workspace_id = $2
  AND status IN ('pending', 'ready', 'queued', 'running', 'submitted', 'waiting_acceptance')
ORDER BY created_at ASC;

-- name: ListWorkflowStepChildren :many
-- AND Join derives its verdict from durable child state (plan section 8).
SELECT * FROM workflow_step_instance
WHERE parent_step_id = $1 AND workspace_id = $2
ORDER BY created_at ASC;

-- name: BindWorkflowStepTask :one
-- Runs in the SAME transaction as CreateAgentTask so a Step can never exist
-- with a dangling task reference, nor a workflow task with no owning Step.
-- The status guard enforces ready -> queued.
UPDATE workflow_step_instance SET
    task_id = sqlc.arg('task_id')::uuid,
    agent_id = COALESCE(sqlc.narg('agent_id'), agent_id),
    routing_reason = COALESCE(sqlc.narg('routing_reason'), routing_reason),
    status = 'queued',
    updated_at = now()
WHERE id = $1 AND workspace_id = $2 AND status = 'ready' AND task_id IS NULL
RETURNING *;

-- name: MarkWorkflowStepReady :one
UPDATE workflow_step_instance SET
    status = 'ready',
    ready_at = COALESCE(ready_at, now()),
    activation_timeout_at = sqlc.narg('activation_timeout_at'),
    updated_at = now()
WHERE id = $1 AND workspace_id = $2 AND status = 'pending'
RETURNING *;

-- name: MarkWorkflowStepRunning :one
UPDATE workflow_step_instance SET
    status = 'running',
    started_at = COALESCE(started_at, now()),
    updated_at = now()
WHERE id = $1 AND workspace_id = $2 AND status IN ('queued', 'running')
RETURNING *;

-- name: MarkWorkflowStepSubmitted :one
UPDATE workflow_step_instance SET
    status = 'submitted',
    output = sqlc.narg('output'),
    updated_at = now()
WHERE id = $1 AND workspace_id = $2 AND status IN ('queued', 'running')
RETURNING *;

-- name: MarkWorkflowStepWaitingAcceptance :one
UPDATE workflow_step_instance SET
    status = 'waiting_acceptance',
    updated_at = now()
WHERE id = $1 AND workspace_id = $2 AND status IN ('ready', 'submitted')
RETURNING *;

-- name: MarkWorkflowStepPassed :one
-- Terminal-success for a step. Guarded to the states that may legitimately
-- pass, so an out-of-order command returns zero rows and the engine raises
-- invalid_transition instead of corrupting the trace.
UPDATE workflow_step_instance SET
    status = 'passed',
    output = COALESCE(sqlc.narg('output'), output),
    completed_at = now(),
    updated_at = now()
WHERE id = $1 AND workspace_id = $2
  AND status IN ('ready', 'submitted', 'waiting_acceptance')
RETURNING *;

-- name: MarkWorkflowStepFailed :one
-- 'ready' is included for the same reason as MarkWorkflowStepBlocked: an
-- activation-time failure precedes any Task. Keep in sync with stepTransitions.
UPDATE workflow_step_instance SET
    status = 'failed',
    failure_reason = sqlc.arg('failure_reason')::text,
    failure_detail = sqlc.narg('failure_detail'),
    completed_at = now(),
    updated_at = now()
WHERE id = $1 AND workspace_id = $2
  AND status IN ('ready', 'queued', 'running', 'submitted', 'waiting_acceptance')
RETURNING *;

-- name: MarkWorkflowStepBlocked :one
-- Blocked is first-class (plan section 4): an unparseable submission lands
-- here with submission_contract_invalid, never as an unknown failure.
-- 'ready' is included because routing failure is detected at activation, before
-- any Task exists — the step never reaches 'queued'. Keep this set in sync with
-- stepTransitions in state.go.
UPDATE workflow_step_instance SET
    status = 'blocked',
    failure_reason = sqlc.arg('failure_reason')::text,
    failure_detail = sqlc.narg('failure_detail'),
    completed_at = now(),
    updated_at = now()
WHERE id = $1 AND workspace_id = $2
  AND status IN ('ready', 'queued', 'running', 'submitted')
RETURNING *;

-- name: MarkWorkflowStepSkipped :one
UPDATE workflow_step_instance SET
    status = 'skipped',
    completed_at = now(),
    updated_at = now()
WHERE id = $1 AND workspace_id = $2 AND status IN ('pending', 'ready')
RETURNING *;

-- name: CancelWorkflowStepInstancesForRun :many
-- Run cancellation cascades to every non-terminal Step in application code
-- (no DB cascades, plan section 4).
UPDATE workflow_step_instance SET
    status = 'cancelled',
    completed_at = now(),
    updated_at = now()
WHERE run_id = $1 AND workspace_id = $2
  AND status IN ('pending', 'ready', 'queued', 'running', 'submitted', 'waiting_acceptance')
RETURNING *;

-- ---------------------------------------------------------------------------
-- Submission
-- ---------------------------------------------------------------------------

-- name: CreateWorkflowSubmission :one
INSERT INTO workflow_submission (
    workspace_id, run_id, step_id, task_id, schema_version, verdict,
    artifact, rationale, confidence, root_cause, raw_result, validation_errors
) VALUES (
    $1, $2, $3, sqlc.narg('task_id'), sqlc.arg('schema_version')::int,
    sqlc.arg('verdict')::text, sqlc.arg('artifact')::jsonb,
    sqlc.arg('rationale')::text, sqlc.narg('confidence'),
    sqlc.narg('root_cause'), sqlc.narg('raw_result'), sqlc.narg('validation_errors')
)
RETURNING *;

-- name: GetLatestWorkflowSubmissionForStep :one
SELECT * FROM workflow_submission
WHERE step_id = $1 AND workspace_id = $2
ORDER BY submitted_at DESC
LIMIT 1;

-- name: ListWorkflowSubmissionsForRun :many
-- Powers the Step/Submission inspector; rework preserves history so a node
-- legitimately has several.
SELECT * FROM workflow_submission
WHERE run_id = $1 AND workspace_id = $2
ORDER BY submitted_at ASC;

-- ---------------------------------------------------------------------------
-- Acceptance
-- ---------------------------------------------------------------------------

-- name: CreateWorkflowAcceptance :one
INSERT INTO workflow_acceptance (
    workspace_id, run_id, step_id, status, context
) VALUES ($1, $2, $3, 'pending', sqlc.arg('context')::jsonb)
RETURNING *;

-- name: GetWorkflowAcceptance :one
SELECT * FROM workflow_acceptance
WHERE id = $1 AND workspace_id = $2;

-- name: GetPendingWorkflowAcceptanceForStep :one
SELECT * FROM workflow_acceptance
WHERE step_id = $1 AND workspace_id = $2 AND status = 'pending';

-- name: ListWorkflowAcceptancesForRun :many
SELECT * FROM workflow_acceptance
WHERE run_id = $1 AND workspace_id = $2
ORDER BY created_at ASC;

-- name: DecideWorkflowAcceptance :one
-- The status='pending' guard is the reviewer-race fence (plan section 8): the
-- second of two concurrent decisions updates zero rows and is rejected as an
-- acceptance conflict rather than overwriting the first reviewer's verdict.
UPDATE workflow_acceptance SET
    status = sqlc.arg('status')::text,
    reviewer_user_id = sqlc.arg('reviewer_user_id')::uuid,
    reason = sqlc.narg('reason'),
    rework_target_node_key = sqlc.narg('rework_target_node_key'),
    context = sqlc.arg('context')::jsonb,
    decided_at = now(),
    updated_at = now()
WHERE id = $1 AND workspace_id = $2 AND status = 'pending'
RETURNING *;

-- name: CancelPendingWorkflowAcceptancesForRun :many
UPDATE workflow_acceptance SET
    status = 'cancelled',
    updated_at = now()
WHERE run_id = $1 AND workspace_id = $2 AND status = 'pending'
RETURNING *;

-- ---------------------------------------------------------------------------
-- Event
-- ---------------------------------------------------------------------------

-- name: CreateWorkflowEvent :one
-- Written in the same transaction as the state change it describes. A replayed
-- command collides on idx_workflow_event_ws_idempotency, rolling back the whole
-- transaction so state cannot advance twice (plan section 4).
INSERT INTO workflow_event (
    workspace_id, run_id, step_id, event_type, idempotency_key,
    actor_type, actor_id, payload
) VALUES (
    $1, $2, sqlc.narg('step_id'), $3, sqlc.arg('idempotency_key')::text,
    sqlc.arg('actor_type')::text, sqlc.narg('actor_id'), sqlc.arg('payload')::jsonb
)
RETURNING *;

-- name: ListWorkflowEventsForRun :many
SELECT * FROM workflow_event
WHERE run_id = $1 AND workspace_id = $2
ORDER BY created_at ASC, id ASC;

-- name: WorkflowEventExists :one
SELECT EXISTS(
    SELECT 1 FROM workflow_event
    WHERE workspace_id = $1 AND idempotency_key = $2
) AS event_exists;

-- ---------------------------------------------------------------------------
-- Agent task linkage
-- ---------------------------------------------------------------------------

-- name: SetAgentTaskWorkflowStep :exec
-- Back-link from the Agent Task to the Step attempt that created it, written in
-- the same transaction as CreateAgentTask + BindWorkflowStepTask. TaskService's
-- terminal hook reads this to find the Step to advance; without it a finished
-- workflow task would be indistinguishable from a legacy task.
UPDATE agent_task_queue
SET workflow_step_instance_id = sqlc.arg('workflow_step_instance_id')::uuid
WHERE id = $1;

-- name: CreateWorkflowAgentTask :one
-- The Agent Task a Step activation enqueues. This exists rather than reusing
-- CreateAgentTask because CreateAgentTask BUILDS its context column from a
-- head_sha argument (`jsonb_build_object('head_sha', ...)`) and therefore cannot
-- carry an arbitrary blob - and a workflow task with no context is a task with
-- no prompt, i.e. an agent asked to do nothing. The step's whole brief
-- (instruction, run input, upstream artifact, submission contract) travels in
-- `context`, exactly the way CreateQuickCreateTask carries the quick-create
-- prompt; handler/daemon.go reads the type discriminator on claim.
--
-- workflow_step_instance_id is set HERE rather than by a follow-up
-- SetAgentTaskWorkflowStep so the row is never briefly visible as an
-- unattributed non-workflow task: one INSERT, one row, already linked. The
-- engine still calls BindWorkflowStepTask in the same transaction to write the
-- reverse link, which is the one-active-task fence.
--
-- originator_user_id / accountable_user_id both take the Run's accountable
-- human. Passing the same value satisfies
-- agent_task_queue_accountable_matches_originator, whose invariant is that a run
-- with an authorization-bearing originator must name the same accountable human.
-- originator_source is deliberately left NULL: the Run row already records how
-- the Run itself was attributed (source + accountable_user_id), and inventing a
-- new waterfall level here would report the same provenance twice with two
-- vocabularies. trigger_evidence_kind/ref_id DO point back at the Step so a task
-- found in isolation can be traced to the activation that produced it.
INSERT INTO agent_task_queue (
    agent_id, runtime_id, issue_id, status, priority,
    context, workflow_step_instance_id,
    originator_user_id, accountable_user_id,
    trigger_evidence_kind, trigger_evidence_ref_id
)
VALUES (
    sqlc.arg('agent_id')::uuid, sqlc.arg('runtime_id')::uuid, sqlc.narg('issue_id'),
    'queued', sqlc.arg('priority')::int,
    sqlc.arg('context')::jsonb,
    sqlc.arg('workflow_step_instance_id')::uuid,
    sqlc.narg('originator_user_id'), sqlc.narg('accountable_user_id'),
    'workflow_step', sqlc.arg('workflow_step_instance_id')::uuid
)
RETURNING *;

-- name: ListActiveAgentTaskIDsForWorkflowRun :many
-- The Run's still-running Agent Tasks, for cancellation.
--
-- Cancelling a Run must also stop the agents it started, or the user sees a
-- "cancelled" Run whose agent keeps burning tokens and pushing commits. This
-- only SELECTs: TaskService remains canonical for task state (plan section 7),
-- so the handler feeds these ids to TaskService.CancelTask rather than UPDATEing
-- agent_task_queue here - going direct would skip the agent-status reconcile and
-- the task:cancelled broadcast, leaving the agent wedged at status='working'.
--
-- Joined through workflow_step_instance rather than filtered on issue_id: a Run's
-- issue is optional, and an issue can carry tasks that have nothing to do with
-- this Run (a chat reply, another Run). The step join is the only link that means
-- exactly "this task belongs to this Run". workspace_id comes from the step for
-- the same reason every other query is scoped: a guessed run id from another
-- tenant must select nothing.
SELECT t.id
FROM agent_task_queue t
JOIN workflow_step_instance s ON s.id = t.workflow_step_instance_id
WHERE s.run_id = $1 AND s.workspace_id = $2
  AND t.status IN ('queued', 'dispatched', 'running', 'waiting_local_directory', 'deferred')
ORDER BY t.created_at ASC;

-- name: GetWorkflowStepIDForTask :one
-- Cheap probe used by the TaskService hook to decide whether a finished task
-- belongs to a workflow at all, before doing any workflow work.
SELECT workflow_step_instance_id FROM agent_task_queue
WHERE id = $1 AND workflow_step_instance_id IS NOT NULL;

-- name: ListWorkflowTasksAwaitingStepProgress :many
-- Reconciler repair: 'terminal Task not consumed by Step' (plan section 7). A
-- task that finished while its Step is still queued/running means the terminal
-- hook was lost to a crash, so the engine replays it.
SELECT t.id AS task_id, t.status AS task_status, t.result, t.error, t.failure_reason,
       s.id AS step_id, s.workspace_id, s.status AS step_status
FROM agent_task_queue t
JOIN workflow_step_instance s ON s.id = t.workflow_step_instance_id
WHERE t.workflow_step_instance_id IS NOT NULL
  AND t.status IN ('completed', 'failed', 'cancelled')
  AND s.status IN ('queued', 'running')
  AND t.completed_at < now() - make_interval(secs => sqlc.arg('stale_seconds')::float)
ORDER BY t.completed_at ASC
LIMIT sqlc.arg('limit_count')::int;

-- name: ListAutopilotWorkflowRunsAwaitingSync :many
-- A workflow Run is canonical; this projects its terminal state onto the
-- linked Autopilot history row after crashes or asynchronous completion.
SELECT ar.id AS autopilot_run_id, wr.id AS workflow_run_id,
       wr.status AS workflow_status, wr.failure_reason, wr.blocked_reason,
       wr.failure_detail
FROM autopilot_run ar
JOIN workflow_run wr ON wr.id = ar.workflow_run_id
WHERE ar.workflow_run_id IS NOT NULL
  AND ar.status IN ('running', 'issue_created')
  AND wr.status IN ('completed', 'failed', 'cancelled', 'blocked')
ORDER BY wr.updated_at ASC
LIMIT sqlc.arg('limit_count')::int;
