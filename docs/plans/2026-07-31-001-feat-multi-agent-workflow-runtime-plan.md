# Multi-Agent Workflow Runtime and External Handoff Plan

Status: Implemented baseline plus external handoff hardening  
Last revised: 2026-08-17  
Tracking: TES-22 / TES-23

## Goal capsule

Multica runs a versioned multi-agent workflow as durable workspace work. Every execution has one Issue for human collaboration, one pinned Workflow Run for orchestration, durable steps/events/submissions, explicit accountability, and recoverable external ingress and egress.

The external handoff loop is complete when a caller can submit one authenticated event, receive stable Issue and Workflow Run identities, observe the source event in run history, and optionally receive signed lifecycle callbacks that can be retried or replayed without duplicating workflow work.

## 1. Decisions

- React Query remains the owner of server state and Zustand remains limited to client/view state.
- Workflow Engine is the only component allowed to materialize and advance Workflow Runs.
- Manual runs, workflow-bound Autopilots, and authenticated external intake all call the same Engine start command.
- Issue creation, owner subscription, Workflow Run creation, initial event, entry-step activation, initial task enqueue, and Workflow-bound Autopilot receipt linkage commit in one database transaction.
- A Run always pins a published template version. Caller-supplied input cannot switch a workflow-bound Autopilot to another template.
- Existing Issue, task, acceptance, notification, and webhook-delivery facilities are reused where they already fit. The only new delivery queue is the narrow workflow callback delivery table.
- Database relationships are enforced in application code. No new foreign keys or cascading actions are introduced.
- Every new index is built with `CREATE [UNIQUE] INDEX CONCURRENTLY` in its own single-statement migration.

## 2. System shape

```text
manual UI ───────────────┐
workflow Autopilot ──────┼─> Workflow Engine StartRun transaction
authenticated intake ────┘      ├─ Issue + owner subscriber
                                ├─ pinned Workflow Run + source_event_id
                                ├─ run.started event
                                ├─ entry Step + Agent task
                                └─ optional callback delivery
                                             │
                                             v
                                leased callback workers
                                  ├─ HTTPS + SSRF guard
                                  ├─ timestamped HMAC
                                  ├─ bounded retry
                                  └─ history / replay / failure inbox
```

The Workflow Engine continues to own sequential execution, routing, fan-out/join, acceptance/rework, terminal state, and reconciliation. External handoff extends the boundaries around that engine; it does not introduce a second orchestrator.

## 3. Core invariants

1. A workflow-owned Issue and its first Run state never exist partially.
2. `(workspace_id, idempotency_key)` identifies one Run. Reusing a key with a different canonical request hash is a conflict, not a replay.
3. A replay returns the original Run and Issue identities and does not emit a second `run.started` callback.
4. `source_event_id` stores the caller's event identity and is returned by Go and TypeScript run-detail contracts.
5. The accountable human is a durable Issue subscriber from creation time.
6. While a Run is `pending`, `running`, `waiting_acceptance`, or `blocked`, ordinary direct or batch Issue status changes are rejected.
7. Setting the Issue to `cancelled` is an explicit command: cancel active Runs and their workflow tasks, then update the Issue.
8. Workflow-bound webhook triggers require HMAC configuration and never accept a payload that conflicts with their statically bound template.
9. Callback event creation is in the same transaction as the workflow event it represents.
10. Delivery leases and event keys make retries, crashes, and operator replay safe.

## 4. Data model

### Existing workflow tables

The runtime uses:

- `workflow_template` and immutable `workflow_template_version`
- `workflow_run`
- `workflow_step_instance`
- `workflow_submission`
- `workflow_acceptance`
- `workflow_event`

The existing unique run idempotency fence and event idempotency fence remain authoritative.

### External-handoff additions

`workflow_run` adds:

- `request_hash TEXT`: canonical request identity used to distinguish safe replay from payload conflict.
- `callback_destination_id UUID`: optional narrow destination selected at intake.

`autopilot_trigger` adds:

- `signing_secret_encrypted BYTEA`: encrypted HMAC secret. Writes and startup backfill clear the legacy plaintext value; runtime reads fail closed instead of consuming plaintext.

`workflow_callback_destination` stores workspace scope, name, HTTPS URL, encrypted signing secret, enabled state, creator, and timestamps. Secret material is never returned by API responses.

`workflow_callback_delivery` stores destination, Run, event type/key, exact JSON payload, status, attempts, availability, lease token/expiry, response metadata, error, and timestamps.

Indexes:

- unique destination name per workspace;
- unique callback event per workspace/destination/event key;
- claim index over queued/available deliveries;
- Run-history index.
- primary-key unique indexes for both callback tables, created concurrently and attached with `PRIMARY KEY USING INDEX` in later migrations.

Workspace deletion explicitly removes callback deliveries before destinations and before the remaining workflow graph.

## 5. StartRun transaction

`workflow.Engine.StartRun` accepts optional Issue materialization data, request hash, source event identity, and callback destination.

Within one transaction it:

1. resolves and validates the published pinned version;
2. checks an existing idempotency key and compares `request_hash`;
3. allocates the workspace Issue number and position;
4. creates the unassigned `in_progress` Issue;
5. inserts accountable owner and configured subscribers;
6. creates the Workflow Run linked to that Issue;
7. records `run.started`;
8. materializes and activates the entry Step;
9. enqueues the first Agent task when the entry node requires one;
10. inserts the optional callback delivery from the recorded event;
11. links the source `autopilot_run` when the caller is a Workflow-bound Autopilot;
12. commits all state together.

Any failure after Issue insertion rolls back the Issue, subscriber, Run, event, Step, task, and callback row.

Manual runs use `source=manual`. Workflow Autopilots use `source=autopilot` and their Autopilot Run ID as `source_event_id`. External intake uses `source=external` and the submitted `event_id`.

## 6. Authenticated intake API

Endpoint:

`POST /api/workspaces/{workspace_id}/workflow-intake`

The endpoint uses normal authenticated workspace membership, including PAT-backed requests handled by existing authentication middleware.

Request:

```json
{
  "source": "ticketing",
  "event_id": "ticket-123",
  "template_key": "bug_fix",
  "title": "External ticket 123",
  "description": "Reproduce and fix the failure.",
  "owner_user_id": "optional-workspace-member-uuid",
  "source_url": "https://tickets.example/123",
  "payload": { "severity": "high" },
  "callback_destination_id": "optional-destination-uuid"
}
```

Validation rules:

- `source`, `event_id`, `template_key`, title, and description are required and bounded;
- the template must exist in the workspace and be published;
- payload must be an object;
- source URL must be HTTP or HTTPS;
- callback destination must be enabled and workspace-scoped;
- ordinary members can assign only themselves; owners/admins may select another workspace member.

The server derives `external:{source}:{sha256(event_id)}` as the Run idempotency key and hashes the canonical validated request. First acceptance returns `201`; identical replay returns `200`; the same source/event with a different canonical body returns `409 idempotency_conflict`.

Both success responses contain stable `receipt_id`, `workflow_run_id`, `issue_id`, status, and template key.

## 7. Workflow-bound Autopilot and webhook contract

Workflow-bound Autopilots pass their receipt ID into the same atomic StartRun path. The Engine links both `workflow_run_id` and `issue_id` back to `autopilot_run` before commit; a missing or failed linkage rolls back the Issue, Run, events, Steps, tasks, and callback delivery.

Webhook ingress:

- requires an encrypted signing secret for a workflow-bound Autopilot;
- verifies provider HMAC using the exact request body;
- validates that the workflow payload is an object;
- accepts an optional `template_key` only when it equals the statically bound template;
- rejects a non-string, empty, or conflicting template key before persistence/dispatch;
- treats the same dedupe key plus exact body as a duplicate;
- returns `409 idempotency_conflict` for the same dedupe key with a different body.

The normalized `eventPayload` object is flattened into workflow input while the normalized event envelope remains available for provenance.

Webhook responses preserve the existing Autopilot Run receipt. Once dispatch has started, duplicate receipts and authenticated delivery detail/replay responses also expose `workflow_run_id` and `issue_id`.

## 8. Ownership, lifecycle, and notifications

The accountable owner is inserted into `issue_subscriber` during the StartRun transaction:

- manual and external intake use the existing `manual` reason;
- workflow Autopilots use `autopilot`;
- configured Autopilot subscribers are deduplicated.

Existing issue and workflow notification listeners therefore deliver comments, mentions, acceptance events, and terminal changes through established channels. The issue listener normalizes both handler `IssueResponse` values and the JSON-shaped maps emitted by background Workflow projection before subscriber delivery.

Direct and batch Issue update handlers preflight active workflow ownership. A transition to `done` or another status returns `409 workflow_run_active`. A transition to `cancelled` calls Workflow Engine cancellation and cancels linked active Agent tasks before the Issue update proceeds.

Permanent callback failure creates an existing inbox item for the accountable member with the Run and delivery identities.

## 9. Callback destinations and delivery

Management API:

- `GET /api/workspaces/{workspace_id}/workflow-callback-destinations`
- `POST /api/workspaces/{workspace_id}/workflow-callback-destinations` for owner/admin
- `GET /api/workflow-runs/{run_id}/callback-deliveries`
- `POST /api/workflow-callback-deliveries/{delivery_id}/replay` for owner/admin

Destinations accept only absolute HTTPS URLs without user info. Configuration and dispatch both reject localhost, `.local`, loopback, private, unspecified, link-local, multicast, CGNAT, benchmarking, documentation, and reserved address ranges.

The production HTTP transport disables proxies and redirects. Its dialer resolves the hostname, validates every address, and connects to the validated IP while retaining the original hostname for TLS. This closes the DNS-rebinding gap between validation and connection.

Callbacks are emitted for:

- `run.started`
- `run.completed`
- `run.failed`
- `run.blocked`
- `run.cancelled`
- `acceptance.requested`
- `acceptance.decided`

Headers:

- `X-Multica-Timestamp`
- `X-Multica-Signature-256: sha256=<hex hmac>`
- `X-Multica-Delivery`

The signed bytes are `timestamp + "." + exact_request_body`.

Workers claim with `FOR UPDATE SKIP LOCKED`, set a two-minute lease, and reclaim expired dispatches. A 2xx response completes delivery. Network failures, 429, and 5xx retry with bounded exponential backoff up to six attempts. Other 4xx responses fail permanently. Response bodies are bounded before persistence.

Replay resets the same delivery row to queued, clears response/error/lease state, resets attempts, and preserves its Run/event link. The unique event fence prevents replay from creating duplicate workflow events or Runs.

## 10. API and frontend provenance

The Go `WorkflowRunResponse` includes nullable `source_event_id`.

`packages/core/workflows/schemas.ts` includes the same nullable field in:

- the TypeScript `WorkflowRun` interface;
- the Zod parser;
- the empty/fallback Run value.

Parser tests cover both explicit source event identity and the backward-compatible missing-field case.

## 11. Recovery and operations

- Workflow reconciliation remains the authority for stalled Steps and Runs.
- Callback workers are bounded to four concurrent loops.
- Queued delivery polling is one second; notification wakes reduce normal latency.
- Expired callback leases are returned to queued state.
- Callback response capture is capped at 4 KiB.
- Callback request timeout is 15 seconds.
- Secrets use the existing application secretbox and require the configured encryption key.
- Metrics use bounded workflow event/status labels; IDs do not become labels.
- Logs may carry Run/delivery IDs for correlation but must never carry secret values.

Operator checks:

1. inspect the Workflow Run and its `source_event_id`;
2. inspect callback history on the Run;
3. fix or re-enable the destination;
4. replay a terminal delivery;
5. inspect the accountable owner's inbox for permanent failures.

## 12. Verification matrix

Required automated coverage:

- atomic Issue + Run + initial task creation;
- rollback when a post-Issue write fails;
- identical and concurrent StartRun/intake replay;
- changed-body idempotency conflicts;
- owner subscriber creation;
- workflow-bound Autopilot linkage;
- webhook missing/invalid/valid HMAC;
- encrypted-only trigger secret storage and idempotent legacy-row secretbox backfill with plaintext clearing;
- static template-key match/conflict;
- exact-body webhook dedupe conflict;
- direct and batch Issue status protection;
- explicit Issue cancellation propagating to Run/tasks;
- callback event transactional enqueue and event-key dedupe;
- callback exact-body HMAC headers;
- SSRF rejection including CGNAT/documentation/benchmark/reserved ranges;
- 429/5xx/network retry, maximum-attempt failure, lease expiry/reclaim, concurrent claim, success, and operator replay;
- actual Workflow notifier bus payload through the notification listener into one owner inbox row for review, blocked, and done projections;
- Go response and TypeScript parser provenance;
- migration lint, sqlc generation, Go tests, TypeScript tests, typecheck, and build.

Manual smoke path:

1. create or select a published workflow template;
2. create a callback destination;
3. POST authenticated intake and save the receipt;
4. repeat the same request and verify stable IDs;
5. change the body with the same event and verify 409;
6. inspect the Run source event and Issue subscription;
7. verify the signed callback;
8. force a callback failure, inspect history/inbox, then replay;
9. attempt to mark the Issue done while active, then cancel it explicitly.

## 13. Rollout and compatibility

1. Apply additive schema migrations and concurrent indexes, then attach callback-table primary-key constraints with `USING INDEX`.
2. Deploy with the existing secretbox key configured. Startup scans legacy trigger rows, encrypts each still-current plaintext value, clears the plaintext column, and logs the migrated count or a fail-closed error.
3. Confirm no plaintext rows remain; runtime webhook verification never falls back to the legacy column.
4. Enable intake and callback destination management for internal workspaces.
5. Observe queue age, attempts, permanent failures, workflow terminal rates, and idempotency conflicts.
6. Expand to external callers after the smoke path and rollback tests pass.
7. Drop the now-unused legacy plaintext column in a later schema cleanup after fleet-wide row verification.

Rollback is additive: stop intake/callback workers first, deploy the previous application, and retain the new tables/columns until data is drained or intentionally discarded.

## 14. Definition of done

- All three entry points use the shared atomic StartRun command.
- No path creates an Issue before the workflow transaction.
- Intake is authenticated, workspace-scoped, replay-safe, and conflict-aware.
- Workflow webhook triggers require HMAC, pin their template, and store secrets encrypted.
- Active workflow ownership cannot be bypassed through direct or batch Issue updates.
- The accountable owner is subscribed and receives established notifications.
- Run detail carries `source_event_id` through Go and TypeScript.
- Callback destinations and delivery history are authenticated and workspace-scoped.
- Callback dispatch has strict HTTPS/SSRF controls, exact-body HMAC, leases, bounded retry, permanent-failure notification, and replay.
- Migrations follow the repository's no-FK and concurrent-index rules.
- Focused integration tests, broad Go/TypeScript verification, and the final delivery report record any unrelated baseline failures explicitly.
