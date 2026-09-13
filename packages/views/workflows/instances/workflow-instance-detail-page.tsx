"use client";
import { useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import {
  unknownInputKeys,
  workflowInstanceOptions,
  workflowInstanceVersionOptions,
  workflowInstanceValidationOptions,
  workflowInstanceRunsOptions,
  workflowTemplateDetailOptions,
  useSaveWorkflowInputInstance,
  useRunWorkflowInstance,
  useArchiveWorkflowInstance,
} from "@multica/core/workflows";
import type {
  WorkflowInputInstance,
  WorkflowNode,
  SaveWorkflowInputInstance,
  RunWorkflowInstance,
} from "@multica/core/workflows";
import { Button } from "@multica/ui/components/ui/button";
import { AppLink, useNavigation } from "../../navigation";
import { useT } from "../../i18n";
import {
  InstanceFields,
  copiedInstanceName,
  inputNode,
  useInstanceLeaveWarning,
} from "./instance-form";
import {
  useWorkflowLocation,
  workflowReturnPath,
} from "../use-workflow-location";
import { WorkflowRunStatusBadge } from "../runs/components/run-status-badge";

import { InputVersionDiff } from "../editor/version-comparison";

function editable(
  row: WorkflowInputInstance,
  node: WorkflowNode | null,
): SaveWorkflowInputInstance {
  return {
    name: row.name,
    description: row.description,
    input: { ...row.input },
    projectId: row.projectId,
    templateVersionId: row.templateVersionId,
    inputNode: row.inputNode ?? node,
    imageAttachmentId: row.imageAttachmentId ?? node?.image_attachment_id ?? "",
  };
}
export function WorkflowInstanceDetailPage({
  instanceId,
}: {
  instanceId: string;
}) {
  const ws = useWorkspaceId();
  const { t } = useT("workflows");
  const query = useQuery(workflowInstanceOptions(ws, instanceId));
  const version = useQuery(
    workflowInstanceVersionOptions(
      ws,
      query.data?.templateId ?? "",
      query.data?.templateVersionId ?? "",
    ),
  );
  if (query.error)
    return (
      <p role="alert" className="p-6">
        {query.error.message}
        <Button onClick={() => void query.refetch()}>
          {t(($) => $.page.retry)}
        </Button>
      </p>
    );
  if (!query.data || (query.data.templateVersionId && version.isLoading))
    return <p className="p-6">{t(($) => $.instances.loading)}</p>;
  return (
    <InstanceEditor
      key={ws + ":" + instanceId}
      row={query.data}
      node={inputNode(version.data?.definition)}
    />
  );
}
function InstanceEditor({
  row,
  node,
}: {
  row: WorkflowInputInstance;
  node: WorkflowNode | null;
}) {
  const ws = useWorkspaceId();
  const paths = useWorkspacePaths();
  const navigation = useNavigation();
  const location = useWorkflowLocation();
  const { t } = useT("workflows");
  const [uploading, setUploading] = useState(false);
  const [base, setBase] = useState(row);
  const [original, setOriginal] = useState(() => editable(row, node));
  const [value, setValue] = useState(() => editable(row, node));
  const [notice, setNotice] = useState("");
  const [upgrade, setUpgrade] = useState("");
  const [fieldDecisions, setFieldDecisions] = useState<
    Record<string, "keep" | "remove">
  >({});
  const [offset, setOffset] = useState(0);
  const tpl = useQuery(workflowTemplateDetailOptions(ws, row.templateId));
  const proposed = useQuery(
    workflowInstanceVersionOptions(ws, row.templateId, upgrade),
  );
  const upgradeUnknown = proposed.data
    ? unknownInputKeys(value.input, inputNode(proposed.data.definition))
    : [];
  const readiness = useQuery(
    workflowInstanceValidationOptions(ws, row.id, base.revision),
  );
  const history = useQuery(workflowInstanceRunsOptions(ws, row.id, offset));
  const save = useSaveWorkflowInputInstance(row.templateId);
  const run = useRunWorkflowInstance(row.id);
  const archive = useArchiveWorkflowInstance(row.id);
  const attempt = useRef({ body: "", key: "" });
  const busy = useRef(false);
  const dirty = JSON.stringify(value) !== JSON.stringify(original);
  const navigateAfterAction = useInstanceLeaveWarning(dirty);
  const pending =
    uploading || save.isPending || run.isPending || archive.isPending;
  const archived = Boolean(row.archivedAt);
  const unavailable = tpl.data?.status === "archived";
  const accept = (next: WorkflowInputInstance) => {
    setBase(next);
    const nextValue = editable(next, node);
    setOriginal(nextValue);
    setValue(nextValue);
  };
  const start = async (
    mode: RunWorkflowInstance["mode"],
    historyRunId?: string,
  ) => {
    if (busy.current) return;
    busy.current = true;
    setNotice("");
    const intent = {
      mode,
      revision: base.revision,
      ...(mode === "temporary"
        ? {
            input: value.input,
            project_id: value.projectId,
            image_attachment_id: value.imageAttachmentId ?? "",
          }
        : {}),
      ...(historyRunId ? { history_run_id: historyRunId } : {}),
    };
    const body = JSON.stringify(intent);
    if (attempt.current.body !== body)
      attempt.current = { body, key: crypto.randomUUID() };
    try {
      const created = await run.mutateAsync({
        ...intent,
        idempotency_key: attempt.current.key,
      });
      attempt.current = { body: "", key: "" };
      navigateAfterAction(() =>
        navigation.push(paths.workflowRunDetail(created.id)),
      );
    } catch (error) {
      setNotice(error instanceof Error ? error.message : String(error));
    } finally {
      busy.current = false;
    }
  };
  const persist = async (copy = false) => {
    if (busy.current) return;
    busy.current = true;
    setNotice("");
    const body = JSON.stringify({ copy, value });
    if (attempt.current.body !== body)
      attempt.current = { body, key: crypto.randomUUID() };
    try {
      const updated = await save.mutateAsync({
        ...value,
        ...(copy
          ? {
              name: copiedInstanceName(
                value.name,
                t(($) => $.instances.copy_suffix),
              ),
              idempotencyKey: attempt.current.key,
            }
          : { id: row.id, revision: base.revision }),
      });
      if (copy)
        navigateAfterAction(() =>
          navigation.push(paths.workflowInstanceDetail(updated.id)),
        );
      else {
        accept(updated);
        setNotice(t(($) => $.input_instances.saved));
      }
    } catch (error) {
      setNotice(error instanceof Error ? error.message : String(error));
    } finally {
      busy.current = false;
    }
  };
  const latest = tpl.data?.versions.find(
    (v) => v.status === "published" && v.version === tpl.data?.current_version,
  );
  const versionLabel = tpl.data?.versions.find(
    (v) => v.id === value.templateVersionId,
  )?.version;
  return (
    <div className="flex h-full flex-col overflow-y-auto">
      <header className="flex flex-wrap items-center gap-3 border-b p-4">
        <AppLink
          href={workflowReturnPath(
            location.params.get("return_to"),
            paths.workflowInstances(),
            [paths.workflowInstances(), paths.workflowDetail(row.templateId)],
          )}
        >
          {t(($) => $.instances.all)}
        </AppLink>
        <AppLink
          href={paths.workflowDetail(row.templateId) + "?section=instances"}
        >
          {tpl.data?.name ?? t(($) => $.page.title)}
        </AppLink>
        <h1 className="min-w-0 flex-1 truncate font-semibold">{value.name}</h1>
        <Button
          variant="outline"
          disabled={pending}
          onClick={() => void persist(true)}
        >
          {t(($) => $.instances.duplicate)}
        </Button>
        <Button
          variant="outline"
          disabled={pending || dirty}
          onClick={async () => {
            try {
              const next = await archive.mutateAsync({
                revision: base.revision,
                archive: !archived,
              });
              accept(next);
            } catch (error) {
              setNotice(error instanceof Error ? error.message : String(error));
            }
          }}
        >
          {archived
            ? t(($) => $.instances.restore)
            : t(($) => $.instances.archive)}
        </Button>
      </header>
      <div className="grid gap-6 p-4 lg:grid-cols-[minmax(0,2fr)_minmax(280px,1fr)]">
        <section className="flex min-w-0 flex-col gap-4">
          <p className="text-caption">
            {value.templateVersionId
              ? t(($) => $.instances.pinned, {
                  version: versionLabel ?? value.templateVersionId,
                })
              : t(($) => $.instances.unbound)}
          </p>
          {latest && latest.id !== value.templateVersionId && (
            <Button
              variant="outline"
              disabled={pending || archived}
              onClick={() => {
                setFieldDecisions({});
                setUpgrade(latest.id);
              }}
            >
              {t(($) => $.instances.upgrade)}
            </Button>
          )}
          {upgrade && (
            <div className="flex flex-col gap-3 rounded-md border p-3">
              <p>{t(($) => $.instances.upgrade_hint)}</p>
              {proposed.error && <p role="alert">{proposed.error.message}</p>}
              {proposed.data && (
                <>
                  <InputVersionDiff
                    before={value.inputNode}
                    after={inputNode(proposed.data.definition)}
                  />
                  {upgradeUnknown.map((key) => (
                    <fieldset
                      key={key}
                      className="rounded border p-2 text-caption"
                    >
                      <legend>
                        {t(($) => $.instances.unknown, { field: key })}
                      </legend>
                      <pre className="max-h-32 overflow-auto whitespace-pre-wrap break-words">
                        {value.input[key]}
                      </pre>
                      <div className="flex gap-3">
                        <label>
                          <input
                            type="radio"
                            name={`upgrade-${key}`}
                            checked={fieldDecisions[key] === "keep"}
                            onChange={() =>
                              setFieldDecisions({
                                ...fieldDecisions,
                                [key]: "keep",
                              })
                            }
                          />{" "}
                          {t(($) => $.authoring.keep_value)}
                        </label>
                        <label>
                          <input
                            type="radio"
                            name={`upgrade-${key}`}
                            checked={fieldDecisions[key] === "remove"}
                            onChange={() =>
                              setFieldDecisions({
                                ...fieldDecisions,
                                [key]: "remove",
                              })
                            }
                          />{" "}
                          {t(($) => $.instances.remove)}
                        </label>
                      </div>
                    </fieldset>
                  ))}
                  <p className="text-caption">
                    {t(($) => $.instances.keep_resources)}
                  </p>
                  <Button
                    disabled={
                      pending ||
                      upgradeUnknown.some((key) => !fieldDecisions[key])
                    }
                    onClick={() => {
                      const input = { ...value.input };
                      for (const key of upgradeUnknown)
                        if (fieldDecisions[key] === "remove") delete input[key];
                      setValue({
                        ...value,
                        input,
                        templateVersionId: upgrade,
                        inputNode: inputNode(proposed.data!.definition),
                      });
                      setUpgrade("");
                    }}
                  >
                    {t(($) => $.instances.apply_upgrade)}
                  </Button>
                </>
              )}
              <Button variant="outline" onClick={() => setUpgrade("")}>
                {t(($) => $.instances.cancel)}
              </Button>
            </div>
          )}
          {row.revision !== base.revision && (
            <p role="alert">{t(($) => $.instances.conflict)}</p>
          )}
          <InstanceFields
            value={value}
            onChange={setValue}
            disabled={pending || archived}
            onUploadingChange={setUploading}
          />
          {notice && (
            <p role="status" className="rounded border p-3">
              {notice}
            </p>
          )}
          {(save.isError || row.revision !== base.revision) && (
            <Button
              variant="outline"
              onClick={() => {
                if (!dirty || window.confirm(t(($) => $.instances.leave)))
                  accept(row);
              }}
            >
              {t(($) => $.instances.reload)}
            </Button>
          )}
          <div className="flex flex-wrap gap-2">
            <Button
              disabled={
                pending ||
                archived ||
                unavailable ||
                !dirty ||
                !value.name.trim()
              }
              onClick={() => void persist()}
            >
              {t(($) => $.input_instances.update)}
            </Button>
            <Button
              variant="outline"
              disabled={
                pending ||
                archived ||
                unavailable ||
                !base.templateVersionId ||
                !readiness.data?.ready
              }
              onClick={() => void start("saved")}
            >
              {t(($) => $.instances.run_saved)}
            </Button>
            <Button
              variant="outline"
              disabled={
                pending ||
                archived ||
                unavailable ||
                !dirty ||
                !base.templateVersionId ||
                value.templateVersionId !== base.templateVersionId
              }
              onClick={() => void start("temporary")}
            >
              {t(($) => $.instances.run_temporary)}
            </Button>
          </div>
          <p className="text-caption text-muted-foreground">
            {t(($) => $.instances.temporary_hint)}
          </p>
          {readiness.data && !readiness.data.ready && (
            <ul role="status" className="text-caption">
              {readiness.data.problems.map((p) => (
                <li key={p}>{p}</li>
              ))}
            </ul>
          )}
          {readiness.error && <p role="alert">{readiness.error.message}</p>}
        </section>
        <section className="min-w-0">
          <h2 className="mb-3 font-semibold">
            {t(($) => $.instances.history)}
          </h2>
          {history.error && <p role="alert">{history.error.message}</p>}
          {history.data?.runs.length === 0 && (
            <p>{t(($) => $.instances.not_run)}</p>
          )}
          <div className="flex flex-col gap-3">
            {history.data?.runs.map((item) => (
              <div key={item.id} className="rounded-md border p-3">
                <AppLink href={paths.workflowRunDetail(item.id)}>
                  <WorkflowRunStatusBadge status={item.status} />
                  <p className="mt-2 text-caption">
                    {new Date(item.created_at).toLocaleString()}
                  </p>
                </AppLink>
                <p className="text-caption">
                  {item.input_instance_name} · {t(($) => $.instances.revision)}{" "}
                  {item.input_instance_revision} ·{" "}
                  {item.input_source === "temporary"
                    ? t(($) => $.instances.source_temporary)
                    : item.input_source === "history"
                      ? t(($) => $.instances.source_history)
                      : t(($) => $.instances.source_saved)}
                </p>
                <Button
                  size="sm"
                  variant="ghost"
                  disabled={pending || archived || unavailable}
                  onClick={() => void start("history", item.id)}
                >
                  {t(($) => $.instances.rerun)}
                </Button>
              </div>
            ))}
          </div>
          <div className="mt-3 flex gap-2">
            <Button
              size="sm"
              variant="outline"
              disabled={offset === 0}
              onClick={() => setOffset(Math.max(0, offset - 30))}
            >
              {t(($) => $.instances.previous)}
            </Button>
            <Button
              size="sm"
              variant="outline"
              disabled={offset + 30 >= (history.data?.total ?? 0)}
              onClick={() => setOffset(offset + 30)}
            >
              {t(($) => $.instances.next)}
            </Button>
          </div>
        </section>
      </div>
    </div>
  );
}
