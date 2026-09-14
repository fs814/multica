# TES-7 P1 review fixes and verification

This report supplements the first P1 report. P2 is not included. Review base: `04389a2ef`; original repository base: `1b8244d72`. All implementation remains on `feat/tes-7-workflow-p1` in its independent worktree.

## Finding → fix → evidence

| Review item | Fix commit | Executed evidence |
| --- | --- | --- |
| R1: Desktop toolbar/history bypasses dirty protection | `ffcac7d12` | The supplied failing store test passes. `setHistoryIndex` checks the destination before changing history, covering toolbar, history menu, shortcuts and mouse side buttons. Native Electron tests cancel back/forward, history-menu, keyboard and mouse history attempts and retain graph/unapplied JSON. Clean saved navigation does not prompt. |
| R2: Close other tabs deletes a dirty editor | `ffcac7d12` | The supplied failing store test passes. The batch checks the mounted tab before mutating the group. Native tests create a background tab, cancel tab switching and invoke its actual Close other tabs context-menu item; the editor and raw JSON survive. |
| R3: Web adapter back prompts twice | `ffcac7d12`, `617b9ae05` | The supplied Web regression passes; history protection has one owner. Two additional platform tests cover repeated cancelled native traversal. Real Chromium browser history and application shortcuts confirm exactly once and preserve edits. Production E2E exposed a repeated-forward issue with legacy history traversal after cancellation; the adapter now uses Navigation API traversal and handles both cancellation promises, retaining the router fallback where unavailable. |
| Coarse event / workspace-switch coverage | `fbe75a16d` | Raw `workflow:run_changed` with only `run_id` is bound to the subscription workspace. Tests switch the current workspace before the old event arrives, flush its debounce, destroy the connection with another event pending and reconnect the replacement. Old callbacks/timers cannot invalidate the new workspace. Existing immutable-version, duplicate-event and reconnect tests also pass. |
| Agent execution, cancellation, human acceptance and cross-page state | `2c6db5480` | Web and Electron drive a v1 input → agent → acceptance → end graph through production claim/start/complete HTTP endpoints with deterministic submission output. They cancel one running task, acknowledge cancellation, request rework on another, complete a second attempt and accept it. An independent Chromium page observes running/cancelled/waiting/completed before the 15-second polling fallback; it also disconnects during completion and recovers waiting-for-review on reconnect. The earlier v2 editing/publishing/pinned-history path remains in every run. |
| Minimum visual and keyboard acceptance | `2c6db5480` | Both 1440×900 and 1280×800 on Web and Electron; actual light/dark theme toggle; long raw JSON, a valid long run title and long JSON input; Enter activates Apply, Cancel and Accept; Delete in the JSON textarea preserves its value and the workflow; dirty section switching preserves raw input. Supplemental screenshots scroll the long input into the viewport and check no document-level horizontal overflow. |
| Verification gaps and baseline attribution | This report and evidence logs | Production Web build, final typecheck/lint, targeted publish concurrency and all review regression suites executed. Full Go runner and independent `pkg/agent` phase run on both the original base and fixed server tree, outside the Multica task directory and using separate migrated databases. See exact outcomes below. |

## Results

- Core: 9 files / 134 tests passed. Views workflows: 17 files / 284 passed.
- Desktop navigation/store/tab-content plus reviewer regressions: 4 files / 124 passed.
- Web navigation including reviewer and native-traversal regressions: 1 file / 14 passed.
- Supplied regression evidence was first reproduced unchanged: Desktop 2 failures; Web 1 failure with its original 11 tests still passing. After fixing, all three pass. These are platform/store tests, separately from the real E2E evidence.
- Typecheck: 9 tasks passed. Lint: 6 tasks passed, zero errors; existing warnings remain.
- Production Web build and native Electron build passed. The Web E2E uses `next start` with `NODE_ENV=production`.
- Publish concurrency: `go test -race -v ./internal/handler -run '^TestWorkflowPublish' -count=1`: 3 top-level tests passed, zero skips, in the isolated fixed database.
- Final E2E: `WORKFLOW_DESKTOP_E2E=1 WORKFLOW_WEB_BUILD=production pnpm exec playwright test e2e/workflow-integration.spec.ts`: 5 tests passed in about 1.3 minutes, zero skips, including the final long-input viewport/overflow assertions.
- Performance: 100 nodes / 150 edges, 40 samples, p95 18.8 ms, first screen 1167 ms, production Web build at 1440×900. This is click-dispatch-to-properties-visible sampling at animation frames, not dragging/connecting latency or a cross-device benchmark. Machine: Apple M4 Pro / 48 GiB / macOS 26.6.2.
- `git diff --check` passed; this is whitespace verification, not a behavior test.

## Full Go comparison

Both runs used `bash scripts/test-go.sh --race`, fresh databases, and an environment with inherited `MULTICA_*` variables removed. The independent checkouts sit outside the run-owned directory, avoiding its task-context ancestor marker. `fbe75a16d` was used for the fixed comparison; all later commits change only frontend/tests/docs, so the final server tree is identical.

Both full runs failed; neither `make test` nor the entire repository suite is reported as passing. Both CLI packages (`server/cmd/multica`) passed. This narrows the prior run's CLI failures to the inherited runtime context: no CLI code was changed, and the same CLI suite passes outside that context on both revisions.

The fixed full run fails five groups:

1. `TestUpdateChatSession_UpdatesModel` — eight 404 update cases; also fails in the base full run.
2. `TestClaimTasksByRuntime_ClaimPollHintSchedulesNextDeferredTask` — delay exceeds the 5,000 ms bound. It passed in this base full run, so a paired targeted timing replay was run with `-race -v -count=3`; all three iterations fail on both revisions (about 5,013–5,014 ms). Both logs are included.
3. `TestReportTaskMessagesFallsBackWholeBatchForClockSkew` — timestamp-spacing assertion; also fails on base.
4. `TestMigrationNumericPrefixesStayUniqueAfterLegacySet` — duplicate existing migration prefix; also fails on base.
5. `TestBuiltinSkillsConformToTemplate/multica-workflows` — existing allowed-tools mismatch; also fails on base.

The base full run additionally has heartbeat/sweeper and WeCom timing failures that do not occur in the fixed full run. They are retained in the comparison logs rather than silently removed. No unrelated baseline implementation was changed.

The full runner stops before its separate agent phase when regular packages fail. Therefore `go test -race -p 2 -parallel 2 ./pkg/agent/...` was invoked separately through `scripts/go-test-with-agent-cli-guard.sh` on each revision. Both passed (base 265.386 s; fixed 264.277 s), rather than being treated as implicitly covered by the failed full runner. The guard prevents default tests from resolving user-installed agent CLIs; no paid-agent smoke test was enabled.

The evidence archive includes the failure reproduction logs, final targeted logs, and base/fixed full-run and agent-phase logs. Authentication-like values and connection strings are redacted in shared logs. Environment values, profiles and runtime authentication files are excluded.

## Reproduction and limits

Generate `.env.worktree`, provision and migrate its isolated database, start API with its variables, build Web and run `NODE_ENV=production next start` on the configured frontend port. Build Desktop with `VITE_API_URL`, `VITE_WS_URL` and `VITE_APP_URL` matching that environment before enabling native tests. The native test launcher is currently macOS-specific. TestApiClient cleanup handles its ordinary issue fixtures; dropping the disposable database reclaims all workflow/runtime fixtures after the complete suite.

This run uses API 18126, Web 13046, application database `multica_multica_p1_46`, and separate comparison databases `tes7_rework_base` / `tes7_rework_fixed`. Both comparison worktrees, all three disposable databases and the API/Web test processes were removed or stopped after collecting results; the implementation worktree and review branch remain available. The original checkout and its two pre-existing untracked files remain untouched.

Remaining limits: independent re-review by the captain's reviewer; no PR/CI result or deployment; no paid-agent smoke; Firefox/Safari native history is not validated. Chromium/Electron is the verified history-protection boundary; other browsers retain adapter/unload fallback behavior, without a claim of native-history parity. This is minimum agreed keyboard/long-text verification, not a comprehensive accessibility audit. P2 awaits re-review.
