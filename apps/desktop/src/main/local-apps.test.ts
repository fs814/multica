// @vitest-environment node
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { BrowserWindow, IpcMainInvokeEvent } from "electron";
import { setupLocalApps } from "./local-apps";

const mocks = vi.hoisted(() => ({
  handlers: new Map<string, (event: IpcMainInvokeEvent, ...args: unknown[]) => unknown>(),
  openPath: vi.fn(async () => ""), showItemInFolder: vi.fn(),
  launchable: vi.fn(async () => "/Applications/Xcode.app"), pathOf: vi.fn(() => "/Applications/Xcode.app"), list: vi.fn(),
}));
vi.mock("electron", () => ({
  ipcMain: { handle: (channel: string, handler: (event: IpcMainInvokeEvent, ...args: unknown[]) => unknown) => mocks.handlers.set(channel, handler) },
  app: { getFileIcon: vi.fn() },
  shell: { openPath: mocks.openPath, showItemInFolder: mocks.showItemInFolder },
}));
vi.mock("./apps-catalog", () => ({
  AppsCatalog: class { list = mocks.list; launchable = mocks.launchable; pathOf = mocks.pathOf; keymap = vi.fn(); intro = vi.fn(); },
}));

beforeEach(() => { vi.clearAllMocks(); mocks.handlers.clear(); });

describe("Apps native boundary", () => {
  it("serves only the main window's top frame", () => {
    const webContents = { mainFrame: {} };
    setupLocalApps(() => ({ webContents }) as BrowserWindow);
    expect([...mocks.handlers.keys()].sort()).toEqual(["local-apps:intro", "local-apps:keymap", "local-apps:launch", "local-apps:list", "local-apps:reveal"]);
    for (const handler of mocks.handlers.values()) {
      expect(() => handler({ sender: {} } as IpcMainInvokeEvent, "x")).toThrow("main Desktop window");
    }
  });

  it("launches and reveals by id only, and surfaces an OS launch failure", async () => {
    const webContents = { mainFrame: {} };
    setupLocalApps(() => ({ webContents }) as BrowserWindow);
    const event = { sender: webContents, senderFrame: webContents.mainFrame } as IpcMainInvokeEvent;
    expect(() => mocks.handlers.get("local-apps:launch")!(event, { path: "/bin/sh" })).toThrow("Invalid app id");
    if (process.platform !== "linux") {
      expect(await mocks.handlers.get("local-apps:launch")!(event, "id1")).toBe(true);
      expect(mocks.openPath).toHaveBeenCalledWith("/Applications/Xcode.app");
      mocks.openPath.mockResolvedValueOnce("The application can’t be opened.");
      await expect(mocks.handlers.get("local-apps:launch")!(event, "id1")).rejects.toThrow("can’t be opened");
    }
    mocks.handlers.get("local-apps:reveal")!(event, "id1");
    expect(mocks.showItemInFolder).toHaveBeenCalledWith("/Applications/Xcode.app");
  });
});
