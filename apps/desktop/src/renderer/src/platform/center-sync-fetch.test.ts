// @vitest-environment node
import { describe, expect, it, vi } from "vitest";
import { CenterSyncSession } from "@multica/core/api/center-sync-session";
import { createCenterSyncFetch } from "./center-sync-fetch";
import type { CenterSyncTransport } from "../../../shared/center-sync-transport";

const origin = "http://peer.example:18080";
const user = { id: "12345678-1234-4234-8234-123456789012", email: "owner@example.test" };

describe("Desktop sync fetch adapter", () => {
  it("completes peer sign-in over IPC without invoking renderer fetch", async () => {
    const rendererFetch = vi.spyOn(globalThis, "fetch").mockRejectedValue(new TypeError("browser blocked"));
    try {
      const syncRequest = vi.fn<CenterSyncTransport["syncRequest"]>()
        .mockResolvedValueOnce({ ok: true, status: 200, body: "{}" })
        .mockResolvedValueOnce({ ok: true, status: 200, body: JSON.stringify({ token: "peer-login", user }) })
        .mockResolvedValueOnce({ ok: true, status: 200, body: JSON.stringify(user) });
      const bridge = { syncRequest, cancelSyncRequest: vi.fn().mockResolvedValue(undefined) };
      const session = new CenterSyncSession(origin, createCenterSyncFetch(origin, bridge));
      await session.sendCode(user.email);
      expect(await session.verifyCode(user.email, "123456")).toEqual(user);
      expect(syncRequest.mock.calls.map(([request]) => request.path)).toEqual(["/auth/send-code", "/auth/verify-code", "/api/me"]);
      expect(syncRequest.mock.calls[2]?.[0]).toMatchObject({ origin, token: "peer-login", body: undefined });
      expect(rendererFetch).not.toHaveBeenCalled();
    } finally { rendererFetch.mockRestore(); }
  });

  it("forwards aborts, ignores late responses and removes abort listeners after completion", async () => {
    let finish!: (result: Awaited<ReturnType<CenterSyncTransport["syncRequest"]>>) => void;
    const bridge = { syncRequest: vi.fn<CenterSyncTransport["syncRequest"]>().mockImplementationOnce(() => new Promise(resolve => { finish = resolve; })), cancelSyncRequest: vi.fn().mockResolvedValue(undefined) };
    const signal = new AbortController();
    const fetcher = createCenterSyncFetch(origin, bridge);
    const pending = fetcher(origin + "/auth/send-code", { method: "POST", body: "{}", signal: signal.signal });
    signal.abort();
    expect(bridge.cancelSyncRequest).toHaveBeenCalledWith(bridge.syncRequest.mock.calls[0]?.[0].id);
    finish({ ok: true, status: 200, body: "{}" });
    await expect(pending).rejects.toThrow(/abort/i);
  });

  it("preserves native timeouts through the shared session error boundary", async () => {
    const bridge = { syncRequest: vi.fn<CenterSyncTransport["syncRequest"]>().mockResolvedValue({ ok: false, reason: "timeout" }), cancelSyncRequest: vi.fn() };
    const session = new CenterSyncSession(origin, createCenterSyncFetch(origin, bridge));
    await expect(session.sendCode(user.email)).rejects.toMatchObject({ reason: "timeout" });
  });

  it("rejects a changed origin without sending IPC", async () => {
    const bridge = { syncRequest: vi.fn(), cancelSyncRequest: vi.fn() };
    await expect(createCenterSyncFetch(origin, bridge)("http://other.example/auth/send-code", { method: "POST", body: "{}" })).rejects.toThrow("Invalid sync request");
    expect(bridge.syncRequest).not.toHaveBeenCalled();
  });
});
