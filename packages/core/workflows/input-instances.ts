import {
  queryOptions,
  useMutation,
  useQueryClient,
} from "@tanstack/react-query";
import { api } from "../api";
import { useWorkspaceId } from "../hooks";
import type {
  SaveWorkflowInputInstance,
  WorkflowInputInstance,
  WorkflowInstanceFilters,
  RunWorkflowInstance,
} from "./input-instance-schemas";

export const workflowInputInstanceKeys = {
  list: (wsId: string, templateId: string) =>
    ["workflow-input-instances", wsId, templateId] as const,
};
export function workflowInputInstanceListOptions(
  wsId: string,
  templateId: string,
) {
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
    mutationFn: ({
      id,
      ...body
    }: SaveWorkflowInputInstance & { id?: string }) =>
      api.saveWorkflowInputInstance(templateId, body, id),
    onSuccess: async () => {
      await Promise.all([
        qc.invalidateQueries({
          queryKey: workflowInputInstanceKeys.list(wsId, templateId),
        }),
        qc.invalidateQueries({ queryKey: workflowInstanceKeys.all(wsId) }),
      ]);
    },
  });
}
export function useDeleteWorkflowInputInstance(templateId: string) {
  const wsId = useWorkspaceId();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => api.deleteWorkflowInputInstance(templateId, id),
    onSuccess: async () => {
      await Promise.all([
        qc.invalidateQueries({
          queryKey: workflowInputInstanceKeys.list(wsId, templateId),
        }),
        qc.invalidateQueries({ queryKey: workflowInstanceKeys.all(wsId) }),
      ]);
    },
  });
}
export const workflowInstanceKeys = {
  all: (wsId: string) => ["workflow-instances", wsId] as const,
  detail: (wsId: string, id: string) =>
    ["workflow-instances", wsId, "detail", id] as const,
};
export function workflowInstancesOptions(
  wsId: string,
  filters: WorkflowInstanceFilters = {},
) {
  return queryOptions({
    queryKey: [...workflowInstanceKeys.all(wsId), "list", filters],
    queryFn: () => api.browseWorkflowInstances(filters),
    enabled: Boolean(wsId),
  });
}
export function workflowInstanceOptions(wsId: string, id: string) {
  return queryOptions({
    queryKey: workflowInstanceKeys.detail(wsId, id),
    queryFn: () => api.getWorkflowInstance(id),
    enabled: Boolean(wsId && id),
  });
}
export function workflowInstanceVersionOptions(
  wsId: string,
  templateId: string,
  versionId: string,
) {
  return queryOptions({
    queryKey: [
      ...workflowInstanceKeys.all(wsId),
      "version",
      templateId,
      versionId,
    ],
    queryFn: () => api.getWorkflowInstanceVersion(templateId, versionId),
    enabled: Boolean(wsId && templateId && versionId),
    staleTime: Infinity,
  });
}
export function workflowInstanceValidationOptions(
  wsId: string,
  id: string,
  revision: number,
) {
  return queryOptions({
    queryKey: [...workflowInstanceKeys.detail(wsId, id), "validate", revision],
    queryFn: () => api.validateWorkflowInstance(id),
    enabled: Boolean(wsId && id),
  });
}
export function workflowInstanceRunsOptions(
  wsId: string,
  id: string,
  offset = 0,
) {
  return queryOptions({
    queryKey: [...workflowInstanceKeys.detail(wsId, id), "runs", offset],
    queryFn: () => api.listWorkflowInstanceRuns(id, offset),
    enabled: Boolean(wsId && id),
    refetchInterval: 15000,
  });
}
export function useRunWorkflowInstance(id: string) {
  const wsId = useWorkspaceId();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: RunWorkflowInstance) =>
      api.runWorkflowInstance(id, body),
    onSuccess: () =>
      qc.invalidateQueries({ queryKey: workflowInstanceKeys.all(wsId) }),
  });
}
export function useArchiveWorkflowInstance(id: string) {
  const wsId = useWorkspaceId();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({
      revision,
      archive,
    }: {
      revision: number;
      archive: boolean;
    }) => api.archiveWorkflowInstance(id, revision, archive),
    onSuccess: () =>
      qc.invalidateQueries({ queryKey: workflowInstanceKeys.all(wsId) }),
  });
}

/** Keep each workflow's instances together, preserving recent-first API order. */
export function groupWorkflowInstances(
  instances: readonly WorkflowInputInstance[],
  templates: readonly { id: string; name: string }[] = [],
) {
  const names = new Map(templates.map((template) => [template.id, template.name]));
  const groups = new Map<string, {
    templateId: string;
    name: string;
    instances: WorkflowInputInstance[];
  }>();
  for (const instance of instances) {
    let group = groups.get(instance.templateId);
    if (!group) {
      group = {
        templateId: instance.templateId,
        name: names.get(instance.templateId) || instance.templateName,
        instances: [],
      };
      groups.set(instance.templateId, group);
    }
    group.instances.push(instance);
  }
  return [...groups.values()];
}
