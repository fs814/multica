import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nProvider } from "@multica/core/i18n/react";
import { RESOURCES } from "@multica/views/locales";
import { CenterSyncSession } from "@multica/core/api/center-sync-session";
import { syncCenters } from "@multica/core/api/center-sync";
import { CenterSyncRun } from "./center-sync-run";

const id = "12345678-1234-4234-8234-123456789012";
vi.mock("@multica/core/api", () => {
  const api = { listWorkspaces: async () => [{ id: "12345678-1234-4234-8234-123456789012", name: "Fixture workspace" }], getToken: () => "source-login", getBaseUrl: () => "https://source.example" };
  return { api, getApi: () => api };
});
vi.mock("@multica/core/api/center-sync", async importOriginal => ({ ...await importOriginal<object>(), syncCenters: vi.fn() }));
afterEach(() => vi.restoreAllMocks());
beforeEach(() => vi.clearAllMocks());

async function show() {
  const session = new CenterSyncSession("https://peer.example");
  vi.spyOn(session, "currentUser", "get").mockReturnValue({ id, email: "owner@example.test" });
  const busy = vi.fn();
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  const view = render(<QueryClientProvider client={client}><I18nProvider locale="en" resources={RESOURCES}>
    <CenterSyncRun sourceAddress="https://source.example" session={session} disabled={false} onBusyChange={busy} onExpired={vi.fn()} />
  </I18nProvider></QueryClientProvider>);
  await waitFor(() => expect(client.getQueryData(["workspaces", "list"])).toBeDefined());
  fireEvent.click(screen.getByRole("combobox", { name: "Source workspace" }));
  fireEvent.click(await screen.findByRole("option", { name: "Fixture workspace" }));
  return { ...view, busy };
}

describe("Desktop manual sync action", () => {
  it("starts only on click and displays confirmed counts and conflicts", async () => {
    vi.mocked(syncCenters).mockResolvedValue({ cursor: 7, records: [], pending: 0, conflicts: 2 });
    await show();
    expect(syncCenters).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "Sync between center servers" }));
    expect(await screen.findByRole("status")).toHaveTextContent("cursor 7; 0 pending edits, 2 items needing review");
    expect(syncCenters).toHaveBeenCalledOnce();
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it("cancels an active run without starting another one", async () => {
    vi.mocked(syncCenters).mockImplementation((_source, _destination, _workspace, signal) => new Promise((_resolve, reject) => {
      signal.addEventListener("abort", () => reject(new Error("Cancelled")), { once: true });
    }));
    await show();
    fireEvent.click(screen.getByRole("button", { name: "Sync between center servers" }));
    fireEvent.click(await screen.findByRole("button", { name: "Cancel" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Saved progress is retained");
    expect(syncCenters).toHaveBeenCalledOnce();
  });

  it("aborts when the connected panel unmounts", async () => {
    let runSignal: AbortSignal | undefined;
    vi.mocked(syncCenters).mockImplementation((_source, _destination, _workspace, signal) => {
      runSignal = signal;
      return new Promise((_resolve, reject) => signal.addEventListener("abort", () => reject(new Error("Disconnected")), { once: true }));
    });
    const view = await show();
    fireEvent.click(screen.getByRole("button", { name: "Sync between center servers" }));
    await waitFor(() => expect(runSignal).toBeDefined());
    view.unmount();
    expect(runSignal?.aborted).toBe(true);
  });
});
