# Work replication foundation (TES-26)

This is disabled-by-default replication for the bounded Issues, Projects and
Agents projection below. It includes authenticated HTTP, on-disk replicas and a
joined daemon polling/reconnect lifecycle. It is not complete Work backup or new
Center recovery. No migration/startup enrolls a workspace, issues a credential,
creates a grant, imports LocalIssue or starts an agent from replicated data.

## Explicit activation boundary

Both Center and daemon require `MULTICA_WORK_SYNC_ENABLED=1`; unset means off.
Keep production off until provisioning, retention and storage policy are approved.
The Center mounts `POST /api/daemon/sync/{handshake,pull,push}`. Each body uses
`schema: 1` and the exact `scope: {workspace, group, epoch}`; pull also accepts
`cursor` and `snapshot`, push an `operation`. Handshake returns the authenticated
principal and schema/batch capability. Enrollment and grant administration are
operator-only database/service operations; these endpoints cannot grant access,
enroll, change history, mint tokens or restore a missing Center. There is no new
public UI or CLI for provisioning in this stage.

Before using an isolated replica, an operator must explicitly enroll its scope
with `Center.Enroll`, provision a finite-lived `work_sync_grant` row for that
workspace/group/epoch/actor/node, and provide a matching existing `mdt_` daemon
credential. Grants are not inherited from membership. The actor must currently be
an owner/admin (the existing role allowed to view/manage the complete three-entity
projection). At least one registered runtime must belong to that actor and node;
mixed runtime ownership for a node is rejected. Regular members need a future
filtered-journal protocol. Only non-secret allowlisted fields are exported.

Every HTTP request checks the database directly: token expiration/revocation,
current membership/role, runtime ownership, grant expiration/revocation and scope.
No PAT/JWT/cloud/task-token fallback and no authentication cache are used. Account
and actor come from the grant's live user identity, never client headers/payload.
Push rechecks inside its transaction, even when returning a duplicate receipt.
An already authorized in-flight transaction may complete during revocation;
subsequent requests are denied. Database outages return 503, not a revocation.
On ownership transfer, explicitly revoke grants and rotate old node credentials.
Workspace deletion removes grants transactionally alongside receipts/history.

The daemon also requires `MULTICA_WORK_SYNC_CONFIG` pointing to a JSON file:

```json
{
  "root": "/absolute/persistent/profile/work-sync",
  "targets": [{
    "scope": {"workspace": "<uuid>", "group": "<uuid>", "epoch": "<uuid>"},
    "actor": "<owner-or-admin-user-uuid>",
    "token_file": "/absolute/private/daemon-token"
  }]
}
```

Use an explicit persistent root outside task workspaces/garbage collection.
Windows activation fails closed until a Windows credential ACL implementation is
available; the current credential-file adapter requires Unix permission bits.
The credential file must be a regular file with owner-only access; credentials
never enter the checkpoint. It is reread per request. Targets are bound to the
configured daemon ID and Center origin, and are not discovered from local data.
HTTPS is required except loopback HTTP for isolated tests. Redirects are rejected.
Changing the configured account, origin or scope cannot replay an old queue.
The config accepts at most 32 distinct workspaces and is loaded at daemon startup.

The loop starts before the daemon's network preflight, so an offline startup can
retain a replica. It is canceled and joined before the store is closed on exit.
It polls every 30 seconds and listens to reconnect reconciliation hints; failed
network/429/5xx attempts use jittered exponential backoff (0.5–1 second initially,
up to 2 minutes). Hints cannot bypass failure backoff. Protocol, identity, local
storage and authentication failures stop that target with an operator-action log.
Other configured workspaces keep their independent loops. Local edits currently
use `Replica.Queue` internally; Work UI/local HTTP editing and sync status UI are
still a separate stage. No existing LocalIssue handler is called.

A 401 stops synchronization and preserves the checkpoint for credential repair;
missing/mismatched Center history returns 409 and likewise preserves it. A 403
from a credential-authenticated scope clears the confirmed projection/acknowledged bases and persists a revoked
marker atomically. Pending and conflict edits are moved to an inaccessible local
quarantine of original operations (without Center conflict payloads); normal
State/Queue/Apply/Acknowledge and automatic replay reject the revoked store,
including after restart/regrant. Regrant requires explicit review of quarantine
and provisioning a fresh replica; there is no automatic unquarantine. An I/O
failure prevents reporting purge success and denies further access in memory.
After an interrupted/failed purge, the next online attempt must recheck permission.
This is logical removal, not secure erasure of filesystem backups or an offline
remote wipe. Quarantine is sensitive local intent and has no automated expiry yet.

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

Migrations 538 and 541 install row triggers on `issue`, `project`, and `agent`. They
cover SQL INSERT/UPDATE/DELETE from handlers, background services, bulk statements
and maintenance paths without requiring every writer to call a new service.
TRUNCATE, disabled triggers and administrative schema operations are outside the
contract. Updates outside the field allowlist produce no journal entry. Deletes
and identity moves retain tombstones; agent kind changes enter/leave the projection.
Archive remains an ordinary field change. Explicit enrollment locks all three
tables while installing a baseline; it cannot reconstruct pre-enrollment deletes.
Workspace teardown locks its business rows (including Projects), closes the scope
in a separate statement, then removes receipts and history with a fresh snapshot.
Enrollment takes the workspace lock before business table locks, so enrollment
cannot recreate the scope while teardown is in progress or after it commits.
Capture cannot refill the journal once the scope is closed. This is permanent
workspace removal, not a replica-visible entity deletion or an offline purge.

Migration 541 defers capture until transaction commit. An immediate trigger records
all touched workspace IDs in a transaction-local setting; deferred capture locks
those scope rows in UUID order after business writes finish, then allocates journal
positions. The setting and queued events obey savepoint/transaction rollback. Scope
locks remain held through commit, so late commits cannot be skipped by a cursor.
Cross-workspace transactions use the same order regardless of business-row order.
Repeatable-read snapshots contain a single boundary; incremental batches must be
contiguous. Push locks its one business row before the scope, finishes its update,
then explicitly flushes capture before reading the version and saving the receipt.
It checks authorization again even for retries and merges against retained history.
No business writes may follow an explicit capture flush: ordinary SQL writers must
leave these capture constraints deferred (do not use SET CONSTRAINTS ALL IMMEDIATE).
This also avoids adding transaction retries around unrelated command side effects.
Operation IDs and node incarnation/sequence are independently unique. Retries with changed payloads fail.
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

`daemon.WorkSyncClient.SyncOnce` runs synchronously: authenticate/handshake, pull,
up to 256 pushes, pull. Successful operations and cursors are committed separately
so a lost response replays the same operation ID. The HTTP transport bounds wire
messages and requests to 32 MiB and 30 seconds. Snapshot/wire capacity violations
return terminal 507 errors, so they cannot become an endless network retry loop. `Run` owns automatic retry; tests
can still inject a transport. Errors retain intent, subject to quarantine on denial.

Hard limits: 256 changes per incremental batch, 10,000 snapshot records, 32 MiB
checkpoint, and 1 MiB per field. History/receipts/tombstones are not pruned. Large
workspaces require paginated snapshots, retention policy and a scalable local
store. Checkpoints have local file permissions but no application encryption or
secure erasure. Do not activate until grant provisioning, offline data retention,
encryption requirements and resource limits are approved.

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

The migration test uses a scratch schema: up/repeat through 543, indexes, tenant moves,
deletes, agent-kind transitions, 538/539/541 down/up, full down/up. Center tests use
isolated fixture workspaces and two on-disk replicas, with no real agents.
Coverage includes restart, field conflicts, causal dependencies, response replay,
permissions, rollback, commit order, checksum/gap rejection, I/O failure, and
suppression of wakeup receipts while ordinary updates still emit them.
Handler tests also cover sync-state cleanup, rollback and preservation of another
workspace, concurrent final-phase journal commits and deferred Project writes.
Online multirow transactions are tested against online single-row writes and Push,
including opposite cross-workspace write order and delayed enrollment after deletion.
Migration tests retry after a simulated DDL/ledger interruption.

Interrupted concurrent indexes are cleaned up by the migration runner before
retry. Never manually mark a failed migration applied. Rollback 541 restores
immediate capture and its pre-fix concurrency limitations;
disable enrollment/sync writers before rolling it back. Rollback 540 restores the
ordinary wakeup trigger; disable all sync writers before rollback. Rollback 538
removes capture but retains the journal; writes while capture is absent are not
recoverable through that history. Rollback 533 deletes sync history and receipts,
so do not use it on an enrolled workspace as an operational recovery method.
Keep production disabled and retain checkpoints when investigating errors.

HTTP integration tests use a real loopback HTTP server with two daemon clients
and independent on-disk stores, an isolated PostgreSQL database and fixture-only
credentials. They cover all three entities, two-way edits, same-field conflicts,
deletes versus pending edits, lost successful responses, store restart, background
reconnect convergence, receipt deduplication and wakeup/task suppression. Negative
tests cover cross-workspace/history/actor mismatches, malformed wire requests,
non-daemon credentials, grant/member/role/runtime/expiry revocation, token invalidation without purge, redirect
refusal and durable quarantine. Lifecycle tests start/stop/restart the daemon's
actual sync hook without starting any agent executables. They do not run three
production binaries, simulate OS power loss or prove production recovery.

Remaining acceptance/release gates:

- New Center recovery: trusted recovery identity, multi-replica coverage manifests,
  staging/import, dependency closure, missing-data reports, old-Center fencing,
  epoch lineage and explicit activation are unimplemented. An empty/new Center
  cannot accept uploaded copies through this protocol.
- Full Work: comments, labels, custom attributes/status definitions, memberships,
  squads, relationships, chats/history, automation definitions, Skills/Memory,
  attachment metadata and actual bytes are not replicated. References are not
  dependency backups. No credentials/secrets are included.
- LocalIssue: workspace/actor/visibility choice, stable deduplicated import mapping,
  execution history conversion and no-reexecution import flow are unimplemented.
- Provisioning/revocation UX, filtered member replicas, offline edit/status UI,
  conflict resolution and quarantine recovery UI, encrypted/scalable storage,
  snapshot pagination, retention/compaction, offline create/delete, Windows runtime
  and OS power-loss tests, supported-version/deployment matrix and scale limits
  remain. No production grants, replication or disaster recovery were activated.

User decisions still required for full delivery: exact Work/attachment/secret
scope; whether/where LocalIssue and outputs may be imported; approved replica
machines and storage encryption/retention; recovery owner verification and old
Center isolation; acceptable recovery loss window and offline revocation delay;
confirmation of single active Center, manual same-field conflict resolution and
delete-wins policy for release. None is silently selected by enabling this stage.
The bounded HTTP stage does not satisfy the full TES-26 acceptance criteria.
