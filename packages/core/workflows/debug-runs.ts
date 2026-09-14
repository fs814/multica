import {
  queryOptions,
  useMutation,
  useQueryClient,
} from "@tanstack/react-query";
import { api } from "../api";
import type {
  StartWorkflowDebugRequest,
  WorkflowDebugPolicy,
} from "./debug-schemas";
import type { DecideWorkflowAcceptanceRequest } from "./schemas";
export const workflowDebugKeys = {
  all: (wsId: string) => ["workflow-test-runs", wsId] as const,
  capabilities: (wsId: string) =>
    [...workflowDebugKeys.all(wsId), "capabilities"] as const,
  detail: (wsId: string, id: string) =>
    [...workflowDebugKeys.all(wsId), "detail", id] as const,
};
export const workflowDebugCapabilitiesOptions = (wsId: string) =>
  queryOptions({
    queryKey: workflowDebugKeys.capabilities(wsId),
    queryFn: () => api.getWorkflowDebugCapabilities(wsId),
    retry: false,
  });
export const workflowDebugRunOptions = (wsId: string, id: string) =>
  queryOptions({
    queryKey: workflowDebugKeys.detail(wsId, id),
    queryFn: () => api.getWorkflowDebugRun(id, wsId),
    enabled: !!id,
    refetchInterval: 3000,
  });
export const workflowDebugDefinitionOptions = (wsId: string, id: string) =>
  queryOptions({
    queryKey: [...workflowDebugKeys.detail(wsId, id), "definition"],
    queryFn: () => api.getWorkflowDebugDefinition(id, wsId),
    enabled: !!id,
    staleTime: Infinity,
  });
export const workflowDebugListOptions = (
  wsId: string,
  templateId: string,
  offset = 0,
) =>
  queryOptions({
    queryKey: [...workflowDebugKeys.all(wsId), "list", templateId, offset],
    queryFn: () => api.listWorkflowDebugRuns(templateId, offset, wsId),
  });
export function useStartWorkflowDebugRun(wsId: string) {
  const qc = useQueryClient();
  return useMutation({
    onMutate: () => ({ wsId }),
    mutationFn: ({
      id,
      body,
    }: {
      id: string;
      body: StartWorkflowDebugRequest;
    }) => api.startWorkflowDebugRun(id, body, wsId),
    onSuccess: (data, _variables, context) => {
      qc.setQueryData(
        workflowDebugKeys.detail(context.wsId, data.run.id),
        data,
      );
    },
    onSettled: (_data, _error, _variables, context) =>
      qc.invalidateQueries({
        queryKey: workflowDebugKeys.all(context?.wsId ?? wsId),
      }),
  });
}
export function useCancelWorkflowDebugRun(wsId: string) {
  const qc = useQueryClient();
  return useMutation({
    onMutate: () => ({ wsId }),
    mutationFn: (id: string) => api.cancelWorkflowDebugRun(id, wsId),
    onSuccess: (data, _variables, context) =>
      qc.setQueryData(
        workflowDebugKeys.detail(context.wsId, data.run.id),
        data,
      ),
    onSettled: (_data, _error, _variables, context) =>
      qc.invalidateQueries({
        queryKey: workflowDebugKeys.all(context?.wsId ?? wsId),
      }),
  });
}
export function useDecideWorkflowDebugAcceptance(wsId: string) {
  const qc = useQueryClient();
  return useMutation({
    onMutate: () => ({ wsId }),
    mutationFn: ({
      id,
      body,
    }: {
      id: string;
      body: DecideWorkflowAcceptanceRequest;
    }) => api.decideWorkflowDebugAcceptance(id, body, wsId),
    onSettled: (_data, _error, _variables, context) =>
      qc.invalidateQueries({
        queryKey: workflowDebugKeys.all(context?.wsId ?? wsId),
      }),
  });
}
export function useUpdateWorkflowDebugSettings(wsId: string) {
  const qc = useQueryClient();
  return useMutation({
    onMutate: () => ({ wsId }),
    mutationFn: ({
      revision,
      settings,
    }: {
      revision: number;
      settings: WorkflowDebugPolicy;
    }) => api.updateWorkflowDebugSettings(revision, settings, wsId),
    onSettled: (_data, _error, _variables, context) =>
      qc.invalidateQueries({
        queryKey: workflowDebugKeys.all(context?.wsId ?? wsId),
      }),
  });
}
