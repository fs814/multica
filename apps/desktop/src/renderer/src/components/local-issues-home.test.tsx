import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { LocalIssuesHome } from "./local-issues-home";

const fixture = vi.hoisted(() => ({
  createLocal: vi.fn(),
  createCenter: vi.fn(),
  listWorkspaces: vi.fn(),
  listRuntimes: vi.fn(),
  listAgents: vi.fn(),
  setWorkspace: vi.fn(),
}));

vi.mock("@multica/core/api", () => ({ api: {
  createIssue: fixture.createCenter, listWorkspaces: fixture.listWorkspaces,
  listRuntimes: fixture.listRuntimes, listAgents: fixture.listAgents,
} }));
vi.mock("@multica/core/platform", () => ({ setCurrentWorkspace: fixture.setWorkspace }));
vi.mock("@multica/views/platform", () => ({ DragStrip: () => null }));
vi.mock("./center-settings-tab", () => ({ LocalDaemonConnection: () => <span>Connected</span>, CenterSettingsTab: () => null }));

beforeEach(() => {
  vi.clearAllMocks();
  fixture.createLocal.mockResolvedValue({ id: "local-1", title: "Fix local", machine: "local", directory: "/project", provider: "codex", status: "queued" });
  fixture.createCenter.mockResolvedValue({ id: "remote-1" });
  fixture.listWorkspaces.mockResolvedValue([]);
  Object.defineProperty(window, "daemonAPI", { configurable: true, value: {
    getStatus: vi.fn().mockResolvedValue({ state: "running", daemonId: "this-machine", agents: ["codex"] }),
    listLocalIssues: vi.fn().mockResolvedValue([]),
    getLocalCapabilities: vi.fn().mockResolvedValue({ provider: "codex", skills: [{ key: "review", name: "Review" }], skills_supported: true, mcp_servers: [{ name: "fetch", enabled: true }], mcp_supported: true }),
    createLocalIssue: fixture.createLocal,
  } });
  Object.defineProperty(window, "desktopAPI", { configurable: true, value: {
    pickDirectory: vi.fn().mockResolvedValue({ ok: true, path: "/project" }),
  } });
});

describe("issue machine selection", () => {
  it("defaults to local and never calls Center when creating an ordinary issue", async () => {
    render(<LocalIssuesHome centerAvailable={false} centerUserId={null} onOpenCenter={vi.fn()} />);
    expect(screen.queryByRole("region", { name: "Create issue" })).not.toBeInTheDocument();
    await waitFor(() => expect(screen.getByRole("button", { name: "New issue" })).toBeEnabled());
    fireEvent.click(screen.getByRole("button", { name: "New issue" }));
    expect(screen.getByLabelText("Issue machine")).toHaveValue("local");
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
      { id: "local-runtime", daemon_id: "this-machine", status: "online", name: "Local" },
      { id: "remote-runtime", daemon_id: "other-machine", status: "online", name: "Other" },
    ]);
    fixture.listAgents.mockResolvedValue([{ id: "agent-1", runtime_id: "remote-runtime", name: "Agent" }]);
    const openCenter = vi.fn();
    render(<LocalIssuesHome centerAvailable centerUserId="user-1" onOpenCenter={openCenter} />);
    await waitFor(() => expect(screen.getByRole("button", { name: "New issue" })).toBeEnabled());
    fireEvent.click(screen.getByRole("button", { name: "New issue" }));
    await waitFor(() => expect(screen.getByRole("option", { name: "Work · Other · Agent" })).toBeInTheDocument());
    expect(fixture.listAgents).toHaveBeenCalledWith({ workspace_id: "workspace-1" }, "work");
    expect(screen.queryByRole("option", { name: /Local ·/ })).not.toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Issue machine"), { target: { value: "workspace-1:remote-runtime:agent-1" } });
    fireEvent.change(screen.getByLabelText("Issue title"), { target: { value: "Fix remote" } });
    fireEvent.click(screen.getByRole("button", { name: "Create issue" }));
    await waitFor(() => expect(fixture.createCenter).toHaveBeenCalledWith({
      title: "Fix remote", description: "", status: "todo", assignee_type: "agent", assignee_id: "agent-1",
    }));
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
});
