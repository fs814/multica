import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nProvider } from "@multica/core/i18n/react";
import { RESOURCES } from "@multica/views/locales";
import { CenterSyncSession } from "@multica/core/api/center-sync-session";
import { syncCenters, type CenterSyncProgress, type CenterSyncResult } from "@multica/core/api/center-sync";
import { CenterSyncRun } from "./center-sync-run";
import { getApi } from "@multica/core/api";

const id = "12345678-1234-4234-8234-123456789012";
vi.mock("@multica/core/api", () => {
  const api = { listWorkspaces: async () => [{ id: "12345678-1234-4234-8234-123456789012", name: "Fixture workspace" }], getToken: () => "source-login", getBaseUrl: () => "https://source.example" };
  return { api, getApi: () => api };
});
vi.mock("@multica/core/api/center-sync", async importOriginal => ({ ...await importOriginal<object>(), syncCenters: vi.fn() }));
afterEach(() => vi.restoreAllMocks());
beforeEach(() => vi.clearAllMocks());

async function show(select = true, peer = "https://peer.example") {
  const session = new CenterSyncSession(peer);
  vi.spyOn(session, "currentUser", "get").mockReturnValue({ id, email: "owner@example.test" });
  const busy = vi.fn();
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  const view = render(<QueryClientProvider client={client}><I18nProvider locale="en" resources={RESOURCES}>
    <CenterSyncRun sourceAddress="https://source.example" session={session} disabled={false} onBusyChange={busy} onExpired={vi.fn()} />
  </I18nProvider></QueryClientProvider>);
  if (select) {
    await waitFor(() => expect(screen.getByRole("combobox", { name: "Source workspace" })).toBeEnabled());
    fireEvent.click(screen.getByRole("combobox", { name: "Source workspace" }));
    const option = await screen.findByRole("option", { name: "Fixture workspace" });
    fireEvent.mouseMove(option);
    fireEvent.click(option);
    await waitFor(() => expect(screen.getByRole("combobox")).toHaveTextContent("Fixture workspace"));
  }
  return { ...view, busy, client };
}

describe("Desktop manual sync action", () => {
  it("keeps the chosen workspace label and sends its ID, not the previous selection", async () => {
    const otherId = "22345678-1234-4234-8234-123456789012";
    vi.spyOn(getApi(), "listWorkspaces").mockResolvedValue([
      { id, name: "Fixture workspace" },
      { id: otherId, name: "Second workspace" },
    ] as Awaited<ReturnType<ReturnType<typeof getApi>["listWorkspaces"]>>);
    vi.mocked(syncCenters).mockResolvedValue({ cursor: 0, records: [], pending: 0, conflicts: 0 });
    await show();
    fireEvent.click(screen.getByRole("combobox", { name: "Source workspace" }));
    const option = await screen.findByRole("option", { name: "Second workspace" });
    fireEvent.mouseMove(option);
    fireEvent.click(option);
    await waitFor(() => expect(screen.getByRole("combobox")).toHaveTextContent("Second workspace"));
    fireEvent.click(screen.getByRole("button", { name: "Sync between center servers" }));
    await waitFor(() => expect(syncCenters).toHaveBeenCalledOnce());
    expect(vi.mocked(syncCenters).mock.calls[0]?.[2]).toBe(otherId);
    await screen.findByText(/Merge finished for 1 workspace/);
    expect(vi.mocked(syncCenters).mock.calls[0]?.[5]).toBe(true);
  });

  it("explains the HTTP prerequisite before starting a run", async () => {
    await show(true, "http://peer.example:18080");
    expect(screen.getByText(/Configure valid HTTPS on both centers/)).toBeVisible();
    const button = screen.getByRole("button", { name: "Sync between center servers" });
    expect(button).toBeDisabled();
    expect(button).toHaveAccessibleDescription(/Sync is unavailable over HTTP/);
    fireEvent.click(button);
    expect(syncCenters).not.toHaveBeenCalled();
    expect(screen.queryByRole("progressbar")).not.toBeInTheDocument();
  });

  it("shows loading and lets the user retry a failed workspace request", async () => {
    let reject!: (error: Error) => void;
    const list = vi.spyOn(getApi(), "listWorkspaces").mockImplementationOnce(() => new Promise((_resolve, fail) => { reject = fail; }));
    await show(false);
    expect(screen.getByText("Loading source workspaces...")).toBeVisible();
    expect(screen.getByRole("combobox")).toBeDisabled();
    await act(async () => reject(new Error("Offline")));
    expect(await screen.findByRole("alert")).toHaveTextContent("Could not load workspaces");
    fireEvent.click(screen.getByRole("button", { name: "Refresh workspaces" }));
    await waitFor(() => expect(screen.getByRole("combobox")).toBeEnabled());
    expect(list).toHaveBeenCalledTimes(2);
    expect(syncCenters).not.toHaveBeenCalled();
  });

  it("invalidates a removed selection and explains an empty workspace list", async () => {
    await show();
    expect(screen.getByRole("combobox")).toHaveTextContent("Fixture workspace");
    vi.spyOn(getApi(), "listWorkspaces").mockResolvedValue([]);
    fireEvent.click(screen.getByRole("button", { name: "Refresh workspaces" }));
    expect(await screen.findByText(/No source workspaces/)).toBeVisible();
    expect(screen.getByRole("combobox")).toBeEnabled();
    expect(screen.getByRole("combobox")).not.toHaveTextContent("Fixture workspace");
    expect(screen.getByRole("button", { name: "Sync between center servers" })).toBeDisabled();
    expect(syncCenters).not.toHaveBeenCalled();
  });

  it("starts only on click and displays confirmed counts and conflicts", async () => {
    vi.mocked(syncCenters).mockResolvedValue({ cursor: 7, records: [], pending: 0, conflicts: 2 });
    await show();
    expect(syncCenters).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "Sync between center servers" }));
    await waitFor(() => expect(screen.getByRole("status")).toHaveTextContent("2 conflicts need review"));
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
    expect(screen.getByText(/Stopped — Stage 1 of 5/)).toBeVisible();
    expect(screen.getByRole("progressbar", { name: "Sync progress" })).toHaveAttribute("aria-valuenow", "0");
    expect(syncCenters).toHaveBeenCalledOnce();
  });

  it("advances the stage bar from confirmed progress and completes only after success", async () => {
    let update!: (progress: CenterSyncProgress) => void;
    let finish!: (result: CenterSyncResult) => void;
    vi.mocked(syncCenters).mockImplementation((_source, _destination, _workspace, _signal, onProgress) => {
      update = onProgress!;
      return new Promise(resolve => { finish = resolve; });
    });
    await show();
    expect(screen.queryByRole("progressbar")).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Sync between center servers" }));
    const bar = await screen.findByRole("progressbar", { name: "Sync progress" });
    expect(bar).toHaveAttribute("aria-valuenow", "0");
    await waitFor(() => expect(update).toBeDefined());
    act(() => update({ phase: "pulling", batches: 3, records: 42, edits: 0 }));
    expect(bar).toHaveAttribute("aria-valuenow", "2");
    expect(bar).toHaveAttribute("aria-valuemax", "5");
    expect(screen.getByRole("status")).toHaveTextContent("Stage 3 of 5: Reading content from both centers");
    expect(screen.getByText(/42 record updates/)).toBeVisible();
    act(() => update({ phase: "verifying", batches: 3, records: 42, edits: 2 }));
    expect(bar).toHaveAttribute("aria-valuenow", "4");
    expect(screen.queryByText("Sync complete")).not.toBeInTheDocument();
    await act(async () => { finish({ cursor: 42, records: [], pending: 0, conflicts: 0 }); });
    await waitFor(() => expect(bar).toHaveAttribute("aria-valuenow", "5"));
    expect(screen.getByText("Sync complete")).toBeVisible();
    expect(screen.queryByRole("button", { name: "Cancel" })).not.toBeInTheDocument();
  });

  it("retains partial confirmed counts on failure and resets them before retrying", async () => {
    vi.mocked(syncCenters).mockImplementationOnce(async (_source, _destination, _workspace, _signal, update) => {
      update?.({ phase: "pulling", batches: 2, records: 10, edits: 0 });
      throw new Error("Destination offline");
    }).mockImplementationOnce((_source, _destination, _workspace, signal) => new Promise((_resolve, reject) => {
      signal.addEventListener("abort", () => reject(new Error("Cancelled")), { once: true });
    }));
    await show();
    fireEvent.click(screen.getByRole("button", { name: "Sync between center servers" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Destination offline");
    expect(screen.getByText(/10 record updates/)).toBeVisible();
    expect(screen.getByRole("progressbar")).toHaveAttribute("aria-valuenow", "2");
    fireEvent.click(screen.getByRole("button", { name: "Sync between center servers" }));
    await waitFor(() => expect(screen.getByRole("progressbar")).toHaveAttribute("aria-valuenow", "0"));
    expect(screen.queryByText(/10 record updates/)).not.toBeInTheDocument();
    fireEvent.click(await screen.findByRole("button", { name: "Cancel" }));
    await screen.findByRole("alert");
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
