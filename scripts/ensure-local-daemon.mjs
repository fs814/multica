import { spawn } from "node:child_process";
import { existsSync, mkdirSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import { setTimeout as delay } from "node:timers/promises";

const checkout = resolve(dirname(fileURLToPath(import.meta.url)), "..");

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

// Memory is serviced by this daemon's existing control channel. Never create
// another owner, rebind a project, or stop a daemon already serving this profile.
export async function ensureLocalDaemon({
  root = checkout, env = process.env, platform = process.platform,
  run = command, exists = existsSync, mkdir = mkdirSync,
  ready = waitForAPI, log = console.log,
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
  const built = join(root, ".multica", "bin", "local-daemon", profile, name);
  const candidates = [built, join(root, "apps", "desktop", "resources", "bin", name), join(root, "server", "bin", name)];
  let cli = candidates.find(exists);
  let builtThisRun = false;
  const commandEnv = { ...env, MULTICA_SERVER_URL: server };
  const invoke = (file, args) => run(file, args, { cwd: root, env: commandEnv });
  const loginHint = () => `Sign in first: "${cli}" --server-url ${server} login --profile ${profile}. Then rerun: node scripts/ensure-local-daemon.mjs`;
  let sourceVersion;
  const checkoutVersion = async () => {
    if (sourceVersion !== undefined) return sourceVersion;
    try {
      const result = await invoke("git", ["describe", "--tags", "--always", "--dirty"]);
      const revision = result.stdout?.trim();
      sourceVersion = result.code === 0 && revision ? `local-${revision}` : "local-unknown";
    } catch { sourceVersion = "local-unknown"; }
    return sourceVersion;
  };
  const build = async () => {
    mkdir(dirname(built), { recursive: true });
    log("==> Building local daemon with project Memory support...");
    const version = await checkoutVersion();
    const result = await invoke("go", ["-C", "server", "build", "-p", "2", "-ldflags", `-X main.version=${version}`, "-o", built, "./cmd/multica"]);
    if (result.code !== 0) throw new Error(`Daemon build failed: ${result.stderr}`);
    cli = built;
    builtThisRun = true;
  };
  const status = async () => {
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
  if (!cli) await build();
  let state = await status();
  if (state.status === "running" && state.cli_version) {
    const expected = await checkoutVersion();
    if (expected !== "local-unknown" && state.cli_version !== expected) {
      log(`==> WARNING: running daemon ${state.cli_version} differs from checkout ${expected}. ` +
        `Active tasks: ${state.active_task_count ?? "unknown"}. It was not restarted. ` +
        `After active runs finish, stop profile ${profile} and rerun this helper to rebuild from local source.`);
    }
  }
  if (state.status === "stopped") {
    // Rebuild only while stopped; an active Windows binary cannot be replaced.
    if (!builtThisRun) await build();
    const args = ["--server-url", server, "daemon", "start", "--profile", profile,
      "--no-auto-update", "--no-auto-reload"];
    if (env.MULTICA_DEV_NO_TASK_CLAIMS === "1") args.push("--no-task-claims");
    const result = await invoke(cli, args);
    if (result.code !== 0) throw new Error(`Daemon startup failed: ${result.stderr}\n${loginHint()}`);
    state = await status();
    if (state.status !== "running") throw new Error(`Daemon did not become ready. ${loginHint()}`);
  }
  log(`==> Daemon + Memory owner: ${profile}, PID ${state.pid}${state.no_task_claims ? " (maintenance: business task claims disabled)" : ""}`);
  log(`==> Final source Memory: datas/memory/; candidates: .multica/project-memory-candidates/`);
  return state;
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  ensureLocalDaemon().catch((error) => {
    console.error(`==> Daemon/Memory unavailable: ${error.message}`);
    process.exitCode = 1;
  });
}
