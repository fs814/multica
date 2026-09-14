P2-D R2 implementation is ready for independent code review. The parent issue
remains in progress; this is not acceptance or authorization to enable production.
Contract: approved design attachment `01a09aab-1a01-7e82-8330-9db3862c078c`.
Branch: `feat/tes-7-workflow-draft-trials` in its separate worktree.

Integration preserves the D checkpoint `014f0c5d5` (D1/D2 `3ea98406d`,
D3/D4 `887c27c7a`) and includes every accepted ABC repair through `95f03b1ea`:

| Accepted ABC commit | D cherry-pick | Behavior |
| --- | --- | --- |
| `7a7520e5e` | `26801c0d2` | Preserve producer contracts during output rename |
| `c102b938e` | `254a82afc` | Require own, explicit upgrade decisions |
| `d25f1bdfb` | `3b8de5557` | Fall back for partial validation diagnostics |
| `0228c4335` | `e76bc04ba` | Fence both rename endpoints, including condition verdict |
| `95f03b1ea` | `b78903567` | Safe input undo/redo and rejected-history regression |

Conflicts occurred only in four locale files. Both the D debug section and the
final ABC output-contract message were retained. Shared template controls and
authoring docs merged automatically. ABC's original worktree and fixed review
commit remain unchanged. Review D's full incremental implementation with
`git diff 95f03b1ea HEAD`; review this integration round with
`git diff 014f0c5d5 HEAD`.

New database and HTTP regressions close the principal prior evidence gaps:

- Both graph schemas execute all seven node kinds from immutable B while the
  template changes. Fresh Engine instances continue the run; v1 rejection reworks
  and accepts, v2 rejection fails explicitly. No issue, instance or callback rows.
- Claim/cancel uses overlapping transactions with a held-row-lock barrier in
  both orders. Expired submit/acceptance races cancel and maintenance. Distinct
  starts compete for user, workspace, hourly and exact remaining storage quotas.
- Invalid graph/input/image and stale revisions leave no materialized rows or
  bytes. Reconciliation recovers a committed terminal task whose callback was
  lost, including duplicate sweeps while new admission is disabled.
- Owner/admin start, member read/accept/cancel, outsider/cross-workspace refusal,
  forged fields, foreign project/image and membership/project/resource/agent
  revocation are exercised through handlers or production claim transactions.
- An in-flight HTTP upload blocks receipts and purge during cancellation. Both
  successful and failed storage returns settle; exclusive artifacts are deleted,
  shared artifacts detached and retained, and late malformed uploads are ignored.
  The final attachment lookup failure now has a dynamic proxy-URL regression.

A long-description visual check exposed unbounded textarea growth in the trial
confirmation. Only this dialog now bounds textareas and scrolls their content.
The Web/Electron regression asserts a viewport-relative height bound, visible
warning and confirmation button, keyboard acknowledgment, and the final
"Execution snapshot" graph heading.

Validation uses isolated `tes7_p2d_verification`, migrated through 516, controlled
routing/storage and deterministic Node execution over real HTTP. Targeted Go race:
54 top-level tests / 131 including subtests, zero skips. Core workflow/realtime:
243 tests / 17 files. Views workflow (including reducer): 309 tests / 18 files.
Core/Views type checks and changed-file lint pass. Web and Electron build with
an isolated local font response adapter (the earlier Google Fonts request failed).
These static checks are separate from the functional test results.
Final real Web 1280×900 / Electron 1440×900: 2/2 pass (36.6 seconds),
including long description, keyboard acknowledgment, snapshot heading, controlled
HTTP execution and expired payloads. Eight final screenshots are included.

Evidence boundaries remain explicit: no full Go suite, paid model, production
cloud storage or external repository checkout, actual mixed-version worker
rollout/deployment, exhaustive process-kill schedule, or exhaustive combinations
of all simultaneous editing/retry/publish commands. The seven-node tests replace
Engine instances, not OS processes. Daemon restart/receipt/checkout-SHA evidence
comes from unit tests; browser tests use a controlled Node child through the
production execution API rather than launching a complete Go daemon. The storage
last-slot test seeds retained byte usage; object-store errors use a controlled
in-memory implementation. Ambiguous crashed uploads conservatively stay pending;
no forced cleanup is introduced. Current environment flags and workers stay OFF
and unchanged. Final logs, screenshots and the detailed design/evidence matrix
are attached to the issue handoff.
