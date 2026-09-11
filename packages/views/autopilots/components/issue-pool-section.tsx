"use client";

import { useEffect, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { ChevronLeft, ChevronRight, Eye, Save } from "lucide-react";
import {
  issuePoolCyclesOptions,
  issuePoolPolicyOptions,
} from "@multica/core/autopilots/queries";
import {
  useCreateIssuePoolCycle,
  usePreviewIssuePool,
  usePutIssuePoolPolicy,
  useReviewIssuePoolItems,
} from "@multica/core/autopilots/mutations";
import type {
  IssuePoolCycle,
  IssuePoolPolicy,
  IssuePoolReviewDecision,
  PutIssuePoolPolicyRequest,
} from "@multica/core/types";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { toast } from "sonner";
import { useT } from "../../i18n";

const PAGE_SIZE = 5;
const DEFAULT_POLICY: PutIssuePoolPolicyRequest = {
  eligible_statuses: ["backlog", "todo"],
  priorities: [],
  required_label_ids: [],
  excluded_label_ids: [],
  property_match: {},
  inactive_for_days: 0,
  batch_limit: 10,
  max_in_flight: 10,
  allow_human_assignee: false,
  require_description: true,
  require_acceptance_criteria: true,
  priority_weights: { urgent: 400, high: 300, medium: 200, low: 100, none: 0 },
  workflow_input_mapping: { title: "title", description: "description" },
};

function editablePolicy(policy?: IssuePoolPolicy): PutIssuePoolPolicyRequest {
  if (!policy) return DEFAULT_POLICY;
  const {
    id: _id,
    autopilot_id: _autopilotId,
    workspace_id: _workspaceId,
    project_id: _projectId,
    review_mode: _reviewMode,
    created_by_id: _createdById,
    created_at: _createdAt,
    updated_at: _updatedAt,
    ...editable
  } = policy;
  return editable;
}

function csv(value: string): string[] {
  return value.split(",").map((part) => part.trim()).filter(Boolean);
}

function failureMessage(cycle: IssuePoolCycle): string | null {
  const item = cycle.items.find((candidate) =>
    candidate.status === "blocked" || candidate.status === "deferred" || candidate.status === "failed");
  if (!item) return null;
  const detail = item.failure_detail?.message;
  return typeof detail === "string" && detail.trim() ? detail : item.failure_code;
}

export function IssuePoolCycleReview({
  cycle,
  canWrite,
  issueHref,
  workflowHref,
  onReview,
}: {
  cycle: IssuePoolCycle;
  canWrite: boolean;
  issueHref: (id: string) => string;
  workflowHref: (id: string) => string;
  onReview: (decisions: IssuePoolReviewDecision[]) => Promise<void>;
}) {
  const { t } = useT("autopilots");
  const claimed = useMemo(
    () => cycle.items.filter((item) => item.status === "claimed"),
    [cycle.items],
  );
  const [selected, setSelected] = useState<string[]>([]);
  const [rejectReason, setRejectReason] = useState("");

  useEffect(() => {
    setSelected(claimed.map((item) => item.id));
  }, [claimed, cycle.id, cycle.updated_at]);

  const decide = async (decision: "approve" | "reject") => {
    if (selected.length === 0) return;
    if (decision === "reject" && !rejectReason.trim()) {
      toast.error(t(($) => $.issue_pool.reject_reason_required));
      return;
    }
    await onReview(selected.map((item_id) => ({
      item_id,
      decision,
      reason: decision === "reject" ? rejectReason.trim() : undefined,
    })));
  };

  return (
    <div className="space-y-3" data-testid="issue-pool-cycle">
      <div className="flex flex-wrap gap-3 text-caption text-muted-foreground">
        <span>{t(($) => $.issue_pool.cycle_status, { status: cycle.status })}</span>
        <span>{t(($) => $.issue_pool.cycle_counts, {
          scanned: cycle.scanned_count,
          eligible: cycle.eligible_count,
          claimed: cycle.claimed_count,
        })}</span>
      </div>
      {failureMessage(cycle) && (
        <div role="alert" className="rounded-md border border-amber-500/40 bg-amber-500/5 p-3 text-caption">
          <div className="font-medium">{t(($) => $.issue_pool.remediation_title)}</div>
          <div className="text-muted-foreground">
            {failureMessage(cycle)} · {t(($) => $.issue_pool.remediation_hint)}
          </div>
        </div>
      )}
      <div className="divide-y rounded-md border">
        {cycle.items.map((item) => {
          const title = typeof item.issue_snapshot.title === "string"
            ? item.issue_snapshot.title
            : item.issue_id;
          return (
            <div key={item.id} className="flex items-start gap-3 p-3">
              {item.status === "claimed" && canWrite && (
                <input
                  type="checkbox"
                  aria-label={t(($) => $.issue_pool.select_candidate, { title })}
                  checked={selected.includes(item.id)}
                  onChange={(event) => setSelected((current) =>
                    event.target.checked
                      ? [...current, item.id]
                      : current.filter((id) => id !== item.id))}
                />
              )}
              <div className="min-w-0 flex-1">
                <a className="font-medium hover:underline" href={issueHref(item.issue_id)}>{title}</a>
                <div className="text-caption text-muted-foreground">
                  {item.status} · {t(($) => $.issue_pool.score, { score: item.score })}
                </div>
                <div className="text-micro text-muted-foreground">
                  {item.selection_reasons.join(" · ")}
                </div>
              </div>
              {item.workflow_run_id && (
                <a className="text-caption text-primary hover:underline" href={workflowHref(item.workflow_run_id)}>
                  {item.status === "waiting_acceptance"
                    ? t(($) => $.issue_pool.review_acceptance)
                    : t(($) => $.issue_pool.open_workflow_run)}
                </a>
              )}
            </div>
          );
        })}
      </div>
      {claimed.length > 0 && canWrite && (
        <div className="flex flex-wrap items-center gap-2">
          <Input
            value={rejectReason}
            onChange={(event) => setRejectReason(event.target.value)}
            placeholder={t(($) => $.issue_pool.reject_reason_placeholder)}
            className="min-w-52 flex-1"
          />
          <Button variant="outline" disabled={selected.length === 0} onClick={() => void decide("reject")}>
            {t(($) => $.issue_pool.reject_selected, { count: selected.length })}
          </Button>
          <Button disabled={selected.length === 0} onClick={() => void decide("approve")}>
            {t(($) => $.issue_pool.approve_selected, { count: selected.length })}
          </Button>
        </div>
      )}
    </div>
  );
}

export function IssuePoolSection({
  autopilotId,
  canWrite,
  workflowTemplateId,
  workflowTemplateVersionId,
}: {
  autopilotId: string;
  canWrite: boolean;
  workflowTemplateId: string | null;
  workflowTemplateVersionId: string | null;
}) {
  const { t } = useT("autopilots");
  const wsId = useWorkspaceId();
  const wsPaths = useWorkspacePaths();
  const [page, setPage] = useState(0);
  const policyQuery = useQuery(issuePoolPolicyOptions(wsId, autopilotId));
  const cyclesQuery = useQuery(issuePoolCyclesOptions(wsId, autopilotId, {
    limit: PAGE_SIZE,
    offset: page * PAGE_SIZE,
  }));
  const createCycle = useCreateIssuePoolCycle();
  const savePolicy = usePutIssuePoolPolicy();
  const previewPool = usePreviewIssuePool();
  const review = useReviewIssuePoolItems();
  const [policy, setPolicy] = useState<PutIssuePoolPolicyRequest>(DEFAULT_POLICY);
  const [mapping, setMapping] = useState(JSON.stringify(DEFAULT_POLICY.workflow_input_mapping, null, 2));

  useEffect(() => {
    if (!policyQuery.data) return;
    const next = editablePolicy(policyQuery.data);
    setPolicy(next);
    setMapping(JSON.stringify(next.workflow_input_mapping, null, 2));
  }, [policyQuery.data]);

  const cycles = cyclesQuery.data?.cycles ?? [];
  const total = cyclesQuery.data?.total ?? 0;
  const excluded = useMemo(() => Object.entries(previewPool.data?.excluded_by_rule ?? {})
    .filter(([, count]) => count > 0), [previewPool.data]);

  const persist = async () => {
    try {
      const parsed = JSON.parse(mapping) as Record<string, string>;
      await savePolicy.mutateAsync({
        autopilotId,
        policy: { ...policy, workflow_input_mapping: parsed },
      });
      toast.success(t(($) => $.issue_pool.policy_saved));
    } catch (error) {
      toast.error(error instanceof SyntaxError
        ? t(($) => $.issue_pool.mapping_invalid)
        : t(($) => $.issue_pool.policy_save_failed));
    }
  };

  return (
    <section className="space-y-4" data-testid="issue-pool-section">
      <div className="flex items-center justify-between">
        <h2 className="text-body font-medium text-muted-foreground uppercase tracking-wider">
          {t(($) => $.issue_pool.title)}
        </h2>
        <span className="text-micro text-muted-foreground">
          {t(($) => $.issue_pool.pinned_template, {
            template: workflowTemplateId ?? "—",
            version: workflowTemplateVersionId ?? "—",
          })}
        </span>
      </div>

      <div className="grid gap-3 rounded-md border p-4 sm:grid-cols-3">
        <label className="text-caption">
          {t(($) => $.issue_pool.eligible_statuses)}
          <Input
            value={policy.eligible_statuses.join(", ")}
            onChange={(event) => setPolicy({ ...policy, eligible_statuses: csv(event.target.value) })}
            disabled={!canWrite}
          />
        </label>
        <label className="text-caption">
          {t(($) => $.issue_pool.priorities)}
          <Input
            value={policy.priorities.join(", ")}
            onChange={(event) => setPolicy({ ...policy, priorities: csv(event.target.value) })}
            disabled={!canWrite}
          />
        </label>
        <label className="text-caption">
          {t(($) => $.issue_pool.inactive_days)}
          <Input
            type="number"
            min={0}
            value={policy.inactive_for_days}
            onChange={(event) => setPolicy({ ...policy, inactive_for_days: Number(event.target.value) })}
            disabled={!canWrite}
          />
        </label>
        <label className="text-caption">
          {t(($) => $.issue_pool.batch_limit)}
          <Input
            type="number"
            min={1}
            max={100}
            value={policy.batch_limit}
            onChange={(event) => setPolicy({ ...policy, batch_limit: Number(event.target.value) })}
            disabled={!canWrite}
          />
        </label>
        <label className="text-caption">
          {t(($) => $.issue_pool.max_in_flight)}
          <Input
            type="number"
            min={1}
            max={100}
            value={policy.max_in_flight}
            onChange={(event) => setPolicy({ ...policy, max_in_flight: Number(event.target.value) })}
            disabled={!canWrite}
          />
        </label>
        <label className="text-caption sm:col-span-3">
          {t(($) => $.issue_pool.input_mapping)}
          <textarea
            value={mapping}
            onChange={(event) => setMapping(event.target.value)}
            disabled={!canWrite}
            className="mt-1 min-h-24 w-full rounded-md border bg-background p-2 font-mono text-caption"
          />
        </label>
        {canWrite && (
          <div className="flex gap-2 sm:col-span-3">
            <Button variant="outline" onClick={() => void previewPool.mutateAsync(autopilotId)}>
              <Eye className="mr-1 size-3.5" />{t(($) => $.issue_pool.preview)}
            </Button>
            <Button variant="outline" disabled={createCycle.isPending} onClick={async () => {
              try {
                await createCycle.mutateAsync({ autopilotId, idempotencyKey: `ui:${crypto.randomUUID()}` });
                setPage(0);
              } catch (error) { toast.error(error instanceof Error ? error.message : String(error)); }
            }}>{t(($) => $.issue_pool_panel.create_batch)}</Button>
            <Button onClick={() => void persist()}>
              <Save className="mr-1 size-3.5" />{t(($) => $.issue_pool.save_policy)}
            </Button>
          </div>
        )}
      </div>

      {previewPool.data && (
        <div className="space-y-2 rounded-md border p-4">
          <div className="text-caption">
            {t(($) => $.issue_pool.preview_summary, {
              scanned: previewPool.data.scanned_count,
              eligible: previewPool.data.eligible_count,
              selected: previewPool.data.selected_count,
            })}
          </div>
          {excluded.length > 0 && (
            <div className="text-micro text-muted-foreground">
              {t(($) => $.issue_pool.excluded)}: {excluded.map(([reason, count]) => `${reason} (${count})`).join(" · ")}
            </div>
          )}
          {previewPool.data.candidates.map((candidate) => (
            <div key={candidate.issue_id} className="flex items-center justify-between border-t pt-2 text-caption">
              <a href={wsPaths.issueDetail(candidate.issue_id)} className="font-medium hover:underline">
                {candidate.title}
              </a>
              <span className="text-muted-foreground">
                {candidate.selection_reasons.join(" · ")}
              </span>
            </div>
          ))}
        </div>
      )}

      {cyclesQuery.isLoading ? (
        <Skeleton className="h-40 w-full" />
      ) : cycles.length === 0 ? (
        <div className="rounded-md border border-dashed p-4 text-center text-caption text-muted-foreground">
          {t(($) => $.issue_pool.no_cycles)}
        </div>
      ) : (
        <div className="space-y-5">
          {cycles.map((cycle) => (
            <IssuePoolCycleReview
              key={cycle.id}
              cycle={cycle}
              canWrite={canWrite}
              issueHref={wsPaths.issueDetail}
              workflowHref={wsPaths.workflowRunDetail}
              onReview={async (decisions) => {
                await review.mutateAsync({ autopilotId, cycleId: cycle.id, decisions });
                toast.success(t(($) => $.issue_pool.review_saved));
              }}
            />
          ))}
          <div className="flex items-center justify-between">
            <Button
              variant="outline"
              size="sm"
              disabled={page === 0}
              onClick={() => setPage((current) => Math.max(0, current - 1))}
            >
              <ChevronLeft className="size-3.5" />{t(($) => $.issue_pool.previous)}
            </Button>
            <span className="text-caption text-muted-foreground">
              {t(($) => $.issue_pool.page, { page: page + 1, total })}
            </span>
            <Button
              variant="outline"
              size="sm"
              disabled={(page + 1) * PAGE_SIZE >= total}
              onClick={() => setPage((current) => current + 1)}
            >
              {t(($) => $.issue_pool.next)}<ChevronRight className="size-3.5" />
            </Button>
          </div>
        </div>
      )}
    </section>
  );
}
