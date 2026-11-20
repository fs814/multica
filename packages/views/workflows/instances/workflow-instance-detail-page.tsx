"use client";
import { ScriptPipelineStepActions } from "../components/script-pipeline-step-actions";
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
import { ChevronRight, Play } from "@multica/ui/icons";
import { AppLink, useNavigation } from "../../navigation";
import { useT } from "../../i18n";
import {
  InstanceFields,
  copiedInstanceName,
  inputNode,
  useInstanceLeaveWarning,
} from "./instance-form";
import { InstanceMenu, InstanceStatusChip, instanceStatus } from "./instance-actions";
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

/**
 * One saved set of inputs for a workflow.
 *
 * Layout, top to bottom: where you are and the one primary action (Run the
 * saved inputs) with the rest in "…"; then anything that stops a run, as a
 * banner with its fix; then the inputs. Editing reveals a bar with what you
 * can do with the edits: discard, run them once without saving, save, or
 * save and run. Past runs sit beside the form, each one re-runnable.
 */
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
  // `base` is the server's answer to our last save/archive; `row` catches up on refetch.
  const archived = Boolean(base.archivedAt);
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
    scriptStep?: RunWorkflowInstance["script_step"],
    revision = base.revision,
  ) => {
    if (busy.current) return;
    busy.current = true;
    setNotice("");
    const intent = {
      mode,
      revision,
      ...(scriptStep ? { script_step: scriptStep } : {}),
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
  /** Saves (or, with copy, duplicates) the edits; resolves the saved row, or null when it failed. */
  const persist = async (copy = false): Promise<WorkflowInputInstance | null> => {
    if (busy.current) return null;
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
      return updated;
    } catch (error) {
      setNotice(error instanceof Error ? error.message : String(error));
      return null;
    } finally {
      busy.current = false;
    }
  };
  /** Save, then run exactly the revision just saved. */
  const saveAndRun = async () => {
    const saved = await persist();
    if (saved) await start("saved", undefined, undefined, saved.revision);
  };
  const toggleArchive = async () => {
    try {
      accept(await archive.mutateAsync({ revision: base.revision, archive: !archived }));
    } catch (error) {
      setNotice(error instanceof Error ? error.message : String(error));
    }
  };
  const latest = tpl.data?.versions.find(
    (v) => v.status === "published" && v.version === tpl.data?.current_version,
  );
  const versionLabel = tpl.data?.versions.find(
    (v) => v.id === value.templateVersionId,
  )?.version;
  const backHref = workflowReturnPath(
    location.params.get("return_to"),
    paths.workflowInstances(),
    [paths.workflowInstances(), paths.workflowDetail(row.templateId)],
  );
  const status = instanceStatus(base, readiness.data?.ready);
  // What each run button needs. Saved runs use the server's copy, so edits
  // don't block them; a one-off run sends the edits, so it needs some, on the
  // same version the instance is saved with.
  const canRunSaved = !pending && !archived && !unavailable && Boolean(base.templateVersionId) && Boolean(readiness.data?.ready);
  const canRunOnce = !pending && !archived && !unavailable && dirty && Boolean(base.templateVersionId) && value.templateVersionId === base.templateVersionId;
  const canSave = !pending && !archived && !unavailable && dirty && Boolean(value.name.trim());
  const conflict = row.revision !== base.revision;
  return (
    <div className="flex h-full flex-col">
      <header className="flex flex-wrap items-center gap-3 border-b px-4 py-3">
        <nav aria-label={t(($) => $.instances.all)} className="flex min-w-0 flex-1 items-center gap-1.5 text-body">
          <AppLink className="shrink-0 text-muted-foreground hover:text-foreground" href={backHref}>
            {t(($) => $.instances.all)}
          </AppLink>
          <ChevronRight aria-hidden className="size-3.5 shrink-0 text-muted-foreground" />
          <AppLink
            className="min-w-0 max-w-[30%] shrink truncate text-muted-foreground hover:text-foreground"
            href={paths.workflowDetail(row.templateId) + "?section=instances"}
          >
            {tpl.data?.name ?? t(($) => $.page.title)}
          </AppLink>
          <ChevronRight aria-hidden className="size-3.5 shrink-0 text-muted-foreground" />
          <h1 className="min-w-0 truncate font-semibold">{value.name}</h1>
          <InstanceStatusChip status={status} />
        </nav>
        <Button disabled={!canRunSaved} onClick={() => void start("saved")}>
          <Play className="size-3.5" />{t(($) => $.instances.run_saved)}
        </Button>
        <InstanceMenu
          archived={archived}
          disabled={pending}
          // Archiving would drop unsaved edits; duplicating keeps them (it copies the form).
          archiveDisabled={dirty}
          onDuplicate={() => void persist(true)}
          onArchive={() => void toggleArchive()}
        />
      </header>

      <div className="min-h-0 flex-1 overflow-y-auto">
        <div className="grid gap-6 p-4 lg:grid-cols-[minmax(0,2fr)_minmax(260px,1fr)]">
          <section className="flex min-w-0 flex-col gap-4">
            {archived && (
              <Banner tone="muted">
                <p className="flex-1">{t(($) => $.instances.archived_banner)}</p>
                <Button size="sm" variant="outline" disabled={pending} onClick={() => void toggleArchive()}>
                  {t(($) => $.instances.restore)}
                </Button>
              </Banner>
            )}
            {!archived && !base.templateVersionId && (
              <Banner tone="warning">
                <div className="flex-1">
                  <p>{t(($) => $.instances.binding_required)}</p>
                  <AppLink className="underline" href={paths.workflowDetail(row.templateId) + "?section=canvas"}>
                    {t(($) => $.instances.configure_workflow)}
                  </AppLink>
                </div>
              </Banner>
            )}
            {!archived && base.templateVersionId && readiness.data && !readiness.data.ready && (
              <Banner tone="warning">
                <div className="flex-1">
                  <p className="font-medium">{t(($) => $.instances.blocked_title)}</p>
                  <ul className="ml-4 list-disc">
                    {readiness.data.problems.map((p) => <li key={p}>{p}</li>)}
                  </ul>
                </div>
              </Banner>
            )}
            {readiness.error && <p role="alert" className="text-caption text-destructive">{readiness.error.message}</p>}
            {!archived && latest && latest.id !== value.templateVersionId && !upgrade && (
              <Banner tone="info">
                <p className="flex-1">{t(($) => $.instances.newer_version, { version: latest.version })}</p>
                <Button
                  size="sm"
                  variant="outline"
                  disabled={pending}
                  onClick={() => {
                    setFieldDecisions({});
                    setUpgrade(latest.id);
                  }}
                >
                  {base.templateVersionId ? t(($) => $.instances.upgrade) : t(($) => $.instances.bind_version)}
                </Button>
              </Banner>
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
                      <fieldset key={key} className="rounded border p-2 text-caption">
                        <legend>{t(($) => $.instances.unknown, { field: key })}</legend>
                        <pre className="max-h-32 overflow-auto whitespace-pre-wrap break-words">{value.input[key]}</pre>
                        <div className="flex gap-3">
                          <label>
                            <input
                              type="radio"
                              name={`upgrade-${key}`}
                              checked={fieldDecisions[key] === "keep"}
                              onChange={() => setFieldDecisions({ ...fieldDecisions, [key]: "keep" })}
                            />{" "}
                            {t(($) => $.authoring.keep_value)}
                          </label>
                          <label>
                            <input
                              type="radio"
                              name={`upgrade-${key}`}
                              checked={fieldDecisions[key] === "remove"}
                              onChange={() => setFieldDecisions({ ...fieldDecisions, [key]: "remove" })}
                            />{" "}
                            {t(($) => $.instances.remove)}
                          </label>
                        </div>
                      </fieldset>
                    ))}
                    <p className="text-caption">{t(($) => $.instances.keep_resources)}</p>
                  </>
                )}
                <div className="flex gap-2">
                  {proposed.data && (
                    <Button
                      disabled={
                        pending ||
                        upgradeUnknown.some(
                          (key) =>
                            !Object.hasOwn(fieldDecisions, key) ||
                            !["keep", "remove"].includes(fieldDecisions[key]!),
                        )
                      }
                      onClick={() => {
                        if (
                          upgradeUnknown.some(
                            (key) =>
                              !Object.hasOwn(fieldDecisions, key) ||
                              !["keep", "remove"].includes(fieldDecisions[key]!),
                          )
                        )
                          return;
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
                  )}
                  <Button variant="outline" onClick={() => setUpgrade("")}>
                    {t(($) => $.instances.cancel)}
                  </Button>
                </div>
              </div>
            )}
            {conflict && (
              <Banner tone="warning" role="alert">
                <p className="flex-1">{t(($) => $.instances.conflict)}</p>
              </Banner>
            )}

            <InstanceFields
              value={value}
              onChange={setValue}
              disabled={pending || archived}
              onUploadingChange={setUploading}
            />
            <p className="text-caption text-muted-foreground">
              {value.templateVersionId
                ? t(($) => $.instances.pinned, { version: versionLabel ?? value.templateVersionId })
                : t(($) => $.instances.unbound)}
            </p>
            {notice && (
              <p role="status" className="rounded-md border p-3 text-body">{notice}</p>
            )}
            {(save.isError || conflict) && (
              <div>
                <Button
                  variant="outline"
                  onClick={() => {
                    if (!dirty || window.confirm(t(($) => $.instances.leave))) accept(row);
                  }}
                >
                  {t(($) => $.instances.reload)}
                </Button>
              </div>
            )}
            {value.inputNode?.input_mode === "scripts" && (
              <div className="flex flex-col gap-1">
                <ScriptPipelineStepActions
                  disabled={pending || archived || unavailable || !base.templateVersionId || value.templateVersionId !== base.templateVersionId}
                  onRun={(step) => void start(dirty ? "temporary" : "saved", undefined, step)}
                />
                <p className="text-caption text-muted-foreground">{t(($) => $.scripts.single_step_hint)}</p>
              </div>
            )}
          </section>

          <aside className="min-w-0">
            <h2 className="mb-2 text-label font-semibold">{t(($) => $.instances.history)}</h2>
            {history.error && <p role="alert">{history.error.message}</p>}
            {history.data?.runs.length === 0 && (
              <p className="text-caption text-muted-foreground">{t(($) => $.instances.not_run)}</p>
            )}
            <ul className="divide-y rounded-md border">
              {history.data?.runs.map((item) => (
                <li key={item.id} className="flex items-center gap-2 px-3 py-2">
                  <AppLink className="min-w-0 flex-1" href={paths.workflowRunDetail(item.id)}>
                    <span className="flex items-center gap-2">
                      <WorkflowRunStatusBadge status={item.status} />
                      <span className="truncate text-caption text-muted-foreground">{new Date(item.created_at).toLocaleString()}</span>
                    </span>
                    <span className="mt-0.5 block truncate text-caption text-muted-foreground">
                      {item.input_source === "temporary"
                        ? t(($) => $.instances.source_temporary)
                        : item.input_source === "history"
                          ? t(($) => $.instances.source_history)
                          : t(($) => $.instances.source_saved)}
                      {" · "}{t(($) => $.instances.revision)} {item.input_instance_revision}
                    </span>
                  </AppLink>
                  <Button
                    size="sm"
                    variant="ghost"
                    disabled={pending || archived || unavailable}
                    onClick={() => void start("history", item.id)}
                  >
                    {t(($) => $.instances.rerun)}
                  </Button>
                </li>
              ))}
            </ul>
            {(history.data?.total ?? 0) > 30 && (
              <div className="mt-3 flex gap-2">
                <Button size="sm" variant="outline" disabled={offset === 0} onClick={() => setOffset(Math.max(0, offset - 30))}>
                  {t(($) => $.instances.previous)}
                </Button>
                <Button size="sm" variant="outline" disabled={offset + 30 >= (history.data?.total ?? 0)} onClick={() => setOffset(offset + 30)}>
                  {t(($) => $.instances.next)}
                </Button>
              </div>
            )}
          </aside>
        </div>
      </div>

      {dirty && (
        <footer className="flex flex-wrap items-center gap-2 border-t bg-background px-4 py-2.5">
          <p className="mr-auto text-body text-muted-foreground">{t(($) => $.instances.unsaved)}</p>
          <Button variant="ghost" disabled={pending} onClick={() => setValue(original)}>
            {t(($) => $.instances.discard)}
          </Button>
          <Button variant="outline" disabled={!canRunOnce} onClick={() => void start("temporary")}>
            {t(($) => $.instances.run_temporary)}
          </Button>
          <Button variant="outline" disabled={!canSave} onClick={() => void persist()}>
            {t(($) => $.input_instances.update)}
          </Button>
          <Button disabled={!canSave || !value.templateVersionId} onClick={() => void saveAndRun()}>
            <Play className="size-3.5" />{t(($) => $.instances.save_and_run)}
          </Button>
        </footer>
      )}
    </div>
  );
}

const BANNER_TONES = {
  warning: "border-amber-500/40 bg-amber-500/10",
  info: "border-sky-500/40 bg-sky-500/10",
  muted: "bg-muted/50",
} as const;

function Banner({ tone, role = "status", children }: { tone: keyof typeof BANNER_TONES; role?: "status" | "alert"; children: React.ReactNode }) {
  return (
    <div role={role} className={`flex flex-wrap items-center gap-3 rounded-md border p-3 text-body ${BANNER_TONES[tone]}`}>
      {children}
    </div>
  );
}
