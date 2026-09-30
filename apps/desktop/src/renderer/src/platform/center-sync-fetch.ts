import { CenterSyncRequestError } from "@multica/core/api/center-sync-session";
import type { CenterSyncTransport } from "../../../shared/center-sync-transport";

/** Only used by the independent peer session; never replaces the source API. */
export function createCenterSyncFetch(origin: string, transport: CenterSyncTransport): typeof fetch {
  return async (input, init) => {
    if (typeof input !== "string") throw new Error("Invalid sync URL");
    const url = new URL(input);
    if (url.origin !== origin || url.username || url.password || url.search || url.hash ||
        (init?.body !== undefined && typeof init.body !== "string") ||
        (init?.method ?? "GET") !== (init?.body === undefined ? "GET" : "POST")) throw new Error("Invalid sync request");
    init?.signal?.throwIfAborted();
    const id = crypto.randomUUID();
    const authorization = new Headers(init?.headers).get("Authorization");
    if (authorization && !authorization.startsWith("Bearer ")) throw new Error("Invalid sync session");
    // Enqueue the request before registering cancellation so IPC preserves order.
    const pending = transport.syncRequest({ id, origin, path: url.pathname,
      body: init?.body as string | undefined, token: authorization?.slice(7) });
    const cancel = () => { void transport.cancelSyncRequest(id).catch(() => {}); };
    init?.signal?.addEventListener("abort", cancel, { once: true });
    if (init?.signal?.aborted) cancel();
    try {
      const result = await pending;
      init?.signal?.throwIfAborted();
      if (!result.ok) {
        if (result.reason === "aborted") throw new DOMException("Sync request cancelled", "AbortError");
        throw new CenterSyncRequestError(result.reason === "timeout" ? "timeout" : result.reason === "network" ? "network" : "invalid_response");
      }
      return new Response([204, 205, 304].includes(result.status) ? null : result.body, { status: result.status });
    } finally { init?.signal?.removeEventListener("abort", cancel); }
  };
}
