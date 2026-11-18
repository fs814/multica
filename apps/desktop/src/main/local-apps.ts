import { execFile } from "node:child_process";
import { homedir } from "node:os";
import { app, ipcMain, shell, type BrowserWindow, type IpcMainInvokeEvent } from "electron";
import { AppsCatalog } from "./apps-catalog";

/** Linux .desktop entries are launched by id; other platforms open the bundle or shortcut. */
function launchDesktopEntry(path: string): Promise<void> {
  return new Promise((resolve, reject) => {
    execFile("gio", ["launch", path], (error) => {
      if (!error) return resolve();
      const id = path.split("/").pop()!;
      execFile("gtk-launch", [id], (fallback) => (fallback ? reject(new Error(`Could not launch ${id}`)) : resolve()));
    });
  });
}

export function setupLocalApps(windowGetter: () => BrowserWindow | null): void {
  const catalog = new AppsCatalog({
    platform: process.platform,
    home: homedir(),
    env: process.env,
    iconFor: async (path) => {
      const icon = await app.getFileIcon(path, { size: "normal" });
      return icon.isEmpty() ? null : icon.toDataURL();
    },
  });
  function requireMainRenderer(event: IpcMainInvokeEvent) {
    const main = windowGetter()?.webContents;
    if (!main || event.sender !== main || event.senderFrame !== main.mainFrame) {
      throw new Error("Apps requires the main Desktop window");
    }
  }
  const idOf = (value: unknown) => {
    if (typeof value !== "string" || value.length > 64) throw new Error("Invalid app id");
    return value;
  };
  ipcMain.handle("local-apps:list", (event) => { requireMainRenderer(event); return catalog.list(); });
  ipcMain.handle("local-apps:launch", (event, id: unknown) => {
    requireMainRenderer(event);
    const appId = idOf(id);
    return catalog.launchable(appId).then(async (path) => {
      if (process.platform === "linux" && path.endsWith(".desktop")) {
        await launchDesktopEntry(path);
      } else {
        const failure = await shell.openPath(path);
        if (failure) throw new Error(failure);
      }
      return true;
    });
  });
  ipcMain.handle("local-apps:intro", (event, id: unknown) => {
    requireMainRenderer(event);
    return catalog.intro(idOf(id));
  });
  ipcMain.handle("local-apps:keymap", (event, id: unknown) => {
    requireMainRenderer(event);
    return catalog.keymap(idOf(id));
  });
  ipcMain.handle("local-apps:reveal", (event, id: unknown) => {
    requireMainRenderer(event);
    shell.showItemInFolder(catalog.pathOf(idOf(id)));
    return true;
  });
}
