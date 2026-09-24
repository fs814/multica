import { fireEvent, render as renderView, screen, waitFor, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { LocalIssuesHome } from "./local-issues-home";

const fixture = vi.hoisted(() => ({
  createLocal: vi.fn(),
  createCenter: vi.fn(),
  listWorkspaces: vi.fn(),
  listRuntimes: vi.fn(),
  listAgents: vi.fn(),
  listSquads: vi.fn(),
  setWorkspace: vi.fn(),
}));

vi.mock("@multica/core/api", () => ({ api: {
  createIssue: fixture.createCenter, listWorkspaces: fixture.listWorkspaces,
  listRuntimes: fixture.listRuntimes, listAgents: fixture.listAgents, listSquads: fixture.listSquads,
} }));
vi.mock("@multica/core/platform", () => ({ setCurrentWorkspace: fixture.setWorkspace }));
vi.mock("@multica/views/platform", () => ({ DragStrip: () => null }));
vi.mock("./center-settings-tab", () => ({ LocalDaemonConnection: () => <span>Connected</span>, CenterSettingsTab: () => null }));

beforeEach(() => {
  vi.clearAllMocks();
  fixture.createLocal.mockResolvedValue({ id: "local-1", title: "Fix local", machine: "local", directory: "/project", provider: "codex", status: "queued" });
  fixture.createCenter.mockResolvedValue({ id: "remote-1" });
  fixture.listWorkspaces.mockResolvedValue([]);
  fixture.listSquads.mockResolvedValue([]);
  Object.defineProperty(window, "daemonAPI", { configurable: true, value: {
    getStatus: vi.fn().mockResolvedValue({ state: "running", daemonId: "this-machine", agents: ["codex"] }),
    listLocalIssues: vi.fn().mockResolvedValue([]),
    getLocalIssueDefaultDirectory: vi.fn().mockResolvedValue("/profile/local-workspace"),
    getLocalCapabilities: vi.fn().mockResolvedValue({ provider: "codex", skills: [{ key: "review", name: "Review" }], skills_supported: true, mcp_servers: [{ name: "fetch", enabled: true }], mcp_supported: true }),
    createLocalIssue: fixture.createLocal,
  } });
  Object.defineProperty(window, "desktopAPI", { configurable: true, value: {
    pickDirectory: vi.fn().mockResolvedValue({ ok: true, path: "/project" }),
  } });
});

function render(children: ReactNode) {
  return renderView(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>{children}</QueryClientProvider>);
}

async function openRemoteForm() {
  fixture.listWorkspaces.mockResolvedValue([{ id: "workspace-1", slug: "work", name: "Work" }]);
  fixture.listRuntimes.mockResolvedValue([{ id: "runtime", daemon_id: "remote", status: "online", name: "Remote", provider: "codex" }]);
  fixture.listAgents.mockResolvedValue([{ id: "agent", runtime_id: "runtime", name: "Remote agent", owner_id: "user-1" }]);
  fixture.listSquads.mockResolvedValue([{ id: "squad", leader_id: "agent", name: "Remote team" }]);
  render(<LocalIssuesHome centerAvailable centerUserId="user-1" onOpenCenter={vi.fn()} />);
  fireEvent.click(screen.getByRole("button", { name: "New issue" }));
  fireEvent.click(screen.getByRole("switch", { name: "Remote only" }));
  await screen.findByRole("option", { name: /Remote team/ });
  fireEvent.change(screen.getByLabelText("Issue title"), { target: { value: "Remote work" } });
  fireEvent.change(screen.getByLabelText("Issue machine"), { target: { value: "workspace-1:runtime:squad:squad" } });
}

describe("issue machine selection", () => {
  it("can create using the managed default without choosing a directory", async () => {
    render(<LocalIssuesHome centerAvailable={false} centerUserId={null} onOpenCenter={vi.fn()} />);
    await waitFor(() => expect(screen.getByRole("button", { name: "New issue" })).toBeEnabled());
    fireEvent.click(screen.getByRole("button", { name: "New issue" }));
    fireEvent.change(screen.getByLabelText("Issue title"), { target: { value: "Default directory" } });
    await waitFor(() => expect(screen.getByRole("button", { name: "Create issue" })).toBeEnabled());
    expect(screen.getByLabelText("Local working directory")).toHaveAttribute("placeholder", "/profile/local-workspace");
    fireEvent.click(screen.getByRole("button", { name: "Create issue" }));
    await waitFor(() => expect(fixture.createLocal).toHaveBeenCalledWith(expect.objectContaining({ directory: "", machine: "local" })));
    expect(fixture.createCenter).not.toHaveBeenCalled();
  });
  it("defaults to local and never calls Center when creating an ordinary issue", async () => {
    render(<LocalIssuesHome centerAvailable={false} centerUserId={null} onOpenCenter={vi.fn()} />);
    expect(screen.queryByRole("region", { name: "Create issue" })).not.toBeInTheDocument();
    await waitFor(() => expect(screen.getByRole("button", { name: "New issue" })).toBeEnabled());
    fireEvent.click(screen.getByRole("button", { name: "New issue" }));
    expect(screen.getByRole("switch", { name: "Remote only" })).not.toBeChecked();
    expect(screen.queryByLabelText("Issue machine")).not.toBeInTheDocument();
    await waitFor(() => expect(screen.getByTestId("local-capabilities")).toHaveTextContent("Review"));
    expect(screen.getByTestId("local-capabilities")).toHaveTextContent("fetch");
    fireEvent.change(screen.getByLabelText("Issue title"), { target: { value: "Fix local" } });
    fireEvent.change(screen.getByLabelText("Local working directory"), { target: { value: "/project" } });
    fireEvent.click(screen.getByRole("button", { name: "Create issue" }));
    await waitFor(() => expect(fixture.createLocal).toHaveBeenCalledWith({
      title: "Fix local", description: "", directory: "/project", provider: "", machine: "local",
    }));
    await waitFor(() => expect(screen.getByRole("complementary", { name: "Issue properties" })).toHaveTextContent("Local (this machine)"));
    expect(fixture.createCenter).not.toHaveBeenCalled();
  });

  it("sends only an explicitly selected other machine through Center", async () => {
    fixture.listWorkspaces.mockResolvedValue([{ id: "workspace-1", slug: "work", name: "Work" }]);
    fixture.listRuntimes.mockResolvedValue([
      { id: "local-runtime", daemon_id: "this-machine", status: "online", name: "Local", provider: "codex" },
      { id: "remote-runtime", daemon_id: "other-machine", status: "online", name: "Other", provider: "codex" },
    ]);
    fixture.listAgents.mockResolvedValue([{ id: "agent-1", runtime_id: "remote-runtime", name: "Agent", owner_id: "user-1" }]);
    const openCenter = vi.fn();
    render(<LocalIssuesHome centerAvailable centerUserId="user-1" onOpenCenter={openCenter} />);
    await waitFor(() => expect(screen.getByRole("button", { name: "New issue" })).toBeEnabled());
    fireEvent.click(screen.getByRole("button", { name: "New issue" }));
    fireEvent.click(screen.getByRole("switch", { name: "Remote only" }));
    await waitFor(() => expect(screen.getByRole("option", { name: /Work · Other/ })).toBeInTheDocument());
    expect(fixture.listAgents).toHaveBeenCalledWith({ workspace_id: "workspace-1" }, "work");
    expect(screen.queryByRole("option", { name: /Local ·/ })).not.toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Issue machine"), { target: { value: "workspace-1:remote-runtime:agent:agent-1" } });
    fireEvent.change(screen.getByLabelText("Issue title"), { target: { value: "Fix remote" } });
    fireEvent.click(screen.getByRole("button", { name: "Create issue" }));
    await waitFor(() => expect(fixture.createCenter).toHaveBeenCalledWith({
      title: "Fix remote", description: "", status: "todo", assignee_type: "agent", assignee_id: "agent-1",
    }, "work"));
    expect(fixture.createLocal).not.toHaveBeenCalled();
    expect(fixture.setWorkspace).toHaveBeenCalledWith("work", "workspace-1");
    expect(openCenter).toHaveBeenCalled();
  });

  it("shows a stopped-daemon hint without requesting local issues", async () => {
    vi.mocked(window.daemonAPI.getStatus).mockResolvedValue({ state: "stopped" });
    render(<LocalIssuesHome centerAvailable={false} centerUserId={null} onOpenCenter={vi.fn()} />);
    await waitFor(() => expect(screen.getByText(/Start the local daemon/)).toBeInTheDocument());
    expect(screen.getByRole("button", { name: "New issue" })).toBeDisabled();
    expect(window.daemonAPI.listLocalIssues).not.toHaveBeenCalled();
  });

  it("creates a remote squad issue without requiring a running local daemon or local directory", async () => {
    vi.mocked(window.daemonAPI.getStatus).mockResolvedValue({ state: "stopped", daemonId: "this-machine" });
    await openRemoteForm();
    expect(screen.queryByLabelText("Local working directory")).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Create issue" }));
    await waitFor(() => expect(fixture.createCenter).toHaveBeenCalledWith(expect.objectContaining({ assignee_type: "squad", assignee_id: "squad" }), "work"));
    expect(fixture.listSquads).toHaveBeenCalledWith("work");
    expect(fixture.createLocal).not.toHaveBeenCalled();
  });

  it("never falls back to local execution after a remote create failure", async () => {
    await openRemoteForm();
    fixture.createCenter.mockRejectedValueOnce(new Error("Center unavailable"));
    fireEvent.click(screen.getByRole("button", { name: "Create issue" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Center unavailable");
    expect(fixture.createLocal).not.toHaveBeenCalled();
    expect(fixture.setWorkspace).not.toHaveBeenCalled();
    expect(screen.getByRole("switch", { name: "Remote only" })).toBeChecked();
  });

  it("blocks submission when a chosen remote target disappears during revalidation", async () => {
    await openRemoteForm();
    fixture.listRuntimes.mockResolvedValue([]);
    fireEvent.click(screen.getByRole("button", { name: "Create issue" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("no longer available");
    expect(fixture.createCenter).not.toHaveBeenCalled();
    expect(fixture.createLocal).not.toHaveBeenCalled();
  });

  it("clears the remote choice when toggled back to local, and requires a new choice when enabled again", async () => {
    await openRemoteForm();
    fireEvent.click(screen.getByRole("switch", { name: "Remote only" }));
    expect(screen.getByLabelText("Local working directory")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("switch", { name: "Remote only" }));
    expect(screen.getByLabelText("Issue machine")).toHaveValue("");
    expect(screen.getByRole("button", { name: "Create issue" })).toBeDisabled();
  });

  it("separates remote agents and squads into independently sorted option groups", async () => {
    await openRemoteForm();
    fixture.listAgents.mockResolvedValue([
      { id: "z", name: "Zulu agent", runtime_id: "runtime", owner_id: "user-1" },
      { id: "a", name: "Alpha agent", runtime_id: "runtime", owner_id: "user-1" },
    ]);
    fixture.listSquads.mockResolvedValue([
      { id: "sz", name: "Zulu squad", leader_id: "a" },
      { id: "sa", name: "Alpha squad", leader_id: "z" },
    ]);
    fireEvent.click(screen.getByRole("switch", { name: "Remote only" }));
    fireEvent.click(screen.getByRole("switch", { name: "Remote only" }));
    await screen.findByRole("option", { name: /Alpha agent/ });
    const agents = within(screen.getByRole("group", { name: "Agents" }));
    const squads = within(screen.getByRole("group", { name: "Squads" }));
    expect(agents.getAllByRole("option").map(option => option.textContent)).toEqual([
      expect.stringContaining("Alpha agent"), expect.stringContaining("Zulu agent"),
    ]);
    expect(squads.getAllByRole("option").map(option => option.textContent)).toEqual([
      expect.stringContaining("Alpha squad"), expect.stringContaining("Zulu squad"),
    ]);
  });
});
