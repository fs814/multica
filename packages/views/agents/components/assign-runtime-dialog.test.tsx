// @vitest-environment jsdom
import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nProvider } from "@multica/core/i18n/react";
import type { Agent, AgentRuntime } from "@multica/core/types";
import enAgents from "../../locales/en/agents.json";
import { AssignRuntimeDialog } from "./assign-runtime-dialog";

const client = vi.hoisted(() => ({ getAgent: vi.fn(), updateAgent: vi.fn() }));
vi.mock("@multica/core/api", () => ({ api: client, getApi: () => client }));
vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "workspace" }));
// The shared picker's grouping and permission behavior has its own tests.
// Here we exercise draft selection vs confirmation and the real mutation path.
vi.mock("./inspector/runtime-picker", () => ({ RuntimePicker: ({ onChange, canEdit }: { onChange: (id: string) => void; canEdit: boolean }) =>
  <button disabled={!canEdit} onClick={() => onChange("runtime")}>Choose fixture runtime</button>,
}));
const runtime = { id: "runtime", workspace_id: "workspace", owner_id: "owner", visibility: "private" } as AgentRuntime;
const agent = (id: string, overrides = {}) => ({ id, name: `Agent ${id}`, workspace_id: "workspace", owner_id: "owner", runtime_id: "", runtime_bound: false, archived_at: null, ...overrides } as Agent);
const complete = vi.fn(), busy = vi.fn();
function show(agents = [agent("a"), agent("b")], runtimes = [runtime]) {
  const queryClient = new QueryClient({ defaultOptions: { mutations: { retry: false }, queries: { retry: false } } });
  const ui = (nextRuntimes: AgentRuntime[]) => <QueryClientProvider client={queryClient}><I18nProvider locale="en" resources={{ en: { agents: enAgents } }}>
    <AssignRuntimeDialog agents={agents} runtimes={nextRuntimes} members={[]} currentUserId="owner" disabled={false} onComplete={complete} onBusyChange={busy} />
  </I18nProvider></QueryClientProvider>;
  const view = render(ui(runtimes));
  return { ...view, changeRuntimes: (next: AgentRuntime[]) => view.rerender(ui(next)) };
}
async function choose() {
  fireEvent.click(screen.getByRole("button", { name: "Assign runtime" }));
  await screen.findByRole("dialog", { name: "Assign runtime" });
  fireEvent.click(screen.getByRole("button", { name: "Choose fixture runtime" }));
}
beforeEach(() => {
  vi.clearAllMocks();
  client.getAgent.mockImplementation(async id => agent(id));
  client.updateAgent.mockImplementation(async id => agent(id, { runtime_id: "runtime", runtime_bound: true }));
});

describe("bulk runtime assignment dialog", () => {
  it("requires confirmation, skips ineligible selected agents, and closes only on success", async () => {
    show([agent("a"), agent("b"), agent("bound", { runtime_id: "old" }), agent("archived", { archived_at: "2026-01-01" }), agent("other", { owner_id: "other" })]);
    await choose();
    expect(screen.getByText(/Eligible agents: 2. Skipped: 3/)).toBeVisible();
    expect(client.getAgent).not.toHaveBeenCalled();
    expect(client.updateAgent).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "Assign", exact: true }));
    await waitFor(() => expect(complete).toHaveBeenCalledOnce());
    expect(client.updateAgent.mock.calls.map(call => call[0])).toEqual(["a", "b"]);
    expect(busy.mock.calls).toEqual([[true], [false]]);
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  });

  it("keeps failed assignments visible and retries only failures", async () => {
    client.updateAgent.mockImplementation(async id => {
      if (id === "b") throw new Error("offline");
      return agent(id, { runtime_id: "runtime", runtime_bound: true });
    });
    show();
    await choose();
    fireEvent.click(screen.getByRole("button", { name: "Assign", exact: true }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Assigned: 1. Skipped: 0. Failed: 1.");
    expect(complete).not.toHaveBeenCalled();
    expect(screen.getByRole("dialog")).toBeVisible();
    client.updateAgent.mockImplementation(async id => agent(id, { runtime_id: "runtime", runtime_bound: true }));
    fireEvent.click(screen.getByRole("button", { name: "Retry failed agents" }));
    await waitFor(() => expect(complete).toHaveBeenCalledOnce());
    expect(client.updateAgent.mock.calls.map(call => call[0])).toEqual(["a", "b", "b"]);
  });

  it("explains when no destination runtime is available", async () => {
    show(undefined, []);
    await choose();
    expect(screen.getByText(/No usable runtime is registered/)).toBeVisible();
    expect(screen.getByRole("button", { name: "Assign", exact: true })).toBeDisabled();
    expect(client.updateAgent).not.toHaveBeenCalled();
  });

  it("disables confirmation if the chosen runtime disappears or becomes private to another owner", async () => {
    const view = show();
    await choose();
    expect(screen.getByRole("button", { name: "Assign", exact: true })).toBeEnabled();
    view.changeRuntimes([{ ...runtime, owner_id: "other" }]);
    expect(screen.getByRole("button", { name: "Assign", exact: true })).toBeDisabled();
    view.changeRuntimes([]);
    expect(screen.getByRole("button", { name: "Assign", exact: true })).toBeDisabled();
  });

  it("disables the action for selections without eligible owned agents", () => {
    show([agent("bound", { runtime_id: "runtime" }), agent("other", { owner_id: "other" })]);
    expect(screen.getByRole("button", { name: "Assign runtime" })).toBeDisabled();
  });

  it("locks confirmation and dismissal while writes are pending", async () => {
    let finish!: (value: Agent) => void;
    client.updateAgent.mockImplementationOnce(() => new Promise(resolve => { finish = resolve; }));
    show([agent("a")]);
    await choose();
    fireEvent.click(screen.getByRole("button", { name: "Assign", exact: true }));
    await waitFor(() => expect(client.updateAgent).toHaveBeenCalledOnce());
    expect(screen.getByRole("button", { name: "Assigning..." })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Cancel" })).toBeDisabled();
    fireEvent.keyDown(screen.getByRole("dialog"), { key: "Escape" });
    expect(screen.getByRole("dialog")).toBeVisible();
    await act(async () => finish(agent("a", { runtime_id: "runtime", runtime_bound: true })));
    await waitFor(() => expect(complete).toHaveBeenCalledOnce());
  });

  it("stops remaining writes when the panel unmounts", async () => {
    let finish!: (value: Agent) => void;
    client.getAgent.mockImplementationOnce(() => new Promise(resolve => { finish = resolve; }));
    const view = show();
    await choose();
    fireEvent.click(screen.getByRole("button", { name: "Assign", exact: true }));
    await waitFor(() => expect(client.getAgent).toHaveBeenCalledOnce());
    view.unmount();
    await act(async () => finish(agent("a")));
    expect(client.updateAgent).not.toHaveBeenCalled();
  });
});
