// @vitest-environment node
import { expect, it, vi } from "vitest";
import type { BrowserWindow, WebPreferences } from "electron";
import { installWebSites, webSitePreferences } from "./web-sites";

function setup() {
  const on = vi.fn();
  installWebSites({ webContents: { on } } as unknown as BrowserWindow);
  return on;
}
it("rejects non-web URLs and the application's session before attaching", () => {
  const attach = setup().mock.calls.find(([name]) => name === "will-attach-webview")![1];
  for (const params of [{ src: "file:///private", partition: "persist:multica-web-sites" }, { src: "https://example.com", partition: "" }]) {
    const event = { preventDefault: vi.fn() };
    attach(event, {}, params);
    expect(event.preventDefault).toHaveBeenCalled();
  }
});
it("overrides guest attributes and removes preloads before loading a remote page", () => {
  const attach = setup().mock.calls.find(([name]) => name === "will-attach-webview")![1];
  const event = { preventDefault: vi.fn() };
  const preferences: WebPreferences = { preload: "/app/preload.js", nodeIntegration: true, webSecurity: false, sandbox: false };
  attach(event, preferences, { src: "http://localhost:8080", partition: "persist:multica-web-sites" });
  expect(event.preventDefault).not.toHaveBeenCalled();
  expect(preferences.preload).toBeUndefined();
  expect(preferences).toMatchObject(webSitePreferences());
});
it("keeps popup links in the embedded tab and blocks non-web navigation", () => {
  const attached = setup().mock.calls.find(([name]) => name === "did-attach-webview")![1];
  const guest = { on: vi.fn(), loadURL: vi.fn().mockResolvedValue(undefined), setWindowOpenHandler: vi.fn(), session: { setPermissionRequestHandler: vi.fn(), setPermissionCheckHandler: vi.fn() } };
  attached({}, guest);
  const popup = guest.setWindowOpenHandler.mock.calls[0]![0];
  expect(popup({ url: "https://example.com/login" })).toEqual({ action: "deny" });
  expect(guest.loadURL).toHaveBeenCalledWith("https://example.com/login");
  guest.loadURL.mockClear();
  expect(popup({ url: "javascript:alert(1)" })).toEqual({ action: "deny" });
  expect(guest.loadURL).not.toHaveBeenCalled();
  for (const type of ["will-navigate", "will-redirect"]) {
    const navigate = guest.on.mock.calls.find(([name]) => name === type)![1];
    const event = { preventDefault: vi.fn() };
    navigate(event, "file:///private");
    expect(event.preventDefault).toHaveBeenCalled();
  }
});
