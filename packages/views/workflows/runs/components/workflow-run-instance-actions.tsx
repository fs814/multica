"use client";

import { ScriptPipelineStepActions } from "../../components/script-pipeline-step-actions";
import type { RunWorkflowInstance } from "@multica/core/workflows";
import { useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { RotateCcw } from "lucide-react";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import {
  useRunWorkflowInstance,
  workflowInstanceOptions,
} from "@multica/core/workflows";
import { Button } from "@multica/ui/components/ui/button";
import { AppLink, useNavigation } from "../../../navigation";
import { useT } from "../../../i18n";

export function WorkflowRunInstanceActions({
  instanceId,
  runId,
  scripts = false,
}: {
  instanceId: string;
  runId: string;
  scripts?: boolean;
}) {
  const ws = useWorkspaceId();
  const paths = useWorkspacePaths();
  const navigation = useNavigation();
  const { t } = useT("workflows");
  const instance = useQuery(workflowInstanceOptions(ws, instanceId));
  const run = useRunWorkflowInstance(instanceId);
  const attempt = useRef({ intent: "", key: "" });
  const busy = useRef(false);
  const [starting, setStarting] = useState(false);
  const [error, setError] = useState("");
  const start = async (scriptStep?: RunWorkflowInstance["script_step"]) => {
    if (busy.current || !instance.data || instance.data.archivedAt) return;
    busy.current = true;
    setStarting(true);
    setError("");
    const revision = instance.data.revision;
    const intent = JSON.stringify({ revision, scriptStep });
    if (attempt.current.intent !== intent) {
      attempt.current = { intent, key: crypto.randomUUID() };
    }
    try {
      const created = await run.mutateAsync({
        revision,
        mode: "history",
        ...(scriptStep ? { script_step: scriptStep } : {}),
        history_run_id: runId,
        idempotency_key: attempt.current.key,
      });
      navigation.push(paths.workflowRunDetail(created.id));
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
      void instance.refetch();
    } finally {
      busy.current = false;
      setStarting(false);
    }
  };
  return (
    <div className="flex flex-col items-end gap-1">
      <div className="flex flex-wrap items-center gap-2">
        <AppLink
          className="text-caption underline"
          href={paths.workflowInstanceDetail(instanceId)}
        >
          {t(($) => $.runs.detail.edit_instance_inputs)}
        </AppLink>
        <Button
          size="sm"
          disabled={starting || !instance.data || Boolean(instance.data.archivedAt)}
          title={t(($) => $.runs.detail.rerun_hint)}
          onClick={() => void start()}
        >
          <RotateCcw className="mr-1 size-3.5" aria-hidden="true" />
          {starting
            ? t(($) => $.runs.detail.rerunning)
            : t(($) => $.runs.detail.rerun)}
        </Button>
      </div>
      {scripts && (
        <ScriptPipelineStepActions
          disabled={starting || !instance.data || Boolean(instance.data.archivedAt)}
          onRun={(step) => void start(step)}
        />
      )}
      {instance.data?.archivedAt && (
        <p role="status" className="text-caption text-muted-foreground">
          {t(($) => $.runs.detail.rerun_archived)}
        </p>
      )}
      {instance.isError && (
        <div role="alert" className="text-caption">
          {t(($) => $.runs.detail.rerun_instance_unavailable)}
          <Button size="sm" variant="ghost" onClick={() => void instance.refetch()}>
            {t(($) => $.page.retry)}
          </Button>
        </div>
      )}
      {error && (
        <p role="alert" className="max-w-sm text-caption text-destructive">
          {error}
        </p>
      )}
    </div>
  );
}
