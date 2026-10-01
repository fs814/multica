import { useEffect, useRef } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { getApi } from "../api";
import type { AgentRuntime } from "../types";
import { workspaceKeys } from "../workspace/queries";
import { assignMissingRuntimes, type RuntimeAssignmentResult } from "./bulk-runtime-assignment";

export function useAssignMissingRuntimes(workspaceId: string, userId: string | null) {
  const queryClient = useQueryClient();
  const controller = useRef<AbortController | null>(null);
  useEffect(() => () => controller.current?.abort(), [workspaceId, userId]);
  return useMutation({
    mutationFn: async ({ agentIds, runtime, report }: { agentIds: string[]; runtime: AgentRuntime; report: (result: RuntimeAssignmentResult) => void }) => {
      controller.current?.abort();
      const active = new AbortController();
      controller.current = active;
      const client = getApi();
      return assignMissingRuntimes({ workspaceId, userId: userId ?? "", agentIds, runtime, signal: active.signal }, client, report);
    },
    onSettled: () => Promise.all([
      queryClient.invalidateQueries({ queryKey: workspaceKeys.agents(workspaceId) }),
      queryClient.invalidateQueries({ queryKey: ["agents"] }),
    ]),
  });
}
