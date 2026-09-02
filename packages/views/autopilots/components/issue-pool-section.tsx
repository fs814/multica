"use client";

import { useEffect, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@multica/core/api";
import type { IssuePoolPreview } from "@multica/core/types";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Loader2 } from "lucide-react";
import { toast } from "sonner";
import { useT } from "../../i18n";
import { AppLink } from "../../navigation";

const pageSize = 10;

export function IssuePoolSection({ autopilotId, canWrite }: { autopilotId: string; canWrite: boolean }) {
  const wsId = useWorkspaceId();
  const wsPaths = useWorkspacePaths();
  const queryClient = useQueryClient();
  const { t } = useT("autopilots");
  const [preview, setPreview] = useState<IssuePoolPreview | null>(null);
  const [busy, setBusy] = useState(false);
  const [page, setPage] = useState(1);
  const [inactiveDays, setInactiveDays] = useState(30);
  const [batchLimit, setBatchLimit] = useState(10);
  const [maxInFlight, setMaxInFlight] = useState(10);
  const [requireDescription, setRequireDescription] = useState(true);
  const [requireAcceptance, setRequireAcceptance] = useState(true);
  const [inputMapping, setInputMapping] = useState("{}");
  const policyKey = ["autopilots", wsId, "issue-pool", autopilotId, "policy"] as const;
  const policy = useQuery({
    queryKey: policyKey,
    queryFn: () => api.getIssuePoolPolicy(autopilotId),
    retry: false,
  });
  const cyclesKey = ["autopilots", wsId, "issue-pool", autopilotId, "cycles", page] as const;
  const cycles = useQuery({
    queryKey: cyclesKey,
    queryFn: () => api.listIssuePoolCycles(autopilotId, page, pageSize),
    refetchInterval: 30_000,
  });

  useEffect(() => {
    if (!policy.data) return;
    setInactiveDays(policy.data.inactive_for_days);
    setBatchLimit(policy.data.batch_limit);
    setMaxInFlight(policy.data.max_in_flight);
    setRequireDescription(policy.data.require_description);
    setRequireAcceptance(policy.data.require_acceptance_criteria);
    setInputMapping(JSON.stringify(policy.data.workflow_input_mapping, null, 2));
  }, [policy.data]);

  const run = async (action: () => Promise<void>) => {
    setBusy(true);
    try {
      await action();
    } catch (error) {
      toast.error(error instanceof Error ? error.message : t(($) => $.issue_pool_panel.action_failed));
    } finally {
      setBusy(false);
    }
  };

  const refreshCycles = () => queryClient.invalidateQueries({
    queryKey: ["autopilots", wsId, "issue-pool", autopilotId, "cycles"],
  });

  const savePolicy = () => run(async () => {
    let mapping: Record<string, string>;
    try {
      mapping = JSON.parse(inputMapping) as Record<string, string>;
    } catch {
      throw new Error(t(($) => $.issue_pool_panel.mapping_invalid));
    }
    const current = policy.data;
    await api.putIssuePoolPolicy(autopilotId, {
      eligible_statuses: current?.eligible_statuses ?? ["backlog"],
      priorities: current?.priorities ?? [],
      required_label_ids: current?.required_label_ids ?? [],
      excluded_label_ids: current?.excluded_label_ids ?? [],
      property_match: current?.property_match ?? {},
      inactive_for_days: inactiveDays,
      batch_limit: batchLimit,
      max_in_flight: maxInFlight,
      review_mode: "manual",
      allow_human_assignee: current?.allow_human_assignee ?? false,
      require_description: requireDescription,
      require_acceptance_criteria: requireAcceptance,
      priority_weights: current?.priority_weights ?? {},
      workflow_input_mapping: mapping,
    });
    await queryClient.invalidateQueries({ queryKey: policyKey });
    toast.success(t(($) => $.issue_pool_panel.policy_saved));
  });

  const cycleRows = cycles.data?.cycles ?? [];
  const total = cycles.data?.total ?? 0;
  const lastPage = Math.max(1, Math.ceil(total / pageSize));

  return (
    <section className="space-y-3">
      <div className="flex items-center justify-between gap-3">
        <div>
          <h2 className="text-body font-medium text-muted-foreground uppercase tracking-wider">{t(($) => $.issue_pool_panel.title)}</h2>
          <p className="text-caption text-muted-foreground">{t(($) => $.issue_pool_panel.description)}</p>
        </div>
        {canWrite && (
          <div className="flex gap-2">
            <Button size="sm" variant="outline" disabled={busy} onClick={() => run(async () => setPreview(await api.previewIssuePool(autopilotId)))}>
              {t(($) => $.issue_pool_panel.preview)}
            </Button>
            <Button size="sm" disabled={busy} onClick={() => run(async () => {
              const key = `ui:${Date.now()}:${globalThis.crypto?.randomUUID?.() ?? "cycle"}`;
              await api.createIssuePoolCycle(autopilotId, key);
              setPreview(null);
              setPage(1);
              await refreshCycles();
            })}>
              {busy && <Loader2 className="mr-1 h-3.5 w-3.5 animate-spin" />} {t(($) => $.issue_pool_panel.create_batch)}
            </Button>
          </div>
        )}
      </div>

      <div className="grid gap-3 rounded-md border p-3 sm:grid-cols-3">
        <label className="space-y-1 text-caption text-muted-foreground">
          {t(($) => $.issue_pool_panel.inactive_days)}
          <Input type="number" min={0} max={3650} value={inactiveDays} onChange={(event) => setInactiveDays(Number(event.target.value))} disabled={!canWrite || busy} />
        </label>
        <label className="space-y-1 text-caption text-muted-foreground">
          {t(($) => $.issue_pool_panel.batch_limit)}
          <Input type="number" min={1} max={100} value={batchLimit} onChange={(event) => setBatchLimit(Number(event.target.value))} disabled={!canWrite || busy} />
        </label>
        <label className="space-y-1 text-caption text-muted-foreground">
          {t(($) => $.issue_pool_panel.max_in_flight)}
          <Input type="number" min={1} max={100} value={maxInFlight} onChange={(event) => setMaxInFlight(Number(event.target.value))} disabled={!canWrite || busy} />
        </label>
        <label className="flex items-center gap-2 text-caption">
          <input type="checkbox" checked={requireDescription} onChange={(event) => setRequireDescription(event.target.checked)} disabled={!canWrite || busy} />
          {t(($) => $.issue_pool_panel.require_description)}
        </label>
        <label className="flex items-center gap-2 text-caption">
          <input type="checkbox" checked={requireAcceptance} onChange={(event) => setRequireAcceptance(event.target.checked)} disabled={!canWrite || busy} />
          {t(($) => $.issue_pool_panel.require_acceptance)}
        </label>
        {canWrite && <Button size="sm" variant="outline" disabled={busy} onClick={savePolicy}>{t(($) => $.issue_pool_panel.save_policy)}</Button>}
        <label className="space-y-1 text-caption text-muted-foreground sm:col-span-3">
          {t(($) => $.issue_pool_panel.input_mapping)}
          <textarea className="min-h-20 w-full rounded-md border bg-background p-2 font-mono text-caption" value={inputMapping} onChange={(event) => setInputMapping(event.target.value)} disabled={!canWrite || busy} />
        </label>
        {policy.isError && <p className="sm:col-span-3 text-caption text-muted-foreground">{t(($) => $.issue_pool_panel.policy_required)}</p>}
      </div>

      {preview && (
        <div className="rounded-md border p-3 space-y-2">
          <div className="text-caption text-muted-foreground">
            {t(($) => $.issue_pool_panel.preview_summary, { scanned: preview.scanned_count, eligible: preview.eligible_count, selected: preview.selected_count })}
          </div>
          {preview.candidates.map((candidate) => (
            <div key={candidate.issue_id} className="flex items-center gap-2 text-body">
              <AppLink href={wsPaths.issueDetail(candidate.issue_id)} className="min-w-0 flex-1 truncate hover:underline">
                #{candidate.number} {candidate.title}
              </AppLink>
              <span className="text-caption tabular-nums text-muted-foreground" title={candidate.selection_reasons.join(", ")}>{t(($) => $.issue_pool_panel.score, { score: candidate.score })}</span>
            </div>
          ))}
        </div>
      )}

      {cycles.isLoading ? <div className="text-caption text-muted-foreground">{t(($) => $.issue_pool_panel.loading)}</div> :
        cycleRows.length === 0 ? <div className="rounded-md border border-dashed p-4 text-center text-body text-muted-foreground">{t(($) => $.issue_pool_panel.empty)}</div> :
        <div className="space-y-2">
          {cycleRows.map((cycle) => (
            <div key={cycle.id} className="rounded-md border p-3 space-y-2">
              <div className="flex justify-between text-caption">
                <span className="font-medium">{cycle.status.replaceAll("_", " ")}</span>
                <span className="text-muted-foreground">{t(($) => $.issue_pool_panel.cycle_summary, { completed: cycle.completed_count, review: cycle.waiting_acceptance_count, blocked: cycle.blocked_count, failed: cycle.failed_count })}</span>
              </div>
              {cycle.items.map((item) => (
                <div key={item.id} className="flex items-center gap-2 text-body">
                  <AppLink href={wsPaths.issueDetail(item.issue_id)} className="min-w-0 flex-1 truncate hover:underline">
                    {String(item.issue_snapshot.title ?? item.issue_id)}
                  </AppLink>
                  {item.workflow_run_id && (
                    <AppLink href={wsPaths.workflowRunDetail(item.workflow_run_id)} className="text-caption text-primary hover:underline">
                      {t(($) => $.issue_pool_panel.workflow_run)}
                    </AppLink>
                  )}
                  <span className="text-caption text-muted-foreground">{item.status.replaceAll("_", " ")}</span>
                  {canWrite && item.status === "claimed" && (
                    <>
                      <Button size="sm" variant="outline" disabled={busy} onClick={() => run(async () => { await api.reviewIssuePoolItem(autopilotId, cycle.id, item.id, "approve"); await refreshCycles(); })}>{t(($) => $.issue_pool_panel.approve)}</Button>
                      <Button size="sm" variant="ghost" disabled={busy} onClick={() => run(async () => { await api.reviewIssuePoolItem(autopilotId, cycle.id, item.id, "reject", t(($) => $.issue_pool_panel.reject_reason)); await refreshCycles(); })}>{t(($) => $.issue_pool_panel.reject)}</Button>
                    </>
                  )}
                </div>
              ))}
            </div>
          ))}
          <div className="flex items-center justify-end gap-2 text-caption text-muted-foreground">
            <span>{t(($) => $.issue_pool_panel.pagination, { total, page, pages: lastPage })}</span>
            <Button size="sm" variant="outline" disabled={page <= 1} onClick={() => setPage((value) => Math.max(1, value - 1))}>{t(($) => $.issue_pool_panel.previous)}</Button>
            <Button size="sm" variant="outline" disabled={page >= lastPage} onClick={() => setPage((value) => Math.min(lastPage, value + 1))}>{t(($) => $.issue_pool_panel.next)}</Button>
          </div>
        </div>}
    </section>
  );
}
