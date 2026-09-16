The Git bundle requires upstream commit 9d18186e6e8cfa168d6053d33d652e86fadfc12b.
Fetch refs/heads/tes85-p0-contract for completed P0, or refs/heads/tes85-p1-contract
for the explicitly incomplete P1 probe. P0 includes all query corrections.

Run from server with sqlc v1.31.1 and Go 1.26.6:
  sqlc generate
  go build ./pkg/db/generated ./cmd/migrate
  go vet ./cmd/migrate ./pkg/db/generated
With DATABASE_URL targeting an OWNED disposable PostgreSQL 17 database named
with the tes85_p0_ prefix, set TES85_RUN_MIGRATION_TESTS=1, then:
  go test ./cmd/migrate -run 'TestTES85|TestEvery|TestConcurrentIndexCleanupsMatchTheirMigrations' -count=1 -v
P1 core tests require the P0 full schema in a disposable DB:
  go test ./internal/workflow -count=1 -v
The service package intentionally still fails at the documented permission scope gate.

reproduction/ scripts are provenance of this run's synthetic harness, not turnkey
operator scripts: their old/source paths and binary paths must be remapped to the
fixed checkouts above. Do not run them against a deployment DB. The three database
shapes and row/constraint assertions are explicit; no raw backup is distributed.
The NOT-APPLIED permission patch is for scope review only. It has not been imported,
compiled or tested as part of the candidate. Native hosts and production are untouched.
