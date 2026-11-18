// @vitest-environment node
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { BrowserWindow, IpcMainInvokeEvent } from "electron";
import type { ContainerEngine } from "./container-engine";
import { setupLocalContainers } from "./local-containers";

const mocks = vi.hoisted(() => ({
  handlers: new Map<string, (event: IpcMainInvokeEvent, ...args: unknown[]) => unknown>(),
  openPath: vi.fn(async () => ""),
  openInTerminal: vi.fn(async () => undefined),
}));
vi.mock("electron", () => ({
  ipcMain: { handle: (channel: string, handler: (event: IpcMainInvokeEvent, ...args: unknown[]) => unknown) => mocks.handlers.set(channel, handler) },
  shell: { openPath: mocks.openPath },
}));
vi.mock("./terminal-launch", () => ({ openInTerminal: mocks.openInTerminal }));

function fakeEngine() {
  return {
    status: vi.fn(async () => ({ engines: [], active: null })), use: vi.fn(), list: vi.fn(async () => []),
    containerAction: vi.fn(async () => undefined), removeContainer: vi.fn(async () => undefined), rename: vi.fn(async () => undefined),
    logs: vi.fn(async () => "log"), inspect: vi.fn(async () => "{}"), runDetached: vi.fn(async () => "newid"),
    pull: vi.fn(async () => "pulled"), remove: vi.fn(async () => undefined), prune: vi.fn(async () => "reclaimed"),
    terminalArgv: vi.fn(async (args: string[]) => ["/usr/bin/docker", ...args]), startPodmanMachine: vi.fn(async () => "started"),
  };
}

const webContents = { mainFrame: {} };
const event = { sender: webContents, senderFrame: webContents.mainFrame } as IpcMainInvokeEvent;
const call = (channel: string, ...args: unknown[]) => mocks.handlers.get(`local-containers:${channel}`)!(event, ...args);
let engine: ReturnType<typeof fakeEngine>;

beforeEach(() => {
  vi.clearAllMocks();
  mocks.handlers.clear();
  engine = fakeEngine();
  setupLocalContainers(() => ({ webContents }) as BrowserWindow, engine as unknown as ContainerEngine);
});

describe("Containers native boundary", () => {
  it("serves only the main window's top frame", () => {
    expect([...mocks.handlers.keys()].sort()).toEqual([
      "attach", "container-action", "exec", "inspect", "list", "logs", "prune", "pull", "remove", "remove-container",
      "rename-container", "run", "start-engine", "status", "use-engine",
    ].map((c) => `local-containers:${c}`));
    for (const handler of mocks.handlers.values()) {
      expect(() => handler({ sender: {} } as IpcMainInvokeEvent, "x")).toThrow("main Desktop window");
    }
  });

  it("validates every argument before calling the engine", () => {
    expect(() => call("container-action", "web", "exec")).toThrow("Invalid action");
    expect(() => call("container-action", "--all", "stop")).toThrow("Invalid id");
    expect(() => call("list", "secrets")).toThrow("Invalid resource");
    expect(() => call("logs", "web", 0, false)).toThrow("Invalid line count");
    expect(() => call("exec", "web", "sh; id")).toThrow("Invalid shell");
    expect(() => call("remove", "containers", "web")).toThrow("Invalid resource");
    expect(() => call("use-engine", "lxc")).toThrow("Invalid engine");
    expect(() => call("run", { image: "alpine", ports: ["x"] })).toThrow("Invalid port mapping");
    expect(engine.containerAction).not.toHaveBeenCalled();
    expect(engine.runDetached).not.toHaveBeenCalled();
  });

  it("routes actions, and opens interactive commands in a terminal", async () => {
    await call("container-action", "web", "restart");
    expect(engine.containerAction).toHaveBeenCalledWith("web", "restart");
    await call("remove-container", "web", true);
    expect(engine.removeContainer).toHaveBeenCalledWith("web", true);
    expect(await call("logs", "web", 500, true)).toBe("log");
    expect(engine.logs).toHaveBeenCalledWith("web", 500, true);
    expect(await call("run", { image: "alpine", interactive: false, name: "bg" })).toBe("newid");

    expect(await call("exec", "web", "bash")).toBe(true);
    expect(mocks.openInTerminal).toHaveBeenLastCalledWith(["/usr/bin/docker", "exec", "-it", "web", "bash"], "web bash", expect.anything());
    await call("attach", "old");
    expect(mocks.openInTerminal).toHaveBeenLastCalledWith(["/usr/bin/docker", "start", "-ai", "old"], "old", expect.anything());
    await call("run", { image: "ubuntu:24.04", interactive: true, remove: true, command: ["bash"] });
    expect(mocks.openInTerminal).toHaveBeenLastCalledWith(["/usr/bin/docker", "run", "-it", "--rm", "ubuntu:24.04", "bash"], "ubuntu:24.04", expect.anything());
    expect(engine.runDetached).toHaveBeenCalledTimes(1);

    expect(await call("start-engine", "podman")).toBe("started");
    await call("prune", "images", true);
    expect(engine.prune).toHaveBeenCalledWith("images", true);
  });
});
