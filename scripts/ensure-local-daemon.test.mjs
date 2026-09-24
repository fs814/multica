import assert from "node:assert/strict";
import { dirname, join } from "node:path";
import test from "node:test";
import {
  artifactCandidates, ensureLocalDaemon, parseBuildInfo, parseSidecar, sameRevision,
  versionCarriesRevision, waitForAPI,
} from "./ensure-local-daemon.mjs";

const HEAD = "c3cea26b44ca6833f3880c7cbf35ec9842b157b9";
const OTHER = "596e6c57d0000000000000000000000000000000";
const OLD = "9c3fa6c5838000000000000000000000000000000";
const VERSION = "v0.4.39-196-gc3cea26b4";
const OLD_VERSION = "v0.4.39-100-g9c3fa6c58";

const buildInfo = (revision = HEAD, version = VERSION) => [
  "multica.exe: go1.26.6",
  "\tpath\tgithub.com/multica-ai/multica/server/cmd/multica",
  `\tbuild\t-ldflags="-X main.version=${version} -X main.commit=${revision.slice(0, 9)}"`,
  "\tbuild\tvcs=git",
  `\tbuild\tvcs.revision=${revision}`,
  "\tbuild\tvcs.modified=false",
  "",
].join("\n");

const running = {
  status: "running", profile: "desktop-localhost-8081", pid: 123,
  server_url: "http://localhost:8081", daemon_id: "owner-1", no_task_claims: true,
  cli_version: VERSION, active_task_count: 0,
};

const INSTALLED = join(process.cwd(), ".multica", "bin", "local-daemon", "desktop-localhost-8081", "multica.exe");
const ARTIFACT = join(process.cwd(), "server", "bin", "multica.exe");
const DESKTOP_ARTIFACT = join(process.cwd(), "apps", "desktop", "resources", "bin", "multica.exe");
const SIDECAR = join(dirname(INSTALLED), "installed-buildinfo.txt");
const BACKUP = `${INSTALLED}.bak`;

// A virtual filesystem: no test may touch the real checkout or the real daemon.
function fixture({
  initial = running, final = running, artifact = HEAD, desktopArtifact = null,
  installedBuild = null, sidecar = null, head = HEAD, env = {}, fail = "",
  tamperStaging = false,
} = {}) {
  const calls = [];
  const logs = [];
  const files = new Map();
  const installs = [];
  if (artifact) files.set(ARTIFACT, buildInfo(artifact));
  if (desktopArtifact) files.set(DESKTOP_ARTIFACT, buildInfo(desktopArtifact));
  if (installedBuild) files.set(INSTALLED, buildInfo(installedBuild));
  if (sidecar) files.set(SIDECAR, sidecar);
  let state = { ...initial };
  let startAttempts = 0;
  let statusCalls = 0;
  return {
    calls, logs, installs, files,
    options: {
      root: process.cwd(), env: { BACKEND_PORT: "8081", ...env }, platform: "win32",
      exists: (path) => files.has(path),
      mkdir: () => {},
      read: (path) => { if (!files.has(path)) throw new Error(`ENOENT ${path}`); return files.get(path); },
      write: (path, data) => { files.set(path, data); },
      copy: (from, to) => {
        if (fail === "copy") throw new Error("fixture copy failure");
        installs.push([from, to]);
        // tamperStaging models a file swapped between the gate's read and the
        // install's read: the copy lands content the gate never approved.
        files.set(to, tamperStaging ? buildInfo(OTHER) : files.get(from) ?? "");
      },
      move: (from, to) => { files.set(to, files.get(from) ?? ""); files.delete(from); },
      remove: (path) => { files.delete(path); },
      now: () => "2026-09-22T00:00:00.000Z",
      log: (line) => logs.push(line),
      ready: async (url) => { calls.push(["ready", url]); },
      run: async (file, args) => {
        calls.push([file, ...args]);
        if (file === "git") return { code: head ? 0 : 1, stdout: head ? `${head}\n` : "", stderr: "" };
        if (file === "go") return { code: 0, stdout: files.get(args.at(-1)) ?? "", stderr: "" };
        if (args.includes("status")) {
          // The first status call reports the running daemon; the second one
          // (after the stop) is the one that can be made to fail.
          if (fail === "status-after-stop" && ++statusCalls > 1) return { code: 1, stdout: "", stderr: "fixture status failure" };
          return { code: 0, stdout: JSON.stringify(state), stderr: "" };
        }
        if (args.includes("stop")) {
          if (fail === "stop") return { code: 1, stdout: "", stderr: "fixture stop failure" };
          state = { status: "stopped", profile: initial.profile };
          return { code: 0, stdout: "", stderr: "" };
        }
        if (args.includes("start")) {
          const attempt = ++startAttempts;
          if (fail === "start" || (fail === "first-start" && attempt === 1)) {
            return { code: 1, stdout: "", stderr: "fixture start failure" };
          }
          state = { ...final };
          return { code: 0, stdout: "", stderr: "" };
        }
        return { code: 0, stdout: "", stderr: "" };
      },
    },
  };
}

const text = (f) => f.logs.join("\n");
const verbs = (f) => f.calls.map((call) => call.join(" "));
// Install stages into a temporary neighbour and renames it into place, so the
// recorded copy target carries a suffix; the destination is what matters.
const installSources = (f) => f.installs.map(([from]) => from);
const installTargets = (f) => f.installs.map(([, to]) => to.replace(/\.new-\d+$/, ""));
const touchedDaemon = (f) => verbs(f).some((call) => call.includes("stop") || call.includes("start"));

test("reuses a daemon already serving the build in this checkout", async () => {
  const f = fixture();
  const state = await ensureLocalDaemon(f.options);
  assert.equal(state.pid, running.pid);
  assert.equal(state.no_task_claims, true);
  assert.match(text(f), /already serves/);
  assert.ok(!touchedDaemon(f));
  assert.equal(f.installs.length, 0);
});

test("installs the build product instead of recompiling when the daemon is stale", async () => {
  const f = fixture({ initial: { ...running, cli_version: "dev", active_task_count: 0 } });
  await ensureLocalDaemon(f.options);
  assert.ok(!f.calls.some((call) => call[0] === "go" && call[1] === "-C"), "must not rebuild from source");
  assert.deepEqual(installSources(f), [ARTIFACT]);
  assert.deepEqual(installTargets(f), [INSTALLED]);
  const stop = f.calls.findIndex((call) => call.includes("stop"));
  const start = f.calls.findIndex((call) => call.includes("start"));
  assert.ok(stop > -1 && start > stop, "stop must precede start");
  assert.ok(f.installs.length === 1);
  assert.match(f.files.get(SIDECAR), new RegExp(`revision=${HEAD}`));
  assert.match(f.files.get(SIDECAR), new RegExp(`version=${VERSION.replace(/[.+]/g, "\\$&")}`));
});

test("keeps the install byte-identical to the artifact: no rewrite of the binary", async () => {
  const f = fixture({ initial: { ...running, cli_version: "dev", active_task_count: 0 } });
  await ensureLocalDaemon(f.options);
  assert.equal(f.files.get(INSTALLED), f.files.get(ARTIFACT));
});

test("an artifact is never chosen by mtime: server/bin wins over the desktop copy", async () => {
  const f = fixture({
    initial: { ...running, cli_version: "dev", active_task_count: 0 }, desktopArtifact: HEAD,
  });
  await ensureLocalDaemon(f.options);
  assert.deepEqual(installSources(f), [ARTIFACT]);
});

test("falls back to the desktop bundle only when server/bin is missing", async () => {
  const f = fixture({
    artifact: null, desktopArtifact: HEAD,
    initial: { ...running, cli_version: "dev", active_task_count: 0 },
  });
  await ensureLocalDaemon(f.options);
  assert.deepEqual(installSources(f), [DESKTOP_ARTIFACT]);
  assert.deepEqual(installTargets(f), [INSTALLED]);
});

test("idle gate: a busy daemon is never stopped, installed over, or restarted", async () => {
  const f = fixture({ initial: { ...running, cli_version: "dev", active_task_count: 2 } });
  const state = await ensureLocalDaemon(f.options);
  assert.equal(state.pid, running.pid);
  assert.match(text(f), /2 task\(s\) are active/);
  assert.ok(!touchedDaemon(f));
  assert.equal(f.installs.length, 0);
});

test("idle gate fails closed: an unreported task count is never read as idle", async () => {
  const { active_task_count: _dropped, ...withoutCount } = running;
  for (const initial of [
    { ...withoutCount, cli_version: "dev" },
    { ...running, cli_version: "dev", active_task_count: null },
  ]) {
    const f = fixture({ initial });
    const state = await ensureLocalDaemon(f.options);
    assert.equal(state.pid, running.pid);
    assert.match(text(f), /WARNING/);
    assert.match(text(f), /was not restarted/);
    assert.ok(!touchedDaemon(f));
    assert.equal(f.installs.length, 0);
  }
});

test("a Desktop-managed daemon is never stopped or replaced", async () => {
  const f = fixture({ initial: { ...running, cli_version: "dev", active_task_count: 0, launched_by: "desktop" } });
  const state = await ensureLocalDaemon(f.options);
  assert.equal(state.pid, running.pid);
  assert.match(text(f), /launched by the Desktop app/);
  assert.ok(!touchedDaemon(f));
  assert.equal(f.installs.length, 0);
});

test("the copied bytes are re-verified, so a swap after the gate is refused", async () => {
  const f = fixture({
    initial: { status: "stopped" }, installedBuild: OLD, tamperStaging: true,
    sidecar: `revision=${OLD}\nversion=${OLD_VERSION}\n`,
  });
  await assert.rejects(ensureLocalDaemon(f.options), /not the build that was verified/);
  assert.equal(f.files.get(INSTALLED), buildInfo(OLD), "the installed binary must survive");
  assert.equal(f.files.has(BACKUP), false);
  assert.ok(!f.files.has(`${INSTALLED}.new-${process.pid}`), "staging must be cleaned up");
});

test("a failed start puts the previous binary back and starts it again", async () => {
  const oldSidecar = `source=D:\\old\\multica.exe\nrevision=${OLD}\nmodified=false\nversion=${OLD_VERSION}\n`;
  const f = fixture({
    initial: { status: "stopped" }, installedBuild: OLD, sidecar: oldSidecar, fail: "first-start",
  });
  const error = await ensureLocalDaemon(f.options).then(() => null, (thrown) => thrown);
  assert.match(error.message, /Daemon startup failed/);
  assert.match(error.message, /previous daemon was restored from/);
  assert.equal(error.daemonDown, false, "the profile is serving again, so it is not left down");
  assert.equal(f.files.get(INSTALLED), buildInfo(OLD));
  assert.equal(f.files.get(SIDECAR), oldSidecar, "the sidecar must describe what is installed now");
  assert.equal(f.files.has(BACKUP), false);
});

test("a failure that cannot be recovered reports both errors and stays down", async () => {
  const f = fixture({
    initial: { status: "stopped" }, installedBuild: OLD, fail: "start",
    sidecar: `revision=${OLD}\nversion=${OLD_VERSION}\n`,
  });
  const error = await ensureLocalDaemon(f.options).then(() => null, (thrown) => thrown);
  assert.match(error.message, /Daemon startup failed/);
  assert.match(error.message, /Recovery failed as well/);
  assert.equal(error.daemonDown, true);
  assert.equal(f.files.get(INSTALLED), buildInfo(OLD), "the old binary is still on disk to retry with");
});

test("an install that fails before touching anything leaves the profile as it was", async () => {
  const f = fixture({ initial: { status: "stopped" }, installedBuild: OLD, fail: "copy" });
  const error = await ensureLocalDaemon(f.options).then(() => null, (thrown) => thrown);
  assert.match(error.message, /Cannot install/);
  assert.equal(error.daemonDown, true);
  assert.equal(f.files.get(INSTALLED), buildInfo(OLD));
  assert.equal(f.files.has(BACKUP), false);
});

test("a completed upgrade removes the backup it made", async () => {
  const f = fixture({
    initial: { status: "stopped" }, installedBuild: OLD,
    sidecar: `revision=${OLD}\nversion=${OLD_VERSION}\n`,
  });
  await ensureLocalDaemon(f.options);
  assert.equal(f.files.has(BACKUP), false);
  assert.equal(f.files.get(INSTALLED), buildInfo(HEAD));
  assert.match(f.files.get(SIDECAR), new RegExp(`revision=${HEAD}`));
});

test("a profile left down after a successful stop is never reported as untouched", async () => {
  // The stop lands, then the follow-up status call fails: the daemon is gone
  // either way, and the caller must be told so.
  const f = fixture({ initial: { ...running, cli_version: "dev", active_task_count: 0 }, fail: "status-after-stop" });
  const error = await ensureLocalDaemon(f.options).then(() => null, (thrown) => thrown);
  assert.equal(error.daemonDown, true);
  assert.equal(f.installs.length, 0);
});

test("a refusal names the other build product instead of blaming a foreign tree", async () => {
  const f = fixture({
    artifact: OTHER, desktopArtifact: HEAD, head: HEAD,
    initial: { ...running, cli_version: "dev", active_task_count: 0 },
  });
  const state = await ensureLocalDaemon(f.options);
  assert.equal(state.pid, running.pid);
  assert.match(text(f), /Refusing to install/);
  assert.match(text(f), /Other build products in this checkout/);
  assert.ok(text(f).includes(DESKTOP_ARTIFACT));
  assert.ok(text(f).includes(HEAD), "the matching candidate's revision must be shown");
  assert.ok(!touchedDaemon(f));
  assert.equal(f.installs.length, 0);
});

test("anti-hijack interlock: a build from another checkout never replaces a serving daemon", async () => {
  const f = fixture({ artifact: OTHER, initial: { ...running, cli_version: "dev", active_task_count: 0 } });
  const state = await ensureLocalDaemon(f.options);
  assert.equal(state.pid, running.pid);
  assert.match(text(f), /built from 596e6c57d000/);
  assert.match(text(f), /Nothing was stopped or replaced/);
  assert.ok(!touchedDaemon(f));
  assert.equal(f.installs.length, 0);
});

test("unverifiable provenance is refused rather than guessed", async () => {
  const f = fixture({ artifact: null, installedBuild: HEAD, head: "" });
  f.files.delete(SIDECAR);
  const state = await ensureLocalDaemon(f.options);
  assert.equal(state.pid, running.pid);
  assert.match(text(f), /cannot prove|no build product/);
  assert.equal(f.installs.length, 0);
});

test("a stale daemon with no build product is diagnosed, not rebuilt", async () => {
  const f = fixture({ artifact: null, installedBuild: HEAD, initial: { ...running, cli_version: "dev" } });
  const state = await ensureLocalDaemon(f.options);
  assert.equal(state.pid, running.pid);
  assert.match(text(f), /no build product/);
  assert.ok(!touchedDaemon(f));
});

test("a stopped profile installs the build product and starts it with maintenance flags", async () => {
  const f = fixture({ initial: { status: "stopped" }, env: { MULTICA_DEV_NO_TASK_CLAIMS: "1" } });
  await ensureLocalDaemon(f.options);
  assert.deepEqual(installSources(f), [ARTIFACT]);
  const start = f.calls.find((call) => call.includes("start"));
  assert.ok(start.includes("--no-task-claims"));
  assert.ok(start.includes("desktop-localhost-8081"));
  assert.ok(start.includes("--no-auto-update"));
  assert.ok(!start.includes("--daemon-id")); // CLI keeps the persisted machine ID.
  assert.equal(start[0], INSTALLED, "the daemon must start from the installed copy");
});

test("a stopped profile without any build product cannot be started from nothing", async () => {
  const f = fixture({ artifact: null, desktopArtifact: null, initial: { status: "stopped" } });
  await assert.rejects(ensureLocalDaemon(f.options), /No daemon binary found/);
  assert.equal(f.installs.length, 0);
});

test("a failed stop is reported and nothing is installed", async () => {
  const f = fixture({ initial: { ...running, cli_version: "dev" }, fail: "stop" });
  await assert.rejects(ensureLocalDaemon(f.options), /daemon stop failed/);
  assert.equal(f.installs.length, 0);
});

test("failure to start cannot be reported as Memory ready", async () => {
  const f = fixture({ initial: { status: "stopped" }, fail: "start" });
  await assert.rejects(ensureLocalDaemon(f.options), /Daemon startup failed/);
  assert.ok(!f.logs.some((line) => line.includes("Daemon + Memory owner:")));
});

test("a successful start command still requires a running health response", async () => {
  const f = fixture({ initial: { status: "stopped" }, final: { status: "stopped" } });
  await assert.rejects(ensureLocalDaemon(f.options), /did not become ready/);
});

test("dry run reports the plan without copying, stopping, or starting anything", async () => {
  const f = fixture({ initial: { ...running, cli_version: "dev" } });
  const state = await ensureLocalDaemon({ ...f.options, dryRun: true });
  assert.equal(state.pid, "dry-run");
  assert.match(text(f), /DRY RUN: would stop profile desktop-localhost-8081/);
  assert.match(text(f), /DRY RUN: would install/);
  assert.equal(f.installs.length, 0);
  assert.ok(!touchedDaemon(f));
});

test("the sidecar is the baseline when no build product is around", async () => {
  const f = fixture({
    artifact: null, installedBuild: HEAD,
    initial: { ...running, cli_version: "v0.4.39-196-gc3cea26b4" },
    sidecar: "source=D:\\build\\multica.exe\nrevision=" + HEAD + "\nmodified=false\nversion=" + VERSION + "\n",
  });
  await ensureLocalDaemon(f.options);
  assert.match(text(f), /already serves/);
  assert.equal(f.installs.length, 0);
});

test("a label difference on the same revision is not treated as a different build", async () => {
  const f = fixture({
    initial: { ...running, cli_version: `local-${VERSION}` }, active_task_count: 0,
  });
  await ensureLocalDaemon(f.options);
  assert.match(text(f), /already serves/);
  assert.equal(f.installs.length, 0);
});

test("a different revision under any label is stale", async () => {
  const f = fixture({
    initial: { ...running, cli_version: "local-v0.4.39-100-g596e6c57d", active_task_count: 0 },
  });
  await ensureLocalDaemon(f.options);
  assert.deepEqual(installSources(f), [ARTIFACT]);
});

test("another server, profile, or health-port collision is left untouched", async () => {
  for (const initial of [
    { ...running, server_url: "http://localhost:9999" },
    { ...running, profile: "other" },
    { status: "stopped", port_conflict: { profile: "other" } },
  ]) {
    const f = fixture({ initial: { ...initial, cli_version: "dev" } });
    await assert.rejects(ensureLocalDaemon(f.options));
    assert.ok(!touchedDaemon(f));
    assert.equal(f.installs.length, 0);
  }
});

test("missing login reports the exact profile without starting or inventing an owner", async () => {
  const f = fixture({ initial: { status: "unknown_profile" } });
  await assert.rejects(ensureLocalDaemon(f.options), /login --profile desktop-localhost-8081/);
  assert.ok(!touchedDaemon(f));
});

test("configured owner identity must match and equivalent loopback endpoints are accepted", async () => {
  const wrong = fixture({ env: { MULTICA_DAEMON_ID: "other" } });
  await assert.rejects(ensureLocalDaemon(wrong.options), /daemon ID does not match/);
  const equivalent = fixture({ initial: { ...running, server_url: "ws://127.0.0.1:8081/ws" } });
  await ensureLocalDaemon(equivalent.options);
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

test("build info parsing reads the VCS revision and the Makefile's version ldflag", () => {
  assert.deepEqual(parseBuildInfo(buildInfo()), { revision: HEAD, modified: false, version: VERSION });
  assert.equal(parseBuildInfo(buildInfo(HEAD, "dev")).version, "dev");
  assert.equal(parseBuildInfo(buildInfo(HEAD).replace("vcs.modified=false", "vcs.modified=true")).modified, true);
  assert.deepEqual(parseBuildInfo("not a go binary"), { revision: "", modified: false, version: "" });
});

test("revision comparison tolerates git's abbreviations but not different commits", () => {
  assert.ok(sameRevision(HEAD, HEAD.slice(0, 9)));
  assert.ok(sameRevision(HEAD.slice(0, 9), HEAD));
  assert.ok(!sameRevision(HEAD, OTHER));
  assert.ok(!sameRevision("", HEAD));
  assert.ok(versionCarriesRevision(`local-${VERSION}`, HEAD));
  assert.ok(versionCarriesRevision(VERSION, HEAD));
  assert.ok(!versionCarriesRevision("dev", HEAD));
  assert.ok(!versionCarriesRevision("v0.4.39-100-g596e6c57d", HEAD));
});

test("the sidecar parser ignores comments and keeps every recorded field", () => {
  const parsed = parseSidecar("# comment\nsource=x\nrevision=abc\nversion=v1\n");
  assert.deepEqual(parsed, { source: "x", revision: "abc", version: "v1" });
});

test("candidate paths are fixed and never ordered by timestamp", () => {
  assert.deepEqual(artifactCandidates("R", "multica.exe"), [
    join("R", "server", "bin", "multica.exe"),
    join("R", "apps", "desktop", "resources", "bin", "multica.exe"),
  ]);
});
