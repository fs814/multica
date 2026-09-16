TES-85 P0 delivery and P1 permission-scope gate — 2026-09-16

Status: partial engineering delivery. TES-85 stays in_progress; apply_allowed=false.
No production service, live database, template, instance, automation, or daemon was changed.
The eight real historical instances remain subject to the archive-preservation policy.
This report does not claim a current read of their raw stored identity keys.

Fixed stages

- Upstream integration base: 9d18186e6e8cfa168d6053d33d652e86fadfc12b.
- Contract source: e44b6a8d70b826ff368390cab62de5bbffb7167e.
- P0 final: 8e24b7f4afbeac7cc79678288582a94b867a35c1 (includes 9f3aa64e8f95b328c8182e0e87e8a713c11bea71).
- P1 extraction probe: d69546e8437bf2bd7d687661e26b42b5d909f11f. NOT a completed P1 or deployable server.
- TES-88 R2 was already redelivered as f79f17ceaae0d269967437cc4c2de0496d8e482e before this work. Its independent review is not repeated here.

P0 changes and verification

The selected workflow schema and published queries were extracted while preserving upstream files. Seven NOT NULL UUID primary keys use separate single-statement concurrent unique-index builds followed by constraint attachment. request_hash is a separate nullable text addition. Callback, instance-snapshot and debug fields are excluded from P0 queries. The three approved autopilot linkage queries are included; their initial omission was corrected in 8e24b7f4, without additional scope authorization.

Existing compatible primary keys and request_hash are preserved without claiming ownership. Precise index shape validation precedes invalid-index cleanup. The runner refuses destructive down before any higher-numbered file can execute. Default rollback retains additive schema. Only an explicitly enabled, empty, dedicated tes85_p0_ database with this batch's object provenance may rehearse the new primary-key/hash down files. Other P0 down files remain refused. Stop consumers before an empty-schema rehearsal; the override is not a deployment rollback procedure.

Validation performed:
- sqlc v1.31.1 generation from the actual candidate migration/query tree; repeated generation byte-stable and Git-normalized generated content unchanged. A Windows checkout line-ending normalization was refreshed without a content change.
- Generated DB package and migrate build; go vet for both packages. Fixed migration binary metadata records vcs.revision=8e24b7f4... and vcs.modified=false.
- Real PostgreSQL 17: complete candidate migration on an upstream fresh database; upgrade of an upstream database containing a synthetic user; and restore/upgrade of a complete fork database containing legacy 232/235/244/273 and its later schema.
- The richer restored fork contains eight SYNTHETIC archived instances, eight completed run snapshots and a paused automation with an older instance revision. These are not the real eight IDs and are not raw-key migration evidence.
- Backup/restore preserved all table row digests, the ledger and constraints. Candidate up preserved every business-row digest; repeated up preserved every row including the ledger. No archival, input, snapshot or binding was rewritten.
- Actual invalid concurrent index failure from duplicate IDs, retry after correction, wrong index definition, incompatible primary key, nullable ID and wrong request_hash type; index-before-ledger and attachment-before-ledger recovery; existing equivalent primary keys; repeated up; owned empty primary-key/hash up/down/up; refusal with data or old ownership. Real DB tests ran, not skipped.
- Protected full down on the restored fork exited 1 before any row, ledger or constraint change. Final fixed-commit repeat up exited 0; protected down again exited 1.
- Old upstream and full-fork server binaries were started only against owned isolated databases, returned HTTP 200 health and were terminated/joined. Their health commit field is "unknown", so this is startup compatibility evidence, not release/version readiness or a complete old-application acceptance suite. Build source and binary digests are recorded separately.

An initially broad "all old constraints unchanged" assertion was false: two issue_status checks changed to the upstream category expansion. A control restored the SAME backup and used the UNMODIFIED upstream migration runner/files. Its resulting constraints exactly match the candidate. Thus both differences come from the pre-existing upstream 478 migration, with zero additional P0 constraint drift; the failed broad assertion and exact differences remain in the evidence. Do not report byte-identical constraints across that upstream upgrade.

P1 progress and remaining gate

The published engine preserves run/event/task transactions, post-commit notifications, pinned-version resolution and request-hash replay checks. Debug/instance/callback core couplings were removed as authorized. The atomicity test now checks the persisted run.started event; invalid-subscriber and missing-Autopilot-receipt transaction rollback assertions remain.

- internal/workflow builds; workflow/metrics go vet passes.
- Real database core suite: 111 top-level tests PASS, 0 FAIL, 0 SKIP, including nested cases. This is the engine suite, not full server/CLI acceptance.
- internal/service still FAILS to build: invokeActorForRun, InvokeActor and AgentInvokePermitted are absent. P1 shared service/handler/daemon wiring and unsupported-callback API rejection are not completed or verified. P2/P3 and matching raw-identity CLI/server work have not started.

Concrete scope decision needed

The source implementation of those permission symbols is server/internal/service/agent_invoke.go; the corresponding source handler delegates through server/internal/handler/agent_access.go. BOTH are absent from the 162-entry source list, and the upstream candidate has no service-level equivalent. The existing upstream handler permission verdict and denial log remain intact. The independent Autopilot member-only admission path must also retain its existing semantics.

permission-proposal/NOT-APPLIED-permission-extraction.patch is a concrete two-file proposal, not applied to either candidate branch. It adds the source shared permission implementation and replaces only the handler's pure verdict body with delegation, preserving the upstream denial log and view-access code. git apply --check passed. scope-proof.json records fixed source blobs and confirms both paths are outside the 162 entries. Please authorize these two files plus focused parity tests before continuing this security-sensitive dependency. No duplicate allow-list implementation, unconditional allow, or service stub was introduced.

Required follow-up validation: owner/private/unknown modes, workspace/member/team targets, membership lookup errors, member versus system/agent originator attribution, cross-workspace loading, unchanged denial logging, transaction-scoped reads, and preservation of Autopilot trigger principal semantics. Then finish P1 shared wiring and run server/CLI and request-hash concurrency/API tests before P2.

Delivery and limits

The bundle contains both fixed branch tips relative to the fixed upstream prerequisite. P0.patch is the final P0 tree delta; P1-probe.patch is explicitly incomplete. Use the matching branch/tree and its evidence; do not apply the P1 probe as a finished feature. Stage scope lists and full migration filename/SHA256 manifests are included.

No remote PR was created: GitHub authentication is unavailable (gh auth status exit 1), and P1 is not buildable. No release, deployment, service replacement, raw live-key read, identity CAS migration, or production rollback occurred. Mac/Linux native acceptance and all production switching gates remain open. Raw dumps and binaries are not attached; only synthetic row digests and metadata are delivered. All nine owned test databases were cleaned up; every cleanup command exited 0, recorded in evidence/cleanup.json.

Post-startup preservation limitation

The final snapshot after old-fork application startup and final repeat/protected-down
is NOT byte-identical across every table: task_usage_hourly_rollup_state and
sys_cron_executions changed. final-preservation.json preserves the failed broad
assertion and both exact before/after digests. No other table, including the eight
synthetic archived instances, their historical runs, automation bindings and the
migration ledger, changed. This is distinct from the successful all-row comparison
immediately before/after candidate migrations and their repeat. Treat the old-app
result as startup compatibility only; do not silently exclude these two tables and
claim complete database immutability or full old-application acceptance.
