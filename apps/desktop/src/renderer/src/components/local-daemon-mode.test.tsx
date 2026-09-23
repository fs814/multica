import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { LocalDaemonMode } from "./local-daemon-mode";

vi.mock("@multica/views/platform", () => ({ DragStrip: () => null }));
vi.mock("./center-settings-tab", () => ({
  LocalDaemonConnection: () => <span>Daemon connected</span>,
  CenterSettingsTab: () => <span>Center settings</span>,
}));
vi.mock("./local-issues-home", () => ({ LocalIssuesHome: () => <span>Local issue list</span> }));

beforeEach(() => {
  Object.defineProperty(window, "daemonAPI", { configurable: true, value: {
    getStatus: vi.fn().mockResolvedValue({ state: "running", agents: ["codex"] }),
    getLocalCapabilities: vi.fn().mockResolvedValue({
      provider: "codex", skills: [{ key: "review", name: "Review" }],
      skills_supported: true, mcp_servers: [{ name: "fetch", enabled: true }], mcp_supported: true,
    }),
  } });
});

describe("local daemon mode", () => {
  it("opens the local issue workspace and discovers capabilities without Center", async () => {
    const onBack = vi.fn();
    const onOpenCenter = vi.fn();
    render(<LocalDaemonMode onBack={onBack} onOpenCenter={onOpenCenter} centerConfigured={false} centerUserId={null} />);
    expect(screen.getByText("Local issue list")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Open Center" })).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Skills & MCP" }));
    await waitFor(() => expect(screen.getByText("Review")).toBeInTheDocument());
    expect(screen.getByText("fetch")).toBeInTheDocument();
    expect(window.daemonAPI.getLocalCapabilities).toHaveBeenCalledWith("codex");
    fireEvent.click(screen.getByRole("button", { name: "Choose mode" }));
    expect(onBack).toHaveBeenCalledOnce();
    expect(onOpenCenter).not.toHaveBeenCalled();
  });
});
