import type { Agent, AgentRuntime } from "../types";
import { deriveRuntimeHealth } from "./derive-health";
import { runtimeDisplayLabel } from "./display";

export function splitRuntimeName(name: string): { base: string; hostname: string | null } {
  const m = name.match(/^(.+?)\s+\(([^)]+)\)$/);
  return m?.[1] && m[2] ? { base: m[1], hostname: m[2] } : { base: name, hostname: null };
}

export function runtimeDeviceName(runtime: AgentRuntime): string | null {
  return splitRuntimeName(runtime.name).hostname ?? runtime.device_info?.trim().split(" · ")[0]?.trim() ?? null;
}

// Display names and IP addresses are never machine identity keys.
export function runtimeMachineId(runtime: AgentRuntime): string {
  return runtime.daemon_id ? `${runtime.runtime_mode}:${runtime.daemon_id}` : `${runtime.runtime_mode}:runtime:${runtime.id}`;
}

export function sharedCustomName(runtimes: AgentRuntime[]): string | null {
  const first = runtimes[0]?.custom_name?.trim();
  return first && runtimes.every(r => r.custom_name?.trim() === first) ? first : null;
}

export function machineTitle(runtimes: AgentRuntime[], options: { isCurrent: boolean; localMachineName?: string | null }): string {
  const shared = sharedCustomName(runtimes);
  if (shared) return shared;
  if (options.isCurrent && options.localMachineName) return options.localMachineName;
  const first = runtimes[0];
  if (!first) return "Unknown machine";
  return runtimeDeviceName(first) || (first.runtime_mode === "cloud" ? `${first.provider.charAt(0).toUpperCase()}${first.provider.slice(1)} cloud` : first.daemon_id?.slice(0, 8)) || "Unknown machine";
}

// Input runtimes must come from the workspace's visibility-filtered query.
// This is current agent binding only. Historical runs must use task.runtime_id.
export function executionLocation(agent: Pick<Agent, "runtime_id" | "runtime_bound"> | undefined, runtimes: AgentRuntime[], now: number) {
  if (!agent) return { state: "unavailable" as const };
  if (!agent.runtime_id || agent.runtime_bound === false) return { state: "unbound" as const };
  const runtime = runtimes.find(r => r.id === agent.runtime_id);
  if (!runtime) return { state: "unavailable" as const };
  const machineId = runtimeMachineId(runtime);
  const peers = runtimes.filter(r => runtimeMachineId(r) === machineId);
  return {
    state: "bound" as const, machineId,
    machineName: machineTitle(peers, { isCurrent: false }),
    shortId: (runtime.daemon_id || runtime.id).slice(0, 8),
    runtimeLabel: runtimeDisplayLabel(runtime),
    deviceInfo: [runtime.device_info, runtime.metadata?.os, runtime.metadata?.arch].filter((value): value is string => typeof value === "string" && value.trim() !== "").join(" · ") || null,
    health: deriveRuntimeHealth(runtime, now),
  };
}

export function summarizeExecutionLocations(agents: (Pick<Agent, "runtime_id" | "runtime_bound"> | undefined)[], runtimes: AgentRuntime[], now: number) {
  const groups = new Map<string, { label: string; count: number }>();
  let unbound = 0, unavailable = 0;
  for (const agent of agents) {
    const location = executionLocation(agent, runtimes, now);
    if (location.state === "unbound") { unbound++; continue; }
    if (location.state === "unavailable") { unavailable++; continue; }
    const group = groups.get(location.machineId) ?? { label: `${location.machineName} · ${location.shortId}`, count: 0 };
    group.count++;
    groups.set(location.machineId, group);
  }
  return { machines: [...groups.values()], unbound, unavailable };
}
