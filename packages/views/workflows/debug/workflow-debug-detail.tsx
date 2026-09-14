"use client";
import { useQuery } from "@tanstack/react-query";
import { useWorkspaceId } from "@multica/core/hooks";
import {
  workflowDebugRunOptions,
  workflowDebugDefinitionOptions,
  useCancelWorkflowDebugRun,
  useDecideWorkflowDebugAcceptance,
} from "@multica/core/workflows";
import { WorkflowRunPresentation } from "../runs/components/workflow-run-detail-page";
import { WorkflowDebugLimitsSummary } from "./workflow-debug-limits";
import { useT } from "../../i18n";

export function WorkflowDebugDetailPage({ runId }: { runId: string }) {
  const wsId = useWorkspaceId();
  const { t } = useT("workflows");
  const detail = useQuery(workflowDebugRunOptions(wsId, runId));
  const expired = !!detail.data?.debug.detailsPurgedAt;
  const definition = useQuery({
    ...workflowDebugDefinitionOptions(wsId, runId),
    enabled: !!detail.data && !expired,
  });
  const cancel = useCancelWorkflowDebugRun(wsId);
  const decide = useDecideWorkflowDebugAcceptance(wsId);
  const ref = detail.data?.executionRef;
  const matches =
    !!ref &&
    definition.data?.executionRef.snapshotId === ref.snapshotId &&
    definition.data.executionRef.definitionHash === ref.definitionHash;
  return (
    <div
      className="flex min-h-0 flex-1 flex-col"
      data-testid="workflow-debug-detail"
    >
      <div className="space-y-2 border-b bg-muted/40 px-4 py-3 text-caption">
        <strong>{t(($) => $.debug.title)}</strong>
        <p>{t(($) => $.debug.real_execution)}</p>
        {ref && (
          <p>
            {t(($) => $.debug.snapshot)}: {ref.definitionHash.slice(0, 12)} ·{" "}
            {t(($) => $.instances.revision)} {ref.baseRevision}
          </p>
        )}
        {detail.data && (
          <p>
            {t(($) => $.debug.deadline)}:{" "}
            {new Date(detail.data.debug.deadlineAt).toLocaleString()}
          </p>
        )}
        {detail.data && (
          <WorkflowDebugLimitsSummary
            limits={detail.data.debug.effectiveLimits}
            retentionSeconds={detail.data.debug.retentionSeconds}
          />
        )}
        {detail.data?.debug.cleanupState === "waiting_stop" && (
          <p role="status">{t(($) => $.debug.waiting_stop)}</p>
        )}
      </div>
      {expired ? (
        <p role="status" className="p-6">
          {t(($) => $.debug.expired)}
        </p>
      ) : (
        <WorkflowRunPresentation
          runId={runId}
          data={detail.data?.run}
          isLoading={detail.isLoading}
          error={detail.error}
          version={{
            data: matches ? definition.data : undefined,
            isError: definition.isError || (!!definition.data && !matches),
            refetch: definition.refetch,
          }}
          cancelRun={cancel}
          debug={{
            label: t(($) => $.debug.snapshot),
            decision: {
              isPending: decide.isPending,
              mutateAsync: ({ runId: id, ...body }) =>
                decide.mutateAsync({ id, body }),
            },
          }}
        />
      )}
    </div>
  );
}
