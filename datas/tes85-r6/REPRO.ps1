param([Parameter(Mandatory=$true)][string]$CandidateRoot)
$ErrorActionPreference='Stop'
if (-not $env:DATABASE_URL -or $env:DATABASE_URL -notmatch '/tes85_[a-zA-Z0-9_]+(?:\?|$)') {
 throw 'Set DATABASE_URL to a pre-created disposable tes85_* database. Never use a product DB.'
}
Push-Location (Join-Path (Resolve-Path $CandidateRoot) 'server')
try {
 go run ./cmd/migrate up
 if ($LASTEXITCODE -ne 0) {throw 'migration failed'}
 go build ./...
 if ($LASTEXITCODE -ne 0) {throw 'build failed'}
 go vet ./...
 if ($LASTEXITCODE -ne 0) {throw 'vet failed'}
 go test ./internal/handler -run 'TestR6|TestR5|TestWorkflow|TestDeleteWorkspace_|TestWorkspaceDeletionManifest' -count=1
 if ($LASTEXITCODE -ne 0) {throw 'handler regression failed'}
 go test ./internal/workflow -count=1
 if ($LASTEXITCODE -ne 0) {throw 'workflow regression failed'}
 go test ./cmd/multica -run '^TestDaemonLocalCommandsFailClosedInTaskContext$|^TestHumanAuthCommandsFailClosedInTaskContext$' -count=1
 if ($LASTEXITCODE -ne 0) {throw 'CLI boundary regression failed'}
 # Known failure, intentionally retained until migration policy is approved.
 go test ./internal/migrations -run '^TestMigrationNumericPrefixesAreUnique$' -count=1
 if ($LASTEXITCODE -eq 0) {throw 'Unexpected migration-lint pass: verify candidate and policy'}
 Write-Output 'Expected migration-number blocker reproduced. This is not full acceptance.'
} finally {Pop-Location}
