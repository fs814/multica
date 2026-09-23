TES-85 resumed engineering delivery, r4

P0 R1/R2 have a fixed repair candidate with new regression evidence. P0 still requires independent acceptance. P1 advances the authorized permission extraction and published execution wiring, but remains incomplete. apply_allowed=false; the eight real historical instances remain archived. No live service, production database, workflow instance, automation, runtime, or deployment was changed.

Fixed source:

- Integration base: 9d18186e6e8cfa168d6053d33d652e86fadfc12b.
- P0 repair: 361dfb2f2 (code/tests); P0 final: 0ca4c63c0dd02e00c2fdacc9d8c4f480e291f815 (adds recovery documentation).
- P1 final probe: 122a8fd72e1252316aa1939a6ecc44b95212de81, continuing d69546e8437bf2bd7d687661e26b42b5d909f11f and containing the same P0 repair.
- Fixed old application: e44b6a8d70b826ff368390cab62de5bbffb7167e.
- Supplied Windows/amd64 binaries were built with Go 1.26.6. All five supplied executables have matching Git revision metadata and vcs.modified=false. The P1 server health reports the short 122a8fd72 revision; its embedded VCS revision is the full SHA above. These are isolated rehearsal binaries, not releases.

Failure-workspace audit found both quota-failed task directories, no worktree and no output files. This turn's initial HEAD was a8bcbeff2; only runtime AGENTS.md was modified. The earlier scratch integration's untracked work was preserved. Candidates were reconstructed from the prior delivered bundle. No new quota error occurred and no retry loop or extra agent dispatch was started.

P0 repair and evidence:

- R1: index creation provenance now lives on the index itself. A primary-key attachment inherits ownership only when that provenance exists. Equivalent pre-existing unique indexes retain their OIDs and remain unowned; destructive down rejects without catalog/ledger changes. Legacy v1 constraint comments cannot establish index ownership. Unknown invalid indexes are preserved instead of being cleaned up.
- Successful creates and this runner's unique-violation artifacts receive a v2 marker. Marked invalid indexes remain retryable after duplicate data is corrected. A crash before the comment is deliberately conservative: valid unmarked indexes may be reused without ownership, while invalid unmarked indexes require operator inspection. No automatic ownership reconstruction from a name or migration ledger is allowed.
- R2: starts_with replaces SQL LIKE. A literal tes85_p0_ prefix succeeds; five lookalike/missing-prefix databases refuse with catalog and ledger unchanged. Each database was actually created and tested, then dropped.
- Nine top-level PostgreSQL regression tests pass, zero failures/skips, including seven pre-existing-index subcases, uncertain provenance, legacy claims, crash/retry, invalid-index recovery, wrong shapes and protected rollback.
- Real upstream fresh, upstream existing and full-fork backup-restored databases all pass up, repeat up and default full-down refusal. Existing business rows remain unchanged. The fork's two status-constraint changes match the unmodified upstream control; they are not reported as byte-identical constraints across upgrade.
- sqlc generation and repeat generation are stable; generated DB/migrate build and vet pass. Migration matrix used the code-identical 361dfb2f2 repair; final 0ca4c63c0 differs only in README. The final binary was also exercised by the old-application rehearsal.

Fixed old-application validation now goes beyond health:

- Authenticated HTTP created/published an end-node template, started a completed run and read its persisted history before migration.
- After stopping the consumer, applying P0 and restarting the same fixed old version, HTTP read the old run/template, updated its issue and started another completed run.
- Protected down refused. Backup restore into a new database restored the earlier issue state; the fixed old application could read the historical run again.
- Eight SYNTHETIC archived input instances and one paused TEMPLATE binding remain byte-identical in these checks. This fixed source lacks Autopilot instance-binding columns, so this fixture does not claim validation of those later columns, historical instance run snapshots, or the real eight identities. The old binary retains its callback/debug implementation, but those API behaviors were not retested here.
- All server PIDs were stopped/joined and databases dropped. This covers old-version compatibility and backup recovery, not a complete new-service switch/rollback or whole-database immutability. Maintenance and expected business writes are not disguised as zero writes.

P1 authorized progress:

- Applied the two-file shared invocation predicate extraction, preserving handler denial logging, view access and actor-field forwarding. Corrected lookup-error and unattributed-source comments without changing permission semantics.
- Added independent expected permission cases, comparison against the unchanged Autopilot member predicate, membership/target lookup fault injection and transaction visibility checks. Four route strategies observe transaction-scoped revocation; committed revocation is re-evaluated and no tasks are enqueued.
- Wired the published HTTP routes, Engine/Router/Notifier, TaskService terminal observer, Autopilot engine/binding fields, context-managed Reconciler and metrics. Added workflow task fields, prompt forwarding and Codex output schema while retaining upstream provider/process behavior.
- Removed unavailable instance/debug response reads. The published run endpoint explicitly refuses callback/instance/debug configuration. Callback table fixtures were changed to event/replay plus refusal/zero-side-effect assertions; the intake tests remain present and currently cannot compile.

Actual validation and its limits:

| Check | Result |
|---|---|
| go build ./... | PASS |
| Matched server, existing CLI, migrate builds | PASS; supplied binary hashes and clean VCS metadata |
| Full workflow core PostgreSQL suite | 111 PASS / 0 FAIL / 0 SKIP |
| Selected service suite | 33 PASS / 0 FAIL / 0 SKIP; includes engine-to-TaskService terminal execution |
| Final permission/revocation suite | 3 top-level PASS, including 26 matrix cases and four routing strategies; overlaps the selected service suite |
| Daemon workflow prompt/output and agent output-schema tests | 2 daemon + 1 agent PASS; deterministic tests, no real provider execution |
| Focused service/workflow/daemon/agent vet | PASS |
| Real authenticated server HTTP | PASS: six concurrent starts produce one run/issue; conflicting payload 409; callback, instance and draft configuration 400 with no new run/event/issue/task rows |
| Matched CLI synthetic HTTP request | REFUSED by the existing daemon task-context boundary: a human PAT cannot replace the required mat_ identity. Boundary was not bypassed. Binary build succeeds, request acceptance remains open |
| go vet ./... and go test ./... -run '^$' | FAIL at missing WorkflowIntake/WorkflowIntakeResponse in retained handler tests |
| Complete scripts/test-go.sh / make test / race / native hosts | Not completed; handler compilation blocks the suite. No full-suite claim |

The real HTTP fixture uses an end node and proves actual middleware/router/engine persistence, not a real daemon task. Service tests directly exercise the terminal observer with deterministic output. Daemon failure/cancel/restart/lost-terminal end-to-end acceptance, handler actor/logging/middleware regressions and all remaining whole-service gates remain open.

Concrete scope decisions, not applied:

1. server/internal/handler/workflow_intake.go is absent from the 162-entry source list, yet retained P1 tests call it. proposal/NOT-APPLIED-intake.patch extracts the fixed-source endpoint, removes callback queries/engine parameters and explicitly rejects callback destinations. Its authenticated workspace route must be wired in the already-authorized router. git apply --check passes; proposal has not been compiled or applied.
2. server/internal/handler/issue.go is also absent from the 162 entries. Static audit confirms the candidate lacks the workflow-owned status/cancel guard expected by TestWorkflowOwnedIssueStatusRequiresRunCancellation. proposal/NOT-APPLIED-issue-lifecycle.patch contains only the import, single/batch update hooks and guard from fixed source, excluding issue filtering and Issue Pool. git apply --check passes. This is a static missing dependency, not a dynamically executed failing test; compilation currently stops earlier at intake. Review transaction/cancellation semantics before approval.

Please authorize or resolve these precise additions before P1 continuation. Do not remove the retained tests or add excluded schema to make them pass. After scope resolution, rerun full handler/service/daemon/CLI acceptance and independent review. P2/P3 and matching raw-identity diagnostics remain future work.

Two harness mistakes were corrected with failed-attempt snapshots retained: the old fixed source had no autopilot.workflow_input_instance_id, and issue points to workflow_run through workflow_run.issue_id rather than issue.workflow_run_id. Earlier worktree builds omitted VCS stamping; clean standalone Git clones produced the supplied stamped binaries. No failed attempt is counted as success.

The source/evidence ZIP contains the fixed bundle, patches, manifests, actual logs, scripts and unapplied proposals. The separate Windows ZIP contains actual executables and their SHA256 manifest. No remote PR, merge, release, production replacement or native Mac/Linux acceptance occurred. TES-85 stays in_progress; P0 is awaiting independent re-review and P1 remains blocked on precise scope dependencies. Real-eight raw-key reads/CAS recovery and production switching gates are unchanged.
