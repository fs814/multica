// @vitest-environment node
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { BrowserWindow, IpcMainInvokeEvent } from "electron";
import { setupLocalTools } from "./local-tools";

const mocks = vi.hoisted(() => ({
  handlers: new Map<string, (event: IpcMainInvokeEvent, value?: string) => unknown>(),
  catalog: vi.fn(), run: vi.fn(), stopAll: vi.fn(), on: vi.fn(), quit: vi.fn(),
}));
vi.mock("electron", () => ({
  ipcMain: { handle: (channel: string, handler: (event: IpcMainInvokeEvent, value?: string) => unknown) => mocks.handlers.set(channel, handler) },
  app: { on: mocks.on, quit: mocks.quit },
}));
vi.mock("./local-tools-runner", () => ({
  LocalToolsRunner: class {
    catalog = mocks.catalog;
    run = mocks.run;
    stopAll = mocks.stopAll;
  },
}));

beforeEach(() => { vi.clearAllMocks(); mocks.handlers.clear(); });

describe("Tools native boundary", () => {
  it("allows only the main window's top frame to read or execute local scripts", () => {
    const webContents = { mainFrame: {} };
    setupLocalTools(() => ({ webContents }) as BrowserWindow);
    for (const handler of mocks.handlers.values()) {
      expect(() => handler({ sender: {} } as IpcMainInvokeEvent, "run.sh")).toThrow("main Desktop window");
      expect(() => handler({ sender: webContents, senderFrame: {} } as IpcMainInvokeEvent, "run.sh")).toThrow("main Desktop window");
    }
    const event = { sender: webContents, senderFrame: webContents.mainFrame } as IpcMainInvokeEvent;
    mocks.handlers.get("local-tools:catalog")!(event);
    mocks.handlers.get("local-tools:run")!(event, "game/run.sh");
    expect(mocks.catalog).toHaveBeenCalledTimes(1);
    expect(mocks.run).toHaveBeenCalledWith("game/run.sh");
  });
  it("waits for local processes to stop before quitting", async () => {
    let finish!: () => void;
    mocks.stopAll.mockImplementation(() => new Promise<void>((resolve) => { finish = resolve; }));
    setupLocalTools(() => null);
    const beforeQuit = mocks.on.mock.calls.find(([event]) => event === "before-quit")![1] as (event: { preventDefault(): void }) => void;
    const event = { preventDefault: vi.fn() };
    beforeQuit(event);
    expect(event.preventDefault).toHaveBeenCalled();
    expect(mocks.quit).not.toHaveBeenCalled();
    finish();
    await vi.waitFor(() => expect(mocks.quit).toHaveBeenCalledTimes(1));
  });
});
