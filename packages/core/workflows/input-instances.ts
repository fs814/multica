import { queryOptions, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { useWorkspaceId } from "../hooks";
import type { SaveWorkflowInputInstance } from "./input-instance-schemas";

export const workflowInputInstanceKeys = {
  list: (wsId: string, templateId: string) => ["workflow-input-instances", wsId, templateId] as const,
};
export function workflowInputInstanceListOptions(wsId: string, templateId: string) {
  return queryOptions({
    queryKey: workflowInputInstanceKeys.list(wsId, templateId),
    queryFn: () => api.listWorkflowInputInstances(templateId),
    enabled: Boolean(wsId && templateId),
  });
}
export function useSaveWorkflowInputInstance(templateId: string) {
  const wsId = useWorkspaceId();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, ...body }: SaveWorkflowInputInstance & { id?: string }) => api.saveWorkflowInputInstance(templateId, body, id),
    onSuccess: () => qc.invalidateQueries({ queryKey: workflowInputInstanceKeys.list(wsId, templateId) }),
  });
}
export function useDeleteWorkflowInputInstance(templateId: string) {
  const wsId = useWorkspaceId();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => api.deleteWorkflowInputInstance(templateId, id),
    onSuccess: () => qc.invalidateQueries({ queryKey: workflowInputInstanceKeys.list(wsId, templateId) }),
  });
}