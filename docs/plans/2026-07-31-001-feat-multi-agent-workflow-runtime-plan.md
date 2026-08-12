# Multi-Agent Workflow Runtime - Implementation Plan

> Status: Proposed  
> Date: 2026-07-31  
> Product reference: https://zhuanlan.zhihu.com/p/2058935265348134625  
> Scope: server, Agent/Runtime protocol, Autopilot, shared web/desktop UI, observability, and rollout. Mobile is deferred.

## Goal Capsule

- **Problem:** Multica dispatches Agent Tasks but lacks durable multi-step handoff, structured verdicts, fan-out/join, human acceptance, and targeted rework.
- **Decision:** Add a workflow control plane above Issue and Agent Task. Reuse the queue, runtimes, Autopilot, durable webhooks, failure taxonomy, realtime, and metrics.
- **First slice:** A versioned Bug Fix workflow: Analyze -> Implement -> Validate -> Human Acceptance -> End.
- **Compatibility:** Workflow linkage is additive and nullable. Existing Issue, Chat, Quick Create, Squad, and Autopilot behavior remains available.
- **Success:** Runs survive duplicate events and restarts, cannot complete without End, and expose a Run -> Step -> Task trace.

## 1. Executive Decision

Do not replace the current Agent Task scheduler. It already owns runtime selection, queueing, daemon dispatch, task lifecycle, failure classification, recovery, usage accounting, and realtime updates.

The new coordination layer will:

1. Materialize an immutable template version into a durable Workflow Run.
2. Activate ready steps and create existing Agent Tasks transactionally and idempotently.
3. Convert task output into a structured Submission and Verdict.
4. Choose the next step, join behavior, rework target, or human acceptance state.
5. Reconcile partial transitions after crashes and expose a complete trace.

The first release agentizes a workflow humans already understand. Dynamic AI-generated graphs, a node canvas, and Temporal are not prerequisites.

## 2. Scope

### In scope

- Immutable template versions and graph validation.
- Run and Step Instance state machines.
- Agent, Acceptance, End, Condition, Fan-out, and Join nodes.
- Submission fields: artifact, pass/fail/blocked verdict, rationale, confidence, root cause.
- Explicit, previous-step, capability-match, and fallback routing.
- Targeted bounded rework with rejection context.
- Autopilot and external event intake.
- Workflow reconciliation, notifications, traces, metrics, and shared web/desktop UI.

### Deferred

- AI-created or runtime-mutated graphs.
- Arbitrary cycles; only explicit bounded rework edges.
- Drag-and-drop canvas until graph semantics stabilize.
- Replacing Issue, Agent Task, daemon dispatch, or Runtime Sweeper.
- Automatic conversion of existing Autopilots.
- Mobile UI and cross-workspace workflows.

## 3. Current Foundations and Gaps

Reuse:

- Task queue: server/pkg/db/queries/agent.sql
- Task lifecycle: server/internal/service/task.go
- Runtime/task recovery: server/cmd/server/runtime_sweeper.go
- Failure contract: server/pkg/taskfailure/failure.go and classify.go
- Daemon protocol: server/pkg/protocol/messages.go and events.go
- Autopilot: server/migrations/042_autopilot.up.sql, server/internal/service/autopilot.go, server/internal/scheduler/jobs_autopilot.go
- Core API/query state: packages/core
- Shared UI: packages/views
- Metrics/analytics: server/internal/metrics and server/internal/analytics

Gaps:

- No Template, Run, Step, graph validator, or transition engine.
- Task result is not a stable cross-Agent handoff contract.
- Task-level parallelism has no durable fan-out/join semantics.
- acceptance_criteria lacks a complete acceptance/rework lifecycle.
- Provider capabilities are technical, not business capability routing.
- Task/runtime recovery cannot repair a lost workflow transition.
- Metrics lack first-pass acceptance, rework, and node failure distribution.

## 4. Invariants

- Agent Task completion is not business acceptance; a Run completes only through End.
- Published versions and completed Step attempts are immutable.
- Rework creates a new attempt and preserves prior Submissions.
- Each Agent Step attempt has at most one active Task.
- Blocked is first-class and never silently becomes unknown failure.
- Retry, rework, fan-out, duration, token, and cost are bounded.
- Every event and command has a workspace-scoped idempotency key.
- State and Workflow Event commit atomically; replay cannot advance twice.
- Every query filters workspace_id; routing reuses invocation permissions.
- Do not add database foreign keys or cascades. Validate relationships and clean up in application transactions.
- Every index uses CREATE INDEX CONCURRENTLY in its own migration.
- React Query owns server state; do not mirror workflows into Zustand.
- Shared web/desktop views live in packages/views.
- API responses use zod and parseWithFallback with malformed-response tests.

## 5. Data Model

Migration numbers are selected from the current head. Relationships are logical only.

### workflow_template

id, workspace_id, key, name, description, draft/published/archived status, current_version, creator, timestamps. Unique concurrent index on workspace_id plus key.

### workflow_template_version

id, workspace_id, template_id, version, definition JSONB, schema version, status, publisher, timestamps. Published rows are immutable. Unique concurrent index on template_id plus version.

### workflow_run

id, workspace_id, issue_id, template/version IDs, status, source, source_event_id, idempotency_key, accountable_user_id, input, context, policy snapshot, blocked/failure reason, timestamps.

Statuses: pending, running, waiting_acceptance, blocked, completed, failed, cancelled. Unique concurrent index on workspace_id plus idempotency_key.

### workflow_step_instance

id, workspace_id, run_id, node_key, node_type, attempt, status, parent_step_id, agent_id, task_id, routing_reason, input, output, failure_reason, timestamps.

Node types: agent, condition, fan_out, join, acceptance, end. Statuses: pending, ready, queued, running, submitted, passed, failed, blocked, waiting_acceptance, skipped, cancelled. Unique concurrent index on run, node key, attempt.

### workflow_submission

id, workspace_id, run_id, step_id, task_id, schema_version, verdict pass/fail/blocked, artifact JSONB, rationale, confidence, root_cause, raw_result, validation_errors, submitted_at.

### workflow_acceptance

id, workspace_id, run_id, step_id, pending/accepted/rejected/cancelled status, reviewer, reason, rework target, context, timestamps.

### workflow_event

Append-only id, workspace_id, run_id, step_id, event_type, idempotency_key, actor, payload, created_at. Unique concurrent index on workspace_id plus idempotency_key.

### Existing-table additions

- agent_task_queue.workflow_step_instance_id UUID nullable
- autopilot.workflow_template_id UUID nullable
- autopilot.workflow_template_version_id UUID nullable
- autopilot_run.workflow_run_id UUID nullable

Add workflow_callback_delivery only if the current durable delivery table cannot support typed inbound/outbound delivery safely.

## 6. Contracts and State

Template definition contains entry node, nodes, edges, routing, Submission schemas, failure policies, rework targets, and hard limits.

Publishing rejects missing/duplicate nodes, dangling edges, no reachable End, outgoing End edges, non-rework cycles, unbounded rework, unmatched Join, unresolved routing, unknown Submission schemas, invalid rework targets, and limits above workspace policy.

Canonical Submission:

    {
      "schema_version": 1,
      "step_instance_id": "uuid",
      "verdict": "pass",
      "artifact": { "type": "code_change", "summary": "...", "references": [] },
      "rationale": "Why the result satisfies the step",
      "confidence": 0.92,
      "root_cause": null
    }

Preferred submission uses an authenticated Multica MCP/CLI command bound to the claimed Task. A delimited final JSON payload is a measured compatibility path. Parsing failure blocks the Step; natural language never implies pass.

Run transitions:

- pending -> running
- running -> waiting_acceptance/blocked/completed/failed/cancelled
- waiting_acceptance -> running/completed/cancelled
- blocked -> running/failed/cancelled

Step transitions:

- pending -> ready/skipped/cancelled
- ready -> queued/waiting_acceptance/passed
- queued -> running/failed/blocked/cancelled
- running -> submitted/failed/blocked/cancelled
- submitted -> passed/failed/blocked
- waiting_acceptance -> passed/failed/cancelled

Issue is a projection: running -> in_progress, waiting_acceptance -> in_review, blocked -> blocked, End -> done, failed -> todo plus failure activity. Cancelling Issue cancels the active Run; done before End is rejected.

## 7. Engine and Recovery

Commands: StartRun, ActivateStep, RecordTaskTerminal, SubmitResult, DecideAcceptance, RequestRework, CancelRun, ReconcileRun.

Each command resolves workspace resources, starts a transaction, locks Run/Step rows in stable order, checks idempotency, validates transition, writes state plus Event, creates Task/outbox if needed, commits, then emits realtime invalidation.

Step activation and Task enqueue must share one application transaction. Refactor TaskService to accept transaction-scoped sqlc queries if required; dual-write repair must not be the normal path.

TaskService stays canonical for Task terminal state. It invokes an idempotent workflow command after commit. Valid completion creates Submission; invalid output blocks with submission_contract_invalid; classified failure goes through node policy.

Run a bounded workflow reconciler beside Runtime Sweeper, initially every 30 seconds. Repair:

- Running Run with no active/ready Step.
- Agent Step without Task.
- Terminal Task not consumed by Step.
- Ready Step past activation timeout.
- Offline Runtime before start.
- Stale Acceptance or callback.
- Terminal children without Join progress.
- Exceeded duration/retry/rework/cost limits.

Repairs replay engine commands, not direct status edits. Irreconcilable state blocks with workflow_invariant_violation.

## 8. Routing, Parallelism, Acceptance

Routing order: explicit Agent, prior-Step selection, business capability match, configured fallback. Candidates pass workspace, archive, invocation, accountable-human, Runtime, provider, concurrency, Skill/MCP, and budget checks. Persist routing reason.

Fan-out creates bounded children with deterministic expansion keys and reserves budget. AND Join passes when required children pass/skip, waits for non-terminal children, and applies fail_fast, continue, pause, or rework policy to failure. Join derives from durable state and is replay-safe.

Acceptance shows criteria, artifacts, verdicts, validation evidence, warnings, cost, and duration. Rejection requires a reason and allowed target. It records an immutable decision, creates a new target attempt, invalidates only downstream nodes, preserves history, injects context, and enforces limits.

## 9. Intake, Autopilot, API, UI

Add POST /api/workspaces/{workspace}/workflow-intake with source, event_id, template_key, title, description, owner, source_url, payload, optional callback. Authenticate, deduplicate by workspace/source/event, resolve a published version, and atomically create one Issue and Run.

Autopilot may bind a published template. It retains trigger admission and concurrency policy; existing create_issue/run_only behavior remains unchanged without a template.

Callbacks use timestamped HMAC, capped backoff with jitter, redacted responses, and dead-letter notification.

Endpoint groups:

- Template CRUD/publish/archive.
- Run create/list/detail/cancel/resume/reconcile.
- Acceptance accept/reject.
- Daemon Task submission/RPC.
- External intake/callback status.

Use typed error codes for invalid definition/transition/submission, idempotency conflict, budget, routing, acceptance conflict, rework limit, and invariant violation.

In packages/core add schemas, ApiClient, workspace query keys, queries/mutations, and realtime invalidation. In packages/views/workflows add Template list/detail, structured form plus advanced JSON editor, Run list/detail, timeline, Step/Submission inspector, Acceptance panel, and Rework dialog.

Wire web and desktop renderer through NavigationAdapter. Do not modify Electron main process. Add a graph library only after fan-out/join and accessibility semantics stabilize.

## 10. Implementation Units

### U0. ADR and pilot

State-machine ADR, definition/submission schemas, policy matrix, Bug Fix template, acceptance examples, flags, owners, estimates. Exit when every transition and repair is unambiguous.

### U1. Schema and repository

Tables/columns, standalone concurrent indexes, sqlc, workspace services, logical-reference validation, cleanup. Test isolation, races, immutability, cleanup.

### U2. Sequential engine

Domain validator, commands, Agent/Acceptance/End executors, transactional enqueue, Events, realtime. Test happy path, illegal transitions, duplicates, crash fences, End-only completion.

### U3. Structured Submission

Tool/RPC, schema registry, compatibility parser, storage, verdict policy, redaction. Test valid/invalid/oversized/duplicate/post-cancel payloads.

### U4. Capability routing

Business capability bindings and resolver with permission/runtime/Skill/MCP/concurrency gates. Test deterministic selection, no candidate, fallback, cross-workspace denial, archival race.

### U5. Acceptance and rework

Complete acceptance_criteria product support, endpoints, notifications, targeted attempts, Issue projection, UI. Test targets, limits, reviewer race, immutable history.

### U6. Autopilot and intake

Template binding, intake idempotency, delivery-table audit, callback worker/signing. Test duplicate delivery, crash/reclaim, archived-template race, dead letter, legacy regression.

### U7. Fan-out and Join

Deterministic expansion, AND Join, sibling policies, parallelism/budgets. Test all pass, failure policies, blocked child, duplicate expansion, restart, hard limits.

### U8. Reconciler

Bounded claims, repair rules, diagnostics, metrics, manual owner/admin action. Test partial states, replica race, replay, offline reroute.

### U9. Complete UI and E2E

Branched trace, malformed/unauthorized/disabled states, routes, accessibility, browser/desktop smoke, operator actions.

### U10. Metrics, pilot, Temporal decision

Dashboards, alerts, rollout/rollback runbook, pilot report, evidence-based Temporal ADR.

Dependency order:

    U0 -> U1 -> U2 -> U3 -> U5
                  |     |     |
                  |     +----> U7 -> U8
                  +----> U4 ---^
                  +----> U6
                  +----> U9, expanded after U5/U7
    Metrics start in U2; U10 closes rollout.

## 11. Pilot and Verification

Bug Fix v1: Analyze -> Implement -> Validate -> Acceptance -> End. After U7, Validate fans out to tests and independent review, then AND joins. Rework targets: Analyze, Implement, Validate/Review; default maximum three rounds.

Pilot entry: two eligible Agents, authentication preflight, repository/test commands supplied, criteria include happy path and edge case, budgets set.

Pilot exit:

- 30 completed internal Runs across 10 Issues.
- Zero duplicate Tasks caused by replay.
- Zero Runs bypassing Acceptance/End.
- Fault-injected states recover without manual DB edits.
- Every blocked/failed Run has a reason and action.
- First-pass acceptance, rework, median/p95 duration, and cost are visible.

Verification:

- Backend: graph, transitions, routing, submissions, Join, rework, policy.
- Database: isolation, concurrent Run/Step creation, enqueue fence, acceptance races, reconciler claims, cleanup.
- Protocol: roles, UUID boundaries, Task binding, malformed payloads, callback replay/signature, old clients.
- Frontend: fallback schemas, query keys, realtime, branches, Acceptance/Rework errors, disabled states.
- E2E: manual/Autopilot Run, duplicate intake, block/resume, rework, failed sibling, restart, Issue cancellation.
- Reliability: 1,000 Runs, maximum fan-out, callback outage, lost realtime, bounded reconciliation.

Run narrow tests per unit, then make test, pnpm typecheck, pnpm test, targeted Playwright, and make check before broad rollout.

## 12. Observability and Rollout

Add bounded-label Run start/terminal/duration, Step terminal/duration, first-pass acceptance, rework, blocked, reconciliation, acceptance wait, and callback metrics. Never use workspace/template/Run/Step/Issue/Agent IDs as Prometheus labels.

Initial SLOs:

- p95 engine transition below one second, excluding Agent/human wait.
- No active Step lacks Task or terminal explanation beyond two reconciler intervals.
- Duplicate workflow-created Task rate is zero.
- 99.9% of accepted intake creates a Run or typed rejection within 60 seconds.
- Every failed/blocked terminal Run has a classified reason.

Backend-authoritative flags: workflow_runtime, workflow_template_management, workflow_external_intake, workflow_parallel_nodes.

Deploy schema/inactive backend, enable developer cohort, fault-test and pilot, enable Acceptance UI, then Autopilot/intake, then fan-out/join, then wider cohorts.

Rollback disables new Runs, drains or explicitly pauses active Runs, retains read/Acceptance access, and never drops records or unlinks Tasks. Legacy paths continue. Failed workflow creation never silently falls back to a legacy run.

## 13. Risks and Temporal Gate

- Issue/Run conflict: Workflow owns execution; Issue is a guarded projection.
- Duplicate work: transactional enqueue, active-task fence, idempotency, fault tests.
- False success: strict structured submission and block on invalid.
- Unsafe routing: reuse workspace/invocation/accountability/runtime checks.
- Runaway cost: hard concurrency, step, retry, rework, duration, token, cost limits.
- Lost rework context: immutable attempts and downstream-only invalidation.
- Premature canvas: form/JSON first.
- Metrics: bounded normalized labels only.

Evaluate Temporal only when at least two are true: substantial Runs exceed one day, timer/compensation code dominates, reconciliation threatens SLOs, cross-service durable signals are required, or replay/history is costly. Temporal may replace orchestration execution, not Multica records used for permissions, audit, queries, and reporting.

## 14. Definition of Done

- [ ] Versions are immutable and graph-validated.
- [ ] Sequential Run reaches End through durable transitions.
- [ ] Existing Task/Runtime executes Agent nodes.
- [ ] Replay and crash recovery never duplicate Tasks.
- [ ] Results become Submission or typed blocked state.
- [ ] Acceptance differs from Task completion.
- [ ] Rework creates bounded attempts with history/context.
- [ ] Fan-out/AND Join replay deterministically.
- [ ] Reconciler repairs every documented partial state.
- [ ] Autopilot/intake are idempotent and compatible.
- [ ] Shared web/desktop UI passes unit, accessibility, smoke tests.
- [ ] Metrics, alerts, runbook, rollback exist.
- [ ] Isolation and invocation permissions have integration coverage.
- [ ] Legacy paths pass regression.
- [ ] Pilot gates pass before broad rollout.
- [ ] Temporal decision uses production evidence.

## 15. Delivery Shape

Assuming two backend and one shared frontend engineer:

- Week 1: U0.
- Weeks 2-3: U1.
- Weeks 3-5: U2 and early Run UI.
- Weeks 5-6: U3/U4.
- Weeks 6-8: U5/U6.
- Weeks 8-10: U7/U8.
- Weeks 9-11: U9/E2E.
- Weeks 11-12: U10 and pilot hardening.

U0 replaces these ordering estimates with owner estimates; they are not commitments.

## 16. Working-Tree Guard

At plan creation the repository has unrelated user changes in apps/desktop/src/main/index.ts and turbo.json. Preserve them. Workflow desktop wiring belongs in renderer platform and should not require Electron main-process changes.
