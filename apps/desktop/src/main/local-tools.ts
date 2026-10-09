import { app, ipcMain, type BrowserWindow, type IpcMainInvokeEvent } from "electron";
import { LocalToolsRunner } from "./local-tools-runner";

export function setupLocalTools(windowGetter: () => BrowserWindow | null): void {
  const runner = new LocalToolsRunner();
  function requireMainRenderer(event: IpcMainInvokeEvent) {
    const main = windowGetter()?.webContents;
    if (!main || event.sender !== main || event.senderFrame !== main.mainFrame) {
      throw new Error("Tools requires the main Desktop window");
    }
  }
  ipcMain.handle("local-tools:catalog", (event) => { requireMainRenderer(event); return runner.catalog(); });
  ipcMain.handle("local-tools:runs", (event) => { requireMainRenderer(event); return runner.runs(); });
  ipcMain.handle("local-tools:run", (event, path: string) => { requireMainRenderer(event); return runner.run(path); });
  ipcMain.handle("local-tools:output", (event, id: string) => { requireMainRenderer(event); return runner.output(id); });
  ipcMain.handle("local-tools:stop", (event, id: string) => { requireMainRenderer(event); return runner.stop(id); });
  let shuttingDown = false;
  let stopped = false;
  app.on("before-quit", (event) => {
    if (stopped) return;
    event.preventDefault();
    if (shuttingDown) return;
    shuttingDown = true;
    void runner.stopAll().then(() => {
      stopped = true;
      app.quit();
    }).catch((error: unknown) => {
      shuttingDown = false;
      console.error("Unable to stop local Tools scripts before quitting", error);
    });
  });
}
