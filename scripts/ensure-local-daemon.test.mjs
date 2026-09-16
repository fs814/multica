import assert from "node:assert/strict";
import test from "node:test";
import { ensureLocalDaemon, waitForAPI } from "./ensure-local-daemon.mjs";

const running = {
  status: "running", profile: "desktop-localhost-8081", pid: 123,
  server_url: "http://localhost:8081", daemon_id: "owner-1", no_task_claims: true,
};

function fixture({ initial = running, final = running, present = true, fail = "", env = {} } = {}) {
  const calls = [];
  const logs = [];
  let started = false;
  return {
    calls, logs,
    options: {
      root: process.cwd(), env: { BACKEND_PORT: "8081", ...env }, platform: "win32",
      exists: () => present, mkdir: () => {}, log: (line) => logs.push(line),
      ready: async (url) => { calls.push(["ready", url]); },
      run: async (file, args) => {
        calls.push([file, ...args]);
        if (args.includes("status")) return { code: 0, stdout: JSON.stringify(started ? final : initial) };
        if (args.includes("start")) started = true;
        const code = (fail === "build" && file === "go") || (fail === "start" && args.includes("start")) ? 1 : 0;
        return { code, stdout: "", stderr: code ? "fixture failure" : "" };
      },
    },
  };
}

test("reuses the existing maintenance owner without build, restart or changing task claims", async () => {
  const f = fixture();
  const state = await ensureLocalDaemon(f.options);
  assert.equal(state.pid, running.pid);
  assert.equal(state.no_task_claims, true);
  assert.equal(f.calls.length, 2);
  assert.match(f.logs.join("\n"), /maintenance/);
});

test("waits for API, builds a persistent binary and starts the same profile with maintenance flags", async () => {
  const f = fixture({ initial: { status: "stopped" }, env: { MULTICA_DEV_NO_TASK_CLAIMS: "1" } });
  await ensureLocalDaemon(f.options);
  assert.equal(f.calls[0][0], "ready");
  const builds = f.calls.filter((call) => call[0] === "go");
  assert.equal(builds.length, 1);
  assert.match(builds[0].join(" "), /\.multica/);
  const start = f.calls.find((call) => call.includes("start"));
  assert.ok(start.includes("--no-task-claims"));
  assert.ok(start.includes("desktop-localhost-8081"));
  assert.ok(start.includes("--no-auto-update"));
  assert.ok(!start.includes("--daemon-id")); // CLI keeps the persisted machine ID.
});

test("a clean checkout builds only once and enables normal task execution by default", async () => {
  const f = fixture({ present: false, initial: { status: "stopped" }, final: { ...running, no_task_claims: false } });
  await ensureLocalDaemon(f.options);
  assert.equal(f.calls.filter((call) => call[0] === "go").length, 1);
  assert.ok(!f.calls.find((call) => call.includes("start")).includes("--no-task-claims"));
});

for (const [name, initial] of [
  ["another server", { ...running, server_url: "http://localhost:9999" }],
  ["another profile", { ...running, profile: "other" }],
  ["health port collision", { status: "stopped", port_conflict: { profile: "other" } }],
]) {
  test(`does not start or stop anything when it finds ${name}`, async () => {
    const f = fixture({ initial });
    await assert.rejects(ensureLocalDaemon(f.options));
    assert.equal(f.calls.length, 2);
  });
}

test("missing login reports the exact profile without starting or inventing an owner", async () => {
  const f = fixture({ initial: { status: "unknown_profile" } });
  await assert.rejects(ensureLocalDaemon(f.options), /login --profile desktop-localhost-8081/);
  assert.equal(f.calls.length, 2);
});

test("failure to build or start cannot be reported as Memory ready", async () => {
  for (const fail of ["build", "start"]) {
    const f = fixture({ initial: { status: "stopped" }, fail });
    await assert.rejects(ensureLocalDaemon(f.options), /failed/);
    assert.ok(!f.logs.some((line) => line.includes("Daemon + Memory owner:")));
  }
});

test("a successful start command still requires a running health response", async () => {
  const f = fixture({ initial: { status: "stopped" }, final: { status: "stopped" } });
  await assert.rejects(ensureLocalDaemon(f.options), /did not become ready/);
});

test("API readiness retries failures and stops on success, with a finite failure bound", async () => {
  let attempts = 0;
  await waitForAPI("http://localhost:8081", {
    fetcher: async () => ({ ok: ++attempts === 3 }), sleep: async () => {}, attempts: 3,
  });
  assert.equal(attempts, 3);
  await assert.rejects(waitForAPI("http://localhost:8081", {
    fetcher: async () => { throw new Error("offline"); }, sleep: async () => {}, attempts: 2,
  }), /daemon was not started/);
});

test("configured owner identity must match and equivalent loopback endpoints are accepted", async () => {
  const wrong = fixture({ env: { MULTICA_DAEMON_ID: "other" } });
  await assert.rejects(ensureLocalDaemon(wrong.options), /daemon ID does not match/);
  const equivalent = fixture({ initial: { ...running, server_url: "ws://127.0.0.1:8081/ws" } });
  await ensureLocalDaemon(equivalent.options);
});
