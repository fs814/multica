// @vitest-environment node
import { describe, expect, it, vi } from "vitest";
import { CenterSyncRequestError, CenterSyncSession } from "./center-sync-session";

const user = { id: "12345678-1234-4234-8234-123456789012", email: "owner@example.test", name: "Owner" };
const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status });

describe("independent sync-center sign-in", () => {
  it("lists workspaces with only its own source login and clears an expired source session", async () => {
    const workspaces = [{ id: user.id, name: 'Source workspace' }];
    const fetcher = vi.fn<typeof fetch>()
      .mockResolvedValueOnce(json({ token: 'source-https-session', user }))
      .mockResolvedValueOnce(json(user))
      .mockResolvedValueOnce(json(workspaces))
      .mockResolvedValueOnce(json({}, 401));
    const session = new CenterSyncSession('https://source.example', fetcher);
    await expect(session.listWorkspaces()).rejects.toThrow('Connect to the sync source first');
    expect(fetcher).not.toHaveBeenCalled();
    await session.verifyCode(user.email, '123456');
    expect(await session.listWorkspaces()).toEqual(workspaces);
    expect(fetcher).toHaveBeenLastCalledWith('https://source.example/api/workspaces', expect.objectContaining({
      method: 'GET', credentials: 'omit', redirect: 'error', headers: { 'Content-Type': 'application/json', Authorization: 'Bearer source-https-session' },
    }));
    await expect(session.listWorkspaces()).rejects.toThrow('401');
    expect(session.currentUser).toBeNull();
  });
  it("authenticates only at the chosen origin without ambient cookies or workspace headers", async () => {
    const fetcher = vi.fn<typeof fetch>()
      .mockResolvedValueOnce(json({}))
      .mockResolvedValueOnce(json({ token: "destination-session", user }))
      .mockResolvedValueOnce(json(user));
    const session = new CenterSyncSession("https://peer.example/", fetcher);
    await session.sendCode(user.email);
    expect(await session.verifyCode(user.email, "123456")).toEqual(user);
    expect(session.currentUser).toEqual(user);
    expect(fetcher.mock.calls.map(([url]) => url)).toEqual([
      "https://peer.example/auth/send-code", "https://peer.example/auth/verify-code", "https://peer.example/api/me",
    ]);
    for (const [, init] of fetcher.mock.calls) {
      expect(init).toMatchObject({ credentials: "omit", redirect: "error", cache: "no-store" });
      expect(init?.headers).not.toHaveProperty("X-Workspace-Slug");
      expect(init?.headers).not.toHaveProperty("X-CSRF-Token");
    }
    expect(fetcher.mock.calls[0]![1]?.headers).not.toHaveProperty("Authorization");
    expect(fetcher.mock.calls[1]![1]?.headers).not.toHaveProperty("Authorization");
    expect(fetcher.mock.calls[2]![1]?.headers).toHaveProperty("Authorization", "Bearer destination-session");
    session.disconnect();
    expect(session.currentUser).toBeNull();
  });

  it.each([{}, { token: "secret", user: {} }, { token: "bad token", user }])("rejects malformed login responses", async response => {
    const fetcher = vi.fn<typeof fetch>().mockResolvedValue(json(response));
    const session = new CenterSyncSession("https://peer.example", fetcher);
    await expect(session.verifyCode(user.email, "123456")).rejects.toThrow("Invalid server sign-in response");
    expect(session.currentUser).toBeNull();
    expect(fetcher).toHaveBeenCalledOnce();
  });

  it("does not connect when the authenticated identity differs from the login response", async () => {
    const fetcher = vi.fn<typeof fetch>()
      .mockResolvedValueOnce(json({ token: "session", user }))
      .mockResolvedValueOnce(json({ ...user, id: "87654321-1234-4234-8234-123456789012" }));
    const session = new CenterSyncSession("https://peer.example", fetcher);
    await expect(session.verifyCode(user.email, "123456")).rejects.toThrow("identity mismatch");
    expect(session.currentUser).toBeNull();
  });

  it("does not connect when the session is rejected", async () => {
    const fetcher = vi.fn<typeof fetch>()
      .mockResolvedValueOnce(json({ token: "session", user }))
      .mockResolvedValueOnce(json({}, 401));
    const session = new CenterSyncSession("https://peer.example", fetcher);
    await expect(session.verifyCode(user.email, "123456")).rejects.toThrow("401");
    expect(session.currentUser).toBeNull();
  });

  it("clears only its own session when a later connection check expires", async () => {
    const fetcher = vi.fn<typeof fetch>()
      .mockResolvedValueOnce(json({ token: "session", user }))
      .mockResolvedValueOnce(json(user))
      .mockResolvedValueOnce(json({}, 401));
    const session = new CenterSyncSession("https://peer.example", fetcher);
    await session.verifyCode(user.email, "123456");
    await expect(session.checkConnection()).rejects.toThrow("401");
    expect(session.currentUser).toBeNull();
    expect(await session.checkConnection()).toBeNull();
    expect(fetcher).toHaveBeenCalledTimes(3);
  });

  it("ignores a login response that arrives after disconnect", async () => {
    let finish!: (response: Response) => void;
    const fetcher = vi.fn<typeof fetch>().mockImplementation(() => new Promise(resolve => { finish = resolve; }));
    const session = new CenterSyncSession("https://peer.example", fetcher);
    const pending = session.verifyCode(user.email, "123456");
    session.disconnect();
    finish(json({ token: "session", user }));
    await expect(pending).rejects.toThrow();
    expect(session.currentUser).toBeNull();
    expect(fetcher).toHaveBeenCalledOnce();
  });

  it("uses only the peer login for sync and aborts it on disconnect", async () => {
    let requestSignal: AbortSignal | null | undefined;
    const fetcher = vi.fn<typeof fetch>()
      .mockResolvedValueOnce(json({ token: "peer-only-session", user }))
      .mockResolvedValueOnce(json(user))
      .mockImplementationOnce((_url, init) => {
        requestSignal = init?.signal;
        return new Promise((_resolve, reject) => requestSignal?.addEventListener("abort", () => reject(new Error("Disconnected")), { once: true }));
      });
    const session = new CenterSyncSession("https://peer.example", fetcher);
    await session.verifyCode(user.email, "123456");
    const pending = session.syncRequest("info", {}, new AbortController().signal);
    expect(fetcher.mock.calls[2]![1]?.headers).toHaveProperty("Authorization", "Bearer peer-only-session");
    session.disconnect();
    await expect(pending).rejects.toThrow(/abort/i);
    expect(requestSignal?.aborted).toBe(true);
    await expect(session.syncRequest("info", {}, new AbortController().signal)).rejects.toThrow("Connect for sync first");
    expect(fetcher).toHaveBeenCalledTimes(3);
  });

  it.each(["https://user:password@peer.example", "https://peer.example/path", "https://peer.example/?redirect=evil", "file:///tmp/server", "http://0.0.0.0"]) ("rejects unsafe address %s", address => {
    expect(() => new CenterSyncSession(address)).toThrow();
  });

  it.each([400, 401, 403, 404, 429, 500, 503])("preserves HTTP %s without exposing server response contents", async status => {
    const fetcher = vi.fn<typeof fetch>().mockResolvedValueOnce(json({ error: "private-server-details" }, status));
    const session = new CenterSyncSession("http://peer.example", fetcher);
    const error = await session.sendCode(user.email).catch(error => error);
    expect(error).toBeInstanceOf(CenterSyncRequestError);
    expect(error).toMatchObject({ reason: "http", status });
    expect(error.message).not.toContain("private-server-details");
    expect(session.currentUser).toBeNull();
    expect(fetcher).toHaveBeenCalledOnce();
  });

  it("distinguishes a failed fetch from an HTTP error without exposing request data", async () => {
    const fetcher = vi.fn<typeof fetch>().mockRejectedValue(new TypeError("secret-code-and-email"));
    const session = new CenterSyncSession("http://peer.example", fetcher);
    await expect(session.sendCode(user.email)).rejects.toMatchObject({ reason: "network", message: "Sync server request failed: network" });
  });

  it("reports a timeout separately and leaves sign-in retryable", async () => {
    const timeout = new AbortController();
    const spy = vi.spyOn(AbortSignal, "timeout").mockReturnValue(timeout.signal);
    try {
      const fetcher = vi.fn<typeof fetch>().mockImplementationOnce(async () => { timeout.abort(); throw new Error("private detail"); });
      const session = new CenterSyncSession("http://peer.example", fetcher);
      await expect(session.sendCode(user.email)).rejects.toMatchObject({ reason: "timeout" });
      expect(session.currentUser).toBeNull();
    } finally { spy.mockRestore(); }
  });

  it("reports invalid proxy responses without displaying or logging their body", async () => {
    const fetcher = vi.fn<typeof fetch>().mockResolvedValueOnce(new Response("<html>private proxy details</html>"));
    const session = new CenterSyncSession("http://peer.example", fetcher);
    await expect(session.sendCode(user.email)).rejects.toMatchObject({ reason: "invalid_response", message: "Sync server request failed: invalid_response" });
    expect(session.currentUser).toBeNull();
  });
});
