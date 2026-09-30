// @vitest-environment node
import { describe, expect, it, vi } from "vitest";
import { createServer } from "node:http";
import type { AddressInfo } from "node:net";
import type { BrowserWindow, IpcMain, IpcMainInvokeEvent } from "electron";
import { createCenterSyncTransport, registerCenterSyncTransport } from "./center-sync-transport";

const peer = "http://peer.example:18080";
const source = "https://source.example";
const id = "12345678-1234-4234-8234-123456789012";
const input = { id, origin: peer, path: "/auth/send-code", body: JSON.stringify({ email: "owner@example.test" }) };

describe("native peer transport", () => {
  it('isolates the HTTPS source channel, permits authenticated workspace reads only there, and rejects HTTP', async () => {
    const fetcher = vi.fn<typeof fetch>().mockImplementation(async () => Response.json([]));
    const transport = createCenterSyncTransport(() => ({ peer: source, source: 'https://peer.example' }), fetcher, 'source');
    const workspaces = { id, origin: source, path: '/api/workspaces', token: 'source-https-login' };
    expect(await transport.request(workspaces)).toMatchObject({ ok: true, status: 200 });
    expect(fetcher).toHaveBeenCalledExactlyOnceWith(source + '/api/workspaces', expect.objectContaining({ method: 'GET', credentials: 'omit', redirect: 'error', headers: { 'Content-Type': 'application/json', Authorization: 'Bearer source-https-login' } }));
    for (const request of [{ ...workspaces, token: undefined }, { ...workspaces, body: '{}' }, { ...workspaces, origin: peer }, { ...workspaces, path: '/api/workspaces?all=1' }, { ...workspaces, path: '/api/issues' }]) {
      expect(await transport.request(request)).toEqual({ ok: false, reason: 'invalid_request' });
    }
    expect(await createCenterSyncTransport(() => ({ peer: source, source: peer }), fetcher).request(workspaces)).toEqual({ ok: false, reason: 'invalid_request' });
    expect(await createCenterSyncTransport(() => ({ peer, source }), fetcher, 'source').request(input)).toEqual({ ok: false, reason: 'invalid_request' });
    expect(fetcher).toHaveBeenCalledTimes(1);
    expect(await transport.request({ id, origin: source, path: '/api/center-sync/apply', body: '{}', token: 'source-https-login' })).toEqual({ ok: false, reason: 'invalid_request' });
  });
  it("uses the saved HTTP peer without browser cookies, redirects or source credentials", async () => {
    const fetcher = vi.fn<typeof fetch>().mockResolvedValueOnce(Response.json({ message: "sent" }));
    const transport = createCenterSyncTransport(() => ({ peer, source }), fetcher);
    expect(await transport.request(input)).toEqual({ ok: true, status: 200, body: '{"message":"sent"}' });
    expect(fetcher).toHaveBeenCalledExactlyOnceWith(peer + "/auth/send-code", expect.objectContaining({ method: "POST", body: input.body,
      headers: { "Content-Type": "application/json" }, credentials: "omit", redirect: "error", cache: "no-store" }));
  });

  it("preserves errors for the renderer and uses only explicitly supplied peer credentials", async () => {
    const fetcher = vi.fn<typeof fetch>().mockResolvedValueOnce(Response.json({ error: "invalid code" }, { status: 400 }))
      .mockResolvedValueOnce(Response.json({ id: "peer-user" }));
    const transport = createCenterSyncTransport(() => ({ peer, source }), fetcher);
    expect(await transport.request({ ...input, path: "/auth/verify-code", body: JSON.stringify({ email: "owner@example.test", code: "123456" }) })).toMatchObject({ ok: true, status: 400 });
    await transport.request({ ...input, path: "/api/me", body: undefined, token: "peer-login" });
    expect(fetcher).toHaveBeenLastCalledWith(peer + "/api/me", expect.objectContaining({ method: "GET", headers: { "Content-Type": "application/json", Authorization: "Bearer peer-login" } }));
  });

  it("rejects arbitrary origins, paths, credentials, methods and malformed requests before networking", async () => {
    const fetcher = vi.fn<typeof fetch>();
    const transport = createCenterSyncTransport(() => ({ peer, source }), fetcher);
    for (const request of [null, {}, { ...input, id: "bad" }, { ...input, origin: source }, { ...input, origin: "http://evil.example" },
      ...["//evil.example/auth/send-code", "/auth/send-code?redirect=evil", "/api/center/recovery/snapshot", "/api/issues", "/auth/logout", "/api/center-sync/apply"].map(path => ({ ...input, path })),
      { ...input, token: "source-token" }, { ...input, body: "not-json" }, { ...input, body: '{"email":"a","redirect":"evil"}' },
      { ...input, path: "/auth/verify-code", body: '{"email":"a","code":"123"}' },
      { ...input, path: "/api/me", body: undefined }, { ...input, path: "/api/me", body: undefined, token: "with spaces" },
      { ...input, body: "x".repeat(32 * 1024 * 1024 + 1) }]) {
      expect(await transport.request(request)).toEqual({ ok: false, reason: "invalid_request" });
    }
    expect(await createCenterSyncTransport(() => ({ peer, source: peer }), fetcher).request(input)).toEqual({ ok: false, reason: "invalid_request" });
    expect(fetcher).not.toHaveBeenCalled();
  });

  it("supports only the explicit HTTPS sync endpoints, with a peer login", async () => {
    const secure = "https://peer.example";
    const fetcher = vi.fn<typeof fetch>().mockResolvedValueOnce(Response.json({ schema: 1 }));
    const transport = createCenterSyncTransport(() => ({ peer: secure, source }), fetcher);
    const sync = { ...input, origin: secure, path: "/api/center-sync/info", body: "{}", token: "peer-login" };
    expect(await transport.request(sync)).toMatchObject({ ok: true, status: 200 });
    expect(await transport.request({ ...sync, token: undefined })).toEqual({ ok: false, reason: "invalid_request" });
    expect(fetcher).toHaveBeenCalledTimes(1);
  });

  it("cancels a request without stopping another and rejects duplicate request IDs", async () => {
    const signals: AbortSignal[] = [];
    const fetcher = vi.fn<typeof fetch>().mockImplementation((_url, init) => new Promise((_resolve, reject) => {
      signals.push(init!.signal!);
      init!.signal!.addEventListener("abort", () => reject(new Error("private details")), { once: true });
    }));
    const transport = createCenterSyncTransport(() => ({ peer, source }), fetcher);
    const first = transport.request(input);
    const second = transport.request({ ...input, id: "87654321-1234-4234-8234-123456789012" });
    expect(await transport.request(input)).toEqual({ ok: false, reason: "capacity" });
    transport.cancel(input.id);
    expect(await first).toEqual({ ok: false, reason: "aborted" });
    expect(signals[1]?.aborted).toBe(false);
    transport.cancelAll();
    expect(await second).toEqual({ ok: false, reason: "aborted" });
  });

  it("does not return a stale login response after the selected peer changes", async () => {
    let selected = peer;
    const fetcher = vi.fn<typeof fetch>().mockImplementationOnce(async () => { selected = "http://different.example"; return Response.json({ token: "stale-secret" }); });
    const transport = createCenterSyncTransport(() => ({ peer: selected, source }), fetcher);
    expect(await transport.request(input)).toEqual({ ok: false, reason: "aborted" });
  });

  it("bounds response bodies even without Content-Length", async () => {
    const fetcher = vi.fn<typeof fetch>().mockResolvedValueOnce(new Response("x".repeat(65 * 1024)))
      .mockResolvedValueOnce(new Response("small", { headers: { "Content-Length": "1000000" } }));
    const transport = createCenterSyncTransport(() => ({ peer, source }), fetcher);
    expect(await transport.request(input)).toEqual({ ok: false, reason: "capacity" });
    expect(await transport.request(input)).toEqual({ ok: false, reason: "capacity" });
  });

  it("sanitizes transport errors without disclosing tokens, emails or network details", async () => {
    const fetcher = vi.fn<typeof fetch>().mockRejectedValueOnce(new Error("secret email and token"));
    expect(await createCenterSyncTransport(() => ({ peer, source }), fetcher).request(input)).toEqual({ ok: false, reason: "network" });
  });

  it("works against a fixture without CORS headers and never follows redirects", async () => {
    let redirected = 0;
    const server = createServer((request, response) => {
      if (request.url === "/auth/send-code") {
        expect(request.headers.origin).toBeUndefined();
        expect(request.headers.cookie).toBeUndefined();
        response.setHeader("Content-Type", "application/json");
        response.end('{"message":"fixture-only, no email sent"}');
      } else if (request.url === "/api/me") {
        response.writeHead(307, { Location: "/redirect-target" }); response.end();
      } else { redirected++; response.end("must not reach here"); }
    });
    await new Promise<void>(resolve => server.listen(0, "127.0.0.1", resolve));
    const origin = `http://127.0.0.1:${(server.address() as AddressInfo).port}`;
    try {
      const transport = createCenterSyncTransport(() => ({ peer: origin, source }));
      expect(await transport.request({ ...input, origin })).toMatchObject({ ok: true, status: 200 });
      expect(await transport.request({ ...input, origin, path: "/api/me", body: undefined, token: "fixture-only-token" })).toEqual({ ok: false, reason: "network" });
      expect(redirected).toBe(0);
    } finally { server.closeAllConnections(); await new Promise<void>(resolve => server.close(() => resolve())); }
  });

  it.each(["center:sync", "center:sync-source"] as const)("allows %s IPC only from the main window's top-level frame", async prefix => {
    const handlers = new Map<string, Parameters<IpcMain["handle"]>[1]>();
    const ipc = { handle: vi.fn<IpcMain["handle"]>((channel, handler) => { handlers.set(channel, handler); }) };
    const frame = {};
    const sender = { mainFrame: frame, on: vi.fn(), once: vi.fn() };
    const main = { webContents: sender } as unknown as BrowserWindow;
    const transport = { request: vi.fn().mockResolvedValue({ ok: true, status: 200, body: "{}" }), cancel: vi.fn(), cancelAll: vi.fn() };
    registerCenterSyncTransport(ipc, () => main, transport, prefix);
    const event = { sender, senderFrame: frame } as unknown as IpcMainInvokeEvent;
    for (const channel of [`${prefix}-request`, `${prefix}-cancel`]) {
      const handler = handlers.get(channel)!;
      expect(() => handler({ ...event, senderFrame: null }, input)).toThrow("main Desktop window");
      expect(() => handler({ ...event, sender: {} as IpcMainInvokeEvent["sender"] }, input)).toThrow("main Desktop window");
    }
    expect(transport.request).not.toHaveBeenCalled();
    expect(transport.cancel).not.toHaveBeenCalled();
    await handlers.get(`${prefix}-request`)!(event, input);
    await handlers.get(`${prefix}-request`)!(event, input);
    expect(transport.request).toHaveBeenCalledTimes(2);
    expect(sender.once).toHaveBeenCalledTimes(1);
    const navigation = sender.on.mock.calls.find(([name]) => name === "did-start-navigation")![1];
    navigation({}, "https://other.example", false, true);
    expect(transport.cancelAll).toHaveBeenCalledOnce();
    navigation({}, "https://other.example", false, false);
    navigation({}, "https://other.example", true, true);
    expect(transport.cancelAll).toHaveBeenCalledOnce();
    sender.once.mock.calls[0]![1]();
    expect(transport.cancelAll).toHaveBeenCalledTimes(2);
  });
});
