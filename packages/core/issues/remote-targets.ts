import { queryOptions, useMutation } from "@tanstack/react-query";
import { api } from "../api";
import { isAgentRuntimeBound } from "../agents/runtime-binding";
import { runtimeDisplayLabel } from "../runtimes";
import { canAssignAgentToIssue } from "../permissions";
import type { CreateIssueRequest } from "../types";

export function remoteIssueTargetsOptions(userId: string | null, localDaemonId: string | undefined) {
  return queryOptions({
    queryKey: ["remote-issue-targets", userId, localDaemonId],
    enabled: !!userId && !!localDaemonId,
    queryFn: async () => {
      if (!localDaemonId) throw new Error("Local machine identity is unavailable");
      const workspaces = await api.listWorkspaces();
      return (await Promise.all(workspaces.map(async (workspace) => {
        const [runtimes, agents, squads] = await Promise.all([
          api.listRuntimes({ workspace_id: workspace.id }, workspace.slug),
          api.listAgents({ workspace_id: workspace.id }, workspace.slug),
          api.listSquads(workspace.slug),
        ]);
        return runtimes.filter(runtime => runtime.daemon_id && runtime.daemon_id !== localDaemonId && runtime.status === "online")
          .flatMap(runtime => agents.filter(agent => !agent.archived_at && isAgentRuntimeBound(agent) && agent.runtime_id === runtime.id
            && canAssignAgentToIssue(agent, { userId, role: "member" }).allowed)
            .flatMap(agent => [
              { type: "agent" as const, id: agent.id, name: agent.name },
              ...squads.filter(squad => !squad.archived_at && squad.leader_id === agent.id)
                .map(squad => ({ type: "squad" as const, id: squad.id, name: squad.name })),
            ]).map(target => ({
              id: `${workspace.id}:${runtime.id}:${target.type}:${target.id}`,
              name: target.name,
              label: `${workspace.name} · ${runtimeDisplayLabel(runtime)} · ${target.name}`,
              workspaceId: workspace.id, workspaceSlug: workspace.slug,
              assigneeType: target.type, assigneeId: target.id,
            })));
      }))).flat();
    },
  });
}

export function useCreateRemoteIssue() {
  return useMutation({
    mutationFn: ({ data, workspaceSlug }: { data: CreateIssueRequest; workspaceSlug: string }) => api.createIssue(data, workspaceSlug),
  });
}
