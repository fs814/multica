// @vitest-environment node
import type { BrowserWindow } from "electron";
import { describe, expect, it, vi } from "vitest";
import { sendToLiveRenderer } from "./renderer-send";

function fixture() {
  const frame = { isDestroyed: vi.fn(() => false), detached: false, send: vi.fn() };
  const webContents = {
    isDestroyed: vi.fn(() => false), isCrashed: vi.fn(() => false),
    isLoadingMainFrame: vi.fn(() => false), mainFrame: frame, send: vi.fn(),
  };
  const window = { isDestroyed: vi.fn(() => false), webContents };
  const send = () => sendToLiveRenderer(window as unknown as BrowserWindow, "daemon:status", { state: "running" });
  return { frame, webContents, window, send };
}

describe("renderer notification lifetime", () => {
  it("ignores an absent window", () => {
    expect(() => sendToLiveRenderer(null, "daemon:status", {})).not.toThrow();
  });

  it.each(["window", "contents", "crashed", "loading", "frame", "detached"])(
    "does not send to an unavailable renderer: %s", state => {
      const f = fixture();
      if (state === "window") f.window.isDestroyed.mockReturnValue(true);
      if (state === "contents") f.webContents.isDestroyed.mockReturnValue(true);
      if (state === "crashed") f.webContents.isCrashed.mockReturnValue(true);
      if (state === "loading") f.webContents.isLoadingMainFrame.mockReturnValue(true);
      if (state === "frame") f.frame.isDestroyed.mockReturnValue(true);
      if (state === "detached") f.frame.detached = true;
      f.send();
      expect(f.frame.send).not.toHaveBeenCalled();
      expect(f.webContents.send).not.toHaveBeenCalled();
    },
  );

  it("sends through the exact frame that was checked", () => {
    const f = fixture();
    f.send();
    expect(f.frame.send).toHaveBeenCalledWith("daemon:status", { state: "running" });
    expect(f.webContents.send).not.toHaveBeenCalled();
  });

  it("resumes notifications to a new frame after reload", () => {
    const f = fixture();
    f.frame.detached = true;
    f.send();
    const replacement = { isDestroyed: vi.fn(() => false), detached: false, send: vi.fn() };
    f.webContents.mainFrame = replacement;
    f.send();
    expect(f.frame.send).not.toHaveBeenCalled();
    expect(replacement.send).toHaveBeenCalledTimes(1);
  });

  it.each([
    "Object has been destroyed",
    "Render frame was disposed before WebFrameMain could be accessed",
  ])("tolerates disposal during frame access: %s", message => {
    const f = fixture();
    Object.defineProperty(f.webContents, "mainFrame", { get: () => { throw new Error(message); } });
    expect(f.send).not.toThrow();
    expect(f.webContents.send).not.toHaveBeenCalled();
  });

  it("does not swallow unrelated send errors", () => {
    const f = fixture();
    f.frame.send.mockImplementation(() => { throw new Error("could not be cloned"); });
    expect(f.send).toThrow("could not be cloned");
  });
});
