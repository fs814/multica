# Work replication foundation (TES-26)

This is a disabled-by-default, bounded foundation for stages 0–2, not an
activated sync service or disaster recovery implementation. There are no HTTP
routes, production authorization adapter, daemon startup hooks, timers, UI, or
LocalIssue import. No migration enrolls a workspace. Enabling a Go object alone
does not grant access: Center also requires an explicit authorization callback
and enrolled workspace/group/epoch; the replica and client have separate flags.

## Projection and writes

| Entity | Replicated fields | Offline writable fields |
| --- | --- | --- |
| Issue | title, description, priority, status, number, project_id, parent_issue_id, assignee_type, assignee_id | title, description, priority |
| Project | title, description, priority, status, icon | title, description, priority, icon |
| User-defined Agent | name, description, avatar_url, archived_at | description, avatar_url |

System agents and all credentials, custom environment, MCP configuration,
instructions, permissions and runtime bindings are excluded. References in the
projection do not imply that the referenced records are backed up. Creation,
deletion, workflow transitions and relationship changes remain online commands.

Migration 538 installs row triggers on `issue`, `project`, and `agent`. They
cover SQL INSERT/UPDATE/DELETE from handlers, background services, bulk statements
and maintenance paths without requiring every writer to call a new service.
TRUNCATE, disabled triggers and administrative schema operations are outside the
contract. Updates outside the field allowlist produce no journal entry. Deletes
and identity moves retain tombstones; agent kind changes enter/leave the projection.
Archive remains an ordinary field change. Explicit enrollment locks all three
tables while installing a baseline; it cannot reconstruct pre-enrollment deletes.
Workspace teardown removes its scope, receipts and history transactionally before
business-row deletion, so capture cannot refill the journal. This is permanent
workspace removal, not a replica-visible entity deletion or an offline purge.

The workspace row serializes journal allocation through commit, so late commits
cannot be skipped by a cursor. Repeatable-read snapshots contain a single boundary;
incremental batches must be contiguous. Push locks the business row before the
scope, checks authorization again even for retries, merges against retained
history and saves the receipt in the same transaction. Operation IDs and node
incarnation/sequence are independently unique. Retries with changed payloads fail.
Dependency receipts advance only fields edited by that local chain; unrelated
center changes are never silently adopted as the next edit's base. Conflicts and
rejected edits remain in the replica review queue, with the original operation.

Push invokes no command handlers, notifications, task dispatch, or external
automation. Migration 540 guards the existing issue collaboration wakeup trigger
with a transaction-local setting used only during sync writes. Ordinary online
writes retain wakeup behavior. Read-only status/assignment fields prevent entry
into other workflow side effects.

## Replica storage and transport

The initial store uses one atomic, fsynced JSON checkpoint rather than adding an
embedded database dependency. It commits the confirmed projection, cursor, outbox,
local sequence, acknowledged bases and review queue together. A process lock
enforces one writer. The namespace includes account, actor, node, workspace,
group and epoch. Corrupt/unknown data and history changes fail closed, preserving
the existing checkpoint. An applied receipt does not advance the pull cursor.
Snapshots never infer deletion from absence or clear pending operations.

`daemon.WorkSyncClient.SyncOnce` runs synchronously: pull, up to 256 pushes, pull.
Errors leave unacknowledged intent durable for a caller-controlled retry. The
transport is injected; there is no network implementation or automatic retry loop.

Hard limits: 256 changes per incremental batch, 10,000 snapshot records, 32 MiB
checkpoint, and 1 MiB per field. History/receipts/tombstones are not pruned. Large
workspaces require paginated snapshots, retention policy and a scalable local
store. Checkpoints have local file permissions but no application encryption or
revocation/purge implementation. Do not activate until authorization, offline
data retention, encryption requirements and resource limits are approved.

## Verification and recovery

Use the checkout's managed `.env.worktree` and isolated database; never point
tests at an active Center. `make test` migrates that database and runs Go tests
with the repository's real-agent CLI guard. Targeted checks after loading the
worktree environment:

```sh
cd server
../scripts/go-test-with-agent-cli-guard.sh -- go test -race ./internal/worksync -count=1 -timeout=2m
go test -race ./cmd/migrate -run 'TestWorkSyncMigrations|TestConcurrentIndexCleanupsMatch' -count=1 -timeout=2m
```

The migration test uses a scratch schema: up/repeat, indexes, tenant moves,
deletes, agent-kind transitions, 538/539 down/up, full down/up. Center tests use
isolated fixture workspaces and two on-disk replicas, with no real agents.
Coverage includes restart, field conflicts, causal dependencies, response replay,
permissions, rollback, commit order, checksum/gap rejection, I/O failure, and
suppression of wakeup receipts while ordinary updates still emit them.
Handler tests also cover sync-state cleanup, rollback and preservation of another
workspace. Migration tests retry after a simulated DDL/ledger interruption.

Interrupted concurrent indexes are cleaned up by the migration runner before
retry. Never manually mark a failed migration applied. Rollback 540 restores the
ordinary wakeup trigger; disable all sync writers before rollback. Rollback 538
removes capture but retains the journal; writes while capture is absent are not
recoverable through that history. Rollback 533 deletes sync history and receipts,
so do not use it on an enrolled workspace as an operational recovery method.
Keep production disabled and retain checkpoints when investigating errors.

Uncovered release gates: real authenticated HTTP and resource-level ACLs,
revocation/purge, network backoff, live daemon lifecycle, encrypted/scalable
storage, offline create/delete, conflict-resolution UI, Windows execution and
power-loss testing, full Work entities/attachments, LocalIssue import, multiple
center fencing, recovery ownership/staging/activation, and performance at scale.
This foundation does not satisfy the full TES-26 acceptance criteria.
