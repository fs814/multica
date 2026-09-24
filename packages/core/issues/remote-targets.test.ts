// @vitest-environment node
import { QueryClient } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { remoteIssueTargetsOptions } from "./remote-targets";

const mocks = vi.hoisted(() => ({ workspaces: vi.fn(), agents: vi.fn(), runtimes: vi.fn(), squads: vi.fn() }));
vi.mock("../api", () => ({ api: { listWorkspaces: mocks.workspaces, listAgents: mocks.agents, listRuntimes: mocks.runtimes, listSquads: mocks.squads } }));
function discover(localId: string | undefined = "local") {
  return new QueryClient({ defaultOptions: { queries: { retry: false } } }).fetchQuery(remoteIssueTargetsOptions("user", localId));
}
beforeEach(() => {
  vi.resetAllMocks();
  mocks.workspaces.mockResolvedValue([{ id: "ws", slug: "work", name: "Work" }]);
  mocks.runtimes.mockResolvedValue([
    { id: "rt-local", daemon_id: "local", status: "online", provider: "codex", name: "Local" },
    { id: "rt-remote", daemon_id: "remote", status: "online", provider: "codex", name: "Remote" },
    { id: "rt-offline", daemon_id: "offline", status: "offline", provider: "codex", name: "Offline" },
  ]);
  mocks.agents.mockResolvedValue([
    { id: "local-agent", name: "Local", runtime_id: "rt-local", owner_id: "user" },
    { id: "remote-agent", name: "Remote", runtime_id: "rt-remote", owner_id: "user" },
    { id: "private-agent", name: "Private", runtime_id: "rt-remote", owner_id: "other", permission_mode: "private" },
    { id: "archived-agent", name: "Archived", runtime_id: "rt-remote", owner_id: "user", archived_at: "2026-01-01" },
    { id: "unbound-agent", name: "Unbound", runtime_id: "rt-remote", runtime_bound: false, owner_id: "user" },
    { id: "offline-agent", name: "Offline", runtime_id: "rt-offline", owner_id: "user" },
  ]);
  mocks.squads.mockResolvedValue([
    { id: "remote-squad", name: "Remote squad", leader_id: "remote-agent" },
    { id: "local-squad", name: "Local squad", leader_id: "local-agent" },
    { id: "private-squad", name: "Private squad", leader_id: "private-agent" },
    { id: "archived-squad", name: "Archived squad", leader_id: "remote-agent", archived_at: "2026-01-01" },
  ]);
});
describe("remote issue targets", () => {
  it("includes remote squads and agents, excludes local, offline, archived and unauthorized targets", async () => {
    const targets = await discover();
    expect(targets.map(target => [target.assigneeType, target.assigneeId])).toEqual([["agent", "remote-agent"], ["squad", "remote-squad"]]);
    expect(mocks.squads).toHaveBeenCalledWith("work");
    expect(mocks.agents).toHaveBeenCalledWith({ workspace_id: "ws" }, "work");
  });
  it("fails closed before discovery when local identity is missing", async () => {
    await expect(new QueryClient({ defaultOptions: { queries: { retry: false } } }).fetchQuery(remoteIssueTargetsOptions("user", undefined))).rejects.toThrow("identity");
    expect(mocks.workspaces).not.toHaveBeenCalled();
  });
  it("fails discovery instead of treating a failed workspace as a successful empty result", async () => {
    mocks.squads.mockRejectedValue(new Error("Center down"));
    await expect(discover()).rejects.toThrow("Center down");
  });
});
