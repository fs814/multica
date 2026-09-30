import { z } from "zod";
import { parseWithFallback } from "./schema";
import { centerSyncActions, readCenterSyncResponse, type CenterSyncAction } from "./center-sync";

const userSchema = z.object({
  id: z.string().uuid(),
  email: z.string().min(1).max(320),
  name: z.string().max(1024).nullable().optional(),
});

export type CenterSyncUser = z.infer<typeof userSchema>;

export class CenterSyncRequestError extends Error {
  constructor(readonly reason: "network" | "timeout" | "http" | "invalid_response", readonly status?: number) {
    super(status ? `Sync server request failed (HTTP ${status})` : `Sync server request failed: ${reason}`);
    this.name = "CenterSyncRequestError";
  }
}

/** A second-center login, deliberately independent of the primary ApiClient.
 * Never reads shared cookies, workspace headers, auth stores or local storage.
 * Credentials stay in memory and are sent only to this instance's fixed origin.
 */
export class CenterSyncSession {
  readonly origin: string;
  private token: string | null = null;
  private user: CenterSyncUser | null = null;
  private generation = new AbortController();

  constructor(address: string, private readonly fetcher: typeof fetch = fetch) {
    const url = new URL(address.trim());
    if (!["http:", "https:"].includes(url.protocol) || url.username || url.password ||
        url.search || url.hash || url.pathname !== "/" || url.port === "0" ||
        ["0.0.0.0", "[::]", "255.255.255.255"].includes(url.hostname)) {
      throw new Error("Use an HTTP or HTTPS server origin");
    }
    this.origin = url.origin;
  }

  disconnect(): void {
    this.generation.abort();
    this.generation = new AbortController();
    this.token = null;
    this.user = null;
  }

  get currentUser(): CenterSyncUser | null { return this.user; }

  async syncRequest(action: CenterSyncAction, body: unknown, signal: AbortSignal): Promise<unknown> {
    if (!this.token || !this.user || !centerSyncActions.includes(action)) throw new Error("Connect for sync first");
    return this.request(`/api/center-sync/${action}`, body, this.token, signal);
  }

  async checkConnection(): Promise<CenterSyncUser | null> {
    if (!this.token) return null;
    const generation = this.generation;
    try {
      const raw = await this.request("/api/me", undefined, this.token);
      const user = parseWithFallback<CenterSyncUser | null>(raw, userSchema, null, { endpoint: "sync-center/api/me" });
      if (!user || user.id !== this.user?.id) throw new Error("Server sign-in identity mismatch");
      return user;
    } catch (error) {
      if (this.generation === generation) this.disconnect();
      throw error;
    }
  }

  async sendCode(email: string): Promise<void> {
    await this.request("/auth/send-code", { email }, null);
  }

  async verifyCode(email: string, code: string): Promise<CenterSyncUser> {
    const generation = this.generation;
    const raw = await this.request("/auth/verify-code", { email, code }, null);
    // The shared schema logger includes rejected values. Never give it a login
    // response containing a credential; validate only a redacted projection.
    const response = raw && typeof raw === "object" ? raw as Record<string, unknown> : {};
    const token = response.token;
    const validToken = typeof token === "string" && token.length > 0 && token.length <= 8192 && !/\s/.test(token);
    const login = parseWithFallback<{ validToken: true; user: CenterSyncUser } | null>(
      { validToken, user: response.user },
      z.object({ validToken: z.literal(true), user: userSchema }),
      null,
      { endpoint: "sync-center/auth/verify-code" },
    );
    if (!login || !validToken) throw new Error("Invalid server sign-in response");
    generation.signal.throwIfAborted();
    // Verify the session with the peer before presenting Connected. A malformed
    // or failed /me response never commits a half-authenticated local session.
    const me = await this.request("/api/me", undefined, token);
    const user = parseWithFallback<CenterSyncUser | null>(me, userSchema, null, { endpoint: "sync-center/api/me" });
    if (!user || user.id !== login.user.id) throw new Error("Server sign-in identity mismatch");
    generation.signal.throwIfAborted();
    this.token = token;
    this.user = user;
    return user;
  }

  private async request(path: string, body: unknown, token: string | null, signal?: AbortSignal): Promise<unknown> {
    const controller = this.generation;
    const requestSignal = AbortSignal.any([controller.signal, AbortSignal.timeout(30_000), ...(signal ? [signal] : [])]);
    let response: Response;
    try {
      response = await this.fetcher(this.origin + path, {
        method: body === undefined ? "GET" : "POST",
        headers: { "Content-Type": "application/json", ...(token ? { Authorization: `Bearer ${token}` } : {}) },
        body: body === undefined ? undefined : JSON.stringify(body),
        credentials: "omit",
        redirect: "error",
        cache: "no-store",
        signal: requestSignal,
      });
    } catch {
      controller.signal.throwIfAborted();
      signal?.throwIfAborted();
      // Do not include arbitrary fetch errors: they may contain request data.
      throw new CenterSyncRequestError(requestSignal.aborted ? "timeout" : "network");
    }
    controller.signal.throwIfAborted();
    if (!response.ok) {
      if (response.status === 401 && token && token === this.token) this.disconnect();
      throw new CenterSyncRequestError("http", response.status);
    }
    if (response.status === 204) return null;
    let raw: unknown;
    try { raw = path.startsWith("/api/center-sync/") ? await readCenterSyncResponse(response) : await response.json(); }
    catch {
      controller.signal.throwIfAborted();
      signal?.throwIfAborted();
      throw new CenterSyncRequestError(requestSignal.aborted ? "timeout" : "invalid_response");
    }
    controller.signal.throwIfAborted();
    return raw;
  }
}
