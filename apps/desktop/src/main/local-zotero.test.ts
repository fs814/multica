// @vitest-environment node
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { BrowserWindow, IpcMainInvokeEvent } from "electron";
import { setupLocalZotero } from "./local-zotero";

const mocks = vi.hoisted(() => ({
  handlers: new Map<string, (event: IpcMainInvokeEvent, ...args: unknown[]) => unknown>(),
  importFile: vi.fn(), trashFile: vi.fn(), renameFile: vi.fn(), showItemInFolder: vi.fn(), openPath: vi.fn(async () => ""), pathOf: vi.fn(() => "/x/a.pdf"),
}));
vi.mock("electron", () => ({
  ipcMain: { handle: (channel: string, handler: (event: IpcMainInvokeEvent, ...args: unknown[]) => unknown) => mocks.handlers.set(channel, handler) },
  app: { getPath: () => "/tmp/user-data" },
  shell: { trashItem: vi.fn(), showItemInFolder: mocks.showItemInFolder, openPath: mocks.openPath },
}));
vi.mock("./zotero-library", () => ({
  sharedKeyFile: () => "/tmp/key.json",
  ZoteroLibrary: class {
    client = { status: vi.fn(), authorize: vi.fn() };
    list = vi.fn();
    collections = vi.fn();
    ensureCollection = vi.fn();
    importFile = mocks.importFile;
    pathOf = mocks.pathOf;
    trashFile = mocks.trashFile;
    renameFile = mocks.renameFile;
  },
}));

beforeEach(() => { vi.clearAllMocks(); mocks.handlers.clear(); });

describe("Zotero native boundary", () => {
  it("serves only the main window's top frame", () => {
    const webContents = { mainFrame: {} };
    setupLocalZotero(() => ({ webContents }) as BrowserWindow);
    expect([...mocks.handlers.keys()].sort()).toEqual([
      "local-zotero:authorize", "local-zotero:collections", "local-zotero:ensure-collection", "local-zotero:import",
      "local-zotero:list", "local-zotero:preview", "local-zotero:rename", "local-zotero:reveal", "local-zotero:status", "local-zotero:trash",
    ]);
    for (const handler of mocks.handlers.values()) {
      expect(() => handler({ sender: {} } as IpcMainInvokeEvent, "x")).toThrow("main Desktop window");
      expect(() => handler({ sender: webContents, senderFrame: {} } as IpcMainInvokeEvent, "x")).toThrow("main Desktop window");
    }
  });

  it("passes only string ids and a strict boolean for trashing", () => {
    const webContents = { mainFrame: {} };
    setupLocalZotero(() => ({ webContents }) as BrowserWindow);
    const event = { sender: webContents, senderFrame: webContents.mainFrame } as IpcMainInvokeEvent;
    const importHandler = mocks.handlers.get("local-zotero:import")!;
    importHandler(event, "id1", "AAAAAAAA", "yes");
    expect(mocks.importFile).toHaveBeenCalledWith("id1", "AAAAAAAA", false);
    expect(() => importHandler(event, { path: "/etc/passwd" }, "AAAAAAAA", true)).toThrow("Invalid file id");
    mocks.handlers.get("local-zotero:rename")!(event, "id1", "new.pdf");
    expect(mocks.renameFile).toHaveBeenCalledWith("id1", "new.pdf");
    expect(() => mocks.handlers.get("local-zotero:rename")!(event, "id1", ["../x"])).toThrow("Invalid file name");
    mocks.handlers.get("local-zotero:trash")!(event, "id1");
    expect(mocks.trashFile).toHaveBeenCalledWith("id1");
    expect(() => mocks.handlers.get("local-zotero:trash")!(event, 42)).toThrow("Invalid file id");
    mocks.handlers.get("local-zotero:reveal")!(event, "id1");
    expect(mocks.showItemInFolder).toHaveBeenCalledWith("/x/a.pdf");
  });

  it("previews with Quick Look on macOS and the default app elsewhere", async () => {
    const webContents = { mainFrame: {} };
    const previewFile = vi.fn();
    const platform = Object.getOwnPropertyDescriptor(process, "platform")!;
    const event = { sender: webContents, senderFrame: webContents.mainFrame } as IpcMainInvokeEvent;
    try {
      Object.defineProperty(process, "platform", { value: "darwin" });
      setupLocalZotero(() => ({ webContents, previewFile }) as unknown as BrowserWindow);
      expect(await mocks.handlers.get("local-zotero:preview")!(event, "id1")).toBe(true);
      expect(previewFile).toHaveBeenCalledWith("/x/a.pdf", "a.pdf");
      expect(mocks.openPath).not.toHaveBeenCalled();

      Object.defineProperty(process, "platform", { value: "win32" });
      mocks.handlers.clear();
      setupLocalZotero(() => ({ webContents, previewFile }) as unknown as BrowserWindow);
      await mocks.handlers.get("local-zotero:preview")!(event, "id1");
      expect(mocks.openPath).toHaveBeenCalledWith("/x/a.pdf");
      mocks.openPath.mockResolvedValueOnce("No application");
      await expect(mocks.handlers.get("local-zotero:preview")!(event, "id1")).rejects.toThrow("No application");
    } finally {
      Object.defineProperty(process, "platform", platform);
    }
  });
});
