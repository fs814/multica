# Prepares the vendored zhihu-mcp server so an agent can use it.
#
# Idempotent and safe to call on every launch: each step checks whether it is
# already done, so the common case costs about a second and prints a line.
#
# Unlike wecom-doc-mcp (a Node stdio server), zhihu-mcp is Python + FastMCP and
# speaks streamable HTTP on a port, so this script can also START it. Register
# it in Multica as an http transport pointing at http://127.0.0.1:<port>/mcp.
#
# What it does NOT do by default: log in. The Zhihu session needs an
# interactive QR scan, so use -Login for that (or scripts in the Settings repo).
#
# Called from run_multica.ps1 / run_multica_desktop.ps1:
#   & "$PSScriptRoot\setup-zhihu-mcp.ps1" -Start

[CmdletBinding()]
param(
  # Start the MCP server in the background after preparing it.
  [switch]$Start,

  # Open the browser for the interactive Zhihu QR login.
  [switch]$Login,

  # Port for the HTTP endpoint. Upstream's default is 18060.
  [int]$Port = 18060,

  # Browser to drive. Empty = auto-detect (Chrome, then Edge). Driving an
  # installed browser avoids Playwright's ~170MB Chromium download, which
  # endpoint-security software on this fleet blocks mid-extraction.
  [ValidateSet('', 'chrome', 'msedge')]
  [string]$Channel = '',

  # Ignore installed browsers and use Playwright's own Chromium.
  [switch]$UsePlaywright,

  # Print what would happen without changing anything.
  [switch]$DryRun
)

$ErrorActionPreference = "Stop"

function Write-Info { param([string]$Msg) Write-Host "==> $Msg" -ForegroundColor Cyan }
function Write-Ok   { param([string]$Msg) Write-Host "[OK] $Msg"  -ForegroundColor Green }
function Write-Warn2{ param([string]$Msg) Write-Host "[!!] $Msg"  -ForegroundColor Yellow }

$McpDir = Join-Path (Split-Path -Parent $PSScriptRoot) "examples\mcp-servers\zhihu-mcp"
if (-not (Test-Path (Join-Path $McpDir "main.py"))) {
  Write-Warn2 "zhihu-mcp not found at $McpDir - skipping (is this a full checkout?)"
  return
}

$venvPython = Join-Path $McpDir ".venv\Scripts\python.exe"
$cookieFile = Join-Path $McpDir "cookies\cookies.json"

# ---------------------------------------------------------------------------
# Python + dependencies
# ---------------------------------------------------------------------------
# `uv` is preferred: it creates the venv and resolves requirements far faster
# than pip, and it is already on this machine. Plain python is the fallback.
$uv = Get-Command uv -ErrorAction SilentlyContinue
$py = Get-Command python -ErrorAction SilentlyContinue
if (-not $py -and -not $uv) {
  Write-Warn2 "Neither uv nor python is on PATH - zhihu-mcp cannot run."
  return
}

if (Test-Path $venvPython) {
  Write-Ok "zhihu-mcp virtualenv present"
} elseif ($DryRun) {
  Write-Info "[dry-run] would create .venv and install requirements.txt"
} else {
  Write-Info "Creating virtualenv and installing dependencies..."
  Push-Location $McpDir
  try {
    if ($uv) {
      uv venv .venv 2>&1 | Out-String | Write-Verbose
      uv pip install --python .venv -r requirements.txt 2>&1 | Out-String | Write-Verbose
    } else {
      python -m venv .venv 2>&1 | Out-String | Write-Verbose
      & $venvPython -m pip install -r requirements.txt 2>&1 | Out-String | Write-Verbose
    }
    if (-not (Test-Path $venvPython)) { throw "virtualenv was not created" }
    Write-Ok "dependencies installed"
  } catch {
    Write-Warn2 "Dependency install failed: $_"
    Pop-Location
    return
  }
  Pop-Location
}

# ---------------------------------------------------------------------------
# Browser
# ---------------------------------------------------------------------------
$chromePaths = @(
  "$env:ProgramFiles\Google\Chrome\Application\chrome.exe",
  "${env:ProgramFiles(x86)}\Google\Chrome\Application\chrome.exe",
  "$env:LOCALAPPDATA\Google\Chrome\Application\chrome.exe"
)
$edgePaths = @(
  "$env:ProgramFiles\Microsoft\Edge\Application\msedge.exe",
  "${env:ProgramFiles(x86)}\Microsoft\Edge\Application\msedge.exe"
)
$hasChrome = @($chromePaths | Where-Object { Test-Path $_ }).Count -gt 0
$hasEdge   = @($edgePaths   | Where-Object { Test-Path $_ }).Count -gt 0

if ($UsePlaywright) {
  # Re-probe rather than trusting `playwright install`: it can exit 0 with
  # nothing extracted, which then fails much later inside a tool call.
  $probe = 'import sys; from playwright.sync_api import sync_playwright; import os; p=sync_playwright().start(); print("OK" if os.path.exists(p.chromium.executable_path) else "MISSING"); p.stop()'
  $state = (& $venvPython -c $probe 2>$null | Out-String).Trim()
  if ($state -ne "OK") {
    Write-Warn2 "Playwright's bundled Chromium is not installed."
    Write-Warn2 "Allowlist %LOCALAPPDATA%\ms-playwright in your endpoint-security product, then:"
    Write-Warn2 "  cd $McpDir; .venv\Scripts\python.exe -m playwright install chromium"
    Write-Warn2 "Or drop -UsePlaywright to drive the Chrome/Edge already installed."
    return
  }
  $env:ZHIHU_MCP_BROWSER_CHANNEL = ""
  Write-Info "Using Playwright's bundled Chromium"
} else {
  $resolved = $Channel
  if (-not $resolved) {
    if ($hasChrome)   { $resolved = "chrome" }
    elseif ($hasEdge) { $resolved = "msedge" }
  }
  if (-not $resolved) {
    Write-Warn2 "No Chrome or Edge found. Install one, or set up Playwright's Chromium and pass -UsePlaywright."
    return
  }
  $env:ZHIHU_MCP_BROWSER_CHANNEL = $resolved
  Write-Ok "Driving the installed browser: $resolved (skips Playwright's Chromium download)"
}

# ---------------------------------------------------------------------------
# Zhihu session
# ---------------------------------------------------------------------------
if ($Login) {
  if ($DryRun) {
    Write-Info "[dry-run] would run: login.py (opens a browser for the QR scan)"
  } else {
    Write-Info "Opening a browser for the Zhihu QR login..."
    Push-Location $McpDir
    try { & $venvPython login.py } finally { Pop-Location }
  }
} elseif (Test-Path $cookieFile) {
  $ageDays = [math]::Round(((Get-Date) - (Get-Item $cookieFile).LastWriteTime).TotalDays, 1)
  Write-Ok "Zhihu session present (updated $ageDays day(s) ago)"
} else {
  Write-Warn2 "No Zhihu session yet. Read-only tools may work; publishing will not."
  Write-Warn2 "Log in with: .\setup-zhihu-mcp.ps1 -Login"
}

# ---------------------------------------------------------------------------
# Start the server
# ---------------------------------------------------------------------------
$endpoint = "http://127.0.0.1:$Port/mcp"

if ($Start) {
  $listening = @(Get-NetTCPConnection -LocalPort $Port -State Listen -ErrorAction SilentlyContinue).Count -gt 0
  if ($listening) {
    Write-Ok "zhihu-mcp already listening on port $Port"
  } elseif ($DryRun) {
    Write-Info "[dry-run] would start: main.py --headless --port $Port"
  } else {
    Write-Info "Starting zhihu-mcp on $endpoint ..."
    $log = Join-Path ([IO.Path]::GetTempPath()) "zhihu-mcp.log"
    # Headless so a launcher never pops a window; the login flow is the only
    # place a visible browser is wanted, and that runs separately above.
    Start-Process -FilePath $venvPython `
      -ArgumentList "main.py", "--headless", "--port", "$Port" `
      -WorkingDirectory $McpDir `
      -RedirectStandardOutput $log -RedirectStandardError "$log.err" `
      -WindowStyle Hidden | Out-Null
    Start-Sleep -Seconds 6
    if (@(Get-NetTCPConnection -LocalPort $Port -State Listen -ErrorAction SilentlyContinue).Count -gt 0) {
      Write-Ok "zhihu-mcp listening on $endpoint (log: $log)"
    } else {
      Write-Warn2 "zhihu-mcp did not come up; see $log and $log.err"
    }
  }
}

Write-Info "Register in Multica: Settings -> MCP, then assign it on an agent's MCP tab:"
Write-Host "    { `"type`": `"http`", `"url`": `"$endpoint`" }" -ForegroundColor Gray
