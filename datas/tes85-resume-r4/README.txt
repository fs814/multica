Reproduction prerequisites and layout

The bundle is incremental from 9d18186e6e8cfa168d6053d33d652e86fadfc12b. Verify it in a repository containing that base, then fetch refs/heads/tes85-p0-r1r2 and refs/heads/tes85-p1-permissions. The complete old fork source e44b6a8d70b826ff368390cab62de5bbffb7167e is separately required for the upstream/fork migration control. It is the fixed source used in prior TES-85 deliveries; it is NOT supplied by this incremental bundle.

Use an isolated workspace and PostgreSQL 17 test server. Never point these scripts at live data. They create/delete dedicated tes85 test databases and use the local synthetic test database login. The supplied test scripts expect podman container multica-postgres-1 exposed on localhost:5432. Adapt that explicit test connection for another test environment; do not silently fall back to production.

Stage the P0 repository as .multica/p0, the fixed P1 standalone clone as .multica/p1-build, and the fixed old-source standalone clone as .multica/old-build. Copy reproduction/*.py to .multica/evidence, and extract the Windows candidate ZIP there. Run scripts from the isolated workspace root. p0_matrix.py rebuilds upstream/fork migration tools from fixed Git objects and checks sqlc, three DB categories and protected down. prefix_matrix.py runs literal-prefix boundary databases. old_app_rehearsal.py uses those baseline migration tools and the final fixed old server/P0 migrator. p1_http_rehearsal.py reuses its helper definitions.

For P0 tests, set TES85_RUN_MIGRATION_TESTS=1 and DATABASE_URL to a newly created empty tes85_p0_ test database, then from .multica/p0/server run:
  go test ./cmd/migrate -run TestTES85 -count=1 -json
The prefix script supplies the wrong database names for negative cases itself.

For P1, migrate a separate test database first. From the fixed candidate server directory:
  go build ./...
  go test ./internal/workflow -count=1 -json
  go test ./internal/service -run 'Test(AgentInvoke|Workflow|ResolveAutopilotTriggerPrincipal|AutopilotRun)' -count=1 -json
  go test ./internal/daemon ./pkg/agent -run 'Test(Workflow|CodexOutput|ApplyCodexOutput)' -count=1 -json
  go vet ./internal/service ./internal/workflow ./internal/daemon ./pkg/agent
The retained handler tests intentionally expose the unresolved intake dependency; the full test/vet gates are NOT green. Proposals must not be applied without scope authorization. Use the repository's guarded test-go.sh for eventual full-suite verification; no real agent CLI is authorized by this package.

All supplied Windows executables are test candidates, not production release artifacts. Binary VCS revisions, hashes, source differences and remaining gates are in REPORT.md and evidence/. Do not deploy the reduced P1 candidate over the existing complete fork service.
