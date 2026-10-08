import type { BrowserWindow } from "electron";

/** Best-effort notifications; the renderer re-queries state after loading. */
export function sendToLiveRenderer(
  window: BrowserWindow | null,
  channel: string,
  payload: unknown,
): void {
  if (!window) return;
  try {
    if (window.isDestroyed()) return;
    const contents = window.webContents;
    if (contents.isDestroyed() || contents.isCrashed() || contents.isLoadingMainFrame()) return;

    // A BrowserWindow can survive a renderer reload or crash. Checking the
    // window alone is insufficient; do not ask Electron to send to a dead frame.
    const frame = contents.mainFrame;
    if (frame.isDestroyed() || frame.detached) return;
    frame.send(channel, payload);
  } catch (error) {
    // Native objects can disappear between the lifetime check and access.
    if (error instanceof Error && (
      error.message.includes("Object has been destroyed") ||
      error.message.includes("Render frame was disposed before WebFrameMain could be accessed")
    )) return;
    throw error;
  }
}
