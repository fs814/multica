import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { I18nProvider } from "@multica/core/i18n/react";
import type { SupportedLocale } from "@multica/core/i18n";
import { RESOURCES } from "@multica/views/locales";
import { CenterSettingsTab } from "./center-settings-tab";
import { CenterSyncConnect } from "./center-sync-connect";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { syncCenters } from "@multica/core/api/center-sync";
import { getApi } from "@multica/core/api";

vi.mock("@multica/core/api/center-sync", async importOriginal => ({ ...await importOriginal<object>(), syncCenters: vi.fn() }));

vi.mock("@multica/core/api", () => {
  const api = { listWorkspaces: async () => [], getToken: () => "source-login", getBaseUrl: () => "https://source.example" };
  return { api, getApi: () => api };
});

const source = "https://source.example";
const peer = "https://peer.example";
const center = { get: vi.fn(), save: vi.fn(), connect: vi.fn(), test: vi.fn(), saveTransfer: vi.fn(), saveSyncSource: vi.fn(), transferData: vi.fn(), syncRequest: vi.fn(), cancelSyncRequest: vi.fn(), syncSourceRequest: vi.fn(), cancelSyncSourceRequest: vi.fn() };
const state = { saved: { version: 1 as const, url: source, profile: "desktop-services" }, activeUrl: source, transferUrl: peer };

beforeEach(() => {
  vi.resetAllMocks();
  center.get.mockResolvedValue(state);
  center.saveTransfer.mockImplementation(async (url: string) => ({ ...state, transferUrl: new URL(url.trim()).origin }));
  center.cancelSyncRequest.mockResolvedValue(undefined);
  center.cancelSyncSourceRequest.mockResolvedValue(undefined);
  // Model the native bridge; component tests assert the IPC wiring, while the
  // main-process transport tests own origin and endpoint enforcement.
  center.syncRequest.mockImplementation(async (request: { origin: string; path: string; body?: string; token?: string }) => {
    try {
      const response = await fetch(request.origin + request.path, { method: request.body === undefined ? "GET" : "POST", body: request.body,
        headers: request.token ? { Authorization: `Bearer ${request.token}` } : {} });
      return { ok: true, status: response.status, body: await response.text() };
    } catch { return { ok: false, reason: "network" }; }
  });
  center.syncSourceRequest.mockImplementation(request => center.syncRequest.getMockImplementation()!(request));
  Object.defineProperty(window, "desktopAPI", { configurable: true, value: { center } });
});
afterEach(() => { vi.unstubAllGlobals(); vi.restoreAllMocks(); });

async function show(locale: SupportedLocale = "en", title = "Sync between center servers") {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  render(<QueryClientProvider client={queryClient}><I18nProvider locale={locale} resources={RESOURCES}><CenterSettingsTab /></I18nProvider></QueryClientProvider>);
  const section = screen.getByRole("region", { name: title });
  await waitFor(() => expect(within(section).getByRole("textbox")).toBeEnabled());
  return within(section);
}

describe("center sync settings", () => {
  it("saves and tests a separate HTTPS source without changing the HTTP Desktop connection", async () => {
    const http = "http://source.example:18080";
    const current = { ...state, activeUrl: http, saved: { ...state.saved, url: http } };
    center.get.mockResolvedValue(current);
    center.saveSyncSource.mockImplementation(async url => ({ ...current, syncSourceUrl: url }));
    center.test.mockResolvedValue({ reachable: true });
    await show();
    const section = within(screen.getByRole("region", { name: "Source HTTPS connection for sync" }));
    const input = section.getByLabelText("Source HTTPS address");
    expect(input).toHaveValue("");
    for (const address of [http, peer]) {
      fireEvent.change(input, { target: { value: address } });
      expect(section.getByRole("button", { name: "Save" })).toBeDisabled();
      expect(section.getByRole("button", { name: "Connect source for sync" })).toBeDisabled();
    }
    fireEvent.change(input, { target: { value: source + "/" } });
    fireEvent.click(section.getByRole("button", { name: "Test server connection" }));
    await waitFor(() => expect(center.test).toHaveBeenCalledWith(source));
    await waitFor(() => expect(section.getByRole("button", { name: "Save" })).toBeEnabled());
    fireEvent.click(section.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(section.getByRole("button", { name: "Connect source for sync" })).toBeEnabled());
    expect(center.saveSyncSource).toHaveBeenCalledWith(source);
    expect(screen.getByLabelText("Server address")).toHaveValue(http);
    expect(center.save).not.toHaveBeenCalled(); expect(center.connect).not.toHaveBeenCalled();
    expect(center.syncSourceRequest).not.toHaveBeenCalled();
  });

  it("syncs over two separate HTTPS sessions while Desktop stays on HTTP, then disconnects only the source", async () => {
    const http = "http://source.example:18080";
    center.get.mockResolvedValue({ ...state, activeUrl: http, saved: { ...state.saved, url: http }, syncSourceUrl: source });
    const user = { id: "12345678-1234-4234-8234-123456789012", email: "owner@example.test" };
    const workspace = { id: "22345678-1234-4234-8234-123456789012", name: "HTTPS source workspace" };
    vi.stubGlobal("fetch", vi.fn(async (address: string) => {
      const url = new URL(address);
      if (url.pathname === "/auth/verify-code") return Response.json({ token: url.origin === source ? "source-https-token" : "peer-https-token", user });
      if (url.pathname === "/api/me") return Response.json(user);
      if (url.pathname === "/api/workspaces") return Response.json([workspace]);
      return Response.json({});
    }));
    const primaryList = vi.spyOn(getApi(), "listWorkspaces");
    vi.mocked(syncCenters).mockImplementation(async (src, dst, _workspace, signal) => {
      await src.request("info", {}, signal); await dst.request("info", {}, signal);
      return { records: [], cursor: 0, conflicts: 0, pending: 0 };
    });
    const peerSection = await show();
    const sourceSection = within(screen.getByRole("region", { name: "Source HTTPS connection for sync" }));
    async function login(section: typeof peerSection, button: string) {
      fireEvent.click(section.getByRole("button", { name: button }));
      const dialog = within(await screen.findByRole("dialog"));
      fireEvent.change(dialog.getByLabelText("Email"), { target: { value: user.email } });
      fireEvent.click(dialog.getByRole("button", { name: "Send sign-in code" }));
      fireEvent.change(await dialog.findByLabelText("Verification code"), { target: { value: "123456" } });
      fireEvent.click(dialog.getByRole("button", { name: "Sign in" }));
      await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    }
    await login(peerSection, "Connect for sync");
    expect(peerSection.getByRole("button", { name: "Sync between center servers" })).toBeDisabled();
    await login(sourceSection, "Connect source for sync");
    await waitFor(() => expect(peerSection.getByRole("combobox")).toBeEnabled());
    fireEvent.click(peerSection.getByRole("combobox"));
    const workspaceOption = await screen.findByRole("option", { name: workspace.name });
    fireEvent.mouseMove(workspaceOption);
    fireEvent.click(workspaceOption);
    expect(syncCenters).not.toHaveBeenCalled();
    fireEvent.click(peerSection.getByRole("button", { name: "Sync between center servers" }));
    await peerSection.findByText(/Merge finished for 1 workspace/);
    expect(syncCenters).toHaveBeenCalledOnce();
    expect(vi.mocked(syncCenters).mock.calls[0]![0].origin).toBe(source);
    expect(center.syncSourceRequest).toHaveBeenCalledWith(expect.objectContaining({ origin: source, path: "/api/workspaces", token: "source-https-token" }));
    expect(center.syncSourceRequest).toHaveBeenCalledWith(expect.objectContaining({ origin: source, path: "/api/center-sync/info", token: "source-https-token" }));
    expect(center.syncRequest).toHaveBeenCalledWith(expect.objectContaining({ origin: peer, path: "/api/center-sync/info", token: "peer-https-token" }));
    expect(primaryList).not.toHaveBeenCalled();
    expect(center.connect).not.toHaveBeenCalled(); expect(center.save).not.toHaveBeenCalled();
    expect(screen.getByLabelText("Server address")).toHaveValue(http);
    let aborted = false;
    vi.mocked(syncCenters).mockImplementation((_src, _dst, _workspace, signal) => new Promise((_resolve, reject) => {
      signal.addEventListener('abort', () => { aborted = true; reject(new Error('cancelled fixture')); }, { once: true });
    }));
    fireEvent.click(peerSection.getByRole("button", { name: "Sync between center servers" }));
    await waitFor(() => expect(syncCenters).toHaveBeenCalledTimes(2));
    fireEvent.click(sourceSection.getByRole("button", { name: "Disconnect sync source" }));
    await waitFor(() => expect(aborted).toBe(true));
    expect(peerSection.getByRole("button", { name: "Sync between center servers" })).toBeDisabled();
    expect(peerSection.getByText(`Connected to ${peer} as ${user.email}.`)).toBeVisible();
    expect(sourceSection.queryByText(`Connected to ${source} as ${user.email}.`)).not.toBeInTheDocument();
    expect(screen.getByLabelText("Server address")).toHaveValue(http);
  });
  it("does not mount server-data hooks in offline settings without a Query provider", async () => {
    const user = { id: "12345678-1234-4234-8234-123456789012", email: "owner@example.test" };
    const fetcher = vi.fn()
      .mockResolvedValueOnce(new Response("{}"))
      .mockResolvedValueOnce(new Response(JSON.stringify({ token: "peer-session", user })))
      .mockResolvedValueOnce(new Response(JSON.stringify(user)));
    vi.stubGlobal("fetch", fetcher);
    render(<I18nProvider locale="en" resources={RESOURCES}><CenterSyncConnect address={peer} sourceAddress="" disabled={false} onBusyChange={vi.fn()} /></I18nProvider>);
    fireEvent.click(screen.getByRole("button", { name: "Connect for sync" }));
    fireEvent.change(await screen.findByLabelText("Email"), { target: { value: user.email } });
    fireEvent.click(screen.getByRole("button", { name: "Send sign-in code" }));
    fireEvent.change(await screen.findByLabelText("Verification code"), { target: { value: "123456" } });
    fireEvent.click(screen.getByRole("button", { name: "Sign in" }));
    expect(await screen.findByText("Connect and sign in to the source server before syncing.")).toBeVisible();
    expect(screen.getByRole("button", { name: "Sync between center servers" })).toBeDisabled();
    expect(fetcher).toHaveBeenCalledTimes(3);
  });
  it("connects the sync destination without switching the current center or invoking transfer", async () => {
    const user = { id: "12345678-1234-4234-8234-123456789012", email: "owner@example.test", name: "Owner" };
    const fetcher = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({})))
      .mockResolvedValueOnce(new Response(JSON.stringify({ token: "peer-session", user })))
      .mockResolvedValueOnce(new Response(JSON.stringify(user)));
    vi.stubGlobal("fetch", fetcher);
    const section = await show();
    expect(section.getByRole("button", { name: "Disconnect sync server" })).toBeDisabled();
    fireEvent.click(section.getByRole("button", { name: "Connect for sync" }));
    const dialog = within(await screen.findByRole("dialog"));
    expect(dialog.getByText(`Sign in to ${peer}. Your current server connection stays unchanged.`)).toBeVisible();
    fireEvent.change(dialog.getByLabelText("Email"), { target: { value: user.email } });
    fireEvent.click(dialog.getByRole("button", { name: "Send sign-in code" }));
    const code = await dialog.findByLabelText("Verification code");
    fireEvent.change(code, { target: { value: "123456" } });
    fireEvent.click(dialog.getByRole("button", { name: "Sign in" }));
    expect(await section.findByText(`Connected to ${peer} as ${user.email}.`)).toBeVisible();
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(section.getByRole("button", { name: "Connect for sync" })).toBeDisabled();
    expect(section.getByRole("button", { name: "Disconnect sync server" })).toBeEnabled();
    expect(fetcher.mock.calls.map(([url]) => url)).toEqual([
      `${peer}/auth/send-code`, `${peer}/auth/verify-code`, `${peer}/api/me`,
    ]);
    expect(center.connect).not.toHaveBeenCalled();
    expect(center.transferData).not.toHaveBeenCalled();
    expect(center.syncRequest).toHaveBeenCalledTimes(3);
    expect(center.syncRequest).toHaveBeenNthCalledWith(1, expect.objectContaining({ origin: peer, path: "/auth/send-code", body: JSON.stringify({ email: user.email }), token: undefined }));
    expect(screen.getByRole("textbox", { name: "Server address" })).toHaveValue(source);
    expect(section.getByRole("button", { name: "Sync between center servers" })).toBeDisabled();
    fireEvent.click(section.getByRole("button", { name: "Disconnect sync server" }));
    expect(section.getByRole("button", { name: "Connect for sync" })).toBeEnabled();
    expect(section.getByRole("button", { name: "Disconnect sync server" })).toBeDisabled();
    expect(screen.getByRole("textbox", { name: "Server address" })).toHaveValue(source);
    expect(center.connect).not.toHaveBeenCalled();
    expect(fetcher).toHaveBeenCalledTimes(3);
    expect(section.queryByText(`Connected to ${peer} as ${user.email}.`)).not.toBeInTheDocument();
  });

  it("keeps sign-in errors retryable and never reports connected", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response("{}", { status: 503 })));
    const section = await show();
    fireEvent.click(section.getByRole("button", { name: "Connect for sync" }));
    const dialog = within(await screen.findByRole("dialog"));
    fireEvent.change(dialog.getByLabelText("Email"), { target: { value: "owner@example.test" } });
    fireEvent.click(dialog.getByRole("button", { name: "Send sign-in code" }));
    expect(await dialog.findByRole("alert")).toHaveTextContent("Send sign-in code failed. The destination returned HTTP 503.");
    expect(dialog.getByRole("button", { name: "Send sign-in code" })).toBeEnabled();
    fireEvent.click(dialog.getByRole("button", { name: "Cancel" }));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(section.getByRole("button", { name: "Connect for sync" })).toBeEnabled();
  });

  it("distinguishes rate limiting and transport failures while preserving the email", async () => {
    const fetcher = vi.fn().mockResolvedValueOnce(new Response("private backend detail", { status: 429 }))
      .mockRejectedValueOnce(new TypeError("private fetch detail"));
    vi.stubGlobal("fetch", fetcher);
    const section = await show();
    fireEvent.click(section.getByRole("button", { name: "Connect for sync" }));
    const dialog = within(await screen.findByRole("dialog"));
    fireEvent.change(dialog.getByLabelText("Email"), { target: { value: "owner@example.test" } });
    fireEvent.click(dialog.getByRole("button", { name: "Send sign-in code" }));
    expect(await dialog.findByRole("alert")).toHaveTextContent("HTTP 429");
    expect(dialog.getByLabelText("Email")).toHaveValue("owner@example.test");
    fireEvent.click(dialog.getByRole("button", { name: "Send sign-in code" }));
    await waitFor(() => expect(dialog.getByRole("alert")).toHaveTextContent("No readable response from the server"));
    expect(dialog.getByRole("alert")).not.toHaveTextContent("private");
    expect(center.connect).not.toHaveBeenCalled();
  });

  it("identifies a rejected verification code without discarding the email or code", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValueOnce(new Response("{}"))
      .mockResolvedValueOnce(new Response("{}", { status: 400 })));
    const section = await show();
    fireEvent.click(section.getByRole("button", { name: "Connect for sync" }));
    const dialog = within(await screen.findByRole("dialog"));
    fireEvent.change(dialog.getByLabelText("Email"), { target: { value: "owner@example.test" } });
    fireEvent.click(dialog.getByRole("button", { name: "Send sign-in code" }));
    fireEvent.change(await dialog.findByLabelText("Verification code"), { target: { value: "123456" } });
    fireEvent.click(dialog.getByRole("button", { name: "Sign in" }));
    expect(await dialog.findByRole("alert")).toHaveTextContent("Sign in failed. The server rejected the code (HTTP 400).");
    expect(dialog.getByLabelText("Verification code")).toHaveValue("123456");
    expect(dialog.getByRole("button", { name: "Sign in" })).toBeEnabled();
    expect(center.connect).not.toHaveBeenCalled();
  });

  it("requires a saved, different destination before connecting", async () => {
    const section = await show();
    fireEvent.change(section.getByRole("textbox"), { target: { value: "https://unsaved.example" } });
    expect(section.getByRole("button", { name: "Connect for sync" })).toBeDisabled();
    expect(section.getByText("Save a valid address different from your current server to connect for sync.")).toBeVisible();
    fireEvent.change(section.getByRole("textbox"), { target: { value: source } });
    expect(section.getByRole("button", { name: "Connect for sync" })).toBeDisabled();
  });

  it("preserves the saved peer address, tests and saves a draft without changing the active server", async () => {
    const section = await show();
    const address = section.getByRole("textbox", { name: "Peer server address" });
    expect(address).toHaveValue(peer);
    fireEvent.change(address, { target: { value: "https://another.example/" } });
    center.test.mockResolvedValue({ reachable: true });
    fireEvent.click(section.getByRole("button", { name: "Test server connection" }));
    await waitFor(() => expect(center.test).toHaveBeenCalledWith("https://another.example/"));
    expect(await section.findByText("Server reachable. Sync compatibility has not been checked.")).toBeVisible();
    await waitFor(() => expect(section.getByRole("button", { name: "Save" })).toBeEnabled());
    fireEvent.click(section.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(address).toHaveValue("https://another.example"));
    expect(await section.findByText("Peer address saved on this Desktop.")).toBeVisible();
    expect(center.saveTransfer).toHaveBeenCalledWith("https://another.example/");
    expect(center.save).not.toHaveBeenCalled();
    expect(center.connect).not.toHaveBeenCalled();
    expect(center.transferData).not.toHaveBeenCalled();
    expect(screen.getByRole("textbox", { name: "Server address" })).toHaveValue(source);
  });

  it("keeps a failed save editable", async () => {
    const section = await show();
    center.saveTransfer.mockRejectedValue(new Error("Cannot save peer address"));
    fireEvent.change(section.getByRole("textbox"), { target: { value: "https://unsaved.example" } });
    fireEvent.click(section.getByRole("button", { name: "Save" }));
    expect(await section.findByText("Cannot save peer address")).toBeVisible();
    expect(section.getByRole("textbox")).toHaveValue("https://unsaved.example");
    expect(section.getByRole("textbox")).toBeEnabled();
  });

  it("does not present database replacement as sync or request recovery tokens", async () => {
    const section = await show();
    const sync = section.getByRole("button", { name: "Sync between center servers" });
    expect(sync).toBeDisabled();
    expect(section.getByText(/Requires HTTPS/)).toHaveTextContent("Credentials, runtime connections and execution state stay local");
    fireEvent.click(sync);
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(section.queryByLabelText(/token/i)).not.toBeInTheDocument();
    expect(section.queryByText(/transfer/i)).not.toBeInTheDocument();
    expect(center.transferData).not.toHaveBeenCalled();
  });

  it.each([
    ["en", "Sync between center servers"],
    ["zh-Hans", "同步中心服务器"],
    ["ja", "センターサーバー間で同期"],
    ["ko", "센터 서버 간 동기화"],
    ["fr", "Synchroniser les serveurs centraux"],
  ] as const)("shows the sync action in %s", async (locale, label) => {
    const section = await show(locale, label);
    expect(section.getByRole("button", { name: label })).toBeDisabled();
  });
});
