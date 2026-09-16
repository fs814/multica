# Project memory activation and rollback review

This is a proposed operator procedure, not a record of production activation. The accepted knowledge bundle remains `prepared_not_published`. TES-103 stays in progress for independent review.

## Version gate

- Build matching server, daemon and CLI from the reviewed source package on base commit `238a52b3645249e35b50ccef74dbdf4f6f37a564`. Record all binary hashes, the source-package hash and platform/architecture. No released Multica version is claimed to contain this uncommitted candidate.
- Validation toolchain: Go 1.26.6; full migrations 1–522 on a dedicated PostgreSQL 17 pgvector fixture. New migrations remain 517–522; this rework adds no schema migration.
- Capability contract: `project-memory-v1`. Upgrade server and each participating owner/worker daemon together before enabling bindings. Old daemons cannot claim bound projects; new daemons reject missing server context.
- Fake providers test ACP and Codex app-server protocol wiring. Their `0.128.0` response is a fixture constant, not an installed provider version or compatibility certification. Before real activation, record exact Hermes/Codex executable versions and hashes and run a separately approved smoke with those builds. Keep Codex native memory off. Other provider native-memory adapters and cross-owner migration are not certified.
- Accept the Windows process-crash-only persistence scope explicitly. If sudden OS-crash/power-loss survival is required, activation waits for a platform-specific storage solution and evidence. POSIX directory sync does not replace storage-device/backup verification.

## Backup and migration

1. Arrange a maintenance window; pause affected project task claims and memory writers, drain active operations, and record outstanding request IDs/revisions. Do not stop active agents merely to simulate a test.
2. Back up PostgreSQL and all bound owner namespaces in the same paused window. Include source `datas/memory/projects`, managed `project-memory`, provider `project-native-memory`, accepted originals, import bundle and migration provenance. Record hashes, owner IDs, profile roots and selected generations. Protect credentials separately; never attach them to an issue.
3. Preserve existing binaries/configuration and create a restore point on a separate test instance. Rehearse database plus owner-disk restoration together before touching the live instance.
4. Apply the normal migration runner, `go run ./cmd/migrate up` from `server`, using the explicitly selected target database. Check migration 522 and every concurrent-index migration result. Do not wrap concurrent index creation in an outer transaction. Install the matched binaries and verify capability/owner identity.
5. Read actual binding state before initializing. For an uninitialized project, use the reviewed owner runtime and bound source (or managed backend). Example: `multica project memory init <project> --owner-runtime <runtime> --expected-revision 0 --expected-binding-revision 1 --source <approval-reference>`. If a binding already exists, use its actual revisions; never reset or guess them.
6. Review the entire `import.json` map: import replaces the logical file set. Publish with `multica project memory import <project> --content-file ./import.json --expected-revision <current> --expected-binding-revision <current-binding> --source <review-reference>`.
7. Read back `resolve`, `list` and every entry. Compare text hashes and file count against the accepted export; record owner/backend, binding revision, content revision, generation, digest and receipt. Preserve all classification doubts and history gaps.
8. Under a separately authorized smoke, test project A/B selection, task completion, daemon restart and owner-unavailable behavior using fixture projects. Re-read the selected real project after normal restart without adding synthetic content. Only then resume affected claims and writers.

## Recovery and rollback

- Lost callback response: inspect receipt and current revisions first. A committed operation may already be visible. Retrying the old expected revision must conflict rather than overwrite a newer write.
- Owner/process interruption: after the 60-second request deadline, an incomplete request is unavailable/failed. No alternate owner or replica is chosen. Check the selected generation, then submit a new operation with current revisions. Orphan candidates remain unselected; automatic GC is not implemented.
- Content rollback: read the retained verified snapshot and import its complete file map as a new content revision against the current binding revision. This preserves a forward audit trail. Never edit the selected digest or rewind database JSON manually.
- Storage rollback: `migrate` keeps the same owner and old generations. Use `--destination <old-source-root>` to return to source storage; omit it for managed storage. Re-read and hash the result. Binding revision advances; old task contexts stay revoked.
- Missing/corrupt selected generation: stop affected writers; restore the matching generation from the coordinated backup and verify digest/identity, or perform a reviewed recovery import into a valid current binding. The system fails reads rather than silently selecting old bytes.
- Binary rollback: pause claims first. Restore the matching database and owner-disk backup together with the previous binaries/config. Retain the failed-upgrade backup for investigation. Do not blindly run migration downs or start legacy daemons against active new bindings: doing so loses the isolation contract and potentially the revision evidence.

## Evidence required before activation

Independent source review, accepted persistence scope, exact provider smoke versions, backup/restore rehearsal, installed binary hashes, migration log, import receipt plus readback hashes, and restart/owner-unavailable checks. These production steps have not been executed by the implementation agent.

## Moving an existing source namespace to datas

Pause the bound owner with its identity verified and preserve its database binding and selected snapshot. Copy the selected `docs/memory/projects/<workspace>/<project>/versions/<generation>/snapshot.json` byte-for-byte to the same namespace under `datas/memory/projects/`; validate its digest against the live binding. Export its logical entries under the generation's `files/` for review. Install a matching owner/CLI, archive the old directory under `.multica/`, and verify API readback with the old path absent. Do not change the binding revision or digest to hide a storage mismatch. Test one reviewed write and readback to exercise candidate promotion, then restart and read back again. Retain old namespaces for recovery until the new layout is verified.
