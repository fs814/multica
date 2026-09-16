# Persistent project memory

Implementation candidate for TES-103. The installed server and daemon have not been upgraded by this change.

## Authority and storage

The key is (workspace UUID, project UUID). PostgreSQL stores the binding, owner daemon, binding revision, content revision, selected generation and SHA-256. It also stores task context epochs and a durable operation queue. The authoritative bytes are an immutable snapshot on that owner:

- Source binding: `<bound source>/datas/memory/projects/<workspace>/<project>/versions/<generation>/snapshot.json`.
- Managed binding: `<daemon profile>/project-memory/<workspace>/<project>/versions/<generation>/snapshot.json`.

A source worktree is never chosen as the authority. Sharing a source directory still creates independent UUID namespaces. GitHub-only projects use managed storage. With no existing binding, the first claim picks the registered local-directory owner, or its own daemon for managed storage. Human `init --owner-runtime` can establish it before a task.

The snapshot is one JSON document with a logical filename-to-text map. This differs from directly editing individual Markdown files: it allows content and index to publish together. External edits fail digest verification. Snapshot files are synced before the owner reports them; POSIX also syncs each newly created directory up through its pre-existing parent. Windows currently provides process-crash persistence only. Only a server transaction can publish the pointer. A crash or rejected stale write can leave an unreferenced candidate; it cannot select that candidate.

Source candidates are staged under `.multica/project-memory-candidates/<workspace>/<project>/versions/<generation>`. The first read of a server-selected binding verifies and atomically promotes that generation into `datas/memory/projects/`; a restarted owner can finish this promotion. Unselected candidates stay ignored. Each promoted generation includes a readable `files/` export alongside its authoritative `snapshot.json`. Edit through the Memory API, then read back before committing final data.

Queue results are transient transport copies, not another authority. There is no automatic retention policy yet.

## Access and runtime

Project routes require workspace membership. A task credential additionally requires its server-recorded workspace/project/scope/epoch/binding revision and an active task. Client environment variables and shared resources.json do not grant access. Issue and chat project changes increment an epoch, including A -> B -> A and unset. Immutable jobs use a task-specific scope. Binding migration changes its revision and revokes old contexts.

Owner queue endpoints additionally require the authenticated daemon ID and workspace to match the selected runtime. PAT/JWT compatibility requires that runtime's human owner as well as workspace access. A different workspace member cannot process owner work. Cloud identities without that owner binding are denied. The owner remains trusted to report its own on-disk bytes honestly.

The upgraded daemon advertises `project-memory-v1`, fetches the current snapshot using the task token, and writes context.json and snapshot.json into the private execution root. The child receives `MULTICA_PROJECT_MEMORY_CONTEXT`, `MULTICA_PROJECT_MEMORY_SNAPSHOT`, and `MULTICA_PROJECT_ID`. Reads/writes use the CLI; the private snapshot is a point-in-time reference, not a writable authority.

Old provider sessions and workdirs are conservatively withheld for capability-aware claims. Project-scoped execution-environment reuse is disabled until transcript adapters are independently verified. This is intentionally broader than clearing only on a project switch and loses conversational continuity.

Hermes, including agents with no skills, uses a private overlay and persistent memory scoped by workspace/project/agent/profile. A cross-process lock serializes native whole-file writers through provider shutdown. Session GC reserves conversation leaves in both legacy and project namespaces, matching the active-store guard; it only removes empty project containers non-recursively. Codex keeps native memory disabled; the existing explicit MULTICA_CODEX_MEMORY opt-in fails closed in project tasks. Other provider native memories have not been certified for project isolation; the controlled project knowledge API is independent of those stores. No claim of OS filesystem isolation is made: a same-user shell can read sibling files.

## Operations

```text
multica project memory resolve <project> --output json
multica project memory init <project> --owner-runtime <runtime> --expected-revision 0 --expected-binding-revision 1 --source <reference>
multica project memory list <project> --output json
multica project memory read <project> --path README.md
multica project memory write <project> --path README.md --content-file ./entry.md --source <reference> --expected-revision <n> --expected-binding-revision <b>
multica project memory import <project> --content-file ./import.json --source <reference> --expected-revision <n> --expected-binding-revision <b>
multica project memory delete <project> --path old.md --source <reference> --expected-revision <n> --expected-binding-revision <b>
multica project memory migrate <project> --destination <source-root> --source <reference> --expected-revision <n> --expected-binding-revision <b>
```

Import accepts a JSON object mapping relative filenames to UTF-8 content and publishes the entire set atomically. It is a replacement: review the full file list first. Writes support at most 1,024 entries and 4 MiB of text. Paths reject traversal, absolute names, Windows reserved names and case collisions. Storage rejects existing symlinks and junctions. This is not a defense against an arbitrary same-user adversary replacing the storage ancestry concurrently.

Migrate is human-only and keeps the owner daemon. Omit destination to return to managed storage. The old generation is retained. To roll back content, read/export the retained snapshot and import it under the current binding revision; do not change a pointer file by hand. Cross-owner transfer and automatic garbage collection are not implemented.

Owner processing runs with daemon heartbeats and task initialization. A request expires after 60 seconds; there is no fallback owner. A processing crash requires a new request after checking the current revision. CLI/runtime waits are bounded. Database and owner disk must both be backed up.

## Routes and rollout

- GET /api/projects/{id}/memory
- POST /api/projects/{id}/memory/operations
- GET /api/projects/{id}/memory/operations/{requestId}
- GET /api/daemon/runtimes/{runtimeId}/memory/next
- POST /api/daemon/runtimes/{runtimeId}/memory/{requestId}/result

Apply migrations 517–522, install matching server and daemon/CLI binaries, then initialize and import the reviewed bundle. Do not restart an active production daemon merely to test this feature. Once a binding exists, the server refuses claims from daemons missing the capability. An upgraded daemon refuses project tasks without the server-provided context. Projects used only by legacy daemons are not automatically activated; mixed-version rollout is not accepted as project-isolated operation.

Import accepted knowledge through the API and verify the selected snapshot before committing it. Keep handoff bundles, provenance working copies and retired namespaces under ignored `.multica/`; commit the published snapshot and readable exports under `datas/memory/`. Historical missing evidence and unresolved classifications remain explicit.

## Validation

Use PROJECT_MEMORY_TEST_DATABASE_URL pointing at an explicitly dedicated PostgreSQL fixture database. Set DATABASE_URL to the same URL for handler/server integration tests. Coordinator tests create random schemas; product-router tests use the full schema after the normal migration runner. All task/project/source/provider fixtures are temporary.

Coverage includes shared/independent source roots, managed roots, owner and workspace mismatch, persisted bindings, immutable candidates, digest tampering, path rejection, CAS races, A-B-A and unset epochs, migration, expiration, atomic evidence import, task completion, private Hermes overlay, persistent native memory and session-key scoping. See the delivery validation report for exact executed commands and results. No real project is a test fixture.

The rework tests execute NewRouter, authenticated claims, a daemon test process, the real preparation helper, fake ACP/Codex provider processes and cleanup. They check Issue/Chat switching and unsetting, skills, Hermes profile selectors and derived .env, native lock lifetime, persistent A/B markers, and Codex memory config/opt-in rejection. These are protocol fixtures, not certification of installed provider releases.

Real PostgreSQL concurrency tests cover both task-finish/publication orders. Abrupt process-exit tests cover staging before callback, expired callbacks, retry, fresh-process reads, forward/reverse migration, explicit content rollback and missing-file restoration. A real temporary Git worktree test keeps authority at its bound source.

## Recovery guarantee

A process interruption does not publish an incomplete generation: files close/sync before the callback, and PostgreSQL atomically commits the pointer and receipt. If the callback response is lost, read the receipt/current revisions before retrying. Expired processing work is not reassigned; submit a new operation against the current revisions. A missing or corrupted selected generation returns an error, never an automatic fallback to older bytes.

POSIX directory sync is implemented and errors prevent a successful candidate result; the Linux fixture exercises it. This is not a power-cut or storage-device certification. Windows lacks a portable directory fsync contract here, so sudden OS crash/power loss is outside the current guarantee, even if PostgreSQL retained its pointer. Independent acceptance must explicitly accept this narrower Windows scope, or require additional platform storage work before activation. Use coordinated database/owner-disk backups for recovery. Cross-owner transfer remains unsupported.

See [activation and rollback](project-memory-activation.md) for rollout and existing-storage migration.
