# Migration compatibility contract

The TES-26 maintainer authorized an immutable historical generation on
2026-09-26. This accepts historical DDL debt; it does not claim that the old
files satisfy today's authoring rules or rewrite what deployed databases ran.
The feature remains disabled by default.

## Frozen artifacts and new migrations

`internal/migrations/testdata/frozen-migrations.json` pins all 1,334 up/down SQL
files (667 complete stems) at commit
`e641ee11836fff0ee479d855237c259e059ac3fc`, through prefix 541. Its own SHA-256 is
pinned in `migrations_freeze_test.go`. The manifest records all 53 changes from
the previous collision list, including the three-member 284 group. It also
names `507_comment_agent_delivery.up.sql` as accepted implicit-index debt.

Do not rename, edit, delete, reorder, or regenerate these historical artifacts
from a candidate HEAD. A policy change requires explicit review of a new
baseline and its upgrade evidence. The manifest is a distribution integrity
check, not a claim about SQL previously executed on an unknown installation.
The runner still identifies migrations by complete stem and sorts filenames.

New files must have a canonical numeric prefix greater than 541, unique across
new stems; allocate against the current integration branch. They need both
directions. New up and down SQL is checked for implicit primary/unique indexes,
non-concurrent index creation, and concurrent index builds mixed with other
statements. Existing post-466 implicit-index checks also remain, with only the
hash-frozen 507 exception. The pre-466 historical boundary is unchanged. Negative
controls mutate, delete and rename every member of all 53 groups in both
directions, mutate 507, append collisions and numeric aliases, and introduce
invalid new DDL. A hash failure is never an excuse to update the expected hash.

## Runtime safeguards

507 still creates its original primary key for a new installation. An already
valid receipt table and primary key are retained, including their data and
index OID. Before any pending up DDL, under the migration advisory lock, a run
containing 507 checks agreement between its ledger and table. Recorded tables
must retain the expected columns/types/nullability, timestamp defaults, valid
primary index, and validated status constraint. Drift or DDL committed without
a ledger entry stops with a recovery diagnostic; the runner does not rebuild,
drop, or silently mark the table applied. Recover from verified schema/data
and ledger evidence under a separate recovery decision.

Migration lock waiters use session-pinned `pg_try_advisory_lock` attempts. Each
failed attempt finishes before waiting 25 ms, with context cancellation honored.
A blocking `pg_advisory_lock` statement can hold a transaction that a concurrent
index build waits for while the builder owns the same advisory lock. The real
full-history concurrency test reproduced that deadlock before this change.
Unlock uses a bounded independent context, so caller cancellation does not
leave a pooled session holding the lock. DDL and ledger writes remain separate;
no global transaction is added around concurrent indexes.

## Verified scope

The recorded local environment is PostgreSQL 17.10/aarch64, pgcrypto 1.3,
pg_trgm 1.6, without pg_bigm. Tests use explicit `DATABASE_URL` and disposable
`multica_migration_test_*` databases on that isolated test server. The matrix
requires CREATEDB; these are dropped during cleanup. Never point it at a
production server. Database isolation is necessary because historical scripts
own public functions and search-path-visible indexes; a scratch schema over an
already migrated public schema is not an empty installation.

| Scenario | Evidence / boundary |
| --- | --- |
| Empty database, all 001–541 SQL, rerun | 667 exact ledger identities, unchanged timestamps on rerun, 2,847 normalized column/constraint/index/trigger/function definitions |
| Complete sets at `18f60063b` (through 532), `19987e327` (through 540), `77c01418a` and `e641ee118` (through 541) | Git file lists and every SHA-256 match the frozen subset; real SQL builds each fixture; upgrade and rerun converge to the empty-install schema; receipt data and PK OID retained |
| All 53 collision-set changes | Real dependency closure from 001; each group's lexicographic prefix states and complete state rerun with unchanged existing ledger entries; 284 includes both partial cuts and all three stems |
| 507 before/after | Original SQL, receipt duplicate rejection, NOT NULL/status enforcement, 508 valid pending index and pending query |
| Missing table, missing ledger, wrong PK/type/check/default | Explicit rejection without ledger mutation; no automated repair |
| Actual 507 ledger INSERT failure after DDL | Injected trigger failure leaves no success record; retry detects mismatch and stops |
| Concurrent indexes interrupted/invalid/wrong definition | Existing real project-memory, workflow-debug, issue-pool and other index retry/definition tests; no invalid index treated as usable |
| Two migrators, cancellation | Full distribution runs concurrently; existing 16-runner serialization/stress tests; lock waiter cancellation followed by bounded retry |
| Optional condition recorded then availability changes | No replay of an already recorded conditional migration; pg_bigm-absent branch tested |
| Historical issue-pool 1–283 and legacy 449–469 fixtures | Existing real migration compatibility tests retained; these specific fixtures are not a promise for every fork |
| 541 down / re-up | Existing sync round-trip/capture test preserves journal through capture rollback and verifies replay; perform only with sync writes disabled |
| Readiness and build | Every complete required stem present, full package readiness tests, sqlc compile, unchanged SQL distribution |

Not verified or supported by this evidence: PostgreSQL versions other than
17.10, pg_bigm-present execution, arbitrary out-of-order single-side branch
ledgers, unknown deployed binaries or SQL variants, application binary rollback,
full database downgrade, crash/disconnect at every possible historical DDL
statement, real synchronization or disaster recovery activation. In particular,
maximum numeric version alone cannot identify a compatible deployment. Inventory
complete ledgers and schema definitions before adding a deployment to the support
set. 541 down is a narrowly tested technical rollback, not permission to keep
current synchronization writers active or to run unrestricted down migrations.

## Reproduction

From `server/`, with the managed checkout's isolated database selected:

```sh
go test -race -p 1 -parallel 1 -count=1 ./internal/migrations ./cmd/migrate
go test -race -p 1 -parallel 1 -count=5 ./cmd/migrate \
  -run 'TestHistoricalConcurrentRunners|TestRunMigrationsAdvisoryLockSerializes|TestMigrationLockCancellationThenRetry'
sqlc compile
```

Run full backend tests through the repository agent-CLI guard, with dedicated
Redis and an isolated CLI working directory when the checkout is nested under
Multica task markers. The TES-26 attached evidence contains complete commands,
raw JSON events, snapshot file/hash audit, failures before fixes, final counts,
and skipped tests. Docker Compose availability and optional extensions must be
reported separately; neither a skipped test nor a failed wrapper is a pass.
