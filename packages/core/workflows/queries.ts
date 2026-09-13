import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";

/**
 * Query keys and options for workflow templates.
 *
 * Keys are workspace-scoped even though the workspace never appears in the
 * request URL (it rides on the X-Workspace-Slug header). Two workspaces can
 * hold templates with the same `key` - `bug_fix` is seeded into every one - so
 * a workspace-blind cache key would serve one workspace's Bug Fix graph to
 * another after a workspace switch.
 */
export const workflowKeys = {
  all: (wsId: string) => ["workflow-templates", wsId] as const,
  list: (wsId: string) => [...workflowKeys.all(wsId), "list"] as const,
  detail: (wsId: string, id: string) =>
    [...workflowKeys.all(wsId), "detail", id] as const,
};

/**
 * Template list for the Workflows page.
 *
 * `select` unwraps to the array so consumers render `templates` directly,
 * matching `autopilotListOptions`. The envelope (with `total`) stays in the
 * cache so mutations can update the count without refetching.
 *
 * The server seeds built-in templates on this GET, which is the only reason the
 * Bug Fix template shows up in a workspace created before it existed. That
 * makes the first call after a server upgrade a write - so no aggressive
 * caching here; the default staleTime is what we want.
 */
export function workflowTemplateListOptions(wsId: string) {
  return queryOptions({
    queryKey: workflowKeys.list(wsId),
    queryFn: () => api.listWorkflowTemplates(),
    select: (data) => data.templates,
  });
}

/**
 * Full template for the editor, including its newest mutable draft.
 *
 * The default API response remains the effective published graph used by Runs.
 * The editor opts into the draft view so conflict recovery cannot reload an
 * older published definition over the author's working copy.
 */
export function workflowTemplateDetailOptions(wsId: string, id: string) {
  return queryOptions({
    queryKey: workflowKeys.detail(wsId, id),
    queryFn: () => api.getWorkflowTemplate(id, { definition: "draft" }),
  });
}

/** The run form reads the published graph independently of the editor draft. */
export function workflowTemplateRunOptions(wsId: string, id: string, version?: number | null) {
  return queryOptions({
    queryKey: [...workflowKeys.detail(wsId, id), "published", version] as const,
    queryFn: () => api.getWorkflowTemplate(id),
  });
}

/**
 * Query keys for workflow *runs*.
 *
 * A separate tree from `workflowKeys` even though runs belong to templates: a
 * run advances on its own clock (the engine writes on every step transition,
 * driven by agents finishing work) while a template only changes when a human
 * edits it. Nesting runs under the template's key would make every engine
 * event invalidate the editor's cached graph, and `workflow:run_changed`
 * arrives many times per run.
 *
 * Workspace-scoped for the same reason templates are: `bug_fix` is seeded into
 * every workspace, so run lists across workspaces would otherwise collide.
 */
export const workflowRunKeys = {
  all: (wsId: string) => ["workflow-runs", wsId] as const,
  list: (wsId: string) => [...workflowRunKeys.all(wsId), "list"] as const,
  detail: (wsId: string, id: string) =>
    [...workflowRunKeys.all(wsId), "detail", id] as const,
};

/** Filters accepted by `GET /api/workflow-runs`. */
export type WorkflowRunListParams = {
  status?: string;
  template_id?: string;
  limit?: number;
  offset?: number;
};

/**
 * Runs list.
 *
 * The filters are part of the cache key: two different filters are two
 * different server answers, and sharing one entry between them would show a
 * template-filtered list under the unfiltered key after a refetch. `select`
 * unwraps to the array, matching `workflowTemplateListOptions`, while the
 * envelope keeps `total` in the cache for pagination.
 *
 * No `staleTime` override: runs move on their own, and freshness comes from the
 * `workflow:run_changed` invalidation in `use-realtime-sync` rather than from
 * polling.
 */
export function workflowRunListOptions(
  wsId: string,
  params?: WorkflowRunListParams,
) {
  return queryOptions({
    queryKey: [...workflowRunKeys.list(wsId), params ?? {}] as const,
    queryFn: () => api.listWorkflowRuns(params),
    select: (data) => data.runs,
  });
}

/**
 * One run with its full trace (steps, submissions, open acceptance).
 *
 * Callers must check `status !== ""` before trusting the trace: an unreadable
 * response resolves to `EMPTY_WORKFLOW_RUN_DETAIL` with the requested id spread
 * on, which otherwise reads as a real run with no steps. See that constant.
 */
export function workflowRunDetailOptions(
  wsId: string,
  id: string,
  options?: { enabled?: boolean },
) {
  return queryOptions({
    queryKey: workflowRunKeys.detail(wsId, id),
    queryFn: () => api.getWorkflowRun(id),
    enabled: (options?.enabled ?? true) && id !== "",
  });
}

/** Paginated consumers retain the envelope; existing array consumers stay unchanged. */
export function workflowRunPageOptions(wsId: string, params: WorkflowRunListParams) {
  return queryOptions({
    queryKey: [...workflowRunKeys.list(wsId), params] as const,
    queryFn: () => api.listWorkflowRuns(params),
  });
}
