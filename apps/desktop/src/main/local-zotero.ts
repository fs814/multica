import { readFileSync } from "node:fs";
import { homedir } from "node:os";
import { basename, join } from "node:path";
import { app, ipcMain, shell, type BrowserWindow, type IpcMainInvokeEvent } from "electron";
import { sharedKeyFile, ZoteroLibrary } from "./zotero-library";

/** WeChat 3.x for Windows keeps a custom "file location" in this ini (MyDocument: = default). */
function wechat3Root(): string | undefined {
  const appData = process.env.APPDATA;
  if (process.platform !== "win32" || !appData) return undefined;
  try {
    const value = readFileSync(join(appData, "Tencent", "WeChat", "All Users", "config", "3ebffe94.ini"), "utf8").trim();
    return value && value !== "MyDocument:" ? value : undefined;
  } catch {
    return undefined;
  }
}

export function setupLocalZotero(windowGetter: () => BrowserWindow | null): void {
  const home = homedir();
  const library = new ZoteroLibrary({
    platform: process.platform,
    home,
    env: { ...process.env, WECHAT3_FILE_ROOT: wechat3Root() },
    stateFile: join(app.getPath("userData"), "zotero-imports.json"),
    keyFile: sharedKeyFile(home),
    trash: (path) => shell.trashItem(path),
  });
  function requireMainRenderer(event: IpcMainInvokeEvent) {
    const main = windowGetter()?.webContents;
    if (!main || event.sender !== main || event.senderFrame !== main.mainFrame) {
      throw new Error("Zotero requires the main Desktop window");
    }
  }
  const text = (value: unknown, name: string) => {
    if (typeof value !== "string" || value.length > 512) throw new Error(`Invalid ${name}`);
    return value;
  };
  ipcMain.handle("local-zotero:list", (event) => { requireMainRenderer(event); return library.list(); });
  ipcMain.handle("local-zotero:status", (event) => { requireMainRenderer(event); return library.client.status(); });
  ipcMain.handle("local-zotero:authorize", (event) => { requireMainRenderer(event); return library.client.authorize(); });
  ipcMain.handle("local-zotero:collections", (event) => { requireMainRenderer(event); return library.collections(); });
  ipcMain.handle("local-zotero:ensure-collection", (event, path: unknown) => {
    requireMainRenderer(event);
    return library.ensureCollection(text(path, "collection"));
  });
  ipcMain.handle("local-zotero:import", (event, id: unknown, collectionKey: unknown, trashOriginal: unknown) => {
    requireMainRenderer(event);
    return library.importFile(text(id, "file id"), text(collectionKey, "collection"), trashOriginal === true);
  });
  ipcMain.handle("local-zotero:preview", (event, id: unknown) => {
    requireMainRenderer(event);
    const path = library.pathOf(text(id, "file id"));
    const window = windowGetter();
    if (process.platform === "darwin" && window) {
      // Quick Look panel: pdf, Office, iWork and images without leaving Multica.
      window.previewFile(path, basename(path));
      return true;
    }
    return shell.openPath(path).then((failure) => {
      if (failure) throw new Error(failure);
      return true;
    });
  });
  ipcMain.handle("local-zotero:rename", (event, id: unknown, name: unknown) => {
    requireMainRenderer(event);
    return library.renameFile(text(id, "file id"), text(name, "file name"));
  });
  ipcMain.handle("local-zotero:trash", (event, id: unknown) => {
    requireMainRenderer(event);
    return library.trashFile(text(id, "file id"));
  });
  ipcMain.handle("local-zotero:reveal", (event, id: unknown) => {
    requireMainRenderer(event);
    shell.showItemInFolder(library.pathOf(text(id, "file id")));
    return true;
  });
}
