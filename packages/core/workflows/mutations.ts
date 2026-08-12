import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { useWorkspaceId } from "../hooks";
import { workflowKeys, workflowRunKeys } from "./queries";
import type {
  CreateWorkflowTemplateRequest,
  DecideWorkflowAcceptanceRequest,
  RunWorkflowTemplateRequest,
  UpdateWorkflowTemplateRequest,
  WorkflowRunDetail,
  WorkflowTemplateDetail,
  WorkflowTemplateListResponse,
} from "./schemas";

/**
 * Workflow template mutations.
 *
 * All four write endpoints return the authoritative server row, so each seeds
 * the detail cache from the response and then invalidates the list rather than
 * hand-patching a summary out of a detail payload. No optimistic updates: these
 * are deliberate operations (save / create / publish / archive) where a wrong
 * intermediate state is worse than a short spinner - and publish in particular
 * is irreversible, since a published version's definition is immutable.
 */

export function useCreateWorkflowTemplate() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: (body: CreateWorkflowTemplateRequest) =>
      api.createWorkflowTemplate(body),
    onSuccess: (created) => {
      // A parse-miss fallback has an empty id; caching it under
      // detail(ws, "") would poison a later real read.
      if (!created.id) return;
      qc.setQueryData<WorkflowTemplateDetail>(
        workflowKeys.detail(wsId, created.id),
        created,
      );
    },
    onSettled: () => {
      qc.invalidateQueries({ queryKey: workflowKeys.list(wsId) });
    },
  });
}

/** Forks a built-in template into a user-authored draft.
 *
 * The duplicate has a new id and key, so it seeds its own detail cache and
 * invalidates the list. There is no optimistic row: key selection is a
 * server-side, transactionally serialized decision.
 */
export function useDuplicateWorkflowTemplate() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: (id: string) => api.duplicateWorkflowTemplate(id),
    onSuccess: (created) => {
      if (!created.id || !created.key) return;
      qc.setQueryData<WorkflowTemplateDetail>(
        workflowKeys.detail(wsId, created.id),
        created,
      );
    },
    onSettled: () => {
      qc.invalidateQueries({ queryKey: workflowKeys.list(wsId) });
    },
  });
}

/**
 * Draft save from the graph editor.
 *
 * The success guard reads `key`, not `id`: `updateWorkflowTemplate` spreads the
 * requested id onto its parse-miss fallback so the page keeps its identity, which
 * makes a non-empty `id` say nothing about whether the response was readable.
 * `key` is non-empty on every real template row and empty on the fallback, so it
 * is the field that still separates the two - and caching a fallback would
 * replace a good detail with a blank one on a transient contract drift.
 *
 * The detail cache is seeded but the *list* is only invalidated: a save can move
 * `name`, `description`, `node_count` and `updated_at`, and reconstructing a
 * summary from the detail here would be a second, drift-prone projection of the
 * same row.
 *
 * Note for callers: the cached `definition` is the template's *effective* graph
 * (what a Run started now would pin), which after saving over a published
 * template is still the published bytes - the new draft is deliberately
 * invisible to Runs until publish. So this cache entry is not a mirror of the
 * editor's unsaved graph, and the editor must keep that in its own state rather
 * than re-render from here.
 */
export function useUpdateWorkflowTemplate() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: ({
      id,
      ...body
    }: { id: string } & UpdateWorkflowTemplateRequest) =>
      api.updateWorkflowTemplate(id, body),
    onSuccess: (saved, { id }) => {
      if (!saved.key) return;
      qc.setQueryData<WorkflowTemplateDetail>(
        workflowKeys.detail(wsId, id),
        saved,
      );
    },
    onSettled: () => {
      qc.invalidateQueries({ queryKey: workflowKeys.list(wsId) });
    },
  });
}

export function usePublishWorkflowTemplate() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: (id: string) => api.publishWorkflowTemplate(id),
    onSuccess: (published, id) => {
      if (!published.id) return;
      // Publish flips `status` and `current_version` and appends a version row,
      // so the response is a strictly better detail than what is cached.
      qc.setQueryData<WorkflowTemplateDetail>(
        workflowKeys.detail(wsId, id),
        published,
      );
    },
    onSettled: (_data, _err, id) => {
      qc.invalidateQueries({ queryKey: workflowKeys.detail(wsId, id) });
      qc.invalidateQueries({ queryKey: workflowKeys.list(wsId) });
    },
  });
}

export function useArchiveWorkflowTemplate() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: (id: string) => api.archiveWorkflowTemplate(id),
    onSuccess: (archived, id) => {
      if (!archived.id) return;
      // Archive returns the summary shape (no graph / versions), so patch the
      // cached detail's summary fields in place instead of replacing it - the
      // detail page keeps rendering the graph while the row shows "archived".
      qc.setQueryData<WorkflowTemplateDetail>(
        workflowKeys.detail(wsId, id),
        (old) => (old ? { ...old, ...archived } : old),
      );
      qc.setQueryData<WorkflowTemplateListResponse>(
        workflowKeys.list(wsId),
        (old) =>
          old
            ? {
                ...old,
                templates: old.templates.map((t) =>
                  t.id === id ? { ...t, ...archived } : t,
                ),
              }
            : old,
      );
    },
    onSettled: (_data, _err, id) => {
      qc.invalidateQueries({ queryKey: workflowKeys.detail(wsId, id) });
      qc.invalidateQueries({ queryKey: workflowKeys.list(wsId) });
    },
  });
}

/**
 * Workflow run mutations.
 *
 * No optimistic updates anywhere here, and for a stronger reason than on the
 * template side: these three calls hand control to the *engine*, and only the
 * engine knows what happens next. Starting a run may immediately queue a task,
 * or block on `routing_no_candidate` with no eligible agent; accepting may
 * complete the run or activate another node; rejecting rewinds to a rework
 * target. A client-invented intermediate state would be wrong in a way the user
 * acts on ("it's running" for a run that never started), so every one of these
 * waits for the server's own answer.
 *
 * All three guard the cache seed on `status`, not `id`: the run client methods
 * spread the requested id onto their parse-miss fallback, so a non-empty `id`
 * says nothing about whether the response was readable. See
 * EMPTY_WORKFLOW_RUN_DETAIL - caching a fallback would replace a live trace with
 * a blank one on a transient contract drift.
 */

/**
 * Starts a run from a template.
 *
 * Invalidates the *template* detail as well as the run list: a template's page
 * shows its run history, so a new run changes it. The server derives the
 * idempotency key, which is why this mutation is safe to leave un-debounced -
 * two clicks produce one run, not two.
 */
export function useRunWorkflowTemplate() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: ({
      templateId,
      ...body
    }: { templateId: string } & RunWorkflowTemplateRequest) =>
      api.runWorkflowTemplate(templateId, body),
    onSuccess: (run) => {
      // Both guards matter here and they are not redundant: `id` is not spread
      // onto this endpoint's fallback (the id is what the call returns), so an
      // empty id means "no run to cache under", while an empty `status` means
      // "the payload was unreadable". Either way, do not seed.
      if (!run.id || !run.status) return;
      qc.setQueryData<WorkflowRunDetail>(
        workflowRunKeys.detail(wsId, run.id),
        run,
      );
    },
    onSettled: (_data, _err, { templateId }) => {
      qc.invalidateQueries({ queryKey: workflowRunKeys.list(wsId) });
      qc.invalidateQueries({ queryKey: workflowKeys.detail(wsId, templateId) });
    },
  });
}

/**
 * Cancels a running run.
 *
 * Cancel returns the summary shape (no steps / acceptance), so the cached detail
 * is patched in place rather than replaced - the trace stays on screen while the
 * header flips to "cancelled", which is the whole point of cancelling from a
 * trace view.
 */
export function useCancelWorkflowRun() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: (id: string) => api.cancelWorkflowRun(id),
    onSuccess: (cancelled, id) => {
      if (!cancelled.status) return;
      qc.setQueryData<WorkflowRunDetail>(
        workflowRunKeys.detail(wsId, id),
        (old) => (old ? { ...old, ...cancelled } : old),
      );
    },
    onSettled: (_data, _err, id) => {
      qc.invalidateQueries({ queryKey: workflowRunKeys.detail(wsId, id) });
      qc.invalidateQueries({ queryKey: workflowRunKeys.list(wsId) });
    },
  });
}

/**
 * Records a reviewer's accept / reject on the run's open acceptance gate.
 *
 * The response is the full detail *after* the engine reacted, so it is strictly
 * better than what is cached: an accept may have completed the run, a reject may
 * have opened a fresh attempt at a rework target. Seeding it means the reviewer
 * sees the consequence of their decision without waiting for a refetch.
 *
 * Callers must pass a `reason` and a `rework_target` from the acceptance's own
 * `rework_targets` whenever `accept` is false; the server 422s otherwise.
 */
export function useDecideWorkflowAcceptance() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: ({
      runId,
      ...body
    }: { runId: string } & DecideWorkflowAcceptanceRequest) =>
      api.decideWorkflowAcceptance(runId, body),
    onSuccess: (run, { runId }) => {
      if (!run.status) return;
      qc.setQueryData<WorkflowRunDetail>(
        workflowRunKeys.detail(wsId, runId),
        run,
      );
    },
    onSettled: (_data, _err, { runId }) => {
      qc.invalidateQueries({ queryKey: workflowRunKeys.detail(wsId, runId) });
      qc.invalidateQueries({ queryKey: workflowRunKeys.list(wsId) });
    },
  });
}
