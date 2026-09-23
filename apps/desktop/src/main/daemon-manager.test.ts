// @vitest-environment node
import { createServer, type Server } from "node:http";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const fixture = vi.hoisted(() => ({
  handlers: new Map<string, (...args: any[]) => any>(),
  events: new Map<string, (...args: any[]) => any>(),
  install: vi.fn(), read: vi.fn(), write: vi.fn(), exec: vi.fn(), port: 0,
}));
vi.mock("electron", () => ({
  app: { getAppPath: () => "/test-missing-desktop", on: (event: string, handler: any) => fixture.events.set(event, handler) },
  ipcMain: { handle: (name: string, handler: any) => fixture.handlers.set(name, handler), on: vi.fn() },
  BrowserWindow: {}, shell: {},
}));
vi.mock("./cli-bootstrap", () => ({ ensureManagedCli: fixture.install, managedCliPath: () => "/test-missing-cli" }));
vi.mock("fs", () => ({ existsSync: () => false }));
vi.mock("fs/promises", () => ({ readFile: fixture.read, writeFile: fixture.write, mkdir: fixture.write, rm: fixture.write }));
vi.mock("child_process", () => ({ execFile: fixture.exec }));
vi.mock("./daemon-profile", async importOriginal => ({
  ...await importOriginal<typeof import("./daemon-profile")>(),
  healthPortForProfile: (profile: string) => { expect(profile).toBe("desktop-services"); return fixture.port; },
}));

let server: Server;
let health: Record<string, unknown>;
let unavailable = false;
let rejectInstall: (error: Error) => void;

beforeEach(async () => {
  vi.resetModules();
  vi.clearAllMocks();
  fixture.handlers.clear(); fixture.events.clear();
  vi.stubEnv("MULTICA_DESKTOP_DAEMON_PROFILE", "desktop-services");
  vi.stubEnv("MULTICA_DESKTOP_CENTER", "");
  fixture.read.mockRejectedValue(Object.assign(new Error("missing"), { code: "ENOENT" }));
  fixture.install.mockImplementation(() => new Promise((_resolve, reject) => { rejectInstall = reject; }));
  unavailable = false;
  health = { status: "running", profile: "desktop-services", pid: 12345, os: process.platform,
    center_connected: false, server_url: "", offline_reason: "unconfigured", task_ready: false, active_task_count: 0 };
  server = createServer((_req, res) => {
    res.writeHead(unavailable ? 503 : 200, { "Content-Type": "application/json" });
    res.end(JSON.stringify(health));
  });
  await new Promise<void>(resolve => server.listen(0, "127.0.0.1", resolve));
  fixture.port = (server.address() as { port: number }).port;
  const { setupDaemonManager } = await import("./daemon-manager");
  setupDaemonManager(() => null);
  await vi.waitFor(() => expect(fixture.install).toHaveBeenCalled());
});

afterEach(async () => {
  fixture.events.get("before-quit")?.({ preventDefault: vi.fn() });
  rejectInstall(new Error("fixture offline"));
  await new Promise<void>(resolve => server.close(() => resolve()));
  vi.unstubAllEnvs();
});

const status = () => fixture.handlers.get("daemon:get-status")!();

describe("Desktop discovers a standalone offline daemon", () => {
  it("probes the pinned profile before Center selection and while CLI download is pending", async () => {
    expect(await status()).toMatchObject({ state: "running", profile: "desktop-services", pid: 12345, centerConnected: false });
    expect(fixture.read).not.toHaveBeenCalled();
    expect(fixture.write).not.toHaveBeenCalled();
    expect(fixture.exec).not.toHaveBeenCalled();
  });

  it("does not write the renderer's default Center URL into the standalone profile", async () => {
    await fixture.handlers.get("daemon:set-target-api-url")!(null, "http://localhost:8080");
    expect(await status()).toMatchObject({ state: "running", serverUrl: "" });
    expect(fixture.write).not.toHaveBeenCalled();
  });

  it("still discovers the daemon after CLI installation fails and reports failure only if it disappears", async () => {
    rejectInstall(new Error("fixture offline"));
    await vi.waitFor(() => expect(fixture.install).toHaveBeenCalledTimes(1));
    expect(await status()).toMatchObject({ state: "running" });
    unavailable = true;
    await vi.waitFor(async () => expect(await status()).toMatchObject({ state: "cli_not_found" }));
  });

  it("never adopts a different profile sharing the health port", async () => {
    health.profile = "another-profile";
    expect(await status()).toMatchObject({ state: "stopped", profile: "desktop-services" });
  });
});
