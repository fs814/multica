import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { I18nProvider } from "@multica/core/i18n/react";
import type { SupportedLocale } from "@multica/core/i18n";
import { RESOURCES } from "@multica/views/locales";
import { CenterSettingsTab } from "./center-settings-tab";
import { CenterSyncConnect } from "./center-sync-connect";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

vi.mock("@multica/core/api", () => ({ api: { listWorkspaces: async () => [] } }));

const source = "https://source.example";
const peer = "https://peer.example";
const center = { get: vi.fn(), save: vi.fn(), connect: vi.fn(), test: vi.fn(), saveTransfer: vi.fn(), transferData: vi.fn() };
const state = { saved: { version: 1 as const, url: source, profile: "desktop-services" }, activeUrl: source, transferUrl: peer };

beforeEach(() => {
  vi.resetAllMocks();
  center.get.mockResolvedValue(state);
  center.saveTransfer.mockImplementation(async (url: string) => ({ ...state, transferUrl: new URL(url.trim()).origin }));
  Object.defineProperty(window, "desktopAPI", { configurable: true, value: { center } });
});
afterEach(() => vi.unstubAllGlobals());

async function show(locale: SupportedLocale = "en", title = "Sync between center servers") {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  render(<QueryClientProvider client={queryClient}><I18nProvider locale={locale} resources={RESOURCES}><CenterSettingsTab /></I18nProvider></QueryClientProvider>);
  const section = screen.getByRole("region", { name: title });
  await waitFor(() => expect(within(section).getByRole("textbox")).toBeEnabled());
  return within(section);
}

describe("center sync settings", () => {
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
    expect(await section.findByText("Peer address saved on this Desktop. No sync connection has been created.")).toBeVisible();
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
    expect(section.getByText(/Sync requires HTTPS/)).toHaveTextContent("not a full database copy");
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
