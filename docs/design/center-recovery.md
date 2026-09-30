# Center recovery stored on a daemon

For manual workspace replication without recovery-token entry, see the
[manual center sync design and setup](center-sync-manual.md). Recovery below is a separate
backup/replacement workflow, not synchronization between live centers.

Changing the center URL changes the database the client sees. Existing daemons
are executors, not database replicas: their runtime inventory, local issues,
checkout directories and task logs cannot reconstruct all workspace records.
Recovery of an already-lost center requires its database or an earlier backup.

## Recovery contract

Recovery uses a complete PostgreSQL logical backup rather than recreating agents
and issues through create APIs. This preserves UUIDs, workspace memberships,
agents and their credentials, squads and membership, issue identifiers, comments,
history, workflows and relationships. Local uploaded files and the deployment
keys needed to decrypt database secrets travel with the database snapshot.

The center operator explicitly enables daemon exports with a separate recovery
bearer credential, or Desktop export/import by designating a recovery owner
account as described below. Both grant access to the entire center, not just one
workspace. Ordinary workspace membership, agent credentials and task credentials
do not authorize recovery.

The daemon keeps recovery credentials scoped to the exact source origin. A
center switch never sends an old center's credential to the replacement center.
Successful snapshots are stored atomically, encrypted with a machine-local key,
under the daemon profile. Failed requests, empty error responses, incomplete
downloads and changes of center do not replace existing snapshots. An explicit
restore selects the source center and snapshot; connecting to an empty center
never silently imports data or starts recovered tasks.

Restoration requires an empty destination database and a new destination
directory. It preserves the existing destination rather than merging or
overwriting it. Stop the old center before promoting its replacement to avoid
running scheduled work twice. Start the replacement using the restored database,
uploads and deployment keys, then reconnect the daemons. Source snapshots remain
available until the operator deliberately removes them.

## Boundaries

The first implementation covers PostgreSQL and local upload storage. It refuses
to label an S3-backed center a complete local backup. External service state,
git repositories, provider CLI sessions and files on other machines still need
their own backups. Database snapshot consistency does not make concurrent local
filesystem changes transactional; quiesce writes for the final migration copy.
Backups made while the center is active are recovery points, not synchronous
replication. No implementation can recover historical data that was never saved.

Encrypted archives contain sensitive data. Back up the daemon recovery key
separately; losing both that key and the source center makes archives unusable.
Do not publish archives or recovery credentials. Credentials are accepted through
stdin/environment, never command-line values. Windows and macOS use the same
Go archive format and CLI, with PostgreSQL client tools installed separately.

## Operator setup and use

This is an opt-in backup facility for a trusted daemon, not automatic replication
by every workspace member. Install `pg_dump` on the center and `pg_restore` on the
restore machine. Use PostgreSQL clients compatible with the source server (at
least its major version) and restore to the same or a newer supported PostgreSQL
version. For container deployments, include the client tools in the center image.

On the source center, persist these settings in its private deployment environment:

- `MULTICA_RECOVERY_CENTER_ID`: a stable identifier for this database, retained
  when its host/IP changes. Give a genuinely new database a different identifier.
- `MULTICA_RECOVERY_TOKEN`: an independently generated random token of at least
  32 characters. Provision it only to the chosen trusted backup machine.
- `MULTICA_RECOVERY_PG_DUMP`: optional absolute path to `pg_dump`.
- Existing `DATABASE_URL`, `LOCAL_UPLOAD_DIR` and deployment encryption keys must
  describe the live center. A missing upload directory causes capture to fail.

Restart the source center with the new implementation. The export endpoint is
`GET /api/center/recovery/snapshot`; it is disabled unless the recovery token is
configured. This credential is different from a user PAT or provider API key.

Configure the daemon profile, passing the recovery token through stdin. In zsh:

```sh
printf '%s' "$MULTICA_RECOVERY_TOKEN" | multica --profile desktop-services recovery configure --source https://old-center.example
multica --profile desktop-services recovery pull --source https://old-center.example
multica --profile desktop-services recovery list
```

In PowerShell:

```powershell
$env:MULTICA_RECOVERY_TOKEN | multica --profile desktop-services recovery configure --source https://old-center.example
multica --profile desktop-services recovery pull --source https://old-center.example
multica --profile desktop-services recovery list
```

For a trusted private HTTP network, explicitly add `--allow-http` to configure.
The daemon takes a snapshot when its authenticated runtime starts, then hourly.
Configuration is re-read each hour. `pull` verifies setup immediately. Redirects
are never followed, including redirects to another center. Ordinary login tokens
are not reused as recovery credentials.

The profile retains encrypted snapshots under `center-recovery/<source-hash>/`
and its 32-byte key at `center-recovery/recovery.key`. Preserve this directory
when changing the center address. To restore on another machine, securely copy
the archives and key into the selected profile's `center-recovery` directory.
Windows uses the corresponding directory under `%USERPROFILE%/.multica`.
Files have owner-only permissions on POSIX; on Windows protect the profile with
an owner-only directory ACL. The key and source credentials must stay private.
Snapshots are limited to 512 MiB (archive and expanded ZIP entries); exceeding
the limit fails without deleting earlier backups. There is no automatic pruning;
monitor disk space and remove old snapshots deliberately.

For an older center without the endpoint, run the new CLI directly on its host
with the live center environment loaded. This does not require replacing its
running server binary:

```sh
multica --profile recovery-old recovery capture --source https://old-center.example --center-id old-center --uploads /absolute/path/to/uploads
```

For restore, stop writers and prepare a **new empty database using the managed
environment workflow**. Do not start the replacement's API or migrations first.
Set `RECOVERY_DATABASE_URL` privately in the environment, then run:

```sh
multica --profile desktop-services recovery restore --file /path/to/snapshot.mcr --directory /new/restore/directory --confirm-center old-center
```

The command verifies the snapshot and center ID, refuses a nonempty database or
existing output directory, and restores with PostgreSQL's
[`--single-transaction`](https://www.postgresql.org/docs/current/app-pgrestore.html)
option. No `--clean` or drop commands are used. The restored database contains the
source schema migrations, not merely visible API fields. Provision any required
PostgreSQL extensions before the cutover at the server level; do not pre-create
application schema objects in the empty database.

Before starting the replacement, point its `DATABASE_URL` to the restored
database and `LOCAL_UPLOAD_DIR` to `<restore-directory>/uploads`. Apply values in
`deployment-keys.json` to its private environment; do not paste them into logs or
committed files. Keep other deployment settings (public URLs, mail delivery,
external integrations) appropriate for the new machine. Stop the old center
before starting the replacement: restored queued work and schedules can resume
when it starts. Sign in again and reconnect daemons. Provider CLI sessions on
each execution machine are separate from stored agent environment credentials.

## Verification

The unit suite exercises encrypted round trips, simultaneous first-key creation,
corrupt and incomplete downloads, origin-scoped credentials, redirects, missing
uploads, archive path traversal, and authorization failure. The PostgreSQL
integration test is opt-in with `MULTICA_RECOVERY_TEST_SOURCE_URL` (migrated test
schema) and `MULTICA_RECOVERY_TEST_DESTINATION_URL` (empty managed test database).
Optional `MULTICA_RECOVERY_TEST_PG_DUMP` and `MULTICA_RECOVERY_TEST_PG_RESTORE`
select client binaries. It verifies preserved user/workspace/agent/squad/issue/
comment IDs, squad relationships, agent credentials, attachments and refusal to
overwrite the restored database. Never point these test variables at live data.

## Desktop export and import

Settings → Desktop app → Center server has **Export center data** and **Import
and use center data**. Both operate on the active center, not an edited but
unconnected address. The dialogs ask only for the backup password (and password
confirmation on export). Desktop automatically uses the active center's existing
signed-in session; it does not ask for, load, or substitute a recovery token.

Before using these buttons, the center operator sets
`MULTICA_RECOVERY_OWNER_ID` to the UUID of a trusted human account **on that center**
in the private deployment environment and restarts the center. This is an account
identifier, not a new secret. Sign in to Desktop as that account. Being an owner
of a workspace does not automatically grant access to every workspace's data.
Export/import verifies that the configured account still exists and is enabled.
An unset or invalid owner setting denies access; there is no first-user fallback.
Do not configure an account you do not trust with the entire deployment.

Desktop uses these normal-authenticated routes:

- `GET /api/center/recovery/desktop/snapshot`
- `POST /api/center/recovery/desktop/import`
- `GET /api/center/recovery/desktop/import-status?job_id=<accepted-job-id>`

These routes require a valid human login session and the designated owner.
They do not require `MULTICA_RECOVERY_TOKEN`. The operator/daemon routes and their
separate recovery-token policy remain unchanged. Credentials are bound to the
exact connected origin and redirects are rejected. Use HTTPS or a trusted
private network; the backup password only protects the local backup file, not
the transport connection, and is never sent to the server.

Export opens the native save dialog and writes a `.multica-backup` file protected
with a user-selected password (at least 12 characters). This portable format uses
scrypt and AES-256-GCM, authenticating its source metadata along with the snapshot.
It does not depend on the daemon's machine-local key. Keep the password separately;
it cannot be recovered. Cancelled/failed exports do not replace an existing file.
Daemon `.mcr` archives continue to use the CLI restore path described above.

Import opens the native file picker, verifies the password and archive, and asks
for confirmation naming the source and active destination. On success it waits
for the restarted center to become ready, then clears the Desktop session and
its daemon's obsolete token/workspace selection. Sign in to use the restored data.
Import status is bound to the exact initiating session and accepted job for at
most 30 minutes (and never beyond the session's expiry). A private file stores
only the session hash, job ID, owner ID and expiry, never the login credential.
This narrow status permission survives a restored account table; it cannot
authorize another export/import. The destination's JWT secret remains unchanged.
If the importing account no longer exists after restore, the operator must update
`MULTICA_RECOVERY_OWNER_ID` to the intended restored account before further backups.
If status polling expires, check the center's private status file before retrying
an import; loss of polling access does not cancel an already accepted restore.
The UI keeps input on failure, displays progress and does not claim success while
the center is restarting. An older center without these endpoints reports that
it must be updated/configured; no partial API recreation is attempted.

The destination operator enables managed import by setting
`MULTICA_RECOVERY_STATE_DIR` to an **absolute persistent private directory** and
restarting the updated center. Never put it inside an automatically deleted launch
snapshot. Install compatible `pg_restore`/`pg_dump` clients or set
`MULTICA_RECOVERY_PG_RESTORE`/`MULTICA_RECOVERY_PG_DUMP` to their absolute paths.
The PostgreSQL role needs `CREATE DATABASE` permission. This mode supports one
center process with local uploads; Redis, database replicas and S3 are refused.

The launcher-owned supervisor stages the uploaded snapshot encrypted on disk,
drains the API child and waits for its process to exit. It refuses import if
runs or other database client connections remain. It restores into a newly
created database and a new uploads directory; the original database is retained.
An atomic private `active.json` selects the restored database, uploads, center ID
and deployment encryption keys. A new child must pass readiness with a unique boot
identity before Desktop receives completion. A failed candidate boot rolls back
to `previous.json`; failed restoration never changes the active pointer. Source
and destination deployments should run matching schema versions for import.

The center and migration command load the activation before opening database
connections, so subsequent restarts/upgrades keep using the restored database.
CLI local capture also honors activation when the state directory is configured.
The recovery owner, operator recovery token and public URL stay under the destination operator's control.
Source credentials are never sent to another URL; HTTP redirects are refused.

Keep the source center stopped before activation to avoid duplicate scheduled
work. Files in the state directory contain secrets and must remain private.
For manual rollback, stop the managed center, replace `active.json` with its
`previous.json` and restart. Failed candidate databases and staged snapshots are
retained for operator inspection; remove them deliberately after verification.
Do not run multiple supervisors against the same state directory.
