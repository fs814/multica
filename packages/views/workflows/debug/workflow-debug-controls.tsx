"use client";
import { useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { ApiError } from "@multica/core/api/client";
import { api } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import {
  WorkflowDebugFailureSchema,
  workflowDebugCapabilitiesOptions,
  workflowDebugListOptions,
  workflowDebugKeys,
  useStartWorkflowDebugRun,
  useUpdateWorkflowDebugSettings,
  workflowRunInputDefaults,
} from "@multica/core/workflows";
import type {
  WorkflowDefinition,
  WorkflowTemplateDetail,
  SaveWorkflowInputInstance,
  StartWorkflowDebugRequest,
  WorkflowDebugPolicy,
  WorkflowDebugLimits,
} from "@multica/core/workflows";
import { Button } from "@multica/ui/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogTitle,
  DialogDescription,
} from "@multica/ui/components/ui/dialog";
import { AppLink } from "../../navigation";
import { useT } from "../../i18n";
import { InstanceFields, inputNode } from "../instances/instance-form";
import { WorkflowRunStatusBadge } from "../runs/components/run-status-badge";
import { WorkflowDebugLimitsSummary } from "./workflow-debug-limits";
import { WorkflowDebugDetailPage } from "./workflow-debug-detail";

type TrialSnapshot = {
  policyRevision: number;
  effectiveLimits: WorkflowDebugLimits;
  retentionSeconds: number;
  definition: WorkflowDefinition;
  revision: number;
  baseDraftId: string | null;
};
export function WorkflowDebugControls({
  definition,
  baseline,
  jsonDirty,
  canStart,
  isAdmin,
}: {
  definition: WorkflowDefinition;
  baseline: WorkflowTemplateDetail;
  jsonDirty: boolean;
  canStart: boolean;
  isAdmin: boolean;
}) {
  const { t } = useT("workflows");
  const wsId = useWorkspaceId();
  const capabilities = useQuery(workflowDebugCapabilitiesOptions(wsId));
  const [snapshot, setSnapshot] = useState<TrialSnapshot | null>(null);
  const [result, setResult] = useState<string | null>(null);
  const [settings, setSettings] = useState(false);
  return (
    <>
      {capabilities.data?.enabled && (
        <Button
          variant="outline"
          disabled={!canStart || jsonDirty}
          title={jsonDirty ? t(($) => $.debug.apply_json) : undefined}
          onClick={() =>
            setSnapshot({
              policyRevision: capabilities.data!.policyRevision,
              retentionSeconds: capabilities.data!.settings.retentionSeconds,
              effectiveLimits: Object.fromEntries(
                Object.entries(capabilities.data!.defaultLimits).map(
                  ([key, fallback]) => [
                    key,
                    definition.limits[key as keyof WorkflowDebugLimits] ||
                      (key === "max_duration_seconds"
                        ? Math.min(
                            capabilities.data!.settings.maxDurationSeconds,
                            capabilities.data!.durationCeilingSeconds,
                          )
                        : fallback),
                  ],
                ),
              ) as WorkflowDebugLimits,
              definition: structuredClone(definition),
              revision: baseline.revision,
              baseDraftId:
                baseline.versions.find((v) => v.status === "draft")?.id ?? null,
            })
          }
        >
          {t(($) => $.debug.start)}
        </Button>
      )}
      {jsonDirty && capabilities.data?.enabled && (
        <span className="text-caption">{t(($) => $.debug.apply_json)}</span>
      )}
      {isAdmin && capabilities.data && (
        <Button variant="ghost" onClick={() => setSettings(true)}>
          {t(($) => $.debug.settings)}
        </Button>
      )}
      {snapshot && capabilities.data && (
        <TrialDialog
          snapshot={snapshot}
          template={baseline}
          policyRevision={snapshot.policyRevision}
          onClose={() => setSnapshot(null)}
          onStarted={(id) => {
            setSnapshot(null);
            setResult(id);
          }}
        />
      )}
      <Dialog
        open={!!result}
        onOpenChange={(open) => {
          if (!open) setResult(null);
        }}
      >
        <DialogContent className="flex h-[85vh] w-[calc(100vw-2rem)] flex-col overflow-hidden sm:max-w-6xl">
          <DialogTitle>{t(($) => $.debug.result)}</DialogTitle>
          <DialogDescription>{t(($) => $.debug.editor_kept)}</DialogDescription>
          {result && <WorkflowDebugDetailPage runId={result} />}
        </DialogContent>
      </Dialog>
      {settings && <DebugSettings onClose={() => setSettings(false)} />}
    </>
  );
}
function TrialDialog({
  snapshot,
  template,
  policyRevision,
  onClose,
  onStarted,
}: {
  snapshot: TrialSnapshot;
  template: WorkflowTemplateDetail;
  policyRevision: number;
  onClose(): void;
  onStarted(id: string): void;
}) {
  const { t } = useT("workflows");
  const wsId = useWorkspaceId();
  const node = inputNode(snapshot.definition);
  const [value, setValue] = useState<SaveWorkflowInputInstance>(() => ({
    name: "",
    templateVersionId: "",
    input: workflowRunInputDefaults(snapshot.definition, template.name),
    inputNode: node,
    projectId: null,
    imageAttachmentId: node?.image_attachment_id ?? null,
  }));
  const [ack, setAck] = useState(false);
  const [uploading, setUploading] = useState(false);
  const [error, setError] = useState("");
  const [failure, setFailure] = useState<unknown>(null);
  const start = useStartWorkflowDebugRun(wsId);
  const attempt = useRef<{ serialized: string; key: string } | null>(null);
  const submitting = useRef(false);
  const body = {
    schema_version: "1" as const,
    expected_revision: snapshot.revision,
    expected_debug_policy_revision: policyRevision,
    base_draft_version_id: snapshot.baseDraftId,
    definition: snapshot.definition,
    input: value.input,
    project_id: value.projectId ?? null,
    image_attachment_id: value.imageAttachmentId || null,
    execution_acknowledged: true as const,
  };
  async function submit() {
    if (submitting.current) return;
    submitting.current = true;
    setError("");
    setFailure(null);
    const serialized = JSON.stringify(body);
    if (attempt.current?.serialized !== serialized)
      attempt.current = { serialized, key: crypto.randomUUID() };
    try {
      const response = await start.mutateAsync({
        id: template.id,
        body: {
          ...body,
          idempotency_key: attempt.current.key,
        } satisfies StartWorkflowDebugRequest,
      });
      onStarted(response.run.id);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
      setFailure(e instanceof ApiError ? e.body : null);
    } finally {
      submitting.current = false;
    }
  }
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open && !start.isPending) onClose();
      }}
    >
      <DialogContent className="max-h-[85vh] overflow-y-auto sm:max-w-xl">
        <DialogTitle>{t(($) => $.debug.start)}</DialogTitle>
        <DialogDescription>
          {t(($) => $.debug.real_execution)}
        </DialogDescription>
        <p className="rounded-md border border-amber-500/40 bg-amber-500/10 p-3 text-body">
          {t(($) => $.debug.warning)}
        </p>
        <WorkflowDebugLimitsSummary
          limits={snapshot.effectiveLimits}
          retentionSeconds={snapshot.retentionSeconds}
        />
        <InstanceFields
          hideMetadata
          value={value}
          onChange={setValue}
          disabled={start.isPending}
          onUploadingChange={setUploading}
        />
        <label className="flex items-start gap-2 text-body">
          <input
            type="checkbox"
            checked={ack}
            onChange={(e) => setAck(e.target.checked)}
            disabled={start.isPending}
          />
          {t(($) => $.debug.acknowledge)}
        </label>
        <DebugFailureDetails failure={failure} />
        {error && (
          <p role="alert" className="text-destructive">
            {error}
          </p>
        )}
        <Button
          disabled={
            !ack ||
            uploading ||
            start.isPending ||
            !String(value.input.title ?? "").trim() ||
            !String(value.input.description ?? "").trim()
          }
          onClick={() => void submit()}
        >
          {start.isPending
            ? t(($) => $.debug.starting)
            : t(($) => $.debug.confirm)}
        </Button>
      </DialogContent>
    </Dialog>
  );
}
export function WorkflowDebugHistory({ templateId }: { templateId: string }) {
  const { t } = useT("workflows");
  const wsId = useWorkspaceId();
  const paths = useWorkspacePaths();
  const [offset, setOffset] = useState(0);
  const history = useQuery(workflowDebugListOptions(wsId, templateId, offset));
  const capabilities = useQuery(workflowDebugCapabilitiesOptions(wsId));
  return (
    <section className="space-y-4 overflow-auto p-4">
      <h2>{t(($) => $.debug.history)}</h2>
      <p className="text-caption">{t(($) => $.debug.real_execution)}</p>
      {capabilities.data && (
        <p className="text-caption">
          {t(($) => $.debug.usage, {
            user: capabilities.data.usage.userActive,
            userLimit: capabilities.data.settings.userActiveRuns,
            workspace: capabilities.data.usage.workspaceActive,
            workspaceLimit: capabilities.data.settings.workspaceActiveRuns,
            hourly: capabilities.data.usage.userHourly,
            hourlyLimit: capabilities.data.settings.userStartsPerHour,
            bytes: capabilities.data.payloadBytes,
            capacity: capabilities.data.settings.payloadCapacityBytes,
            waiting: capabilities.data.usage.waitingStop,
          })}
        </p>
      )}
      {history.isError && <p role="alert">{history.error.message}</p>}
      {history.data?.items.length === 0 && <p>{t(($) => $.debug.empty)}</p>}
      {history.data?.items.map((item) => (
        <AppLink
          key={item.run.id}
          className="flex flex-wrap items-center gap-3 rounded-md border p-3"
          href={paths.workflowTestRunDetail(item.run.id)}
        >
          <WorkflowRunStatusBadge status={item.run.status} />
          <span>{item.run.created_at}</span>
          <span>{item.executionRef.definitionHash.slice(0, 12)}</span>
          {item.detailsPurgedAt && <span>{t(($) => $.debug.expired)}</span>}
          {item.cleanupState === "waiting_stop" && (
            <span>{t(($) => $.debug.waiting_stop)}</span>
          )}
        </AppLink>
      ))}
      <div className="flex gap-2">
        <Button
          disabled={offset === 0}
          onClick={() => setOffset(Math.max(0, offset - 20))}
        >
          {t(($) => $.debug.previous)}
        </Button>
        <Button
          disabled={!history.data || offset + 20 >= history.data.total}
          onClick={() => setOffset(offset + 20)}
        >
          {t(($) => $.debug.next)}
        </Button>
      </div>
    </section>
  );
}
function DebugSettings({ onClose }: { onClose(): void }) {
  const wsId = useWorkspaceId();
  const { t } = useT("workflows");
  const settings = useQuery({
    queryKey: [...workflowDebugKeys.all(wsId), "settings"],
    queryFn: () => api.getWorkflowDebugSettings(wsId),
  });
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open) onClose();
      }}
    >
      <DialogContent className="max-h-[85vh] overflow-auto">
        <DialogTitle>{t(($) => $.debug.settings)}</DialogTitle>
        <DialogDescription>{t(($) => $.debug.settings_hint)}</DialogDescription>
        {settings.isError && <p role="alert">{settings.error.message}</p>}
        {settings.data && (
          <SettingsForm
            key={settings.data.revision}
            revision={settings.data.revision}
            initial={settings.data.settings}
          />
        )}
      </DialogContent>
    </Dialog>
  );
}
function SettingsForm({
  revision,
  initial,
}: {
  revision: number;
  initial: WorkflowDebugPolicy;
}) {
  const { t } = useT("workflows");
  const wsId = useWorkspaceId();
  const [value, setValue] = useState(initial);
  const [message, setMessage] = useState("");
  const save = useUpdateWorkflowDebugSettings(wsId);
  const fields: {
    key: Exclude<keyof WorkflowDebugPolicy, "enabled">;
    label: string;
    min: number;
    max: number;
  }[] = [
    {
      key: "userActiveRuns",
      label: t(($) => $.debug.user_active),
      min: 1,
      max: 20,
    },
    {
      key: "workspaceActiveRuns",
      label: t(($) => $.debug.workspace_active),
      min: 1,
      max: 100,
    },
    {
      key: "userStartsPerHour",
      label: t(($) => $.debug.hourly),
      min: 1,
      max: 1000,
    },
    {
      key: "maxDurationSeconds",
      label: t(($) => $.debug.duration),
      min: 60,
      max: 86400,
    },
    {
      key: "retentionSeconds",
      label: t(($) => $.debug.retention),
      min: 86400,
      max: 7776000,
    },
    {
      key: "payloadCapacityBytes",
      label: t(($) => $.debug.capacity),
      min: 1048576,
      max: 1073741824,
    },
  ];
  return (
    <form
      className="space-y-3"
      onSubmit={async (e) => {
        e.preventDefault();
        setMessage("");
        try {
          await save.mutateAsync({ revision, settings: value });
          setMessage(t(($) => $.debug.saved));
        } catch (err) {
          setMessage(err instanceof Error ? err.message : String(err));
        }
      }}
    >
      <label className="flex gap-2">
        <input
          type="checkbox"
          checked={value.enabled}
          onChange={(e) => setValue({ ...value, enabled: e.target.checked })}
        />
        {t(($) => $.debug.enabled)}
      </label>
      {fields.map((f) => (
        <label key={f.key} className="block text-caption">
          {f.label}
          <input
            className="mt-1 block w-full rounded border bg-background p-2"
            type="number"
            required
            min={f.min}
            max={f.max}
            step={1}
            value={value[f.key]}
            onChange={(e) =>
              setValue({ ...value, [f.key]: Number(e.target.value) })
            }
          />
        </label>
      ))}
      {message && <p role="status">{message}</p>}
      <Button
        disabled={
          save.isPending || value.userActiveRuns > value.workspaceActiveRuns
        }
        type="submit"
      >
        {t(($) => $.debug.save)}
      </Button>
    </form>
  );
}

function DebugFailureDetails({ failure }: { failure: unknown }) {
  const { t } = useT("workflows");
  const parsed = WorkflowDebugFailureSchema.safeParse(failure);
  if (!parsed.success) return null;
  const labels: Record<string, string> = {
    debug_user_active_limit: t(($) => $.debug.user_active),
    debug_workspace_active_limit: t(($) => $.debug.workspace_active),
    debug_hourly_limit: t(($) => $.debug.hourly),
    debug_storage_limit: t(($) => $.debug.capacity),
  };
  return (
    <div role="alert" className="space-y-1 text-caption">
      {parsed.data.diagnostics?.map((d, i) => (
        <p key={i}>
          {d.node_key || d.field_path}: {d.message}
        </p>
      ))}
      {parsed.data.dimensions?.map((d) => (
        <p key={d.code}>
          {labels[d.code] ?? d.code}: {d.usage} / {d.limit}
        </p>
      ))}
      {parsed.data.retry_after_seconds !== undefined && (
        <p>
          {t(($) => $.debug.retry_after, {
            seconds: parsed.data.retry_after_seconds,
          })}
        </p>
      )}
    </div>
  );
}
