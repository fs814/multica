# Workflow P1 verification — TES-7

Date: 2026-09-13. Branch: `feat/tes-7-workflow-p1`. Base: `1b8244d72ee764346695604f0cedd5f696e8a18c`. P2 awaits the captain's independent review handoff.

## Delivered behavior

| Checklist | Implementation and evidence |
| --- | --- |
| P1-A | Publish carries confirmed revision and draft identity. Template locking, comparison, draft selection, validation and publication share one transaction and the save lock order. Empty legacy bodies remain compatible and atomic, but cannot guarantee the user's reviewed revision. Save-then-publish uses the save response; background refresh cannot substitute a new revision for an old displayed graph. Concurrency tests cover two publishers, interleaved invalid saves and guarded/legacy calls. SQL was regenerated with `make sqlc`. |
| P1-B | Sections, instance filters and offsets live in NavigationAdapter URLs. Detail breadcrumbs retain allowed same-workspace list locations. Graph and unapplied JSON changes use shared leave protection. Web content links and cancelable Chromium history traversal use the guard; existing Desktop switch/close guards remain. Embedded lists omit duplicate headers and canvas tools. |
| P1-C | Events and reconnect invalidate run and mutable instance caches, scoped to the event workspace. Run/cancel/acceptance mutations refresh both. Immutable versions use a separate query key. Events cause refetch rather than client-side status transitions. |
| P1-D | Run details fetch the pinned graph, display confirmed latest node-attempt states and complete actual inputs, and filter by node without dropping previous attempts. Version failures preserve the linear trace. No inferred execution-edge highlighting. |
| P1-E | Server status filters and 30-row pagination retain total and URL state without changing array-query consumers. Version labels, collapsible properties/run graph and four locales complete the shared views. |

## Executed checks

| Check | Actual result |
| --- | --- |
| `pnpm --filter @multica/core test workflows realtime/use-realtime-sync-workflows.test.tsx` | 9 files, 133 tests passed |
| `pnpm --filter @multica/views test workflows` | 17 files, 284 tests passed |
| Web `platform/navigation.test.tsx` | 11 tests passed |
| Desktop navigation, tab-store and tab-content tests | 3 files, 122 tests passed |
| `pnpm typecheck` | 9 tasks passed |
| `pnpm lint` | 6 tasks passed, zero errors; existing warnings remain |
| `go test -race -v ./internal/handler ./internal/workflow -run 'TestWorkflow\|TestGraphV2' -count=1` (server directory) | 53 handler and 15 selected engine top-level tests passed, zero skips |
| `go test -race -v ./internal/workflow -count=1` | 113 engine top-level tests passed, zero skips; includes the preceding 15 |
| Desktop `electron-vite build` | Passed; existing bundle warnings remain |
| `WORKFLOW_DESKTOP_E2E=1 pnpm exec playwright test e2e/workflow-integration.spec.ts` | 5 tests passed, zero skips, final run 49.0 seconds |
| `git diff --check` | Passed; whitespace verification only |

Component tests are not browser tests. Static checks and successful builds do not establish runtime behavior.

## Web, native Desktop and visual evidence

Playwright uses actual Web routes at 1440×900 and 1280×800, then the built Electron application at both sizes. It edits/applies/saves/publishes a graph, dismisses a real sidebar leave prompt while dirty, creates an instance, runs saved APAC inputs, publishes a newer graph while the historical run still shows the original, runs temporary EMEA inputs without saving, and reruns the original APAC snapshot. The Web 1440 case creates 34 runs and verifies 30+4 pagination, status filtering resetting the offset and detail-to-list filter restoration.

The evidence archive contains editor, light-run and dark-run PNGs for all four platform/size combinations, plus the 100-node canvas and measurement JSON. Dark screenshots use the actual application theme toggle. These are observed screenshots, not an exhaustive accessibility/layout audit.

Performance: 100 nodes, 150 edges, 40 selection samples, p95 48.9 ms, first screen 1,366 ms. Machine: Apple M4 Pro, 48 GiB, macOS 26.6.2. This is a Next.js development build at 1440×900. Samples measure click dispatch until selected properties are visible at an animation frame; this is not a production or cross-device benchmark.

## Full-suite failures and skips

`make setup-worktree` failed its Docker Compose step because this host lacks the `docker compose` plugin. An existing PostgreSQL service was used to create and migrate the separate worktree database. The underlying full runner, `bash scripts/test-go.sh --race`, was executed and FAILED. Neither `make test` nor the complete Go suite is reported as passing.

Five failing groups were reproduced on the untouched base commit in a separate detached checkout:

- `TestUpdateChatSession_UpdatesModel`: eight update cases return 404.
- `TestClaimTasksByRuntime_ClaimPollHintSchedulesNextDeferredTask`: observed delay 5,045 ms exceeds the 5,000 ms bound.
- `TestReportTaskMessagesFallsBackWholeBatchForClockSkew`: timestamp-spacing assertion fails.
- `TestMigrationNumericPrefixesStayUniqueAfterLegacySet`: existing duplicate `284_chat_session_model` prefix.
- `TestBuiltinSkillsConformToTemplate/multica-workflows`: existing `allowed-tools` mismatch; this change does not alter that field.

The full run also has CLI failures associated with Multica task context discovered from the runtime ancestor directory, even after clearing inherited `MULTICA_*` environment variables. A representative `TestResolveWorkspaceID_AgentContextSkipsConfig` passes in the standalone base checkout. The entire CLI suite was not rerun there; other CLI failures are not individually classified. The runner aborts before its separate `pkg/agent` phase, so that phase was not executed. The targeted workflow handler/engine runs had zero skips and connected to the isolated database.

## Isolation, reproduction and remaining validation

Implementation used an independent git worktree. The user's checkout remains at the base commit with its two original untracked search-result Markdown files preserved. API 18126, Web 13046 and database `multica_multica_p1_46` were isolated. Native tests use a private Electron home/profile, disable daemon autostart and strip inherited runtime/renderer overrides. Evidence excludes profiles, tokens and authentication logs.

For reproduction, generate `.env.worktree` with `make worktree-env`, provision only its database, migrate it and start API/Web with those variables. Build Desktop with `VITE_API_URL`, `VITE_WS_URL` and `VITE_APP_URL` matching the worktree. The native launcher currently targets macOS Electron. Without `WORKFLOW_DESKTOP_E2E=1`, the two native cases intentionally skip; it was enabled for this report.

Remaining validation: independent review; real-agent execution/cancellation/human acceptance browser scenarios; Firefox/Safari traversal; production Web build/CI; exhaustive long-title/long-JSON, keyboard and accessibility checks. Cancellation, acceptance, retries and graph mutation have component/engine regression coverage; that does not establish unexecuted browser scenarios. No paid agents, external workflow side effects, deployment, merge or P2 implementation were performed.

After verification, the temporary API/Web processes were stopped and the isolated database and clean baseline checkout were removed. The implementation worktree and review branch remain available.
