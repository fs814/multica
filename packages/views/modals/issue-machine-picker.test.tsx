import { useState } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nProvider } from "@multica/core/i18n/react";
import type { Agent, AgentRuntime, IssueAssigneeType } from "@multica/core/types";
import enModals from "../locales/en/modals.json";
import enIssues from "../locales/en/issues.json";
import { IssueMachinePicker, useIssueMachineTarget } from "./issue-machine-picker";

const mocks = vi.hoisted(() => ({
  agents: vi.fn(), runtimes: vi.fn(), members: vi.fn(), squads: vi.fn(),
  select: vi.fn(), createLocal: vi.fn(), status: vi.fn(), browse: vi.fn(),
  squadMembers: vi.fn(), defaultDirectory: vi.fn(),
}));
vi.mock("@multica/core/api", () => ({ api: { listAgents: mocks.agents, listRuntimes: mocks.runtimes, listMembers: mocks.members, listSquads: mocks.squads, getSquadMemberStatus: mocks.squadMembers } }));
vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "workspace-1" }));
vi.mock("@multica/core/auth", () => {
  const state = { user: { id: "user-1" } };
  return { useAuthStore: Object.assign((selector: (s: typeof state) => unknown) => selector(state), { getState: () => state }) };
});

function runtime(id: string, machine: string, overrides: Partial<AgentRuntime> = {}): AgentRuntime {
  return {
    id, workspace_id: "workspace-1", daemon_id: `daemon-${machine}`, name: `Codex (${machine})`,
    custom_name: machine, runtime_mode: "local", provider: "codex", launch_header: "", status: "online",
    device_info: machine, metadata: {}, owner_id: "user-1", visibility: "private",
    last_seen_at: new Date().toISOString(), created_at: "2026-01-01T00:00:00Z", updated_at: "2026-01-01T00:00:00Z", ...overrides,
  };
}
function agent(id: string, runtimeId: string | null, overrides: Partial<Agent> = {}): Agent {
  return { id, name: id, runtime_id: runtimeId, archived_at: null, owner_id: "user-1", permission_mode: "private", ...overrides } as Agent;
}
function mount(type?: IssueAssigneeType, id?: string, remote = false, mode: "manual" | "agent" = "manual") {
  function Harness() {
    const [selection, setSelection] = useState({ type, id });
    const [error, setError] = useState("");
    const target = useIssueMachineTarget({ assigneeType: selection.type, assigneeId: selection.id }, remote);
    return <>
      <IssueMachinePicker mode={mode} target={target} onSelect={(type, id) => { mocks.select(type, id); setSelection({ type, id }); }} />
      <button disabled={target.executionBlocked} onClick={() => void (target.localExecution ? target.localRunner.create("OS version", "Show local OS") : target.validateBoundTarget()).catch(cause => setError(String(cause)))}>Submit target</button>
      <button onClick={() => setSelection({ type: "agent", id: "Win agent" })}>External local selection</button>
      {error && <p role="alert">{error}</p>}
    </>;
  }
  return render(<I18nProvider locale="en" resources={{ en: { modals: enModals, issues: enIssues } }}><QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}><Harness /></QueryClientProvider></I18nProvider>);
}
async function openPicker() {
  await userEvent.click(await screen.findByRole("button", { name: /^Execution machine: (?!Loading)/ }));
}
async function selectDirectory() {
  await userEvent.click(screen.getByRole("button", { name: "Browse" }));
  await waitFor(() => expect(screen.getByRole("button", { name: "Submit target" })).toBeEnabled());
}

beforeEach(() => {
  vi.resetAllMocks();
  mocks.status.mockResolvedValue({ state: "running", daemonId: "daemon-Windows", deviceName: "5090", agents: ["codex", "claude"] });
  mocks.createLocal.mockResolvedValue({ id: "local-1", title: "OS version" });
  mocks.browse.mockResolvedValue({ ok: true, path: "C:\\project" });
  Object.defineProperty(window, "daemonAPI", { configurable: true, value: { getStatus: mocks.status, createLocalIssue: mocks.createLocal, getLocalIssueDefaultDirectory: mocks.defaultDirectory } });
  mocks.defaultDirectory.mockResolvedValue("C:\\profile\\local-workspace");
  mocks.squadMembers.mockResolvedValue({ members: [{ member_type: "agent", member_id: "Win agent" }] });
  Object.defineProperty(window, "desktopAPI", { configurable: true, value: { pickDirectory: mocks.browse } });
  mocks.agents.mockResolvedValue([agent("Win agent", "win"), agent("Mac agent", "mac")]);
  mocks.runtimes.mockResolvedValue([runtime("win", "Windows"), runtime("mac", "Mac")]);
  mocks.members.mockResolvedValue([{ user_id: "user-1", role: "member" }]);
  mocks.squads.mockResolvedValue([]);
});

describe("local daemon issue execution", () => {
  it("uses a visible managed default when no directory was chosen", async () => {
    mount();
    await waitFor(() => expect(screen.getByRole("button", { name: "Submit target" })).toBeEnabled());
    expect(screen.getByLabelText("Local working directory")).toHaveAttribute("placeholder", "C:\\profile\\local-workspace");
    await userEvent.click(screen.getByRole("button", { name: "Submit target" }));
    expect(mocks.createLocal).toHaveBeenCalledWith(expect.objectContaining({ directory: "" }));
  });

  it("keeps an explicit directory when the default arrives late", async () => {
    let resolveDefault!: (path: string) => void;
    mocks.defaultDirectory.mockImplementation(() => new Promise<string>(resolve => { resolveDefault = resolve; }));
    mount();
    await selectDirectory();
    resolveDefault("C:\\default");
    await waitFor(() => expect(screen.getByLabelText("Local working directory")).toHaveAttribute("placeholder", "C:\\default"));
    expect(screen.getByLabelText("Local working directory")).toHaveValue("C:\\project");
  });
  it("defaults to direct local creation even with a remembered remote squad and no local Center agents", async () => {
    mocks.agents.mockResolvedValue([]);
    mount("squad", "remote-squad");
    expect(screen.getByRole("switch", { name: "Remote only" })).not.toBeChecked();
    expect(screen.getByRole("button", { name: "Submit target" })).toBeDisabled();
    await selectDirectory();
    await userEvent.selectOptions(screen.getByLabelText("Local CLI"), "claude");
    await userEvent.click(screen.getByRole("button", { name: "Submit target" }));
    await waitFor(() => expect(mocks.createLocal).toHaveBeenCalledWith({
      title: "OS version", description: "Show local OS", directory: "C:\\project", provider: "claude", machine: "local",
    }));
    expect(mocks.agents).toHaveBeenCalled();
    expect(mocks.select).not.toHaveBeenCalled();
  });

  it("does not require a Center daemon identity for direct local execution", async () => {
    mocks.status.mockResolvedValue({ state: "running", agents: ["codex"] });
    mount();
    await selectDirectory();
    expect(screen.getByRole("button", { name: "Submit target" })).toBeEnabled();
  });

  it("remains usable when Center requests would fail", async () => {
    mocks.runtimes.mockRejectedValue(new Error("Center offline"));
    mount();
    await selectDirectory();
    expect(mocks.createLocal).not.toHaveBeenCalled();
  });

  it("requires the local daemon to be running", async () => {
    mocks.status.mockResolvedValue({ state: "stopped", agents: ["codex"] });
    mount();
    await userEvent.click(screen.getByRole("button", { name: "Browse" }));
    expect(screen.getByRole("button", { name: "Submit target" })).toBeDisabled();
  });

  it("requires an installed local CLI and a supported desktop bridge", async () => {
    mocks.status.mockResolvedValue({ state: "running", agents: [] });
    mount();
    await userEvent.click(screen.getByRole("button", { name: "Browse" }));
    expect(screen.getByRole("button", { name: "Submit target" })).toBeDisabled();
  });

  it("rechecks local readiness before dispatch and never falls back to Center", async () => {
    mount();
    await selectDirectory();
    mocks.status.mockResolvedValue({ state: "stopped", agents: [] });
    await userEvent.click(screen.getByRole("button", { name: "Submit target" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("no longer available");
    expect(mocks.createLocal).not.toHaveBeenCalled();
    expect(mocks.createLocal).not.toHaveBeenCalled();
  });

  it("reports a local creation failure without changing execution mode", async () => {
    mount();
    await selectDirectory();
    mocks.createLocal.mockRejectedValue(new Error("Local API unavailable"));
    await userEvent.click(screen.getByRole("button", { name: "Submit target" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Local API unavailable");
    expect(screen.getByRole("switch", { name: "Remote only" })).not.toBeChecked();
    expect(mocks.select).not.toHaveBeenCalled();
  });

  it("keeps local directory and CLI choices when toggling remote on and off", async () => {
    mount("agent", "Mac agent");
    await selectDirectory();
    await userEvent.selectOptions(screen.getByLabelText("Local CLI"), "claude");
    await userEvent.click(screen.getByRole("switch", { name: "Remote only" }));
    await screen.findByRole("button", { name: "Execution machine: Mac" });
    expect(screen.queryByLabelText("Local working directory")).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("switch", { name: "Remote only" }));
    expect(screen.getByLabelText("Local working directory")).toHaveValue("C:\\project");
    expect(screen.getByLabelText("Local CLI")).toHaveValue("claude");
    expect(screen.getByRole("button", { name: "Submit target" })).toBeEnabled();
  });
});

describe("local bound agents and squads", () => {
  async function useBoundTarget() {
    await userEvent.click(screen.getByRole("switch", { name: "Use a local agent or squad" }));
    expect(screen.getByText("Execution machine: Local")).toBeVisible();
    expect(screen.queryByRole("button", { name: /^Execution machine:/ })).not.toBeInTheDocument();
    await userEvent.click(await screen.findByRole("button", { name: /^Agent \/ squad: (?!Loading)/ }));
  }

  it.each(["manual", "agent"] as const)("keeps Local and a separate target selector in %s mode", async mode => {
    mocks.squads.mockResolvedValue([{ id: "local-squad", name: "Local squad", leader_id: "Win agent" }]);
    mount("agent", "Mac agent", false, mode);
    await useBoundTarget();
    expect(screen.getByRole("button", { name: /Win agent ·/ })).toBeVisible();
    expect(screen.getByRole("button", { name: /Local squad · Squad/ })).toBeVisible();
    await userEvent.click(screen.getByRole("button", { name: /Local squad · Squad/ }));
    expect(screen.getByText("Execution machine: Local")).toBeVisible();
    expect(screen.getByRole("button", { name: "Agent / squad: Local squad" })).toBeVisible();
    expect(mocks.select).toHaveBeenCalledWith("squad", "local-squad");
  });

  it("keeps the selector visible with guidance when this daemon has no bound agents", async () => {
    mocks.agents.mockResolvedValue([agent("Mac agent", "mac")]);
    mount();
    await useBoundTarget();
    expect(screen.getByText("No agents or squads are bound to this daemon. Bind an agent to this machine first, or use Local CLI.")).toBeVisible();
    expect(screen.getByRole("button", { name: "Agent / squad: Not selected" })).toBeVisible();
    expect(screen.getByRole("button", { name: "Submit target" })).toBeDisabled();
  });

  it("groups and sorts only this daemon's agents and squads", async () => {
    mocks.agents.mockResolvedValue([agent("Zulu", "win"), agent("Alpha", "win"), agent("Mac agent", "mac")]);
    mocks.squads.mockResolvedValue([
      { id: "z", name: "Zulu squad", leader_id: "Alpha" },
      { id: "a", name: "Alpha squad", leader_id: "Zulu" },
      { id: "m", name: "Mac squad", leader_id: "Mac agent" },
    ]);
    mount("agent", "Mac agent");
    await useBoundTarget();
    const popup = within(screen.getByRole("dialog"));
    expect(popup.getAllByRole("button").map(button => button.textContent)).toEqual([
      expect.stringContaining("Alpha"), expect.stringContaining("Zulu"),
      expect.stringContaining("Alpha squad"), expect.stringContaining("Zulu squad"),
    ]);
    await userEvent.click(popup.getByRole("button", { name: /^Alpha ·/ }));
    expect(mocks.select).toHaveBeenCalledWith("agent", "Alpha");
    expect(screen.getByRole("button", { name: "Submit target" })).toBeEnabled();
    expect(screen.queryByLabelText("Local working directory")).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Submit target" }));
    await waitFor(() => expect(mocks.agents).toHaveBeenCalledTimes(2));
    expect(mocks.createLocal).not.toHaveBeenCalled();
  });

  it("preserves local squad identity and rejects a remotely bound member", async () => {
    mocks.squads.mockResolvedValue([{ id: "local-squad", name: "Local squad", leader_id: "Win agent" }]);
    mocks.squadMembers.mockResolvedValue({ members: [{ member_type: "agent", member_id: "Mac agent" }] });
    mount();
    await useBoundTarget();
    await userEvent.click(screen.getByRole("button", { name: /Local squad · Squad/ }));
    await waitFor(() => expect(mocks.squadMembers).toHaveBeenCalledWith("local-squad"));
    expect(mocks.select).toHaveBeenCalledWith("squad", "local-squad");
    expect(screen.getByRole("button", { name: "Submit target" })).toBeDisabled();
  });

  it("allows a verified entirely local squad", async () => {
    mocks.squads.mockResolvedValue([{ id: "local-squad", name: "Local squad", leader_id: "Win agent" }]);
    mount();
    await useBoundTarget();
    await userEvent.click(screen.getByRole("button", { name: /Local squad · Squad/ }));
    await waitFor(() => expect(screen.getByRole("button", { name: "Submit target" })).toBeEnabled());
    await userEvent.click(screen.getByRole("button", { name: "Submit target" }));
    await waitFor(() => expect(mocks.squadMembers).toHaveBeenCalledTimes(2));
    expect(mocks.createLocal).not.toHaveBeenCalled();
  });

  it("revalidates local bindings before submission without falling back to the CLI", async () => {
    mount();
    await useBoundTarget();
    await userEvent.click(screen.getByRole("button", { name: /Win agent ·/ }));
    mocks.agents.mockResolvedValue([agent("Win agent", "mac")]);
    await userEvent.click(screen.getByRole("button", { name: "Submit target" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("no longer bound entirely to this machine");
    expect(mocks.createLocal).not.toHaveBeenCalled();
  });
});

describe("remote machine picker", () => {
  it("selects a remote agent and excludes the local machine", async () => {
    mount(undefined, undefined, true);
    await openPicker();
    expect(screen.queryByRole("button", { name: /Win agent/ })).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: /Mac agent/ }));
    expect(mocks.select).toHaveBeenCalledWith("agent", "Mac agent");
    expect(screen.getByRole("button", { name: "Submit target" })).toBeEnabled();
    await userEvent.click(screen.getByRole("button", { name: "External local selection" }));
    expect(screen.getByRole("button", { name: "Submit target" })).toBeDisabled();
  });

  it("does not offer archived, unbound or unauthorized agents", async () => {
    mocks.agents.mockResolvedValue([
      agent("Archived", "mac", { archived_at: "2026-01-01" }),
      agent("Private", "mac", { owner_id: "someone-else" }),
      agent("Unbound", null), agent("Contradictory", "mac", { runtime_bound: false }),
    ]);
    mount(undefined, undefined, true);
    await openPicker();
    expect(screen.getByText(enModals.create_issue.machine.empty)).toBeInTheDocument();
  });

  it("shows offline status and preserves squad identity on its leader's machine", async () => {
    mocks.runtimes.mockResolvedValue([runtime("mac", "Mac", { status: "offline" })]);
    mocks.squads.mockResolvedValue([{ id: "squad", name: "Remote squad", leader_id: "Mac agent" }]);
    mount("squad", "squad", true);
    await screen.findByRole("button", { name: "Execution machine: Mac · Offline" });
    await openPicker();
    await userEvent.click(screen.getByRole("button", { name: /Remote squad · Squad/ }));
    expect(mocks.select).toHaveBeenCalledWith("squad", "squad");
  });

  it("shows unavailable targets without silently selecting another agent", async () => {
    mocks.runtimes.mockResolvedValue([]);
    mount("agent", "Mac agent", true);
    await screen.findByRole("button", { name: "Execution machine: Machine unavailable" });
    expect(screen.getByRole("button", { name: "Submit target" })).toBeDisabled();
    expect(mocks.select).not.toHaveBeenCalled();
  });

  it("distinguishes agent creation from execution", async () => {
    mount("agent", "Mac agent", true, "agent");
    await userEvent.click(await screen.findByRole("button", { name: "Creation machine: Mac" }));
    expect(screen.getByText(enModals.create_issue.machine.creation_hint)).toBeInTheDocument();
  });

  it("reports discovery failure and supports retry", async () => {
    mocks.runtimes.mockRejectedValueOnce(new Error("network"));
    mount(undefined, undefined, true);
    await openPicker();
    await userEvent.click(await screen.findByRole("button", { name: "Retry" }));
    await screen.findByRole("button", { name: /Mac agent/ });
    expect(mocks.runtimes).toHaveBeenCalledTimes(2);
  });

  it("separates and sorts agents and squads, including after search", async () => {
    mocks.agents.mockResolvedValue([agent("Zulu agent", "mac"), agent("Alpha agent", "mac")]);
    mocks.squads.mockResolvedValue([
      { id: "sz", name: "Zulu squad", leader_id: "Alpha agent" },
      { id: "sa", name: "Alpha squad", leader_id: "Zulu agent" },
    ]);
    mount(undefined, undefined, true);
    await openPicker();
    const popup = within(screen.getByRole("dialog"));
    expect(popup.getByText(enModals.create_issue.agent.agents_group)).toBeInTheDocument();
    expect(popup.getByText(enModals.create_issue.agent.squads_group)).toBeInTheDocument();
    expect(popup.getAllByRole("button").map(button => button.textContent)).toEqual([
      expect.stringContaining("Alpha agent"), expect.stringContaining("Zulu agent"),
      expect.stringContaining("Alpha squad"), expect.stringContaining("Zulu squad"),
    ]);
    await userEvent.type(screen.getByPlaceholderText("Search machines or agents..."), "squad");
    expect(popup.queryByText(enModals.create_issue.agent.agents_group)).not.toBeInTheDocument();
    expect(popup.getAllByRole("button")).toHaveLength(2);
  });

  it("blocks remote dispatch without local identity but can switch to direct local mode", async () => {
    mocks.status.mockResolvedValue({ state: "running", agents: ["codex"] });
    mount("agent", "Mac agent", true);
    await screen.findByRole("button", { name: "Execution machine: Mac" });
    expect(screen.getByRole("button", { name: "Submit target" })).toBeDisabled();
    await userEvent.click(screen.getByRole("switch", { name: "Remote only" }));
    await selectDirectory();
    expect(screen.getByRole("button", { name: "Submit target" })).toBeEnabled();
  });
});
