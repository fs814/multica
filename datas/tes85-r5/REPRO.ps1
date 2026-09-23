param([Parameter(Mandatory=$true)][string]$CandidateRoot)
$ErrorActionPreference='Stop'
# Do not fall back to the product database. Pre-create and migrate a disposable DB.
if (-not $env:DATABASE_URL -or $env:DATABASE_URL -notmatch '/tes85_[a-zA-Z0-9_]+(?:\?|$)') {
    throw 'Set DATABASE_URL explicitly to a disposable tes85_* test database first.'
}
Push-Location (Join-Path (Resolve-Path $CandidateRoot) 'server')
try {
    go build ./...
    if ($LASTEXITCODE -ne 0) { throw 'build failed' }
    go vet ./...
    if ($LASTEXITCODE -ne 0) { throw 'vet failed' }
    # TestMain can access the database even with no selected tests. Run serially.
    go test ./... -run '^$'
    if ($LASTEXITCODE -ne 0) { throw 'test compile/initialization failed' }
    go test ./internal/workflow -count=1
    if ($LASTEXITCODE -ne 0) { throw 'workflow suite failed' }
    go test ./internal/handler ./internal/service ./cmd/server -run 'TestR5|TestWorkflow|TestAgentInvoke|TestBatchChildDone|TestBatchUpdate' -count=1
    if ($LASTEXITCODE -ne 0) { throw 'focused suite failed' }
} finally { Pop-Location }
# Required complete scripts/test-go.sh still has documented failures; run separately.
# This reproducer neither starts a daemon nor changes any CLI identity context.
