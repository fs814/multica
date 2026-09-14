# Draft trial implementation checkpoint

Base: `fad0a6be82e823d0dd7ea10b971deb7ae4f6468e` (P2-A/B/C review baseline).
Branch: `feat/tes-7-workflow-draft-trials` in its independent worktree.
Contract: approved P2-D R2, attachment `01a09aab-1a01-7e82-8330-9db3862c078c`.
Status: implementation and targeted verification complete as a review candidate;
full acceptance remains pending. The parent issue stays in progress.

D1/D2 add snapshot authority, guarded migrations, defaults, quotas and transactional
admission. D3 adds fixed environments, capability-gated claims, daemon durable
receipts, payload gates, repeated stop recovery, absolute deadlines and two-phase
retention cleanup. D4 adds dedicated APIs, independent caches and shared Web/Desktop
trial controls, results, acceptance, history, settings and expired tombstones.
All wiring defaults OFF; only the isolated verification environment was enabled.

Actual verification: migrations 501–516 applied to isolated PostgreSQL; 42 selected
Go test functions passed with race detection across workflow, handler, service and
daemon packages (no runtime skips), plus the workspace deletion manifest test.
Final Core workflow/realtime suite: 235 tests in 17 files; Views workflow suite:
287 tests in 18 files. Changed-file Core/Views lint and type checks pass. Web and
Electron production builds pass. Web 1280×900 and real Electron 1440×900 tests
pass, including unsaved snapshot, manual acceptance, controlled subprocess HTTP
claim/delivery, receipt replay, fixed empty repositories and expired payload gates.
The final graph-heading correction is a text-only change after these screenshots.

Web build uses a test-only local font response adapter because Google Fonts failed
with ECONNRESET. Workflow UI fonts are the installed matching fontsource assets;
the unrelated landing Instrument Serif fallback uses Source Serif 4. Initial
failed logs are retained separately from passing results.

Evidence boundaries: no full Go suite, live agent model, external repository,
production object store or mixed-version worker rollout. Stop-receipt restart and
actual checkout SHA use daemon unit tests; E2E controls the production HTTP protocol
with a deterministic Node child, not a complete live Go daemon deployment. Cleanup
object failures are injected, not cloud-storage failures. Lost in-flight server
upload state remains conservatively pending; no forced cleanup is provided.
The full 14-group design matrix (including all seven-node combinations, cross-user
permissions and all deadline races) has not been exhaustively executed.

Integration dependency: ABC independent review found B-1 output rename value-flow,
C-1 inherited-property decision bypass and A-1 partial diagnostics mismatch.
Captain requested separate ABC fixes first (comment `01a09dad-2c4a-75bc-9c7e-9220ede401b9`).
This checkpoint preserves the fixed ABC baseline. Integrate those fixes only after
narrow review approval and rerun affected D surfaces. D must not be treated as
accepted or enabled before that dependency and independent D review are resolved.
