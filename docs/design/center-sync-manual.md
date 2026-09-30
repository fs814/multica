# Manual sync between center servers

Status: Desktop's **Connect for sync** login and **Disconnect sync server** are
implemented. The manual handler in `server/internal/centersync/manual.go` is
mounted behind normal authentication at `/api/center-sync/{action}`. The Desktop
coordinator and Sync action are implemented. Servers disable this API unless
explicitly configured. There is no sync worker and no automatic run.

The handler's local-only tests cover configuration rejection, wrong-account and
machine-actor rejection before database access, durable isolated replica state,
rejection of execution-field edits, node binding, private-directory permissions,
and sanitized errors. These tests use synthetic records and temporary directories;
they do not establish authenticated end-to-end synchronization or database-backed
workspace authorization. An authenticated database round-trip test is included,
but requires a managed test database and has not run in this environment. The
core and Desktop tests use mocked transports; live two-server validation remains
outstanding.

## Server setup

Rebuild and restart both centers and the Desktop. On each center configure:

```dotenv
MULTICA_CENTER_SYNC_ENABLED=1
MULTICA_CENTER_SYNC_OWNER_ID=<local human account UUID>
MULTICA_CENTER_SYNC_ORIGIN=https://this-center.example.com
MULTICA_CENTER_SYNC_DIR=/absolute/private/center-sync
```

Use that center's own account ID (available from authenticated `/api/me`), not an
email, password or recovery token. The source account must also own the selected
workspace. The two servers may have different local account IDs. Existing account
authentication is still required; no `RECOVERY_TOKEN` is used by this feature.
Do not disable or repurpose authentication on the separate recovery endpoints.

Origins must be HTTPS origins without trailing paths. The state directory must
be private (Unix mode `0700`), durable across restarts, and writable by the server.
The replica store currently requires Unix file-lock support. Existing Work Sync
database migrations must be applied using the repository's migration workflow.
The peer must allow the Desktop origin through its CORS configuration.

In Desktop Settings → Desktop app → Center server, save the peer address, choose
**Connect for sync**, and sign in there. Select the source workspace and press
**Sync between center servers**. There is no second approval dialog. Cancel,
Disconnect, changing servers, and closing settings stop further requests; already
committed progress remains on the receiving server. HTTP connections may be
tested or signed into, but data sync requires HTTPS on both servers.

The receiving center stores an isolated replica, not normal workspace rows.
The result includes a preview of its first 50 records. This does not migrate
users, credentials, attachments, comments, runtime bindings, or execution state,
and does not make a destination workspace automatically appear in its task list.
Safe descriptive edits queued through the replica API are pushed back on the next
manual run; the Desktop preview itself is read-only. Conflict receipts remain in
the replica and are counted separately from successful edits.

Each run lasts at most two minutes, with up to 40 pull batches and 256 pending
edits. Each request has a 30-second timeout; wire payloads and checkpoints are
limited to 32 MiB. Initial snapshots support at most 10,000 records. There is no
automatic retry after a failed or cancelled run. Another click resumes from the
durable cursor and outbox, reusing operation IDs for idempotent retries.

## User requirement

Only the designated user's account may initiate synchronization, from Multica
Desktop, by pressing **Sync between center servers**. No timers, automatic
startup/reconnect synchronization, background server workers or standing peer
credentials. Each click requests one bounded run; completing a run does not
authorize another run. No recovery-token input or recovery API is involved.

Desktop must authenticate separately to each center. Logging in to A does not
authorize access to B. An address, device name, request header or the mere presence
of Desktop is not proof of the user's identity. Ordinary account authentication
still operates internally; “no token” means no manually managed recovery secret.

## Connection and scope

Use the current center's existing login. Add **Connect for sync** beside the
saved peer address to sign in independently to the other center. Once both
sessions are authenticated and have the required permissions, pressing Sync is
the user action that starts the run. Do not add a second approval dialog or a
recovery-token field. Show both origins/accounts and the selected workspace,
direction and supported fields before the button is pressed. Each server still
validates its own session and allowed scope on every request. Neither server's
credential may be sent to the other origin.

The operator must designate which local account may use sync. Source workspace
ownership must also be checked live, including retries. Receiving-center access
must cover the specific isolated replica namespace, not arbitrary database
writes. Ordinary members, agents and daemon credentials cannot initiate sync.
API authentication can restrict access to that person, but proving a physical
click on one particular Desktop requires separate device enrollment/attestation;
a spoofable “Desktop” header cannot provide that guarantee.

First-release scope remains the existing Work Sync projection: allowlisted issue,
project and user-defined agent fields. Exclude credentials, other workspaces,
deployment settings and execution. Show the bounded scope beside the action; do not
label it a complete server copy. Import into an isolated owner-only replica view,
never replace the destination database or silently merge independent workspaces.

## One-run lifecycle

1. User opens sync settings and selects the peer server and source workspace.
2. Desktop uses the existing primary login and the separate Connect for sync
   session. Ask for sign-in only when a session is missing or expired.
3. Both servers validate their own account session and scope for the bounded run.
   Reject missing permissions or a changed destination before transferring content.
4. Desktop coordinates bounded, authenticated requests for that run. Servers do
   not dial each other or retain authority to run autonomously. Credentials from
   one origin are never sent to another. Require verified HTTPS.
5. Use the existing snapshot, cursor, outbox and idempotent receipt algorithms.
   Commit replica data with its cursor. Preserve same-field conflicts for review;
   do not claim a conflict was successfully synchronized.
6. Finish with counts, pending conflicts and the confirmed boundary. Concurrent
   edits beyond that boundary wait for another user-triggered run.

Cancel or closing Desktop stops new requests; a transaction already committed
is not rolled back by cancelling the client. Retain enough durable state to
resume safely with valid sessions on another click. Do not automatically retry
an expired run or replay queued work after restart. Concurrent clicks must not
start overlapping runs against the same replica. Set explicit request, byte,
record and run-duration limits; reaching one preserves progress and requires a
new click rather than silently extending the run indefinitely.

## Implementation acceptance gates

- Deny missing/wrong-account, agent, expired, revoked and cross-workspace access.
- Deny missing destination authentication, changed destination, broadened fields
  and requests outside the current user's permissions or the bounded run.
- Prove there is no sync after startup, reconnect, Desktop closure or completion
  without another click and valid sessions.
- Prove unselected data and credentials never enter the payload, the destination
  is not replaced, and applying a replica never triggers agent runs or schedules.
- Verify cancellation, partial progress, lost successful responses, conflicts,
  duplicate clicks and separate-session credential isolation using fixture-only
  servers and managed isolated test databases.

Address Save/Test alone must never enable data sync. The action requires a
separate peer login and a selected source workspace. Missing server configuration
or owner permissions are reported by the API without falling back to recovery.

## Implemented destination connection

The Desktop component `center-sync-connect.tsx` uses `CenterSyncSession` in core
to call the destination's existing email-code sign-in endpoints, then validates
`/api/me` before showing Connected. It never changes the primary auth store,
active center, daemon connection or workspace. Fetch omits ambient cookies and
rejects redirects; only the destination's own session is used for its identity
check. The peer must permit the Desktop origin through its existing CORS policy
and support email-code login. HTTP addresses display an unencrypted-transport
warning; HTTPS is recommended.

The session is intentionally memory-only while these settings remain mounted.
Disconnect, changing either address, or closing settings aborts pending login
requests and clears credentials. A late response cannot reconnect a discarded
session. This connection checks authentication, not future sync authorization or
backend compatibility. The first explicit Sync click checks both configured
server identities before preparing or copying workspace data.
