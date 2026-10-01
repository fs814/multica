// @vitest-environment node
import { describe, expect, it, vi } from "vitest";
import type { AgentRuntime } from "../types";
import { assignMissingRuntimes } from "./bulk-runtime-assignment";

const runtime = { id: "runtime", workspace_id: "workspace", owner_id: "owner", visibility: "private" } as AgentRuntime;
const unbound = (id: string) => ({ id, workspace_id: "workspace", owner_id: "owner", runtime_id: "", runtime_bound: false, archived_at: null });
function fixture() {
  const client = {
    getAgent: vi.fn(async (id: string): Promise<unknown> => unbound(id)),
    updateAgent: vi.fn(async (id: string): Promise<unknown> => ({ ...unbound(id), runtime_id: runtime.id, runtime_bound: true })),
  };
  const input = { workspaceId: "workspace", userId: "owner", agentIds: ["a", "b"], runtime, signal: new AbortController().signal };
  return { input, client };
}

describe("bulk assignment of missing runtimes", () => {
  it("only updates bindings and clears provider-specific settings after re-reading each agent", async () => {
    const { input, client } = fixture();
    const report = vi.fn();
    const result = await assignMissingRuntimes({ ...input, agentIds: ["a", "b", "a"] }, client, report);
    expect(result).toEqual({ assigned: ["a", "b"], skipped: [], failed: [] });
    expect(client.updateAgent.mock.calls).toEqual([
      ["a", { runtime_id: "runtime", model: "", thinking_level: "", service_tier: "" }],
      ["b", { runtime_id: "runtime", model: "", thinking_level: "", service_tier: "" }],
    ]);
    expect(client.getAgent.mock.invocationCallOrder[1]).toBeGreaterThan(client.updateAgent.mock.invocationCallOrder[0]!);
    expect(report.mock.calls[0]?.[0]).toEqual({ assigned: ["a"], skipped: [], failed: [] });
  });

  it.each([
    { runtime_id: "already-bound" }, { runtime_bound: true },
    { runtime_id: "already-bound", runtime_bound: false },
    { archived_at: "2026-01-01" }, { owner_id: "another-owner" }, { workspace_id: "another-workspace" },
  ])("skips agents whose current state is ineligible: %j", async change => {
    const { input, client } = fixture();
    client.getAgent.mockImplementation(async id => ({ ...unbound(id), ...change }));
    expect(await assignMissingRuntimes(input, client)).toEqual({ assigned: [], skipped: ["a", "b"], failed: [] });
    expect(client.updateAgent).not.toHaveBeenCalled();
  });

  it.each([null, {}, { ...unbound("a"), runtime_id: undefined }, { ...unbound("wrong") }])("fails closed on malformed or mismatched responses: %j", async raw => {
    const { input, client } = fixture();
    client.getAgent.mockResolvedValue(raw);
    expect((await assignMissingRuntimes(input, client)).failed).toEqual(["a", "b"]);
    expect(client.updateAgent).not.toHaveBeenCalled();
  });

  it.each([{ owner_id: "other" }, { owner_id: null, visibility: "public" }, { workspace_id: "other" }])("rejects inaccessible destination runtimes: %j", async change => {
    const { input, client } = fixture();
    await expect(assignMissingRuntimes({ ...input, runtime: { ...runtime, ...change } as AgentRuntime }, client)).rejects.toThrow("not available");
    expect(client.getAgent).not.toHaveBeenCalled();
  });

  it("allows a public runtime using the shared runtime permission rule", async () => {
    const { input, client } = fixture();
    const result = await assignMissingRuntimes({ ...input, runtime: { ...runtime, owner_id: "other", visibility: "public" } }, client);
    expect(result.assigned).toEqual(["a", "b"]);
  });

  it("keeps successful assignments and reports failures without automatic retries", async () => {
    const { input, client } = fixture();
    client.updateAgent.mockRejectedValueOnce(new Error("offline"));
    expect(await assignMissingRuntimes(input, client)).toEqual({ assigned: ["b"], skipped: [], failed: ["a"] });
    expect(client.updateAgent).toHaveBeenCalledTimes(2);
  });

  it("does not count an unconfirmed write as success", async () => {
    const { input, client } = fixture();
    client.updateAgent.mockResolvedValue({ ...unbound("a"), runtime_id: "other" });
    expect((await assignMissingRuntimes(input, client)).failed).toEqual(["a", "b"]);
  });

  it("stops before writing if the workspace panel unmounts during a read", async () => {
    const { input, client } = fixture();
    const active = new AbortController();
    client.getAgent.mockImplementation(async id => { active.abort(); return unbound(id); });
    await expect(assignMissingRuntimes({ ...input, signal: active.signal }, client)).rejects.toThrow();
    expect(client.updateAgent).not.toHaveBeenCalled();
    expect(client.getAgent).toHaveBeenCalledTimes(1);
  });
});
