# PostgreSQL Workflow Engine Authority

Status: accepted

Multica's PostgreSQL-backed Workflow Engine is the only orchestration authority. Every entry point—UI, HTTP intake, CLI, MCP, webhook, and autopilot—must invoke the same engine commands and persist state in `workflow_run`, `workflow_step_instance`, and `workflow_event`. Adapters may validate and translate transport data, but they must not implement a second state machine.

Workflow Action Contract v1 is the shared automation surface. It exposes template list/get/validate and run start/get/cancel/acceptance-decision actions. Inputs are closed, versioned JSON Schemas; unknown fields and unknown schema versions fail closed. Automated run starts require a caller-stable idempotency key. Authentication remains in the existing API client and credentials must never enter action results, MCP tool output, events, or logs.

`workflow_event.id` is the durable event identity. Callback delivery and realtime publication carry the same versioned CloudEvents-aligned envelope after the database transaction commits. Callback retries retain that identity and the existing durable callback lease/replay machinery.

Temporal is not part of this implementation or deployment. Evaluating it would require a separate ADR covering migration, cutover, rollback, consistency, operational ownership, and the retirement of the PostgreSQL engine; it must not be introduced as a concurrent authority.

The JSON canvas remains restricted to the seven supported node types. Subflows, an expression language, a plugin marketplace, and full BPMN are explicitly outside this decision.

## Draft trial execution

Draft trials use this same engine and graph v1/v2 validators. A run's immutable
`execution_mode` selects exactly one graph source: `published` reads its pinned
version, and `draft_test` reads its workspace-scoped execution snapshot. Unknown
sources and missing snapshots fail closed. Trial runs have no business issue,
input instance, publication version, or outbound callback. Ordinary histories and
run APIs exclude them; the dedicated test-run API carries an execution reference.

Trial admission serializes workspace quota in PostgreSQL and rechecks idempotency
in a fresh READ COMMITTED statement after taking the quota lock. Request identity,
execution limits, resource environment, deadline and retention are pinned at
admission. Terminal state releases a logical run slot but does not prove a process
stopped, its logs drained, or its uploaded objects are safe to remove.

Each dispatched task has a durable execution identity, original runtime binding,
stop request and sealed delivery receipt. The lock order is quota, run, sorted
steps, tasks, then claims. Cleanup requires receipts (including continuous log
sequences and settled upload intents), or proof that execution rights were never
granted. Database payload removal, byte accounting and the object deletion manifest
commit together. Failed external deletion leaves a retryable `purging` tombstone.
Payload endpoints inspect identity and tombstones before decoding late request
bodies. An ambiguous crashed upload remains pending; operators must not invent a
stop receipt or force-delete it to recover capacity.

The entry is disabled by default. Both `MULTICA_WORKFLOW_DEBUG_ENABLED=true` and
`MULTICA_WORKFLOW_DEBUG_FLEET_READY=true`, plus the workspace setting, are required
for new admission. The fleet-ready flag is an operator assertion: deploy the
source-aware engine to every worker and drain old workers before setting it.
Dedicated claims additionally require `workflow_debug_stop_receipt_v1` and
`workflow_debug_fixed_environment_v1`. Turning admission off does not strand
existing runs or disable stop/cleanup recovery. All trial migrations refuse
rollback while any trial row exists, including a tombstone. This change does not
authorize switching an existing worker fleet, enabling a live workspace, or rollout.
