# Bounded, operator-controlled recovery

Recovery is disabled by default. The separately gated operator HTTP entry point
supplies database-backed recovery identity checks and a durable old-database write
fence to the existing recovery service. Daemon tokens and ordinary user credentials
cannot recover a Center. Keep production replication and recovery off until the
deployment identity, approved machines, external execution isolation,
encryption/retention and acceptable loss are agreed. No production credentials or
deployment permissions are provisioned by this code or its migrations.

## Controlled HTTP entry point

The full Center router exposes `POST /api/recovery/work/{stage,fence,activate}`
only when `MULTICA_WORK_RECOVERY_ENABLED=1`. Normal replication's feature flag
does not enable recovery. Disabled requests return 404 before reading the body.
Configuration also requires:

- `MULTICA_WORK_RECOVERY_TARGET_ID`: independently provisioned destination UUID.
- `MULTICA_WORK_RECOVERY_SOURCE_ID`: independently provisioned old deployment UUID.
- `MULTICA_WORK_RECOVERY_SOURCE_DATABASE_URL`: protected connection configuration
  for the **actual old writer database**, never a replica or restored clone.

Migrate both databases through 549. Operators provision exactly one row in
`work_sync_recovery_deployment` in each, with distinct deployment IDs matching
configuration. Missing, duplicate or mismatched identity fails closed. Keep the
source connection configuration in the deployment's secret store; errors never
return connection strings to clients. Use TLS and restrict the endpoint at ingress.

Provision a separate high-entropy bearer credential (`wrc_` followed by 64 hex
characters from 32 random bytes) through a trusted operator channel. Store only
its SHA-256 hex hash in `work_sync_recovery_authority.token_hash`. Bind that row to
the workspace, verified owner, nonempty operator ID, both deployment IDs, the
complete JSON `RecoveryPlan`, finite `expires_at`, and `revoked_at=NULL`.
Never derive this authority from an uploading machine's claim or ordinary
membership. Operators must independently verify source machines and bundle
digests before approving the plan. The API cannot mint credentials, bootstrap
owners or change these bindings.

Every request and each activation retry rechecks the credential, deployment,
full plan, expiry/revocation and current destination owner membership. Identity
comes from this provisioned row, not headers/body actor claims. No cache or
JWT/PAT/daemon fallback is used. An already-authorized in-flight attempt can finish;
revocation denies subsequent checks, including retries after successful activation.

All bodies contain `schema: 1` and `plan`. Stage also takes `bundles` and returns
`{schema, status: "stage", report}`; it does not publish business records. After
reviewing the returned report, the operator pins its exact digest in
`approved_report_hash` on the authority row. Fence and activate take
`approved_digest` and no bundles, returning `{schema, status}`. Both require that
digest to match the independently approved hash and immutable staged report.
Requests are capped at 32 MiB and 30 seconds; unknown fields/trailing JSON and
unsupported schemas fail. Errors are sanitized: malformed input 400, wrong
credential type 401, authority/fence denial 403, scope mismatch 409, unavailable
database/lock contention 503. Stage/report capacity errors return 507.

`fence` takes NOWAIT barriers on the old Issues, Projects, Agents and scope
tables, verifies the old lineage and installed guards, and commits the exact
plan/report/deployment binding into `work_sync_recovery_fence`. Conflicting plans
cannot replace it. Row guards then reject INSERT/UPDATE/DELETE, workspace moves,
scope removal and re-enrollment on that old workspace, including direct SQL from
old binaries. A preexisting shared guard row serializes writes with closure:
in-flight writes must finish before fencing, blocked READ COMMITTED writers see
the committed fence, and stale repeatable-read writers fail serialization. The
separate guard row avoids upgrading normal writers' shared locks on the capture
scope. Other workspaces retain normal behavior.

Fence data persists across process/database restart and has no un-fence API.
It is intentionally retained across workspace deletion/UUID reuse; the ordinary
delete API cannot remove a fenced source workspace. Rollback of migration 549
refuses active fences. Do not disable triggers, truncate guarded tables, drop
guard rows or downgrade a fenced source. Restrict schema/guard administration to
trusted operators; a privileged database administrator is outside this barrier's
threat model. Normal sync calls to a fenced source return 409, preserving replicas.

Activation checks the proof on the actual source database every attempt and
even on a duplicate request. An unavailable source, wrong deployment, disabled
guard or missing/mismatched proof denies activation. There is no inference from
a failed health probe. A committed fence is kept if destination import fails or
its response is lost; retry the identical approved request after repairing the
failure. There is no cross-database atomic commit and no automatic rollback of
the source fence.

**Fence scope:** this adapter prevents old-center writes to the three replicated
entity tables and scope. It does not revoke already-running agents, external
execution/notification access or writes to other Work domains. Operators must
isolate those separately and keep ordinary destination traffic/workers quiescent
until activation is complete. This bounded adapter is not full deployment
isolation, offline-source recovery or whole-Work disaster recovery.

## Procedure and trust boundary

1. Stop ordinary destination workspace traffic and all of its scheduling/notification
   workers. Bootstrap only the original workspace UUID and independently verified
   owner/member identities. Neither a daemon upload nor this service creates an
   owner, ACL, runtime binding or credential. Use a separately authorized, empty
   destination workspace. Provision its issue prefix and historical number counter
   independently; replicas do not contain pre-baseline deleted issue numbers.
2. Export each approved, initialized replica with `Replica.ExportRecovery` while
   owning its store lock. The envelope includes scope, complete confirmed snapshot,
   cursor, acknowledged records ahead of that cursor, outbox and original review
   records. Revoked/uninitialized/corrupt stores cannot export. The source stores
   are not changed. Protect exports and staging as private business data.
3. Independently authenticate every source machine and inspect/pin its exact
   bundle digest in `RecoveryPlan.Sources`. A SHA-256 checksum is integrity, **not
   provenance or a signature**. A supplied digest, actor or epoch must never grant
   its own authority. The authorization adapter must pin the whole plan, owner,
   target, session ID, expected machines and reviewed boundary, and recheck current
   permission/expiry on every stage/activate call, including retries. At least two
   distinct sources are required. Missing expected machines block this plan.
4. `PlanRecovery` selects the highest complete snapshot on that pinned lineage.
   Equal-boundary snapshots must agree. Older snapshots cannot contain a record
   missing/newer in the selected snapshot. Identical global sequence numbers with
   different records, unknown fields, gaps, other histories, revoked copies,
   invalid pending operations and missing project/parent references are rejected.
   Acknowledged and conflict/rejection receipt records from different replicas
   complement the snapshot only when
   their union covers every sequence through the explicitly approved boundary.
   The union cannot silently discard any later confirmed record. This is bounded
   to 10,000 records, 32 sources and 32 MiB total input; no paginated recovery.
5. `Recovery.Stage` persists the immutable report and all source bundles in
   `work_sync_recovery`. It does not publish business data or enroll the workspace.
   Repeating an identical session succeeds; changing its content fails. The report
   retains local intent and conflicts without applying either. Missing optional
   Work domains and the unknown tail of unreplicated Center commits are explicit
   limitations, not a claim of complete coverage or RPO=0.
6. Isolate external execution/traffic using the deployment's authoritative
   mechanism. For the bounded database projection, call the controlled `fence`
   endpoint after report approval. Keep both forms of isolation in force after
   activation. The older internal binary fixture's process-exit check is only
   fixture evidence; the public adapter verifies persisted database proof.
7. Review and approve the exact report digest, then call `Recovery.Activate`.
   Authorization and fencing are checked again. Under workspace/session/business
   locks it checks the destination is empty, validates owner and assignee identity,
   rejects residual runtimes, daemon credentials, automation/wakeups/workflow runs
   and tasks referencing restored IDs, inserts allowlisted Projects/Agents/Issues,
   verifies the resulting database
   projection, retains tombstones and writes a fresh epoch journal plus activation
   receipt in one transaction. Only the seven built-in issue status keys are
   supported; custom status catalogs block activation. Missing identities, UUID or
   number collisions, invalid field types and database constraints abort all writes.
   Recovered Agents have private permissions, empty execution configuration and no
   runtime. There are no task/notification command calls. The old epoch, reviewed
   boundary and pinned source manifests remain in the immutable report.
8. Open ordinary new Center traffic only after activation succeeds. Provision fresh grants and
   credentials explicitly and open fresh daemon namespaces for the new epoch.
   Old scopes are rejected; no old outbox is automatically retargeted or replayed.
   Resolve retained pending/conflicting intent as new, separately reviewed commands.
   Do not reactivate the fenced old Center as another writer.

The service is a controlled operator building block. Provisioning trusted
deployment identities/credentials and keeping destination business traffic offline
are operator obligations. Membership, a daemon token or a self-reported source
alone never grants recovery permission.

## Failure, retry and deletion

Stage and activate survive service/process restart using the database session.
Canceled/failed imports roll back business rows, journal and activation receipt;
retrying the identical plan/report is safe. Concurrent stage/activate uses
workspace-before-session lock order. An already activated session rechecks current
authority and fencing before returning success, without inserting again. A new
session cannot overwrite a nonempty target. Workspace deletion removes recovery
reports and recovery authority together with sync state; delayed stage cannot
refill a deleted workspace. Source fence rows are retained.
There is no automatic staging expiry, encryption, conflict-resolution UI or secure
erasure of exported files/backups.

The business-table barriers are table-wide, including other workspaces in the
same database. They use NOWAIT so activation never holds one business table while
waiting for an ordinary writer on another. Lock contention, deadlock and
serialization failures roll back the complete transaction and retry at most 20
attempts, with a context-cancelable delay of 25–250 ms between attempts. Every
attempt rechecks authorization, owner, approved report and fence; adapters must
support repeated checks without side effects. Exhaustion returns the database
error and leaves staging available for a later operator retry. Other workspaces
can write between attempts but may briefly wait while a successful activation
holds its barriers. This does not authorize live traffic in the destination
workspace or replace durable isolation of the old Center.

The replica projection does not include complete agent definitions, custom status
catalogs, Comments, attachments/bytes, ACL history, secrets, runtime identities,
LocalIssue, execution history, Skills/Memory or automations. Missing dependencies
are rejected, not fabricated. Creator metadata for imported Issues is the verified
recovery owner because original creators are outside the current projection.
Creation timestamps are new. UUIDs and allowlisted projection values are preserved.
Issue parent references are assigned in a second pass so historical foreign keys
work even when a child UUID sorts before its parent. The workspace counter is advanced to at least the highest restored live number;
an independently provisioned larger historical counter is retained.

## Reproducible binary fixture

Build `cmd/server` and `cmd/multica` from the reviewed commit. Against the managed
isolated database migrated through 549, run the opt-in test:

```sh
WORK_SYNC_CENTER_BINARY=/absolute/fixture/center \
WORK_SYNC_DAEMON_BINARY=/absolute/fixture/multica \
WORK_SYNC_BINARY_LOG_DIR=/absolute/fixture/logs \
WORK_SYNC_PROTECTED_DAEMON_PID=<PID-from-multica-daemon-status> \
../scripts/go-test-with-agent-cli-guard.sh -- \
go test -race -p 1 ./internal/worksync \
  -run '^TestWorkSyncFullBinariesRecovery$' -count=1 -v -timeout=8m
```

The harness runs one Center at a time and two real daemon processes. It creates
unique temporary profiles, pins every Agent CLI to a nonexistent fixture path,
uses `--allow-offline --no-task-claims`, joins every child before returning and
removes its profiles. Never pass a real Center database or credentials. Use a
canonical temp directory outside a daemon task tree; lifecycle commands correctly
refuse daemon-managed task config roots. No real user agent is run.

Assertions cover all three entity kinds, two-way edits, equal-field conflicts,
Center outage/reconnect, durable daemon restarts, deletion versus pending edits,
old-process isolation, staged recovery with double activation, new-epoch fresh
credentials/namespaces, retained old conflicts and zero execution tasks. Local
edits use `Replica.Queue` while the owning daemon is stopped; this does not imply
an offline UI/API exists. The destination is a logically empty workspace in the
isolated database, with independently provisioned fixture identity; the test does
not claim physical host loss, whole-database restoration or production fencing.

Retain both binaries with the evidence, together with build argv and exit codes,
`go version -m` output (including VCS revision and modified state), SHA-256
checksums, test argv/exit code and child logs. A manifest without the binary
artifacts cannot independently establish the executed binaries' checksums.

The public API fixtures use two independently migrated, isolated databases:
`DATABASE_URL` for the old source and `WORK_SYNC_RECOVERY_TEST_DATABASE_URL`
for the destination. Run `TestRecoveryPublic` with race enabled. Without those
explicit database settings, database tests skip. `TestRecoveryPublicCenterBinary`
additionally requires `WORK_SYNC_CENTER_BINARY` and tests the real Center router:
default-off rejection, wrong credential rejection, staging, unfenced rejection,
durable fence, process restart, activation and duplicate activation. It uses
on-disk replica exports; real daemon transport remains covered by the separate
full-binary fixture. None of these isolated fixtures grants production authority.
