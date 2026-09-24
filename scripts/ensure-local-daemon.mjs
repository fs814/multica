import { spawn } from "node:child_process";
import { copyFileSync, existsSync, mkdirSync, readFileSync, renameSync, rmSync, writeFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import { setTimeout as delay } from "node:timers/promises";

const checkout = resolve(dirname(fileURLToPath(import.meta.url)), "..");

// Records which build was copied into the running path. Windows locks a running
// .exe, so the binary itself cannot answer "what is installed here?" — this
// plain-text sidecar sits next to it and does.
const SIDECAR_NAME = "installed-buildinfo.txt";

function command(file, args, options = {}) {
  return new Promise((resolveCommand, reject) => {
    const child = spawn(file, args, {
      ...options, windowsHide: true, timeout: 180_000,
      stdio: ["ignore", "pipe", "pipe"],
    });
    let stdout = "";
    let stderr = "";
    child.stdout.on("data", (chunk) => { stdout += chunk; });
    child.stderr.on("data", (chunk) => { stderr += chunk; });
    child.on("error", reject);
    child.on("close", (code) => resolveCommand({ code, stdout, stderr }));
  });
}

export async function waitForAPI(url, { fetcher = fetch, sleep = delay, attempts = 90 } = {}) {
  for (let attempt = 0; attempt < attempts; attempt++) {
    try {
      const response = await fetcher(`${url}/health`, { signal: AbortSignal.timeout(2000) });
      const healthy = response.ok;
      await response.body?.cancel();
      if (healthy) return;
    } catch { /* The backend may still be building or migrating. */ }
    if (attempt + 1 < attempts) await sleep(2000);
  }
  throw new Error(`Backend did not become ready at ${url}; daemon was not started.`);
}

function endpoint(value) {
  const url = new URL(value);
  if (url.protocol === "ws:") url.protocol = "http:";
  if (url.protocol === "wss:") url.protocol = "https:";
  if (["127.0.0.1", "[::1]"].includes(url.hostname)) url.hostname = "localhost";
  return url.origin;
}

// Where a build puts the CLI. `make build` (Makefile:338-342) writes server/bin
// first; the desktop bundle is a copy of that same build. The order is fixed
// and never falls back to mtime: the copies are identical on the day they are
// made, so once they diverge "newest" picks whichever was written last rather
// than whichever the checkout actually produced.
export function artifactCandidates(root, name) {
  return [
    join(root, "server", "bin", name),
    join(root, "apps", "desktop", "resources", "bin", name),
  ];
}

export function installedPath(root, profile, name) {
  return join(root, ".multica", "bin", "local-daemon", profile, name);
}

// `go version -m <binary>` prints the module's build settings, including the
// VCS revision Go stamped in and the -ldflags a Makefile passed. Reading them
// is the only way to learn a binary's provenance without running it.
export function parseBuildInfo(text) {
  const info = { revision: "", modified: false, version: "" };
  for (const line of String(text).split(/\r?\n/)) {
    const fields = line.trim().split(/\s+/);
    if (fields[0] === "build" && fields[1] === "vcs.modified=true") info.modified = true;
    if (fields[0] === "build" && fields[1]?.startsWith("vcs.revision=")) {
      info.revision = fields[1].slice("vcs.revision=".length).trim();
    }
    const version = /-X\s+main\.version=(\S+)/.exec(line);
    if (version) info.version = version[1];
  }
  return info;
}

// Fallback provenance for a binary whose build info cannot be read: the CLI
// prints the same two facts itself.
export function parseVersionOutput(text) {
  const version = /^multica\s+(\S+)/m.exec(String(text));
  const commit = /\(commit:\s*([0-9a-f]+)/.exec(String(text));
  return { revision: commit?.[1] ?? "", modified: false, version: version?.[1] ?? "" };
}

// Git abbreviates revisions everywhere it prints them (git describe's -g<hash>,
// main.commit's short form). Equal revisions that differ only in length are the
// same commit; this never makes two different commits compare equal.
export function sameRevision(a, b) {
  if (!a || !b) return false;
  return a === b || a.startsWith(b) || b.startsWith(a);
}

// Does a reported version string carry this revision as a sha token? Used only
// as tolerance for *label* differences (local-v0.4.39-… vs v0.4.39-…): the sha
// itself must still match exactly.
export function versionCarriesRevision(version, revision) {
  if (!version || !revision) return false;
  if (version.includes(revision)) return true;
  const short = revision.slice(0, 9);
  return new RegExp(`(^|[^0-9a-f])${short}([^0-9a-f]|$)`, "i").test(version);
}

export function formatSidecar(entry) {
  return [
    "# Written by scripts/ensure-local-daemon.mjs when it installs a build.",
    "# Safe to delete: the helper then trusts the build it finds on disk again.",
    `source=${entry.source}`,
    `installed_at=${entry.installedAt}`,
    `revision=${entry.revision}`,
    `modified=${entry.modified}`,
    `version=${entry.version}`,
    "",
  ].join("\n");
}

export function parseSidecar(text) {
  const entry = {};
  for (const line of String(text).split(/\r?\n/)) {
    if (!line || line.startsWith("#")) continue;
    const at = line.indexOf("=");
    if (at > 0) entry[line.slice(0, at).trim()] = line.slice(at + 1).trim();
  }
  return entry;
}

export async function ensureLocalDaemon({
  root = checkout, env = process.env, platform = process.platform,
  run = command, exists = existsSync, mkdir = mkdirSync, read = readFileSync,
  write = writeFileSync, copy = copyFileSync, move = renameSync, remove = rmSync,
  ready = waitForAPI, log = console.log, now = () => new Date().toISOString(),
  dryRun = false,
} = {}) {
  const port = env.BACKEND_PORT || env.PORT || "8080";
  if (!/^\d+$/.test(port) || Number(port) < 1 || Number(port) > 65535) {
    throw new Error("BACKEND_PORT must be a valid TCP port.");
  }
  const server = `http://localhost:${port}`;
  const profile = env.MULTICA_PROFILE || `desktop-localhost-${port}`;
  if (!/^[a-zA-Z0-9_.-]+$/.test(profile)) throw new Error("Invalid local daemon profile name.");
  await ready(server);

  const name = platform === "win32" ? "multica.exe" : "multica";
  const installed = installedPath(root, profile, name);
  const sidecar = join(dirname(installed), SIDECAR_NAME);
  const artifacts = artifactCandidates(root, name);
  const commandEnv = { ...env, MULTICA_SERVER_URL: server };
  const invoke = (file, args) => run(file, args, { cwd: root, env: commandEnv });
  const loginHint = () => `Sign in first: "${installed}" --server-url ${server} login --profile ${profile}. Then rerun: node scripts/ensure-local-daemon.mjs`;
  const would = (message) => (dryRun ? log(`==> DRY RUN: would ${message}`) : log(`==> ${message}`));

  // Provenance of a build product, preferring the compiler-stamped build info
  // and falling back to what the binary reports about itself.
  const provenance = async (file) => {
    try {
      const result = await invoke("go", ["version", "-m", file]);
      if (result.code === 0 && result.stdout) {
        const info = parseBuildInfo(result.stdout);
        if (info.revision || info.version) return { ...info, origin: "buildinfo" };
      }
    } catch { /* Fall through to the binary's own --version. */ }
    try {
      const result = await invoke(file, ["--version"]);
      if (result.code === 0) {
        const info = parseVersionOutput(result.stdout);
        if (info.revision || info.version) return { ...info, origin: "version" };
      }
    } catch { /* Provenance stays unknown. */ }
    return { revision: "", modified: false, version: "", origin: "unknown" };
  };

  let head;
  const headRevision = async () => {
    if (head !== undefined) return head;
    try {
      const result = await invoke("git", ["rev-parse", "HEAD"]);
      const revision = result.stdout?.trim();
      head = result.code === 0 && revision ? revision : "";
    } catch { head = ""; }
    return head;
  };

  const readSidecar = () => {
    try {
      return exists(sidecar) ? parseSidecar(read(sidecar, "utf8")) : null;
    } catch { return null; }
  };

  const status = async (cli) => {
    const result = await invoke(cli, ["daemon", "status", "--profile", profile, "--output", "json"]);
    let state;
    try { state = JSON.parse(result.stdout); } catch { throw new Error(`Cannot inspect daemon: ${result.stderr}`); }
    if (state.status === "unknown_profile") throw new Error(loginHint());
    if (result.code !== 0 || state.port_conflict) throw new Error(`Cannot use daemon profile ${profile}: ${result.stderr || "health port belongs to another profile"}`);
    if (!["running", "stopped"].includes(state.status)) throw new Error(`Daemon is ${state.status}; wait and retry.`);
    if (state.status === "running") {
      if (state.profile !== profile || !state.server_url || endpoint(state.server_url) !== endpoint(server)) {
        throw new Error(`Existing daemon identity/server does not match ${profile} at ${server}; left untouched.`);
      }
      if (env.MULTICA_DAEMON_ID && state.daemon_id !== env.MULTICA_DAEMON_ID) {
        throw new Error("Existing daemon ID does not match MULTICA_DAEMON_ID; left untouched.");
      }
    }
    return state;
  };

  // Install is a byte-for-byte copy of a build product onto the running path —
  // never a second compile from source. A local rebuild would carry a different
  // version string (and could drift from what the user actually built), which
  // is exactly the discrepancy this helper exists to remove.
  //
  // The gates judged the artifact by path; the bytes that actually reach the
  // running path are the ones copied here. Those are re-checked on the staged
  // copy before it is moved into place, so a file swapped between the two reads
  // cannot be installed unverified.
  //
  // When `backup` is set the previous binary is renamed aside first, so a
  // failure anywhere after this point can put the profile back on the build it
  // was serving. The caller owns restoring it (and starting it again): this
  // function only guarantees the old bytes are still on disk.
  const installArtifact = async (artifact, info, { backup = false } = {}) => {
    would(`install ${info.version || "the current build"} from ${artifact} to ${installed}`);
    if (info.modified) {
      log(`==> NOTE: ${artifact} was built from a working tree with uncommitted changes ` +
        `(vcs.modified=true); installing it as-is.`);
    }
    if (dryRun) return null;
    mkdir(dirname(installed), { recursive: true });
    const staging = `${installed}.new-${process.pid}`;
    const backupPath = `${installed}.bak`;
    let backedUp = false;
    try {
      copy(artifact, staging);
      const staged = await provenance(staging);
      if (!staged.revision || !sameRevision(staged.revision, info.revision)) {
        throw new Error(`the copied file is not the build that was verified ` +
          `(staged revision "${staged.revision || "unknown"}", verified "${info.revision || "unknown"}")`);
      }
      if (backup && exists(installed)) {
        remove(backupPath, { force: true });
        move(installed, backupPath);
        backedUp = true;
      }
      move(staging, installed);
    } catch (error) {
      try { remove(staging, { force: true }); } catch { /* Best effort cleanup. */ }
      const failure = new Error(`Cannot install ${artifact} over ${installed}: ${error.message}`);
      failure.backupPath = backedUp ? backupPath : null;
      throw failure;
    }
    write(sidecar, formatSidecar({
      source: artifact, installedAt: now(),
      revision: info.revision, modified: info.modified, version: info.version,
    }), "utf8");
    return backedUp ? { backupPath } : null;
  };

  // Gate 1: only a build from THIS checkout may replace what the profile runs.
  // Another clone running dev.sh must not be able to hijack a serving daemon.
  //
  // A refusal names the other build products this checkout has, because the
  // candidate order is fixed rather than mtime-based: the first candidate can
  // be a stale leftover while the second is the build the user just made, and
  // "this file is from another tree" would then be the wrong story.
  const otherCandidates = async () => {
    const lines = [];
    for (const candidate of artifacts) {
      if (candidate === artifact || !exists(candidate)) continue;
      const other = await provenance(candidate);
      lines.push(`      ${candidate} → revision ${other.revision || "unknown"}` +
        `${other.version ? ` (${other.version})` : ""}`);
    }
    return lines.length ? `\n    Other build products in this checkout:\n${lines.join("\n")}` : "";
  };

  const acceptableArtifact = async ({ required = true } = {}) => {
    if (!artifact) {
      if (required) {
        log(`==> WARNING: no build product at ${artifacts.join(" or ")}; nothing to install. ` +
          `Run "make build" (or build_multica.ps1) here and rerun this helper.`);
      }
      return null;
    }
    const headCommit = await headRevision();
    if (!artifactInfo.revision || !headCommit) {
      log(`==> WARNING: cannot prove ${artifact} came from this checkout ` +
        `(build revision "${artifactInfo.revision || "unknown"}", checkout "${headCommit || "unknown"}"). ` +
        `Nothing was installed and the daemon was left untouched.` + await otherCandidates());
      return null;
    }
    if (!sameRevision(artifactInfo.revision, headCommit)) {
      log(`==> WARNING: ${artifact} was built from ${artifactInfo.revision.slice(0, 12)} but this checkout is at ` +
        `${headCommit.slice(0, 12)}. Refusing to install it over profile ${profile}; rebuild here first. ` +
        `Nothing was stopped or replaced.` + await otherCandidates());
      return null;
    }
    return artifactInfo;
  };

  const finish = (state) => {
    log(`==> Daemon + Memory owner: ${profile}, PID ${state.pid}${state.no_task_claims ? " (maintenance: business task claims disabled)" : ""}`);
    log(`==> Final source Memory: datas/memory/; candidates: .multica/project-memory-candidates/`);
    return state;
  };

  const artifact = artifacts.find(exists) ?? null;
  const artifactInfo = artifact ? await provenance(artifact) : null;
  const installRecord = readSidecar();

  // The installed copy is what the profile actually runs, so prefer it as the
  // CLI to talk to; a build product is only the fallback on a profile that has
  // never been installed.
  const query = (exists(installed) && installed) || artifact;
  if (!query) {
    throw new Error(`No daemon binary found. Build one first ("make build", or build_multica.ps1). ` +
      `Looked for ${installed} and ${artifacts.join(", ")}.`);
  }

  // What the daemon should be serving: the build this checkout just produced,
  // or — when nothing was built — whatever the sidecar says is installed.
  const baseline = artifactInfo?.version || artifactInfo?.revision
    ? artifactInfo
    : (installRecord ? { version: installRecord.version ?? "", revision: installRecord.revision ?? "" } : null);
  const servesBaseline = (version) =>
    Boolean(baseline && version) &&
    (version === baseline.version || versionCarriesRevision(version, baseline.revision));

  let state = await status(query);

  if (state.status === "running") {
    if (!state.cli_version) {
      log("==> WARNING: the running daemon reports no version, so it cannot be compared with this checkout. Left untouched.");
      return finish(state);
    }
    if (servesBaseline(state.cli_version)) {
      log(`==> Daemon already serves ${state.cli_version}; nothing to install.`);
      return finish(state);
    }
    if (!baseline) {
      log(`==> WARNING: running daemon ${state.cli_version} cannot be compared with this checkout ` +
        `(no build product, no ${SIDECAR_NAME}). It was not restarted.`);
      return finish(state);
    }
    // A daemon the Desktop app spawned is the app's to manage, and the profile
    // name is shared with this helper's default, so leaving it running is the
    // only safe answer — stopping it would take the app's daemon away.
    if (state.launched_by === "desktop") {
      log(`==> WARNING: the running daemon on profile ${profile} was launched by the Desktop app. ` +
        `It was left untouched; close the Desktop app before rerunning this helper to replace it.`);
      return finish(state);
    }
    const info = await acceptableArtifact();
    if (!info) return finish(state);
    // Gate 2: an idle daemon only. Stopping a busy one would interrupt the runs
    // it is serving, which is the one thing this helper must never do by
    // accident. "Unknown" is not "idle": a status response that cannot say how
    // many tasks are active is refused rather than read as zero.
    const active = state.active_task_count;
    if (typeof active !== "number" || active !== 0) {
      log(`==> WARNING: running daemon ${state.cli_version} differs from ${info.version || artifact}, but ` +
        `${typeof active === "number" ? `${active} task(s) are active` : "daemon status did not report an active task count"}. ` +
        `It was not restarted; rerun this helper once the profile is idle.`);
      return finish(state);
    }
    log(`==> Daemon ${state.cli_version} is not ${info.version || artifact} and no task is active; replacing it.`);
    if (dryRun) {
      would(`stop profile ${profile}`);
      state = { status: "stopped", pid: "dry-run" };
    } else {
      log(`==> Stopping profile ${profile} to replace its binary.`);
      const stopped = await invoke(query, ["daemon", "stop", "--profile", profile]);
      if (stopped.code !== 0) throw new Error(`daemon stop failed: ${stopped.stderr}`);
      try {
        state = await status(query);
      } catch (error) {
        // The stop succeeded, so profile is no longer served whatever this
        // follow-up call says — do not report it as "left untouched".
        error.daemonDown = true;
        throw error;
      }
      if (state.status !== "stopped") {
        throw new Error(`Profile ${profile} is ${state.status} after stop; not installing over a running binary.`);
      }
    }
  }

  if (state.status === "stopped") {
    // An active Windows binary cannot be replaced, so this is the only point
    // where install is possible at all.
    const haveInstalled = exists(installed);
    const previousSidecar = exists(sidecar) ? read(sidecar, "utf8") : null;
    const args = ["--server-url", server, "daemon", "start", "--profile", profile,
      "--no-auto-update", "--no-auto-reload"];
    if (env.MULTICA_DEV_NO_TASK_CLAIMS === "1") args.push("--no-task-claims");

    const launch = async (launcher) => {
      would(`run "${launcher}" ${args.join(" ")}`);
      if (dryRun) return;
      const result = await invoke(launcher, args);
      if (result.code !== 0) throw new Error(`Daemon startup failed: ${result.stderr}\n${loginHint()}`);
      state = await status(launcher);
      if (state.status !== "running") throw new Error(`Daemon did not become ready. ${loginHint()}`);
    };

    // This is the one place the helper can leave the profile worse off than it
    // found it: the daemon is already down, and an install or a start can fail
    // halfway. Everything after the stop is therefore wrapped, and a failure
    // puts the previous binary back and starts it again rather than leaving the
    // user with a dead profile and no way out.
    let info = null;
    let backup = null;
    try {
      info = await acceptableArtifact({ required: !haveInstalled });
      if (info) backup = await installArtifact(artifact, info, { backup: haveInstalled });
      else if (!haveInstalled) throw new Error(`No build product to install for profile ${profile}.`);
      // After an install the profile runs the installed copy; only a profile
      // with nothing installed at all falls back to launching a build in place.
      await launch(haveInstalled || info ? installed : query);
      if (backup?.backupPath) {
        try { remove(backup.backupPath, { force: true }); } catch { /* Kept as a leftover. */ }
      }
      if (dryRun) return finish({ pid: "dry-run", no_task_claims: env.MULTICA_DEV_NO_TASK_CLAIMS === "1" });
    } catch (error) {
      const backupPath = backup?.backupPath ?? error.backupPath ?? null;
      let message = error.message;
      let down = true;
      if (backupPath) {
        log(`==> Install failed; putting the previous daemon back from ${backupPath}.`);
        try {
          if (exists(backupPath)) {
            if (exists(installed)) remove(installed, { force: true });
            move(backupPath, installed);
          }
          // The sidecar describes whatever is installed now; a record of the
          // failed upgrade would make the next run misjudge this profile.
          if (previousSidecar === null) remove(sidecar, { force: true });
          else write(sidecar, previousSidecar, "utf8");
          await launch(installed);
          down = false;
          message = `${message}\n==> The previous daemon was restored from ${backupPath} and is running again.`;
        } catch (restoreError) {
          message = `${message}\n==> Recovery failed as well: ${restoreError.message}\n` +
            `==> Profile ${profile} has no daemon running. Start one with: ${loginHint()}`;
        }
      } else if (!dryRun) {
        message = `${message}\n==> Profile ${profile} has no daemon running. Start one with: ${loginHint()}`;
      }
      const failure = new Error(message);
      failure.daemonDown = down;
      throw failure;
    }
  }

  return finish(state);
}

// Exit codes tell the caller what state the profile was left in, so a wrapper
// can say something true about it instead of a single "it failed" line:
//   1 — the daemon was left as it was found (refused, skipped, or still running)
//   2 — the profile has no daemon running now
if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  ensureLocalDaemon({ dryRun: process.argv.includes("--dry-run") }).catch((error) => {
    console.error(`==> Daemon/Memory unavailable: ${error.message}`);
    process.exitCode = error.daemonDown ? 2 : 1;
  });
}
