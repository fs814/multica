# Prepares the vendored wecom-doc-mcp server so an agent can use it.
#
# Idempotent and safe to call on every launch: each step checks whether it is
# already done and skips if so, so the common case (already set up) costs a
# few milliseconds and prints one line.
#
# What it does NOT do: log you in. The WeCom session needs an interactive QR
# scan, so this script only reports whether a session exists. Run it with
# -Login (or run `node src/login.js` yourself) to open the browser.
#
# Called from run_multica.ps1 / run_multica_desktop.ps1:
#   & "$PSScriptRoot\setup-wecom-doc-mcp.ps1"
#
# See examples/mcp-servers/wecom-doc-mcp/INTEGRATION.md for how to register it
# in Multica (Settings -> MCP) and assign it to an agent.

[CmdletBinding()]
param(
  # Open the browser for the interactive WeCom QR login. Off by default so the
  # script never blocks a launcher on human input.
  [switch]$Login,

  # Skip the Chromium download check. Useful on a machine where you know
  # Playwright's browsers are already present and want the fastest path.
  [switch]$SkipBrowserCheck,

  # Print what would happen without installing anything.
  [switch]$DryRun
)

$ErrorActionPreference = "Stop"

function Write-Info { param([string]$Msg) Write-Host "==> $Msg" -ForegroundColor Cyan }
function Write-Ok   { param([string]$Msg) Write-Host "[OK] $Msg"  -ForegroundColor Green }
function Write-Warn2{ param([string]$Msg) Write-Host "[!!] $Msg"  -ForegroundColor Yellow }

# Resolve the MCP directory relative to THIS script so the launcher can live
# anywhere and a moved/renamed checkout keeps working.
$McpDir = Join-Path (Split-Path -Parent $PSScriptRoot) "examples\mcp-servers\wecom-doc-mcp"
$EntryPoint = Join-Path $McpDir "src\index.js"

if (-not (Test-Path $EntryPoint)) {
  Write-Warn2 "wecom-doc-mcp not found at $McpDir - skipping (is this a full checkout?)"
  return
}

# ---------------------------------------------------------------------------
# node
# ---------------------------------------------------------------------------
# Resolve node the same way the daemon will: bare `node` off PATH. If it is
# missing here it will also be missing when an agent launches the MCP server,
# so failing loudly now is more useful than a silent MCP error later.
$node = Get-Command node -ErrorAction SilentlyContinue
if (-not $node) {
  Write-Warn2 "node not on PATH - wecom-doc-mcp cannot run. Install Node.js, then re-run this script."
  return
}

if ($DryRun) {
  Write-Info "[dry-run] would use node at $($node.Source)"
}

# ---------------------------------------------------------------------------
# npm dependencies
# ---------------------------------------------------------------------------
# `npm ci` rather than `npm install`: the vendored package-lock.json pins exact
# versions with integrity hashes, and `install` would let the carets float.
$NodeModules = Join-Path $McpDir "node_modules"
$SdkMarker = Join-Path $NodeModules "@modelcontextprotocol\sdk\package.json"

if (Test-Path $SdkMarker) {
  Write-Ok "wecom-doc-mcp dependencies present"
} elseif ($DryRun) {
  Write-Info "[dry-run] would run: npm ci --no-audit --no-fund (in $McpDir)"
} else {
  Write-Info "Installing wecom-doc-mcp dependencies (npm ci)..."
  Push-Location $McpDir
  try {
    npm ci --no-audit --no-fund 2>&1 | Out-String | Write-Verbose
    if ($LASTEXITCODE -ne 0) { throw "npm ci exited $LASTEXITCODE" }
    Write-Ok "dependencies installed"
  } catch {
    Write-Warn2 "npm ci failed: $_"
    Write-Warn2 "wecom-doc-mcp will not work until this succeeds. Try manually: cd $McpDir; npm ci"
    Pop-Location
    return
  }
  Pop-Location
}

# ---------------------------------------------------------------------------
# Playwright Chromium
# ---------------------------------------------------------------------------
# The playwright npm package does NOT bundle a browser; without this download
# every tool call fails at launch. The cache lives outside the repo, so this is
# per-machine state that survives `git clean`.
if ($SkipBrowserCheck) {
  Write-Info "Skipping Chromium check (-SkipBrowserCheck)"
} else {
  $PwCache = if ($env:PLAYWRIGHT_BROWSERS_PATH) {
    $env:PLAYWRIGHT_BROWSERS_PATH
  } else {
    Join-Path $env:LOCALAPPDATA "ms-playwright"
  }
  $HasChromium = (Test-Path $PwCache) -and
    (Get-ChildItem -Path $PwCache -Filter "chromium*" -Directory -ErrorAction SilentlyContinue | Select-Object -First 1)

  if ($HasChromium) {
    Write-Ok "Playwright Chromium present"
  } elseif ($DryRun) {
    Write-Info "[dry-run] would run: npx playwright install chromium (~150MB)"
  } else {
    Write-Info "Downloading Playwright Chromium (~150MB, one time)..."
    Push-Location $McpDir
    try {
      npx --yes playwright install chromium 2>&1 | Out-String | Write-Verbose
      if ($LASTEXITCODE -ne 0) { throw "playwright install exited $LASTEXITCODE" }
      Write-Ok "Chromium installed"
    } catch {
      Write-Warn2 "Chromium download failed: $_"
      Write-Warn2 "Run manually: cd $McpDir; npx playwright install chromium"
    }
    Pop-Location
  }
}

# ---------------------------------------------------------------------------
# WeCom session
# ---------------------------------------------------------------------------
# state.json holds live WeCom session cookies. It lives in the user profile,
# never in the repo, and the server writes it 0600.
$StateFile = Join-Path $env:USERPROFILE ".wecom-doc-mcp\state.json"

if ($Login) {
  if ($DryRun) {
    Write-Info "[dry-run] would run: node src/login.js (opens a browser for QR scan)"
  } else {
    Write-Info "Opening browser for WeCom QR login..."
    Push-Location $McpDir
    try { node src/login.js } finally { Pop-Location }
  }
} elseif (Test-Path $StateFile) {
  # Sessions last days, not forever. Surface the age so an expired session is
  # an obvious suspect rather than a mystery "tool returned nothing".
  $ageDays = [math]::Round(((Get-Date) - (Get-Item $StateFile).LastWriteTime).TotalDays, 1)
  Write-Ok "WeCom session present (updated $ageDays day(s) ago)"
  if ($ageDays -gt 7) {
    Write-Warn2 "Session is older than 7 days and may have expired. Re-login: cd $McpDir; node src/login.js"
  }
} else {
  Write-Warn2 "No WeCom session yet. The MCP server will start but every document call will fail."
  Write-Warn2 "Log in once with: cd $McpDir; node src/login.js    (or re-run this script with -Login)"
}

# ---------------------------------------------------------------------------
# Summary
# ---------------------------------------------------------------------------
Write-Info "wecom-doc-mcp entry point:"
Write-Host "    $EntryPoint" -ForegroundColor Gray
Write-Info "Register in Multica: Settings -> MCP, then assign it on an agent's MCP tab:"
Write-Host "    { `"command`": `"node`", `"args`": [`"$($EntryPoint -replace '\\','/')`"] }" -ForegroundColor Gray
