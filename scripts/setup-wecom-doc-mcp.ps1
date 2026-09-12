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
  # Ask Playwright itself whether the browser it needs is present, rather than
  # globbing the cache for "chromium*". The cache is versioned per Playwright
  # build (chromium-1208, chromium-1228, ...), so a glob match is a FALSE
  # POSITIVE whenever the installed playwright wants a different revision than
  # whatever an older project left behind — the check passes and the login then
  # dies with "Executable doesn't exist at ...\chromium-1208\...". Resolving
  # executablePath and testing that exact file is the only check that tracks
  # the version actually required.
  $probe = 'try { const p = require("playwright"); const e = p.chromium.executablePath(); process.stdout.write(require("fs").existsSync(e) ? "OK" : "MISSING"); } catch { process.stdout.write("MISSING"); }'
  Push-Location $McpDir
  try {
    $chromiumState = (& node -e $probe 2>$null | Out-String).Trim()
  } catch {
    $chromiumState = "MISSING"
  }
  Pop-Location

  if ($chromiumState -eq "OK") {
    Write-Ok "Playwright Chromium present"
  } elseif ($DryRun) {
    Write-Info "[dry-run] would run: playwright install chromium (~170MB)"
  } else {
    Write-Info "Downloading Playwright Chromium (~170MB, one time)..."
    Push-Location $McpDir
    try {
      # Use the LOCAL playwright, not `npx playwright`. npx may resolve a
      # different playwright version than the one in node_modules, and each
      # version wants its own pinned browser revision — installing with the
      # wrong one populates a revision this project will never look for.
      $pwCli = Join-Path $McpDir "node_modules\playwright\cli.js"
      if (Test-Path -LiteralPath $pwCli) {
        node $pwCli install chromium 2>&1 | Out-String | Write-Verbose
      } else {
        npx --yes playwright install chromium 2>&1 | Out-String | Write-Verbose
      }
      if ($LASTEXITCODE -ne 0) { throw "playwright install exited $LASTEXITCODE" }

      # Re-probe rather than trusting the exit code: the installer can report
      # success while extraction was blocked partway, leaving a directory that
      # holds only the first few files.
      $recheck = (& node -e $probe 2>$null | Out-String).Trim()
      if ($recheck -eq "OK") {
        Write-Ok "Chromium installed"
      } else {
        throw "installer finished but the browser is still missing (extraction was likely blocked)"
      }
    } catch {
      Write-Warn2 "Chromium install did not complete: $_"
      Write-Warn2 "wecom-doc-mcp cannot open a browser until this is fixed."
      # Observed on this fleet: 腾讯电脑管家 (Tencent PC Manager) silently kills
      # the unzip right after chrome-win64\D3DCompiler_47.dll, so the install
      # exits 0 with 3 files on disk and no error anywhere. Worth naming,
      # because nothing in Playwright's own output points at it.
      Write-Warn2 "If the extraction stops after a few files, an endpoint-security"
      Write-Warn2 "product is blocking it (e.g. Tencent PC Manager, Windows Defender)."
      Write-Warn2 "Allowlist this path, then retry:  $env:LOCALAPPDATA\ms-playwright"
      Write-Warn2 "Retry with:  cd $McpDir; node node_modules\playwright\cli.js install chromium"
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
  } elseif ($chromiumState -ne "OK" -and -not $SkipBrowserCheck) {
    # Refuse rather than let login.js die on Playwright's raw
    # "Executable doesn't exist at ...\chromium-NNNN\chrome.exe", which reads
    # like a broken install instead of a missing prerequisite.
    Write-Warn2 "Cannot open the QR login: Chromium is not installed (see above)."
    Write-Warn2 "Fix the browser install first, then re-run with -Login."
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
