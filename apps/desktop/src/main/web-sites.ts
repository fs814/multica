import type { BrowserWindow, WebPreferences } from "electron";
import { normalizeWebUrl } from "@multica/core/web-links/models";

/** Remote tabs never inherit the application renderer's preload or session. */
export function webSitePreferences(): WebPreferences {
  return {
    sandbox: true,
    contextIsolation: true,
    nodeIntegration: false,
    nodeIntegrationInSubFrames: false,
    nodeIntegrationInWorker: false,
    webSecurity: true,
    allowRunningInsecureContent: false,
    webviewTag: false,
    partition: "persist:multica-web-sites",
  };
}

export function installWebSites(window: BrowserWindow): void {
  window.webContents.on("will-attach-webview", (event, preferences, params) => {
    if (!normalizeWebUrl(params.src) || params.partition !== "persist:multica-web-sites") { event.preventDefault(); return; }
    // Never trust webview attributes to enforce the guest's isolation.
    delete preferences.preload;
    Object.assign(preferences, webSitePreferences());
    params.partition = "persist:multica-web-sites";
  });
  window.webContents.on("did-attach-webview", (_event, guest) => {
    guest.session.setPermissionRequestHandler((_contents, _permission, callback) => callback(false));
    guest.session.setPermissionCheckHandler(() => false);
    guest.on("will-navigate", (event, url) => {
      if (!normalizeWebUrl(url)) event.preventDefault();
    });
    guest.on("will-redirect", (event, url) => {
      if (!normalizeWebUrl(url)) event.preventDefault();
    });
    // Keep target=_blank links inside the current website tab as well.
    guest.setWindowOpenHandler(({ url }) => {
      const target = normalizeWebUrl(url);
      if (target) void guest.loadURL(target).catch(() => {});
      return { action: "deny" };
    });
  });
}
