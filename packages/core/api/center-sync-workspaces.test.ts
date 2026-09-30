// @vitest-environment node
import { QueryClient } from "@tanstack/react-query";
import { afterEach, describe, expect, it, vi } from "vitest";
import { getApi } from "./index";
import { centerSyncWorkspaceListOptions } from "./center-sync-workspaces";
import { CenterSyncSession } from "./center-sync-session";

const origin = "https://source.example";
const workspace = { id: "12345678-1234-4234-8234-123456789012", name: "Fixture" };
vi.mock("./index", () => {
  const api = { getToken: () => "login", getBaseUrl: () => "https://source.example", listWorkspaces: async () => [] };
  return { getApi: () => api };
});
afterEach(() => vi.restoreAllMocks());

describe("sync workspace query", () => {
  it("uses only the separate source session and discards results after it disconnects", async () => {
    const client = new QueryClient();
    const session = new CenterSyncSession(origin);
    const user = { id: workspace.id, email: 'fixture@example.test' };
    const identity = vi.spyOn(session, 'currentUser', 'get').mockReturnValue(user);
    const primary = vi.spyOn(getApi(), 'listWorkspaces');
    const list = vi.spyOn(session, 'listWorkspaces').mockResolvedValue([workspace]);
    try {
      expect(await client.fetchQuery(centerSyncWorkspaceListOptions(origin, 'separate', session))).toEqual([workspace]);
      expect(primary).not.toHaveBeenCalled();
      list.mockImplementation(async () => { identity.mockReturnValue(null); return [workspace]; });
      await expect(client.fetchQuery(centerSyncWorkspaceListOptions(origin, 'changed', session))).rejects.toThrow('Source connection changed');
    } finally { client.clear(); }
  });
  it("does not reuse global or previous connection workspace caches", async () => {
    const client = new QueryClient();
    try {
      client.setQueryData(["workspaces", "list"], [{ ...workspace, name: "Wrong center" }]);
      client.setQueryData(centerSyncWorkspaceListOptions(origin, "old").queryKey, [{ ...workspace, name: "Previous login" }]);
      vi.spyOn(getApi(), "listWorkspaces").mockResolvedValue([workspace as Awaited<ReturnType<ReturnType<typeof getApi>["listWorkspaces"]>>[number]]);
      expect(await client.fetchQuery(centerSyncWorkspaceListOptions(origin, "new"))).toEqual([workspace]);
    } finally { client.clear(); }
  });

  it.each([null, {}, [{ id: "invalid", name: "Bad" }], [{ id: "12345678-1234-4234-8234-123456789012" }]])("rejects malformed lists as errors, not empty results: %j", async raw => {
    const client = new QueryClient();
    try {
      vi.spyOn(getApi(), "listWorkspaces").mockResolvedValue(raw as never);
      await expect(client.fetchQuery(centerSyncWorkspaceListOptions(origin, "new"))).rejects.toThrow("Invalid source workspace list");
    } finally { client.clear(); }
  });

  it("rejects source-origin mismatch before fetching", async () => {
    const client = new QueryClient();
    const list = vi.spyOn(getApi(), "listWorkspaces");
    try {
      await expect(client.fetchQuery(centerSyncWorkspaceListOptions("https://different.example", "new"))).rejects.toThrow("Source connection changed");
      expect(list).not.toHaveBeenCalled();
    } finally { client.clear(); }
  });

  it("discards a response after the source session changes", async () => {
    const client = new QueryClient();
    try {
      vi.spyOn(getApi(), "listWorkspaces").mockImplementation(async () => {
        vi.spyOn(getApi(), "getToken").mockReturnValue("changed-login");
        return [];
      });
      await expect(client.fetchQuery(centerSyncWorkspaceListOptions(origin, "new"))).rejects.toThrow("Source connection changed");
    } finally { client.clear(); }
  });
});
