# Draft trial implementation checkpoint

Base: `fad0a6be82e823d0dd7ea10b971deb7ae4f6468e` (P2-A/B/C independent review baseline).
Branch: `feat/tes-7-workflow-draft-trials`. No changes to the ABC worktree.
Later ABC review fixes must be integrated explicitly before final P2-D acceptance.
Contract: approved P2-D R2 design, attachment `01a09aab-1a01-7e82-8330-9db3862c078c`.

Implemented so far: compatibility schema, independently concurrent indexes, guarded rollback,
workspace deletion manifest, graph source resolver, terminal-before-graph command ordering,
published list isolation, internal trial admission, default-disabled policy and quota checking.
This is an implementation checkpoint, not a completed P2-D deliverable or enablement approval.

Actual checks in an isolated database: migrations through 514 applied; existing targeted engine
regressions and new source/admission/policy tests passed. Compile-only probes are not tests.
The initial DB create helper could not load the optional Node `pg` package; a Go/pgx helper
created the isolated database successfully. No production database or worker was changed.

Still required: deterministic lock-wait/retry race coverage; HTTP endpoints; fixed resource
resolution/revalidation; daemon claim and durable receipt protocol; every payload gate;
recoverable cancellation/deadlines; field cleanup and external object retry; shared UI and
Web/Electron evidence; full acceptance matrix. DebugReady remains false in all application wiring.
