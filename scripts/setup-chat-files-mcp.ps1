# Prepares examples/mcp-servers/chat-files-mcp so an agent can use it.
#
# chat-files-mcp is a Python stdio MCP server (like wecom-doc-mcp it is started
# by the agent, so there is nothing to keep running). This script creates its
# .venv, installs the MCP SDK and prints the Multica server config.
# Idempotent: re-running only does what is missing.
#
#   .\scripts\setup-chat-files-mcp.ps1            # prepare + print config
#   .\scripts\setup-chat-files-mcp.ps1 -Test      # also run the unit tests
#   .\scripts\setup-chat-files-mcp.ps1 -ZoteroAuth  # get a local Zotero write key

[CmdletBinding()]
param(
  [switch]$Test,
  [switch]$ZoteroAuth
)

$ErrorActionPreference = "Stop"
function Write-Info { param([string]$Msg) Write-Host "==> $Msg" -ForegroundColor Cyan }
function Write-Ok   { param([string]$Msg) Write-Host "[OK] $Msg"  -ForegroundColor Green }

$McpDir = Join-Path (Split-Path -Parent $PSScriptRoot) "examples\mcp-servers\chat-files-mcp"
if (-not (Test-Path (Join-Path $McpDir "server.py"))) { throw "chat-files-mcp not found at $McpDir" }

$onWindows = [IO.Path]::DirectorySeparatorChar -eq '\'
$venvPython = if ($onWindows) { Join-Path $McpDir ".venv\Scripts\python.exe" } else { Join-Path $McpDir ".venv/bin/python" }

if (-not (Test-Path $venvPython)) {
  Write-Info "Creating .venv"
  $uv = Get-Command uv -ErrorAction SilentlyContinue
  if ($uv) {
    & uv venv (Join-Path $McpDir ".venv")
  } else {
    $py = Get-Command python3, python, py -ErrorAction SilentlyContinue | Select-Object -First 1
    if (-not $py) { throw "Python 3.10+ not found (winget install Python.Python.3.13)" }
    & $py.Source -m venv (Join-Path $McpDir ".venv")
  }
  if ($LASTEXITCODE -ne 0) { throw "venv creation failed" }
}

& $venvPython -c "import mcp" 2>$null
if ($LASTEXITCODE -ne 0) {
  Write-Info "Installing requirements"
  $uv = Get-Command uv -ErrorAction SilentlyContinue
  if ($uv) {
    & uv pip install --python $venvPython -r (Join-Path $McpDir "requirements.txt")
  } else {
    & $venvPython -m pip install -q -r (Join-Path $McpDir "requirements.txt")
  }
  if ($LASTEXITCODE -ne 0) { throw "pip install failed" }
}
Write-Ok "chat-files-mcp ready ($venvPython)"

if ($Test) {
  Push-Location $McpDir
  try {
    & $venvPython -m unittest discover -s tests
    if ($LASTEXITCODE -ne 0) { throw "tests failed" }
  } finally { Pop-Location }
}

if ($ZoteroAuth) {
  Write-Info "Click Allow (or Always Allow) in the Zotero window"
  Push-Location $McpDir
  try { & $venvPython -m chat_files zotero-auth } finally { Pop-Location }
}

$config = [ordered]@{ command = $venvPython; args = @((Join-Path $McpDir "server.py")) } | ConvertTo-Json -Compress
Write-Host ""
Write-Host "Multica: Settings -> MCP -> add server, then assign it on the agent's MCP tab:"
Write-Host "  $config"
