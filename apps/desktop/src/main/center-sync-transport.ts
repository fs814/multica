import { centerSyncActions } from "@multica/core/api/center-sync";
import type { BrowserWindow, IpcMain, IpcMainInvokeEvent, WebContents } from "electron";
import { normalizeCenterUrl } from "../shared/center-settings";
import type { CenterSyncTransportRequest, CenterSyncTransportResult } from "../shared/center-sync-transport";

const capacity = 32 * 1024 * 1024;
const requestID = /^[a-f0-9-]{36}$/;

function parseRequest(raw: unknown, peer: string, source: string): CenterSyncTransportRequest {
  if (!raw || typeof raw !== "object") throw new Error("Invalid request");
  const value = raw as Record<string, unknown>;
  if (typeof value.id !== "string" || !requestID.test(value.id) || typeof value.path !== "string" ||
      value.origin !== peer || peer === source || normalizeCenterUrl(peer) !== peer ||
      (value.body !== undefined && (typeof value.body !== "string" || Buffer.byteLength(value.body) > capacity)) ||
      (value.token !== undefined && (typeof value.token !== "string" || !value.token || value.token.length > 8192 || /\s/.test(value.token)))) {
    throw new Error("Invalid request");
  }
  const path = value.path;
  if (path === "/auth/send-code" || path === "/auth/verify-code") {
    if (value.token !== undefined || typeof value.body !== "string" || value.body.length > 2048) throw new Error("Invalid sign-in request");
    const body: unknown = JSON.parse(value.body);
    if (!body || typeof body !== "object" || Array.isArray(body)) throw new Error("Invalid sign-in request");
    const input = body as Record<string, unknown>;
    const verify = path === "/auth/verify-code";
    if (typeof input.email !== "string" || !input.email || input.email.length > 320 ||
        Object.keys(input).some(key => !["email", ...(verify ? ["code"] : [])].includes(key)) ||
        (verify && (typeof input.code !== "string" || !/^\d{6}$/.test(input.code)))) throw new Error("Invalid sign-in request");
  } else if (path === "/api/me") {
    if (!value.token || value.body !== undefined) throw new Error("Invalid identity request");
  } else {
    // No recovery, arbitrary proxy paths, URL parameters, redirects or cookies.
    if (!peer.startsWith("https:") || !value.token || typeof value.body !== "string" ||
        !centerSyncActions.some(action => path === `/api/center-sync/${action}`)) throw new Error("Invalid sync request");
    const body: unknown = JSON.parse(value.body);
    if (!body || typeof body !== "object" || Array.isArray(body)) throw new Error("Invalid sync request");
  }
  return { id: value.id, origin: peer, path, body: value.body as string | undefined, token: value.token as string | undefined };
}

/** Native requests are bound to the user-saved peer, not a renderer-provided URL.
 * No credentials are persisted, borrowed from the source or sent in redirects.
 */
export function createCenterSyncTransport(target: () => { peer: string; source: string }, fetcher: typeof fetch = fetch) {
  const pending = new Map<string, AbortController>();
  return {
    cancel(id: unknown) { if (typeof id === "string") pending.get(id)?.abort(); },
    cancelAll() { for (const controller of pending.values()) controller.abort(); },
    async request(raw: unknown): Promise<CenterSyncTransportResult> {
      const selected = target();
      let input: CenterSyncTransportRequest;
      try { input = parseRequest(raw, selected.peer, selected.source); }
      catch { return { ok: false, reason: "invalid_request" }; }
      if (pending.size >= 4 || pending.has(input.id)) return { ok: false, reason: "capacity" };
      const controller = new AbortController();
      const timeout = AbortSignal.timeout(30_000);
      pending.set(input.id, controller);
      try {
        const response = await fetcher(input.origin + input.path, {
          method: input.body === undefined ? "GET" : "POST",
          body: input.body,
          headers: { "Content-Type": "application/json", ...(input.token ? { Authorization: `Bearer ${input.token}` } : {}) },
          credentials: "omit", redirect: "error", cache: "no-store",
          signal: AbortSignal.any([controller.signal, timeout]),
        });
        const limit = input.path.startsWith("/api/center-sync/") ? capacity : 64 * 1024;
        const reader = response.body?.getReader();
        const chunks: Uint8Array[] = [];
        let size = 0;
        try {
          if (Number(response.headers.get("Content-Length")) > limit) return { ok: false, reason: "capacity" };
          if (reader) for (;;) {
            const part = await reader.read();
            if (part.done) break;
            size += part.value.byteLength;
            if (size > limit) return { ok: false, reason: "capacity" };
            chunks.push(part.value);
          }
        } finally { await reader?.cancel(); reader?.releaseLock(); }
        if (controller.signal.aborted || target().peer !== selected.peer || target().source !== selected.source) return { ok: false, reason: "aborted" };
        return { ok: true, status: response.status, body: Buffer.concat(chunks).toString("utf8") };
      } catch {
        return { ok: false, reason: controller.signal.aborted ? "aborted" : timeout.aborted ? "timeout" : "network" };
      } finally { pending.delete(input.id); }
    },
  };
}

export function registerCenterSyncTransport(ipc: Pick<IpcMain, "handle">, window: () => BrowserWindow | null, transport: ReturnType<typeof createCenterSyncTransport>) {
  const senders = new WeakSet<WebContents>();
  const authorize = (event: IpcMainInvokeEvent) => {
    const main = window()?.webContents;
    if (!main || event.sender !== main || event.senderFrame !== main.mainFrame) throw new Error("Sync requires the main Desktop window");
  };
  ipc.handle("center:sync-request", (event, request: unknown) => {
    authorize(event);
    if (!senders.has(event.sender)) {
      senders.add(event.sender);
      event.sender.once("destroyed", () => transport.cancelAll());
      event.sender.on("did-start-navigation", (_event, _url, isInPlace, isMainFrame) => {
        if (isMainFrame && !isInPlace) transport.cancelAll();
      });
    }
    return transport.request(request);
  });
  ipc.handle("center:sync-cancel", (event, id: unknown) => { authorize(event); transport.cancel(id); });
}
