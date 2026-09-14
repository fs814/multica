# TES-7 P2 A–C implementation and verification

Reviewed P1 base: `aa1610e3f1923534251fad8c56dc20eea0ff2c57`.
Implementation: `6da185bf6`; supporting type correction: `689c2ca89`.
The existing independent branch is `feat/tes-7-workflow-p1`.
P2-D is outside this implementation and remains a separate design/review track.

## Delivery map

| Item | Change | Executed evidence |
| --- | --- | --- |
| P2-A | Additive server diagnostics retain `messages`, with the existing `workflow_invalid_definition` code, structured field paths and identifiable node/edge IDs. Client diagnostics carry structural context; older/malformed diagnostic payloads retain readable messages. Clicking a node diagnostic selects it, opens its properties, and fits the canvas to it. Graph-wide errors remain textual, without invented node identity. | Core authoring/schema tests cover legacy, unknown-code and malformed payloads, required-input and binding pointers. Go tests cover emitted validator pointers, edge IDs, handler envelopes and unchanged publication gates. The page test and real Web/Electron paths click a missing-input diagnostic and verify node selection. |
| P2-B | Type-labelled source-output selectors, disabled incompatible choices, existing bindings and explicit disconnect. Port ID typing remains local; confirmation rewrites only the owned data-edge and condition-predicate references, preserving stable edge IDs. Rename is one undoable reducer action. Join properties explain activated predecessors. | Core reference/collision/v1 guards, reducer rename/undo/delete restoration, component confirmation tests, existing graph round-trip/connect suites. Web/Electron reconnect a typed source, rename the input, Undo/Redo, save, and verify persisted edge and predicate references. Existing v2 Go cases cover default/conditional execution, parallel joins, missing data, retries and independent failure branches. |
| P2-C | Published-version/draft comparisons summarize node, entry, format, limits and binding changes; input fields show additions, removals, type, option, required and label changes. Instance upgrades require a Keep/Remove decision for each unknown key before applying the local edit. | Core comparison tests and component upgrade/save assertions. Real Web/Electron compare published v1 with the working v2 definition, preserve one old value and remove another, apply without changing the server, explicitly save, confirm retained unknown data blocks running, explicitly remove it, save and run. The original run still has its original version and input snapshot. |
| P1 maintenance | R4's two callbacks lacked parameter annotations under strict TypeScript. Add annotations only; the exact-once cache assertion and production invalidation remain intact. | The initial typecheck failure and final typecheck log are included. Both cache files remain in the 158 passing Core tests. |

No engine scheduling rules, permission model, schema-version migration, database schema,
or run modes were changed. Built-in editor documentation records the additive contract
and the distinction between retaining unknown input for review and executable input.

## Executed checks

- `NODE_OPTIONS=--no-experimental-webstorage pnpm --filter @multica/core test workflows realtime/use-realtime-sync-workflows.test.tsx realtime/use-realtime-sync-ws-instance.test.tsx`: 11 files, **158 passed**, no failures/skips.
- `NODE_OPTIONS=--no-experimental-webstorage pnpm --filter @multica/views test workflows`: 18 files, **287 passed**, no failures/skips. Existing jsdom confirm notices are retained in the log.
- `go test -race -v ./internal/workflow ./internal/handler -run 'TestValidationDiagnosticPointers|TestWorkflowTemplateValidate|TestWorkflowPublish|TestGraphV2' -count=1`: **20 top-level tests passed**, no failures/skips, including database-backed tests. Both packages passed. This is a targeted suite, not full Go verification.
- `pnpm typecheck`: 9 tasks passed, 5 cached. `pnpm lint`: 6 tasks passed, 4 cached; no errors, existing warnings remain.
- Production Web build and Electron build passed. Logs retain existing CSS/dynamic-import warnings.
- `WORKFLOW_DESKTOP_E2E=1 WORKFLOW_WEB_BUILD=production pnpm exec playwright test e2e/workflow-authoring.spec.ts e2e/workflow-integration.spec.ts`: **9 passed**, no failures/skips. Includes P2 on both platforms/two viewports, the accepted P1 main paths and controlled v1 lifecycle, native dirty guards, and the performance sample.
- After adding an explicit wait for the comparison selector to close before screenshots, `pnpm exec playwright test e2e/workflow-authoring.spec.ts`: **4 passed**, no failures/skips. These are the final P2 screenshots; production code was unchanged by that test-only adjustment.
- `git diff --check`: no whitespace errors; this is a static check, not a behavior test.

The production 100-node/150-edge sample recorded 40 selections: p95 **19.1 ms**,
first screen **1170 ms**, 1440×900. Machine: Apple M4 Pro, 48 GiB, macOS 26.6.2.
The measurement is click-dispatch to selected-properties visibility at animation
frames; it is not a claim about drag/connect latency, agent execution or other hardware.

The evidence bundle contains 53 screenshots (20 final P2 screenshots plus 33 P1
regression screenshots), the performance JSON, redacted test/build logs, and SHA-256
checksums. Images demonstrate actual Chromium/Electron UI, not generated mockups.
Representative Web binding/comparison and native Electron upgrade/comparison images
were inspected. The comparison entry was moved out of the primary toolbar to keep
the workflow title readable at 1280 pixels.

## Isolation and evidence limits

All new API/Go/E2E work used the disposable `tes7_p2_verification` database, migrated
before execution. API 18126 and Web 13046 served this checkout; Desktop used matching
compiled endpoints and separate test profiles, with local daemon autostart disabled.
The test API/Web process groups were stopped and the disposable database was dropped
after collecting results. Runtime credentials, profiles and API service logs are not
part of the attachment. The original user checkout and its two untracked Markdown
files were preserved.

Independent P2 review remains outstanding. No P2-D trial-run entry, PR/CI, deployment,
paid-agent smoke, Firefox/Safari tests or full accessibility audit was performed.
Screenshots use English; all four existing locale dictionaries received the new copy,
but this is not multilingual visual acceptance. Full Go was not rerun: the unrelated
baseline failures documented and accepted during P1 remain an explicit boundary, not
an implied passing full suite. Parent TES-7 remains in progress for independent review
and subsequent captain decisions.
