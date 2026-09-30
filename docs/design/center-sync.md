# Sync between center servers

Superseded interaction model: the user now requires owner-only, Desktop-triggered
sync, not continuous server-to-server replication. The
[manual sync contract](center-sync-manual.md) takes precedence over the background
worker, persistent peer approval and autonomous transport proposals below.

Status: archived proposal, not the implemented transport. The current bounded,
manual implementation and its validation limitations are documented in
[manual sync setup](center-sync-manual.md). Existing backup/export/import
authorization is unchanged.

## Product contract

Connect two or more centers, select workspaces, approve pairing once, then keep
their supported workspace data synchronized without entering a recovery token.
Sync continues when Desktop is closed. Clicking Sync on an approved connection
requests immediate reconciliation; it does not replace a database or switch the
Desktop's active center. Multiple centers can contain unrelated workspaces that
must remain untouched.

“No token” means no manually generated, copied, saved or entered
`MULTICA_RECOVERY_TOKEN`. It does not mean anonymous access. Centers create and
protect their own machine keys; people use their ordinary signed-in accounts to
approve access. A new server cannot safely read private data just because someone
knows its address. First-time pairing therefore requires approval on both sides.

Initial consistency choice: edits may originate at any paired center, but each
workspace has one home center that orders accepted writes and owns execution.
Offline edits are explicitly pending proposals, not globally committed writes.
This is not arbitrary multi-master SQL replication or automatic disaster recovery.

## Topology and ownership

```text
                       Desktop / web
                  pairing, status, Sync now
                              |
                  authenticated control API
                              |
       Center B  <========== Center A ==========> Center C
       replica                home                replica
       pending edits ------> order + validate <--- pending edits
       confirmed data <----- workspace journal --> confirmed data
                              |
                       execution authority
                       daemons / schedules
```

The diagram describes one workspace. Another workspace can have B as its home.
Every replica connects directly to that workspace's home; v1 has no gossip,
transitive peer trust or replica-to-replica forwarding. All three centers have
their own database, uploads, identity, durable worker and peer registry.
One persistent mutually authenticated connection can carry pull requests and
write proposals; the replica initiates it, so an inbound-open replica is not
required. A relay/NAT traversal service is outside v1.

Only the home allocates issue numbers, orders workflow commands, dispatches runs,
fires integrations and advances schedules. Replication apply must suppress those
effects, including database wakeup triggers. Replica editing requires a distinct
pending overlay and routing all synced-workspace writes through the sync command
service; existing HTTP handlers must not silently write replica business rows.
Non-synced workspaces continue using their existing behavior.

No automatic home promotion during a partition. Home outage allows cached reads
and bounded pending edits, but no new runs or authoritative changes elsewhere.
A future planned handoff must freeze writes, drain or explicitly terminate runs,
replicate a barrier, durably fence the old home and daemons, then install a new
signed ownership epoch. Merely timing out or changing an epoch at B cannot fence
A. Forced failover requires external fencing or a separately designed quorum
protocol; it is not part of the first release.

## Pairing without recovery tokens

1. Each center generates a persistent machine signing key and TLS identity on
   first enablement. Store private material outside replicated tables, with
   owner-only permissions/ACLs or a deployment secret store. Private keys never
   enter Desktop, logs, snapshots of workspace data or peer responses.
2. Desktop saves a candidate address locally. Test checks reachability and, once
   implemented, a versioned sync capability/identity endpoint. Reachability alone
   neither authenticates a center nor grants access.
3. The user signs in separately to each center. Source authorization is workspace
   `owner` in v1, not arbitrary membership. On the receiving center the approver
   must be allowed to create/own the local replica workspace. A server operator
   explicitly enables the feature and its resource/network policy. Workspace
   ownership does not confer deployment-wide export or administration rights.
4. Create a short-lived pairing request bound to both center IDs, both public-key
   fingerprints, selected workspace IDs, home/replica roles, schema scope, nonce
   and expiry. Show the same pairing summary in the independently authenticated
   sessions on both centers. Each owner approves that exact summary; neither
   unauthenticated discovery nor an arbitrary address can auto-approve a peer.
5. Both servers sign and consume the one-time approval transcript. Persist
   per-workspace peer grants only after both approvals. Retry is idempotent;
   changed scopes, expired requests, replayed nonces and identity mismatches fail.
6. Workers use mutual TLS with the pinned approved machine identity and check live
   workspace grants on every request and write transaction. Routine certificate
   renewal uses the same pinned identity; key rotation requires old-key proof and
   peer acknowledgement. Lost or compromised keys require explicit re-pairing.

Use HTTPS for sign-in and pairing. A self-hosted private network can use a private
CA installed in the platform trust store; do not disable TLS verification. A
plain HTTP address, even if saved successfully, is not sufficient for secure
enrollment. No source cookies or credentials are forwarded to the other origin.
If TLS terminates at a proxy, the sync listener must still verify peer identity
through an authenticated backend channel; arbitrary forwarded headers are not
identity. Never authorize with a request marker or a client IP alone.

Server-side dialing accepts only an operator-enabled sync origin/port, disallows
URL credentials and redirects, and checks every DNS resolution/connection against
egress policy (including loopback, link-local and metadata endpoints). Private
networks require explicit allowed ranges. Users cannot turn Test or pairing into
an unrestricted server-side HTTP proxy. Bound pending requests and rate-limit
discovery, pairing and authentication failures.

A peer is a trusted recipient of the selected data: its operator can read its
replica. Revoking access prevents subsequent transfers but cannot erase copied
data or backups on an offline/untrusted machine. Surface this consequence during
pairing. Revocation closes channels and cancels uncommitted work; replicas that
cooperate quarantine pending edits and remove their accessible confirmed copy.

## Data scope and identity

| Scope | First release / later contract |
| --- | --- |
| Issues, projects, user-defined agents | First release: only the existing Work Sync safe field projection and field-edit allowlist; expose as a limited replica view, not a complete workspace |
| Comments, labels, statuses, squads and relationships | Later versioned expansion with dependency-complete snapshots, commands and conflict rules |
| Attachments | Later content-addressed, resumable blob replication tied to workspace grants; no “in sync” claim while required bytes are missing |
| Membership and permissions | Home-authoritative policy; no offline role changes; receiver access is explicitly mapped and cannot exceed source authorization |
| Run history | Later read-only projection; running tasks and daemon claims are never replayed as commands |
| Password hashes, sessions, PATs, provider keys, deployment keys, daemon credentials | Never replicated by workspace sync |
| Local CLI sessions, repositories, runtime bindings, unapproved agent configuration | Local to each center/machine; configure independently |

Sync is not a complete backup. Even allowlisted text may contain user-entered
secrets; approving a workspace discloses its selected content to the peer.
The initial field list is defined in the existing
[Work Sync contract](../../server/internal/worksync/README.md#projection-and-writes).
Do not quietly broaden it when reusing that code. A future “all workspace data”
capability needs a reviewed manifest for every entity, field, relationship and
side effect before it can be offered in the UI.

Use stable center, workspace, group and history-epoch IDs, not URLs as identity.
Changing an address cannot adopt a new identity or replay another peer's queue.
Preserve entity UUIDs and home-assigned issue numbers. The receiver initially
creates an isolated replica namespace. Never merge independently created
workspaces by name, slug, issue number or email. UUID/content collisions are
reported before activation; they do not cause overwrite. Linking two populated,
independent workspaces needs a separate preview-and-mapping migration workflow.

Accounts are center-local. Link identities using separately authenticated sessions
or an approved common identity provider, never email equality alone. For writes,
the home issues a scoped, expiring user delegation after that user authenticates
and authorizes the peer. The peer cannot choose an arbitrary `actor_id`. The
worker's machine grant plus that delegation plus current home membership must
all pass, including on duplicate-operation receipt lookup. This exchange is
automatic; it is not a recovery-token field. Pending operations whose user
authorization expires stop for sign-in/reapproval, not silent impersonation.
Initial whole-workspace replicas are owner-only; member access requires a
separately designed filtered projection and revocation-aware serving policy.

## Replication protocol

Use a new versioned center protocol. Share tested pure merge, digest, operation
and checkpoint logic from `internal/worksync` where suitable, but do not allow
machine peers to impersonate existing daemon `mdt_` principals or bypass grants.
The current daemon protocol is schema 1, has a 10,000-record snapshot limit and
is not a general center replication API.

1. **Handshake:** negotiate protocol and entity/field capabilities, approved scope,
   peer identity and ownership/history epochs. Fail closed on unsupported required
   fields; surface “update server” rather than falling back to recovery import.
2. **Bootstrap:** create a consistent workspace snapshot at journal watermark S.
   Persist a manifest with schema, record counts, hashes, page tokens and expiry.
   Page from that immutable snapshot, not independent live database queries.
   Stage and validate pages/dependencies before atomically activating the replica
   pointer at S; preserve unrelated workspaces and pending proposals. Continue
   from S. Expired snapshots restart staging without publishing a partial view.
3. **Capture:** commit allowed business mutations, ordered journal entries and
   operation receipts atomically. Journal positions must reflect commit order,
   not merely sequence allocation order. Retain the Work Sync deferred-capture
   lock-order invariant when adapting its SQL triggers. Every writer, including
   background/bulk paths, must be covered and tested.
4. **Propose:** replicas durably queue operation ID, originating center/incarnation,
   local sequence, actor delegation, scope, base revision, dependencies and patch.
   Home revalidates authorization, schema and ownership, merges or rejects, then
   records an idempotent receipt in the same transaction. Reusing an ID with a
   different payload is an error. Do not acknowledge success before durable commit.
5. **Apply:** pull contiguous batches after the durable cursor. Apply business
   projections and advance the cursor together. A receipt alone does not advance
   the pull cursor. Replica apply does not emit a new home event or run automation,
   so A → B → A cannot create a loop. Resume lost responses with the same IDs.
6. **Reconcile:** continuously pull, send queued proposals and pull again. Use
   bounded batches, backpressure, jittered backoff and cancellable workers.
   Reconnect hints and Sync now wake a worker without bypassing backoff or starting
   duplicate workers. Authentication, identity or storage errors pause that scope.

Merge non-overlapping fields with the existing three-way algorithm. Concurrent
changes to the same field retain both values and their common base for explicit
resolution; never use wall-clock last-writer-wins. Delete versus pending edit is
a visible conflict, not resurrection. Relationship/workflow commands added later
must validate dependencies and permissions at the home and cannot reuse generic
field patches to cause side effects.

Retain journal history, receipts and tombstones until all active peer cursors and
pending retry windows allow compaction. Set explicit storage/lag limits. A peer
offline beyond retention is marked `needs_reseed`; preserve/quarantine its outbox
for review and bootstrap a fresh replica. Never drop tombstones while silently
allowing an old peer to replay changes. Restoring a home backup creates a new
history epoch requiring explicit reconciliation, not an automatic cursor reset.

## Proposed implementation boundaries

- `server/internal/centersync`: identity, approvals, grants, protocol, PostgreSQL
  replica store, transactional command/apply service and joined worker lifecycle.
- `server/internal/handler`: signed-in pairing/status/control endpoints under
  `/api/workspaces/{id}/center-sync`; authorization and rate limits on each action.
- Dedicated authenticated `/api/center-sync/v1` transport: handshake, snapshot
  pages, changes, proposals and acknowledgements. Discovery exposes only minimal
  public protocol/identity metadata; data routes always require approved peers.
- Proposed tables: `center_sync_identity` (public metadata only),
  `center_sync_pairing`, `center_sync_peer`, `center_sync_grant`,
  `center_sync_workspace`, `center_sync_actor_link`, `center_sync_journal`,
  `center_sync_cursor`, `center_sync_outbox`, `center_sync_receipt`,
  `center_sync_conflict`, `center_sync_snapshot`. All data keys include workspace,
  group and epoch as applicable. No database FKs/cascades; new unique migration
  numbers above the frozen boundary, concurrent indexes in separate migrations,
  and regenerated sqlc. Existing Work Sync migrations remain immutable.
- `packages/core`: parsed API schemas, status queries and mutations. Malformed or
  unknown capabilities must never enable an unsafe action.
- `packages/views`: shared peer list, approval, conflict resolution and status UI.
  Desktop contributes server-address selection; it is not the replication relay.

Persist operational status per peer/workspace: approval state, negotiated scope,
last successful sync, confirmed cursor/lag, queued operations, conflicts, missing
blobs and actionable error. Log operation IDs and decisions without credentials
or payload contents. Bound per-peer disk, bandwidth, snapshot and queue use.

## Desktop interaction and delivery

Target flow: add peer address → Test → Save → **Sync between center servers** →
select workspaces and approve pairing on both servers → ongoing synchronization.
Subsequent clicks reconcile all selected approved peers. Show multiple peer rows,
independent progress/errors, Pause and Disconnect. Disconnect stops grants and
workers; deleting local copies is a separate confirmed action. Closing Desktop
does not stop replication. Address Save never enrolls a peer or implies success.

Until the backend is implemented, the renamed Desktop section stores just the
existing single candidate address using `saveTransfer` / `transferUrl` and
`center-transfer.json`. These are retained address-storage identifiers, not a
new sync API. Test still only checks reachability. The section never calls
`transferData`, requests a recovery token, displays fake progress or claims sync
completion. Existing recovery IPC remains separate and protected; it is not a
compatibility fallback for synchronization.

Implementation order and acceptance gates:

1. Identity, TLS, two-sided approval, revocation and status API; no data export.
   Prove unapproved peers, replayed requests, wrong fingerprints, cross-workspace
   scope, redirected origins and invalid user delegations fail closed.
2. Two-center read-only, owner-only bounded projection using isolated test DBs.
   Prove consistent snapshot pagination, interrupted restart, no unrelated-data
   writes, limits and no replication-triggered runs/notifications.
3. Pending field edits, durable outbox/receipts and conflict review. Test lost
   successful responses, duplicate IDs with changed content, deletes, membership
   revocation during retries, rollback, cursor gaps and clock skew.
4. Three or more centers, worker lifecycle, retention/reseed and real peer-list
   UI. Prove convergence after partitions, no loops, no duplicate execution, and
   continued sync with Desktop closed. Only now enable the Sync button when both
   servers negotiate the implemented capability and approvals are complete.
5. Expand to dependency-complete workspace data and attachments behind new
   capabilities. Each entity needs authorization, conflict, deletion and
   side-effect tests. Planned ownership handoff is a separate release gate.

All integration tests use managed isolated databases, fixture identities and
fake executors, never existing centers or installed agent CLIs. Deployments need
both centers upgraded; a frontend-only rename cannot make older servers sync.
Full deployment backup and replacement remain the separate
[center recovery](center-recovery.md) workflow.
