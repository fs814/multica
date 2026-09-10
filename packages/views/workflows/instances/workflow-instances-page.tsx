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
import { AppLink, useNavigation } from "../../navigation";
import { useT } from "../../i18n";
import { WorkflowRunStatusBadge } from "../runs/components/run-status-badge";

import { copiedInstanceName } from "./instance-form";

export function WorkflowInstancesPage({ templateId }: { templateId?: string }) {
  const { t } = useT("workflows");
  const ws = useWorkspaceId();
  const paths = useWorkspacePaths();
  const [search, setSearch] = useState("");
  const [filter, setFilter] = useState(templateId ?? "");
  const [archived, setArchived] = useState(false);
  const [offset, setOffset] = useState(0);
  const templates = useQuery(workflowTemplateListOptions(ws));
  const list = useQuery(
    workflowInstancesOptions(ws, {
      template_id: filter || undefined,
      search,
      include_archived: archived,
      offset,
      limit: 30,
    }),
  );
  const groups = groupWorkflowInstances(list.data?.instances ?? [], templates.data);
  return (
    <div className="flex h-full flex-col overflow-y-auto p-4" data-tab-scroll-root="main">
      <header className="mb-4 flex items-center gap-4">
        <div className="flex-1">
          <h1 className="font-semibold">{t(($) => $.instances.all)}</h1>
          {!templateId && (
            <p className="mt-1 text-caption text-muted-foreground">
              {t(($) => $.instances.grouped_hint)}
            </p>
          )}
        </div>
        <AppLink href={paths.workflows()}>{t(($) => $.page.title)}</AppLink>
      </header>
      <div className="mb-4 flex flex-wrap items-center gap-3">
        <Input
          className="max-w-sm"
          aria-label={t(($) => $.instances.search)}
          placeholder={t(($) => $.instances.search)}
          value={search}
          onChange={(e) => {
            setSearch(e.target.value);
            setOffset(0);
          }}
        />
        {!templateId && (
          <select
            aria-label={t(($) => $.page.title)}
            className="rounded border bg-background p-2 text-caption"
            value={filter}
            onChange={(e) => {
              setFilter(e.target.value);
              setOffset(0);
            }}
          >
            <option value="">{t(($) => $.instances.all_workflows)}</option>
            {templates.data?.map((tpl) => (
              <option key={tpl.id} value={tpl.id}>
                {tpl.name}
              </option>
            ))}
          </select>
        )}
        <label className="flex gap-2 text-caption">
          <input
            type="checkbox"
            checked={archived}
            onChange={(e) => {
              setArchived(e.target.checked);
              setOffset(0);
            }}
          />
          {t(($) => $.instances.include_archived)}
        </label>
      </div>
      {list.isLoading && <p>{t(($) => $.instances.loading)}</p>}
      {list.error && (
        <p role="alert">
          {list.error.message}
          <Button onClick={() => void list.refetch()}>
            {t(($) => $.page.retry)}
          </Button>
        </p>
      )}
      {list.data?.instances.length === 0 && (
        <p className="p-6 text-muted-foreground">
          {t(($) => $.instances.empty)}
        </p>
      )}
      <div className="flex flex-col gap-4">
        {groups.map((group) => (
          <section
            key={group.templateId}
            aria-labelledby={templateId ? undefined : `workflow-group-${group.templateId}`}
            className="overflow-hidden rounded-md border"
          >
            {!templateId && (
              <h2
                id={`workflow-group-${group.templateId}`}
                className="border-b bg-muted/40 px-4 py-3 font-medium"
              >
                <AppLink
                  className="hover:underline"
                  href={paths.workflowDetail(group.templateId)}
                >
                  {group.name || t(($) => $.instances.unnamed_workflow)}
                </AppLink>
              </h2>
            )}
            <div className="divide-y">
              {group.instances.map((row) => (
                <InstanceRow key={row.id} row={row} templateName={group.name} />
              ))}
            </div>
          </section>
        ))}
      </div>
      <div className="mt-4 flex items-center gap-3">
        <span className="text-caption">{list.data?.total ?? 0}</span>
        <Button
          variant="outline"
          disabled={offset === 0}
          onClick={() => setOffset(Math.max(0, offset - 30))}
        >
          {t(($) => $.instances.previous)}
        </Button>
        <Button
          variant="outline"
          disabled={offset + 30 >= (list.data?.total ?? 0)}
          onClick={() => setOffset(offset + 30)}
        >
          {t(($) => $.instances.next)}
        </Button>
      </div>
    </div>
  );
}
function InstanceRow({
  row,
  templateName,
}: {
  row: WorkflowInputInstance;
  templateName: string;
}) {
  const { t } = useT("workflows");
  const ws = useWorkspaceId();
  const paths = useWorkspacePaths();
  const navigation = useNavigation();
  const readiness = useQuery(
    workflowInstanceValidationOptions(ws, row.id, row.revision),
  );
  const run = useRunWorkflowInstance(row.id);
  const save = useSaveWorkflowInputInstance(row.templateId);
  const archive = useArchiveWorkflowInstance(row.id);
  const [error, setError] = useState("");
  const attempt = useRef({ action: "", key: "" });
  const lock = useRef(false);
  const action = async (kind: "run" | "copy" | "archive") => {
    if (lock.current) return;
    lock.current = true;
    setError("");
    const identity = kind + ":" + row.revision;
    if (attempt.current.action !== identity)
      attempt.current = { action: identity, key: crypto.randomUUID() };
    try {
      if (kind === "run") {
        const result = await run.mutateAsync({
          mode: "saved",
          revision: row.revision,
          idempotency_key: attempt.current.key,
        });
        navigation.push(paths.workflowRunDetail(result.id));
      }
      if (kind === "copy") {
        const result = await save.mutateAsync({
          name: copiedInstanceName(
            row.name,
            t(($) => $.instances.copy_suffix),
          ),
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
      if (kind === "archive")
        await archive.mutateAsync({
          revision: row.revision,
          archive: !row.archivedAt,
        });
      attempt.current = { action: "", key: "" };
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      lock.current = false;
    }
  };
  const busy = run.isPending || save.isPending || archive.isPending;
  return (
    <article className="flex flex-wrap items-center gap-4 p-4">
      <div className="min-w-0 flex-1">
        <AppLink
          className="font-medium"
          href={paths.workflowInstanceDetail(row.id)}
        >
          {row.name}
        </AppLink>
        <p className="text-caption text-muted-foreground">
          {row.templateName || templateName} ·{" "}
          {row.versionNumber ? "v" + row.versionNumber : ""} ·{" "}
          {t(($) => $.instances.revision)} {row.revision} · {row.editorName} ·{" "}
          {new Date(row.updatedAt).toLocaleString()}
        </p>
        <p className="max-w-xl truncate text-caption">
          {row.input.title} {row.input.description}
        </p>
        <p className="text-caption">
          {row.archivedAt
            ? t(($) => $.instances.archived)
            : !row.templateVersionId
              ? t(($) => $.instances.unbound)
              : readiness.data?.ready
                ? t(($) => $.instances.ready)
                : t(($) => $.instances.needs_input)}
        </p>
        {row.latestRunId ? (
          <AppLink href={paths.workflowRunDetail(row.latestRunId)}>
            <WorkflowRunStatusBadge status={row.latestRunStatus} />
          </AppLink>
        ) : (
          <span className="text-caption">{t(($) => $.instances.not_run)}</span>
        )}
        {error && (
          <p role="alert" className="text-destructive">
            {error}
          </p>
        )}
      </div>
      <Button
        size="sm"
        disabled={busy || !readiness.data?.ready}
        onClick={() => void action("run")}
      >
        {t(($) => $.instances.run_saved)}
      </Button>
      <Button
        size="sm"
        variant="outline"
        disabled={busy}
        onClick={() => void action("copy")}
      >
        {t(($) => $.instances.duplicate)}
      </Button>
      <Button
        size="sm"
        variant="ghost"
        disabled={busy}
        onClick={() => void action("archive")}
      >
        {row.archivedAt
          ? t(($) => $.instances.restore)
          : t(($) => $.instances.archive)}
      </Button>
    </article>
  );
}
