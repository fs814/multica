TES-85 r5 revised implementation scope

Base: 122a8fd72e1252316aa1939a6ecc44b95212de81. P0 remains unaccepted.

Intake: authenticated published-template adapter only. Reject machine actors whose provenance cannot be represented, explicit version headers, and excluded top-level callback/instance/debug controls before StartRun. Keep business payload separate, existing request hash and cross-entry key namespace. Check JSON encoding errors. Cross-member assignment retains owner/admin membership checks and additionally gates every routed agent against the persisted issue creator as well as the accountable member, using the existing invocation predicate without changing it.

Issue lifecycle: perform ordinary request validation first. Reuse the engine cancellation state machine in the caller transaction with post-commit effects. Lock issue before run before step in all workflow commands, matching StartRun. Single update must validate CAS/text/attachments before cancellation; batch status commands need deduplicated targets, pure preflight, stable lock order and one commit. Resolve custom status category under the catalog lock. Preserve direct run cancellation's no-projection contract.

Recovery: after committing cancellation, enumerate associated tasks and invoke TaskService. Reconciler must find cancelled runs with still-active linked tasks after query/stop failure or restart. Daemon polling already interrupts a terminal task even if its cancellation broadcast was lost. No direct task-table state writes, process-name kills or stopping unrelated issue tasks.

Expected support differences: workflow commands/engine/reconciler, workflow SQL and regenerated output, workflow router, run handler, server wiring and focused tests. These are already present in the approved P1 file set. Any additional production file dependency will be supplied as an unapplied diff first. No schema addition, excluded callback/debug/instance tables or TaskService/CLI identity changes.

Verification: narrow PostgreSQL regressions, build/vet/test compile, full test entry point where supported, fixed candidate and reproducible evidence. Complete P1 requires actual permitted CLI identity and daemon lifecycle evidence. No production switch; apply_allowed=false, real eight remain archived.
