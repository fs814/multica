# Operations

- id: multica-verification-entrypoints
- topic: Verification and recovery navigation
- status: verified
- verified_at: 2026-09-14T19:38:38-07:00
- verified_commit: 238a52b3645249e35b50ccef74dbdf4f6f37a564
- owner_role: Implementation engineer
- sources: [Makefile](../../../../../../Makefile), [package scripts](../../../../../../package.json), [repository verification rules](../../../../../../CLAUDE.md), [issue-pool runbook](../../../../../operations/issue-pool-rollout.md)
- conclusion: The following verification entry points and runbook exist in the
  inspected revision. Commands were inspected, not executed during memory setup;
  this entry does not certify a working environment or recovery rehearsal.

## Choose the check for the change

| Entry point | Scope and prerequisites |
| --- | --- |
| `git diff --check` | Whitespace in tracked changes; also inspect new untracked documentation |
| `pnpm typecheck` | Root TypeScript pipeline; root script excludes mobile |
| `pnpm test` | Root Turborepo test pipeline; root script excludes mobile |
| `make test` | Ensures the configured PostgreSQL environment, applies migrations, then runs Go race tests |
| `make check` | Repository verification pipeline; inspect target and prerequisites before execution |

The root package scripts require Node >=22 and specify pnpm 10.28.2 at the checked
revision. `make test` invokes Bash scripts and database setup; this is not a claim
that a bare Windows PowerShell environment can run the entire pipeline. Follow
the source scripts for the target platform and [mobile rules](../../../../../../apps/mobile/CLAUDE.md)
for mobile validation. Do not treat a command listed here as permission to mutate
a database or restart a live service outside the current task's scope.

## Existing runbooks

Read [issue-pool rollout and recovery](../../../../../operations/issue-pool-rollout.md) for its
preconditions, accounting queries, reconciliation and rollback procedure. That
runbook remains the source of operational instructions; no recovery was executed
as part of this documentation change. Consult [self-hosting](../../../../../../SELF_HOSTING.md)
for environment setup. Add an operating lesson here only after recording the
platform, version, reproduction, actual check result and appropriate sources.

## Checking a memory-only change

Check the root entry link, every new relative link and any fragment, UTF-8 decoding,
entry metadata, pending/verified distinctions and the diff's scope. Inspect source
citations at the recorded commit. For a published status snapshot, also check row
uniqueness, membership, revisions and derived counts against PM and live data.
For a replacement decision, check reciprocal links and the index's active target.
Report exactly which checks ran; a document inspection is not a product test pass.
