param([Parameter(Mandatory=$true)][string]$WorkspaceRoot)
$ErrorActionPreference='Stop'
Set-Location $WorkspaceRoot
# Requires the R6 delivery under datas/tes85-r6 and the upstream Git object.
if (Test-Path .multica/tes85-r7) {throw 'Use a clean workspace; candidate directory already exists'}
git init .multica/tes85-r7
if ($LASTEXITCODE -ne 0) {throw 'init failed'}
git -C .multica/tes85-r7 fetch ../.. 9d18186e6e8cfa168d6053d33d652e86fadfc12b
if ($LASTEXITCODE -ne 0) {throw 'upstream object unavailable'}
git -C .multica/tes85-r7 fetch ../../datas/tes85-r7/TES-85-r7.bundle refs/heads/tes85-r7
if ($LASTEXITCODE -ne 0) {throw 'bundle import failed'}
git -C .multica/tes85-r7 checkout --detach 38aad73c26fbb2064d32ff4a9631f3e96d474faf
if ($LASTEXITCODE -ne 0) {throw 'checkout failed'}
New-Item -ItemType Directory -Force .multica/tes85-r7-evidence | Out-Null
Copy-Item datas/tes85-r7/reproduction/* .multica/tes85-r7-evidence/
Push-Location .multica/tes85-r7/server
try {
 go build -o ../../tes85-r7-evidence/dbprobe.exe ../../tes85-r7-evidence/dbprobe.go
 if ($LASTEXITCODE -ne 0) {throw 'probe build failed'}
 go build -o ../../tes85-r7-evidence/r7-migrate.exe ./cmd/migrate
 if ($LASTEXITCODE -ne 0) {throw 'migrate build failed'}
 go vet ./internal/migrations
 if ($LASTEXITCODE -ne 0) {throw 'vet failed'}
} finally {Pop-Location}
python .multica/tes85-r7-evidence/rehearse.py
if ($LASTEXITCODE -ne 0) {throw 'upgrade/re-run failed'}
python .multica/tes85-r7-evidence/regressions.py
if ($LASTEXITCODE -ne 0) {throw 'migration regression failed'}
