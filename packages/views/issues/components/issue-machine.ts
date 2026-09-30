import type { Agent, AgentRuntime, AgentTask, Issue, Squad } from "@multica/core/types";
import { runtimeDisplayName } from "@multica/core/runtimes";

type MachineSource = "last_run" | "assigned" | "unselected" | "unavailable";

/** Resolve the machine that actually ran this issue, then its next assigned target. */
export function resolveIssueMachine(
  issue: Pick<Issue, "assignee_type" | "assignee_id">,
  agents: Pick<Agent, "id" | "runtime_id">[],
  runtimes: Pick<AgentRuntime, "id" | "name" | "custom_name">[],
  tasks: Pick<AgentTask, "runtime_id" | "created_at">[],
  squads: Pick<Squad, "id" | "leader_id">[] = [],
): { source: MachineSource; name?: string } {
  const latestRun = tasks.filter(task => task.runtime_id)
    .sort((a, b) => b.created_at.localeCompare(a.created_at))[0];
  // Squad assignments dispatch through the leader's runtime, just like the
  // backend. Display names are not identities and must never select an agent.
  const assignedAgentId = issue.assignee_type === "squad"
    ? squads.find(squad => squad.id === issue.assignee_id)?.leader_id
    : issue.assignee_type === "agent" ? issue.assignee_id : undefined;
  const assignedAgent = agents.find(agent => agent.id === assignedAgentId);
  const runtimeId = latestRun?.runtime_id || assignedAgent?.runtime_id;
  if (!runtimeId) {
    const missingAssignee = issue.assignee_id &&
      (issue.assignee_type === "agent" || issue.assignee_type === "squad") && !assignedAgent;
    return { source: missingAssignee ? "unavailable" : "unselected" };
  }
  const runtime = runtimes.find(candidate => candidate.id === runtimeId);
  if (!runtime) return { source: "unavailable" };
  return { source: latestRun ? "last_run" : "assigned", name: runtimeDisplayName(runtime) };
}
