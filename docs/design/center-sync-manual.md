# Manual sync between center servers

Status: Desktop's **Connect for sync** login and **Disconnect sync server** are
implemented. The manual handler in `server/internal/centersync/manual.go` is
mounted behind normal authentication at `/api/center-sync/{action}`. The Desktop
coordinator and Sync action are implemented. Servers disable this API unless
explicitly configured. There is no sync worker and no automatic run.

Current Desktop uses the `content_merge: 2` capability to merge into normal
workspace tables. Both backends must support it and have migrations 550–551.
Older replica endpoints remain available for installed older clients; the new
Desktop does not silently fall back to them. Unit tests cover authorization,
field allowlists, conflict detection and cancellation. The two-database test
`TestContentMergeTwoDatabases` exercises normal workspace visibility, account
mapping, agent instructions, attachments, chat history and credential isolation.
It requires two explicitly provisioned managed test databases; never pass live
database URLs. Live user-data synchronization is not part of the test suite.

## Server setup

### Native HTTPS on port 18082

The Go center can serve an additional HTTPS API listener without replacing the
existing HTTP API/Web ports. It uses the same router, sign-in and ownership
checks; enabling TLS does not enable sync or start a transfer.

Add these settings to each center's environment file (or the file selected by
`MULTICA_CENTER_ENV_FILE` for the launcher), then rebuild and restart that center:

```dotenv
MULTICA_CENTER_HTTPS_ADDR=:18082
MULTICA_CENTER_HTTPS_CERT_FILE=/absolute/path/to/fullchain.pem
MULTICA_CENTER_HTTPS_KEY_FILE=/absolute/path/to/privkey.pem
```

Both files are required. Without any of these settings the extra listener stays
disabled. Partial settings, invalid/expired certificates, mismatched keys and
occupied ports fail startup; there is no insecure fallback or automatic port
change. Certificates load at startup, so restart after renewal. Keep the private
key readable only by the server account, outside the checkout and sync directory.
For container deployments, mount the files read-only and explicitly publish
18082; the existing Compose port mappings are not changed automatically.

Use a certificate with a DNS SAN for the hostname, or an IP SAN when connecting
by IP. For example, `https://9.134.118.150:18082` requires that IP in the
certificate SAN. A private-CA certificate also requires the CA to be trusted by
Desktop's Electron/Node and browser transports. Do not turn off certificate
verification. No certificate or CA installation is performed by this feature.

Allow TCP 18082 from the intended clients in the host firewall. Set each center's
`MULTICA_CENTER_SYNC_ORIGIN` to its own exact HTTPS origin including `:18082`.
When TLS is enabled, this origin must match the listener port and certificate
SAN. After binding, the API prints `Center HTTPS (Desktop/API and sync only)`
and its configured `Network: https://HOST:18082` address separately from Next's
HTTP Web banner. This confirms local binding, not remote reachability or trust.
In Desktop, **Source HTTPS connection for sync** can connect separately to the
source's HTTPS origin while the normal **Server address** stays unchanged,
including an HTTP connection on 18080. **Connect for sync** connects to the other
center's HTTPS origin. Both sync connections must use HTTPS. Without a saved
separate source, sync uses the existing primary login and requires it to be
HTTPS. Port 18082 serves the API, not the Next.js web page.

### Native peer certificate trust

For a self-signed source or peer certificate, Desktop's native connection test
and separate sign-in/sync can use `~/.multica/center-certificates.json` (user profile on Windows):

```json
{
  "version": 1,
  "centers": [{
    "origin": "https://192.0.2.10:18082",
    "certificateFile": "/absolute/path/to/peer-fullchain.pem",
    "fingerprint256": "VERIFIED_COLON_SEPARATED_SHA256_FINGERPRINT"
  }]
}
```

Supply exactly one public leaf certificate, not its private key. Verify the
SHA-256 fingerprint directly on the server before adding it. Trust applies only
to the exact HTTPS origin and leaf fingerprint; signatures, hostname and expiry
are still checked and redirects remain rejected. Desktop supports up to 16
entries. Restart Desktop after adding, renewing or removing an entry. A missing
configuration uses normal public trust; malformed configuration fails closed.
This does not install OS/browser/daemon trust or enable sync. The primary
renderer connection still needs its ordinary certificate trust configured.

### Manual sync authorization

Rebuild and restart both centers and the Desktop. On each center configure:

```dotenv
MULTICA_CENTER_SYNC_ENABLED=1
MULTICA_CENTER_SYNC_OWNER_ID=<local human account UUID>
MULTICA_CENTER_SYNC_ORIGIN=https://this-center.example.com
MULTICA_CENTER_SYNC_DIR=/absolute/private/center-sync
```

For `MULTICA_CENTER_SYNC_OWNER_ID`, use that center's own account UUID (available
from authenticated `/api/me`), not an email, password or recovery token. The source account must also own the selected
workspace. The two servers may have different local account IDs. Existing account
authentication is still required; no `RECOVERY_TOKEN` is used by this feature.
Do not disable or repurpose authentication on the separate recovery endpoints.

Alternatively, set `MULTICA_CENTER_SYNC_OWNER_EMAIL` to the intended account's
plain email address instead of setting `MULTICA_CENTER_SYNC_OWNER_ID`. At backend
startup, the center resolves exactly one existing local account by case-insensitive
email. No account is created and no workspace role is changed. Missing/ambiguous
matches or database lookup failures disable sync; sign in to create the account
first, then restart the center. If both email and UUID are configured, they must
identify the same local account. Never copy another center's UUID.

Origins must be HTTPS origins without trailing paths. The state directory must
be private (Unix mode `0700`), durable across restarts, and writable by the server.
The replica store currently requires Unix file-lock support. Existing Work Sync
database migrations must be applied using the repository's migration workflow.
Desktop peer requests use a native, origin-bound transport; no Desktop CORS
allowlist change is needed for that connection. Browser clients still follow
the server's existing CORS policy.

In Desktop Settings → Desktop app → Center server, optionally save the HTTPS
source address under **Source HTTPS connection for sync**, then choose
**Connect source for sync** and sign in. Only the address is saved in
`~/.multica/center-sync-source.json`; credentials stay in memory while settings
are open. Workspace listing uses this source session, not the normal Desktop
session. **Disconnect sync source** cancels the run and drops this login without
disconnecting Desktop or the peer. A configured but disconnected source never
falls back to the normal login. Changing either saved sync address resets sync
sessions; merely saving, testing or signing in never starts a sync run.

Save the peer address, choose **Connect for sync**, and sign in there.
Select the source workspace and press
**Sync between center servers**. There is no second approval dialog. Cancel,
Disconnect, changing servers, and closing settings stop further requests; already
committed progress remains on the receiving server. HTTP connections may be
tested or signed into, but data sync requires HTTPS on both servers.

The Sync button stays disabled if either sync connection uses HTTP and explains
that both centers need valid TLS configuration before connecting at HTTPS addresses.
Merely editing the URL scheme does not configure TLS. The source workspace
picker uses a connection-scoped query, shows loading/empty/error states, and
offers manual refresh. A removed selection cannot start a run.

The default selection is **All owned workspaces on both centers**. An individual
source workspace can also be selected. IDs are preserved: independent workspaces
are not merged merely because their names match. The configured account must own
an existing workspace on either receiving center; a missing workspace is created
and the local configured account becomes its owner. Existing unrelated data stays.
The two configured accounts must have the same email, but may have different UUIDs.

Content includes workspace identity/context, projects, user-defined agent
instructions and model preferences, skills and skill files, squads, issue
statuses/properties/labels, issues and relationships, comments, user-agent chat
history, and locally stored issue/chat attachments. Account profiles and workspace
memberships are mapped by case-insensitive email; existing accounts keep their
local authentication data. The configured owner's onboarding is completed once
the imported workspace commits, so another client can open it normally.

Local-machine runtime identities and agent bindings are included in v2. Both
centers and Desktop must be updated (`runtime_bindings: 1`); older peers fail
before transfer, never silently omit bindings. Wire runtime IDs are derived from
workspace, daemon identity and provider (or custom profile ID), then resolved to
each center's own database IDs. A machine already registered independently on
both centers is reused, not duplicated. Previously imported unbound agents can
acquire their original binding on the next manual sync. Competing bindings remain
conflicts; an empty binding from an older import never detaches the source.

Imported runtimes are private and offline with no copied heartbeat. Existing
local runtime ownership, visibility, liveness and metadata are retained. Custom
profile identities/labels are copied disabled, with an empty command and no fixed
arguments: configure and enable these separately on the receiving center before
registration. Sync never changes an existing profile's command or arguments.
Cloud runtime connections are not portable and are not copied.

Credentials, agent environment/arguments/MCP configuration, provider sessions,
permission settings, task queues and execution state are never copied. Newly
imported agents are private and offline, even when bound. Content is written
directly without invoking task dispatch, notification delivery or schedules.
Credentials embedded manually inside prose or uploaded documents are content,
not detectable credential fields; review such content before syncing.

To keep execution on the original machine, run a second daemon profile **on that
machine**, authenticated to the destination center. The current daemon remains
connected to the original center. Profiles share the machine identity but isolate
center credentials, daemon state and working directories. For example, with a
new, unused profile name:

```sh
multica setup self-host --profile sync-peer --server-url https://9.134.118.150:18082 --app-url http://9.134.118.150:18080
multica daemon status --profile sync-peer
```

This existing setup command performs normal browser sign-in, discovers workspaces
and starts only the selected profile's daemon. The app URL is the web frontend,
not the Desktop/API-only HTTPS port; use HTTP only on a trusted private network,
or configure an HTTPS frontend. The CLI/daemon must trust the destination TLS
certificate independently: Desktop's origin-specific certificate trust does not
configure it. Do not disable TLS verification. Use a fresh profile name to avoid
reconfiguring an existing daemon. An explicit `MULTICA_DAEMON_ID` override, if
used, must match the original daemon. No connection, login, or daemon start is
performed by the sync operation itself. Until this connection is established,
the synced agents have a runtime but it correctly remains offline. Runs submitted
separately to both centers are independent; sync does not deduplicate executions.

For agents without a portable runtime, open the destination **Agents** page, select the
agents showing **Needs a runtime**, and choose **Assign runtime**. Pick a runtime
registered in that destination workspace and confirm. The bulk action only
targets agents owned by the current account that are active and have no runtime;
other selected agents are skipped. It re-reads each agent before updating and
uses the normal runtime permission checks. Model, thinking level and service
tier are reset to the chosen runtime's defaults, as shown in the confirmation.
No credentials are copied and the action does not dispatch runs. If no usable
runtime exists, connect one to that center and workspace first. Requests are
sequential; partial successes are retained and an explicit retry only targets
failed agents. Leaving the panel stops remaining requests, not writes already
accepted by the server. This is separate from the manual sync login: open the
destination workspace through its normal Desktop/Web connection to configure it.

This is a content merge, not a complete database/deployment copy. Workflow and
automation definitions, integration setup, plugin-managed skills, system agents,
execution history, personal preferences, workspace settings/repository bindings,
and S3 storage are outside this version. References to excluded entities fail
closed rather than being silently rewritten. Avatar URLs are preserved, not
downloaded from arbitrary remote hosts. Use separate machine-local configuration
for runtime/integration setup.

Each workspace is applied in a serializable database transaction. Field-level
baselines retain concurrent edits for review rather than overwriting either side.
Physical deletions are not propagated or resurrected; a record deletion conflict
holds the entire workspace unchanged, including its relationships. Unique name/number/ID
collisions return a conflict and roll back the entire workspace transaction.
Review the returned conflicts and edit the corresponding normal records before
another run; no automatic winner is chosen. Immutable attachment files may remain
after a rolled-back database transaction, but no attachment row is published by
that failed transaction. Existing files are never overwritten.

Missing local attachment files no longer abort the run. Export omits their file
bytes and attachment rows and includes their IDs in `unavailable_attachments`.
Desktop shows an explicit **Attachment unavailable** warning with the origin,
workspace and attachment ID, deduplicated across the two exchanges. Warnings
already received remain visible if a later request fails. Missing files are not
counted as copied, and no empty files or broken attachment rows are created on
the receiving center. Existing local metadata, healthy peer files and shared
baselines are preserved; an unavailable marker is never treated as a deletion.
A healthy peer copy can repair a local attachment whose row still exists but
whose bytes are missing. Restored files are eligible again on the next click.
Receiving servers advertise `attachment_warnings: 1`; Desktop refuses to send
unavailable markers to an older server. Unknown fields still fail closed.
Only a missing file inside an accessible upload root is skippable. Missing upload
roots, permission errors, invalid paths, unsupported storage and capacity limits
remain errors. Missing files discovered while reading chunks produce the same
warning and are omitted from that apply, without deleting a healthy peer copy.

Workspace deletion clears content baselines and retains only an empty workspace
tombstone per known peer, preventing a later click from recreating that workspace.
Merge takes the workspace row lock used by the normal deletion flow.

Desktop requires `attachment_chunks: 1` from both centers and exports with
`attachment_mode: "chunked"`. Snapshots carry attachment metadata and SHA-256
digests, never inline file bytes. `merge-file-status`, `merge-file-read` and
`merge-file-write` relay at most 1 MiB decoded bytes per request. All endpoints
retain the normal HTTPS, designated-owner and workspace ownership checks.
There is no 16 MiB aggregate attachment limit on this path; older inline clients
retain that limit. Each local file still has an 8 MiB limit.

Receivers stage bytes privately in `MULTICA_CENTER_SYNC_DIR/attachment-chunks-v1`,
scoped by owner, workspace, peer origin, attachment ID, size and digest. Sequential
offsets and identical duplicate chunks support interruption/retry; conflicting
duplicates and gaps fail. Whole-file SHA-256 verification is required before a
workspace transaction can publish attachment rows. Staging never grants public
access or starts execution. Completed matching local files need no network copy.

Each click lasts at most 15 minutes, with at most 20,000 requests and 1 GiB newly
transferred bytes; individual requests time out after 30 seconds. Private staging
is capped at 1 GiB per center. Feature-owned staging files unused for 24 hours
are reclaimed on explicit chunk requests, not by a background worker. A new
manual click resumes retained staging, including after an API restart. No automatic
retry or synchronization occurs. The progress bar and confirmed chunk/MiB counts
advance after receiving write acknowledgments; staged bytes are not committed
record updates.

A center lists at most 100 owned workspaces. Metadata retains the 10,000-record,
8 MiB content-field and 32 MiB wire limits per workspace. Limits are errors, not
truncated successful copies; chunking does not bypass metadata or per-file limits.
Committed workspaces stay committed if a later workspace fails; reruns compare
durable baselines and do not duplicate records. Both centers and Desktop must be
updated together; missing capabilities fail closed rather than using inline files.

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
must cover the selected workspace and the explicit portable field allowlist, not
arbitrary database writes. Ordinary members, agents and daemon credentials cannot initiate sync.
API authentication can restrict access to that person, but proving a physical
click on one particular Desktop requires separate device enrollment/attestation;
a spoofable “Desktop” header cannot provide that guarantee.

The content merge supersedes the isolated Work Sync projection for the current
Desktop action. Show its supported scope; do not label it a complete server copy.
Never replace the destination database or merge independent workspace identities.

## One-run lifecycle

1. User opens sync settings and selects the peer server and source workspace.
2. Desktop uses the existing primary login and the separate Connect for sync
   session. Ask for sign-in only when a session is missing or expired.
3. Both servers validate their own account session and scope for the bounded run.
   Reject missing permissions or a changed destination before transferring content.
4. Desktop coordinates bounded, authenticated requests for that run. Servers do
   not dial each other or retain authority to run autonomously. Credentials from
   one origin are never sent to another. Require verified HTTPS.
5. Fetch both workspace snapshots before either apply. Merge both ways with
   per-field baselines and transactional writes, then exchange again to acknowledge
   common values. Preserve same-field conflicts; never report them as resolved.
6. Finish with confirmed update counts and conflicts. Concurrent
   edits beyond that boundary wait for another user-triggered run.

Cancel or closing Desktop stops new requests; a transaction already committed
is not rolled back by cancelling the client. Retain enough durable state to
resume safely with valid sessions on another click. Do not automatically retry
an expired run or replay queued work after restart. Concurrent clicks must not
start overlapping runs against the same workspace. Set explicit request, byte,
record and run-duration limits; reaching one preserves progress and requires a
new click rather than silently extending the run indefinitely.

Desktop displays five stages: server identity checks, workspace preparation,
reading both snapshots, merging both ways, and final verification. The bar measures
completed requests across all selected workspaces, not estimated time or bytes.
It never moves backwards when the next workspace begins. Live update counts
advance only after a center confirms a workspace transaction.
Cancellation/failure keeps the last confirmed counts visible; only a successful
run completes the bar. A new click resets the display for that run.

## Implementation acceptance gates

- Deny missing/wrong-account, agent, expired, revoked and cross-workspace access.
- Deny missing destination authentication, changed destination, broadened fields
  and requests outside the current user's permissions or the bounded run.
- Prove there is no sync after startup, reconnect, Desktop closure or completion
  without another click and valid sessions.
- Prove unselected data and credentials never enter the payload, the destination
  is not replaced, and applying content never triggers agent runs or schedules.
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
check. Desktop injects `center-sync-fetch.ts`, which calls a narrow main-process
IPC transport rather than browser fetch. The main process accepts only the main
window's top-level frame, the currently saved peer origin (different from the
source), email-code login, identity checks and allowlisted sync endpoints. It
does not expose a general URL fetch proxy or recovery routes. Certificate checks
remain enabled; neither origin headers nor CORS policies are modified. Login
responses are limited to 64 KiB, sync responses to 32 MiB, and concurrent requests
to four. Address changes, cancellation and main-window reload/closure abort
pending requests. The destination must support email-code login.
HTTP addresses display an unencrypted-transport
warning; HTTPS is recommended.

The session is intentionally memory-only while these settings remain mounted.
Disconnect, changing either address, or closing settings aborts pending login
requests and clears credentials. A late response cannot reconnect a discarded
session. This connection checks authentication, not future sync authorization or
backend compatibility. The first explicit Sync click checks both configured
server identities before preparing or copying workspace data.
