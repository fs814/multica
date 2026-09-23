// @vitest-environment node
import { describe, expect, it } from "vitest";
import type { AgentRuntime } from "../types";
import { executionLocation, summarizeExecutionLocations, runtimeMachineId } from "./execution-location";
const rt = (id: string, daemon: string | null, overrides: Partial<AgentRuntime> = {}): AgentRuntime => ({ id, daemon_id: daemon, workspace_id: "ws", name: "Codex (same-host)", provider: "codex", runtime_mode: "local", status: "online", custom_name: null, device_info: "same-host · macOS arm64", metadata: {}, owner_id: "owner", visibility: "public", last_seen_at: null, created_at: "", updated_at: "", launch_header: "", ...overrides });

describe("execution locations", () => {
  it("keeps identically named machines separate by identity and groups providers", () => {
    const runtimes = [rt("a", "11111111-aaaa"), rt("b", "22222222-bbbb"), rt("c", "11111111-aaaa")];
    const summary = summarizeExecutionLocations(["a", "b", "c"].map(runtime_id => ({ runtime_id })), runtimes, 0);
    expect(summary.machines.map(m => m.count)).toEqual([2, 1]);
    expect(summary.machines[0]?.label).toContain("11111111");
    expect(runtimeMachineId(rt("x", null))).not.toEqual(runtimeMachineId(rt("y", null)));
  });
  it("reflects a rename and offline state without changing identity", () => {
    const before = executionLocation({ runtime_id: "a" }, [rt("a", "daemon-id")], Date.now());
    const after = executionLocation({ runtime_id: "a" }, [rt("a", "daemon-id", { custom_name: "Office", status: "offline" })], Date.now());
    expect(before.state).toBe("bound");
    expect(after).toMatchObject({ state: "bound", machineName: "Office", machineId: "local:daemon-id", health: "long_offline" });
  });
  it("never invents a machine for a hidden, missing or unbound runtime", () => {
    expect(executionLocation({ runtime_id: "private" }, [], 0)).toEqual({ state: "unavailable" });
    expect(executionLocation(undefined, [], 0)).toEqual({ state: "unavailable" });
    expect(executionLocation({ runtime_id: "" }, [], 0)).toEqual({ state: "unbound" });
    expect(executionLocation({ runtime_id: "a", runtime_bound: false }, [rt("a", "daemon")], 0)).toEqual({ state: "unbound" });
    expect(summarizeExecutionLocations([undefined, { runtime_id: "" }], [], 0)).toEqual({ machines: [], unavailable: 1, unbound: 1 });
  });
});
