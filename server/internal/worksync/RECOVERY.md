# Bounded, operator-controlled recovery

Recovery is disabled by default and has no public HTTP, daemon-token, UI or CLI
entry point. `Recovery.Enabled`, an independent `Authorize` implementation and a
`VerifyFence` implementation are all mandatory. The tests supply isolated
fixtures for those interfaces; they are **not production authorization adapters**.
Keep production replication and recovery off until deployment identity, approved
machines, persistent fencing, encryption/retention and acceptable loss are agreed.

## Procedure and trust boundary

1. Stop ordinary destination traffic and all destination scheduling/notification
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
6. Isolate the old Center using the deployment's authoritative mechanism (for
   example revoke its database writer/traffic and execution access). Keep that
   fence in force after activation. `VerifyFence` must verify this durable state;
   an unreachable URL, a new UUID or a supplied Boolean is not a production fence.
   The fixture verifies its exact old child process exited and separately requires
   fixture approval. No deployment fencing policy is inferred from it.
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
8. Start the new Center only after activation succeeds. Provision fresh grants and
   credentials explicitly and open fresh daemon namespaces for the new epoch.
   Old scopes are rejected; no old outbox is automatically retargeted or replayed.
   Resolve retained pending/conflicting intent as new, separately reviewed commands.
   Do not reactivate the fenced old Center as another writer.

The service is a controlled operator building block. Supplying safe deployment
adapters and keeping the destination offline during the procedure are caller
obligations. There is deliberately no permissive production adapter that mistakes
workspace membership, a daemon token, or a self-reported machine for recovery
permission. This stage is not a general public disaster-recovery endpoint.

## Failure, retry and deletion

Stage and activate survive service/process restart using the database session.
Canceled/failed imports roll back business rows, journal and activation receipt;
retrying the identical plan/report is safe. Concurrent stage/activate uses
workspace-before-session lock order. An already activated session rechecks current
authority and fencing before returning success, without inserting again. A new
session cannot overwrite a nonempty target. Workspace deletion removes recovery
reports together with sync state; delayed stage cannot refill a deleted workspace.
There is no automatic staging expiry, encryption, conflict-resolution UI or secure
erasure of exported files/backups.

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
isolated database migrated through 545, run the opt-in test:

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
