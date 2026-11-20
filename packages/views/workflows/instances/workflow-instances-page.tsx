"use client";
import { useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import {
  workflowInstancesOptions,
  groupWorkflowInstances,
  workflowTemplateListOptions,
  workflowInstanceValidationOptions,
  useRunWorkflowInstance,
  useSaveWorkflowInputInstance,
  useArchiveWorkflowInstance,
} from "@multica/core/workflows";
import type { WorkflowInputInstance } from "@multica/core/workflows";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Layers, Play } from "@multica/ui/icons";
import { AppLink, useNavigation } from "../../navigation";
import { useT } from "../../i18n";
import { WorkflowRunStatusBadge } from "../runs/components/run-status-badge";
import { useWorkflowLocation, workflowListOffset } from "../use-workflow-location";
import { copiedInstanceName } from "./instance-form";
import { InstanceMenu, InstanceStatusChip, NewInstanceButton, instanceStatus } from "./instance-actions";

const PAGE_SIZE = 30;

/**
 * Saved inputs, grouped by workflow. Each row answers "can I run this, and
 * how did it go last time" at a glance (status chip + last run) and offers
 * one primary action, Run; the rest sits in the row's "…" menu.
 *
 * With `templateId` it is the Instances section of that workflow's page.
 */
export function WorkflowInstancesPage({ templateId }: { templateId?: string }) {
  const { t } = useT("workflows");
  const ws = useWorkspaceId();
  const paths = useWorkspacePaths();
  const location = useWorkflowLocation();
  const search = location.params.get("search") ?? "";
  const filter = templateId ?? location.params.get("template") ?? "";
  const archived = location.params.get("archived") === "true";
  const offset = workflowListOffset(location.params.get("instance_offset"));
  const setOffset = (value: number) => location.update({ instance_offset: value });
  const templates = useQuery(workflowTemplateListOptions(ws));
  const list = useQuery(
    workflowInstancesOptions(ws, {
      template_id: filter || undefined,
      search,
      include_archived: archived,
      offset,
      limit: PAGE_SIZE,
    }),
  );
  const groups = groupWorkflowInstances(list.data?.instances ?? [], templates.data);
  const total = list.data?.total ?? 0;
  const shown = list.data?.instances.length ?? 0;
  const filtering = Boolean(search || (!templateId && filter) || archived);
  return (
    <div className="flex min-h-0 flex-1 flex-col overflow-y-auto p-4" data-tab-scroll-root="main">
      <header className="mb-4 flex flex-wrap items-center gap-3">
        {!templateId && (
          <div className="min-w-0 flex-1">
            <h1 className="font-semibold">{t(($) => $.instances.all)}</h1>
            <p className="mt-1 text-caption text-muted-foreground">{t(($) => $.instances.grouped_hint)}</p>
          </div>
        )}
        <div className={`flex flex-wrap items-center gap-2 ${templateId ? "flex-1" : ""}`}>
          <Input
            className="h-8 w-56"
            aria-label={t(($) => $.instances.search)}
            placeholder={t(($) => $.instances.search)}
            value={search}
            onChange={(e) => location.update({ search: e.target.value, instance_offset: 0 }, true)}
          />
          {!templateId && (
            <select
              aria-label={t(($) => $.page.title)}
              className="h-8 rounded-md border bg-background px-2 text-caption"
              value={filter}
              onChange={(e) => location.update({ template: e.target.value, instance_offset: 0 })}
            >
              <option value="">{t(($) => $.instances.all_workflows)}</option>
              {templates.data?.map((tpl) => <option key={tpl.id} value={tpl.id}>{tpl.name}</option>)}
            </select>
          )}
          <label className="flex items-center gap-2 text-caption">
            <input
              type="checkbox"
              checked={archived}
              onChange={(e) => location.update({ archived: e.target.checked ? "true" : null, instance_offset: 0 })}
            />
            {t(($) => $.instances.include_archived)}
          </label>
        </div>
        <NewInstanceButton templateId={templateId} />
      </header>

      {list.isLoading && <p className="text-body text-muted-foreground">{t(($) => $.instances.loading)}</p>}
      {list.error && (
        <p role="alert" className="flex items-center gap-3 text-body text-destructive">
          {list.error.message}
          <Button size="sm" variant="outline" onClick={() => void list.refetch()}>{t(($) => $.page.retry)}</Button>
        </p>
      )}
      {list.data && shown === 0 && (
        <div className="flex flex-col items-center gap-2 rounded-md border border-dashed p-8 text-center">
          <Layers className="size-6 text-muted-foreground" />
          <p className="font-medium">{t(($) => $.instances.empty)}</p>
          {!filtering && <p className="max-w-md text-caption text-muted-foreground">{t(($) => $.instances.empty_hint)}</p>}
        </div>
      )}

      <div className="flex flex-col gap-4">
        {groups.map((group) => (
          <section
            key={group.templateId}
            aria-labelledby={templateId ? undefined : `workflow-group-${group.templateId}`}
            className="overflow-hidden rounded-md border"
          >
            {!templateId && (
              <h2 className="flex items-center gap-2 border-b bg-muted/40 px-4 py-2 font-medium">
                <AppLink className="hover:underline" href={paths.workflowDetail(group.templateId)}>
                  <span id={`workflow-group-${group.templateId}`}>{group.name || t(($) => $.instances.unnamed_workflow)}</span>
                </AppLink>
                <span className="text-caption font-normal text-muted-foreground tabular-nums">{group.instances.length}</span>
              </h2>
            )}
            <div className="divide-y">
              {group.instances.map((row) => (
                <InstanceRow
                  key={row.id}
                  row={row}
                  onArchived={() => { if (shown === 1 && offset > 0) setOffset(Math.max(0, offset - PAGE_SIZE)); }}
                />
              ))}
            </div>
          </section>
        ))}
      </div>

      {total > PAGE_SIZE && (
        <div className="mt-4 flex items-center gap-3">
          <span className="text-caption text-muted-foreground tabular-nums">
            {t(($) => $.instances.range, { from: offset + 1, to: offset + shown, total })}
          </span>
          <Button size="sm" variant="outline" disabled={offset === 0} onClick={() => setOffset(Math.max(0, offset - PAGE_SIZE))}>
            {t(($) => $.instances.previous)}
          </Button>
          <Button size="sm" variant="outline" disabled={offset + PAGE_SIZE >= total} onClick={() => setOffset(offset + PAGE_SIZE)}>
            {t(($) => $.instances.next)}
          </Button>
        </div>
      )}
    </div>
  );
}

function InstanceRow({ row, onArchived }: { row: WorkflowInputInstance; onArchived(): void }) {
  const { t } = useT("workflows");
  const ws = useWorkspaceId();
  const paths = useWorkspacePaths();
  const navigation = useNavigation();
  const location = useWorkflowLocation();
  const readiness = useQuery({
    ...workflowInstanceValidationOptions(ws, row.id, row.revision),
    // An archived or unbound instance can't run whatever the checks say.
    enabled: Boolean(ws && !row.archivedAt && row.templateVersionId),
  });
  const run = useRunWorkflowInstance(row.id);
  const save = useSaveWorkflowInputInstance(row.templateId);
  const archive = useArchiveWorkflowInstance(row.id);
  const [error, setError] = useState("");
  // One idempotency key per (action, revision): a retry after a lost response
  // reuses it, a new attempt on a newer revision gets a fresh one.
  const attempt = useRef({ action: "", key: "" });
  const lock = useRef(false);
  const action = async (kind: "run" | "copy" | "archive") => {
    if (lock.current) return;
    lock.current = true;
    setError("");
    const identity = kind + ":" + row.revision;
    if (attempt.current.action !== identity) attempt.current = { action: identity, key: crypto.randomUUID() };
    try {
      if (kind === "run") {
        const result = await run.mutateAsync({ mode: "saved", revision: row.revision, idempotency_key: attempt.current.key });
        navigation.push(paths.workflowRunDetail(result.id));
      }
      if (kind === "copy") {
        const result = await save.mutateAsync({
          name: copiedInstanceName(row.name, t(($) => $.instances.copy_suffix)),
          description: row.description,
          input: row.input,
          projectId: row.projectId,
          templateVersionId: row.templateVersionId,
          inputNode: row.inputNode,
          imageAttachmentId: row.imageAttachmentId,
          idempotencyKey: attempt.current.key,
        });
        navigation.push(paths.workflowInstanceDetail(result.id));
      }
      if (kind === "archive") {
        await archive.mutateAsync({ revision: row.revision, archive: !row.archivedAt });
        if (!row.archivedAt) onArchived();
      }
      attempt.current = { action: "", key: "" };
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      lock.current = false;
    }
  };
  const busy = run.isPending || save.isPending || archive.isPending;
  const status = instanceStatus(row, readiness.data?.ready);
  const preview = [row.input.title, row.input.description].filter(Boolean).join(" — ");
  return (
    <article className="flex items-center gap-3 px-4 py-3 hover:bg-accent/30">
      <div className="min-w-0 flex-1">
        <div className="flex min-w-0 flex-wrap items-center gap-2">
          <AppLink
            className="truncate font-medium hover:underline"
            href={paths.workflowInstanceDetail(row.id) + "?return_to=" + encodeURIComponent(location.current)}
          >
            {row.name}
          </AppLink>
          <InstanceStatusChip status={status} />
          {row.latestRunId ? (
            <AppLink href={paths.workflowRunDetail(row.latestRunId)} title={t(($) => $.instances.last_run)}>
              <WorkflowRunStatusBadge status={row.latestRunStatus} />
            </AppLink>
          ) : (
            <span className="text-caption text-muted-foreground">{t(($) => $.instances.not_run)}</span>
          )}
        </div>
        {preview && <p className="mt-0.5 max-w-2xl truncate text-caption text-muted-foreground">{preview}</p>}
        <p className="mt-0.5 text-caption text-muted-foreground">
          {t(($) => $.instances.updated, { time: new Date(row.updatedAt).toLocaleString() })}
          {row.versionNumber ? ` · v${row.versionNumber}` : ""}
        </p>
        {error && <p role="alert" className="mt-1 text-caption text-destructive">{error}</p>}
      </div>
      {!row.archivedAt && (
        <Button size="sm" disabled={busy || status !== "ready"} onClick={() => void action("run")}>
          <Play className="size-3.5" />{t(($) => $.instances.run_saved)}
        </Button>
      )}
      <InstanceMenu
        archived={Boolean(row.archivedAt)}
        disabled={busy}
        onDuplicate={() => void action("copy")}
        onArchive={() => void action("archive")}
      />
    </article>
  );
}
