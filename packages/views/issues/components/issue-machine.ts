import type { Agent, AgentRuntime, AgentTask, Issue } from "@multica/core/types";
import { runtimeDisplayName } from "@multica/core/runtimes";

type MachineSource = "last_run" | "assigned" | "unselected" | "unavailable";

/** Resolve the machine that actually ran this issue, then its next assigned target. */
export function resolveIssueMachine(
  issue: Pick<Issue, "assignee_type" | "assignee_id">,
  agents: Pick<Agent, "id" | "runtime_id">[],
  runtimes: Pick<AgentRuntime, "id" | "name" | "custom_name">[],
  tasks: Pick<AgentTask, "runtime_id" | "created_at">[],
): { source: MachineSource; name?: string } {
  const latestRun = tasks.filter(task => task.runtime_id)
    .sort((a, b) => b.created_at.localeCompare(a.created_at))[0];
  const assignedRuntimeId = issue.assignee_type === "agent"
    ? agents.find(agent => agent.id === issue.assignee_id)?.runtime_id
    : undefined;
  const runtimeId = latestRun?.runtime_id || assignedRuntimeId;
  if (!runtimeId) return { source: "unselected" };
  const runtime = runtimes.find(candidate => candidate.id === runtimeId);
  if (!runtime) return { source: "unavailable" };
  return { source: latestRun ? "last_run" : "assigned", name: runtimeDisplayName(runtime) };
}
