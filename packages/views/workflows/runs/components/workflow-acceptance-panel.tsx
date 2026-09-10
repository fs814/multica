"use client";

import { useState } from "react";
import { CircleCheck, ScrollText, Undo2 } from "lucide-react";
import { toast } from "sonner";
import { useDecideWorkflowAcceptance } from "@multica/core/workflows";
import type { WorkflowAcceptance, WorkflowStep } from "@multica/core/workflows";
import { Button } from "@multica/ui/components/ui/button";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@multica/ui/components/ui/select";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { useT } from "../../../i18n";
import { WorkflowVerdictBadge } from "./run-status-badge";
import { artifactSummary } from "../submission";

/**
 * The human gate on a run.
 *
 * ## Why rejecting is a two-step interaction
 *
 * A rejection is not the opposite of an accept - it is a *briefing*. The reason
 * becomes the rework attempt's instruction and the target decides which node
 * gets to re-run, so a one-click Reject would either send an empty brief or
 * pick a target on the reviewer's behalf. Accept stays one click because it
 * carries no such payload. So Reject opens a form, and the confirm inside that
 * form is what posts.
 *
 * ## Why "reject with no reason" is impossible here, not merely refused
 *
 * The server 409s a reason-less rejection, and that is the correct trust
 * boundary. But discovering it *after* clicking teaches the reviewer nothing
 * about what to type - and this is exactly the moment their attention is on
 * the work, not on the API. The confirm button is disabled until both a reason
 * and a permitted target are present, and the requirement is stated in place.
 *
 * ## Why the target list comes from the acceptance row, not the template
 *
 * `rework_targets` is copied off the *pinned* node when the gate opens, so it
 * is what the engine will actually validate against (`node.AllowsReworkTo`).
 * Reading today's template instead would offer a target the pinned graph does
 * not admit, and the reviewer would meet a 422 for a choice the UI presented.
 *
 * An empty list is therefore accept-only rather than permissive. That reading is
 * load-bearing: the schema defaults `rework_targets` to `[]` when it cannot be
 * parsed, so treating empty as "anything goes" would turn a contract drift into
 * a stream of rejected submissions.
 */

export function WorkflowAcceptancePanel({
  runId,
  acceptance,
  /** The step the gate is judging, when it can be located in the trace. */
  evidenceStep,
}: {
  runId: string;
  acceptance: WorkflowAcceptance;
  evidenceStep: WorkflowStep | null;
}) {
  const { t } = useT("workflows");
  const decide = useDecideWorkflowAcceptance();

  const [rejecting, setRejecting] = useState(false);
  const [reason, setReason] = useState("");
  const [target, setTarget] = useState("");

  const targets = acceptance.rework_targets;
  const canReject =
    targets.length > 0 || acceptance.can_reject_without_rework === true;
  const canConfirmReject =
    reason.trim().length > 0 &&
    (target !== "" || acceptance.can_reject_without_rework === true) &&
    !decide.isPending;

  const handleAccept = async () => {
    try {
      await decide.mutateAsync({ runId, accept: true });
      toast.success(t(($) => $.runs.acceptance.toast_accepted));
    } catch (err) {
      toast.error(
        err instanceof Error && err.message
          ? err.message
          : t(($) => $.runs.acceptance.toast_failed),
      );
    }
  };

  const handleReject = async () => {
    if (!canConfirmReject) return;
    try {
      await decide.mutateAsync({
        runId,
        accept: false,
        reason: reason.trim(),
        rework_target: target,
      });
      toast.success(
        acceptance.can_reject_without_rework
          ? t(($) => $.graph_v2.rejected)
          : t(($) => $.runs.acceptance.toast_rejected),
      );
      // Only cleared on success. A failed submit keeps the reviewer's text: it
      // is the most expensive thing on this panel to retype, and the failure is
      // usually transport, not content.
      setRejecting(false);
      setReason("");
      setTarget("");
    } catch (err) {
      toast.error(
        err instanceof Error && err.message
          ? err.message
          : t(($) => $.runs.acceptance.toast_failed),
      );
    }
  };

  const submission = evidenceStep?.submission ?? null;
  const summary = submission ? artifactSummary(submission.artifact) : "";

  return (
    <section
      // `status`, not `alert`: this is a steady state the reviewer navigated to,
      // not an event that just fired at them.
      role="status"
      className="rounded-lg border border-primary/40 bg-primary/5 p-4"
    >
      <h2 className="flex items-center gap-2 text-body font-medium">
        <CircleCheck
          className="size-4 shrink-0 text-primary"
          aria-hidden="true"
        />
        {t(($) => $.runs.acceptance.title)}
      </h2>

      <div className="mt-3 flex flex-col gap-4">
        <div>
          <h3 className="text-caption font-semibold tracking-wide text-muted-foreground uppercase">
            {t(($) => $.runs.acceptance.criteria)}
          </h3>
          {acceptance.criteria.length === 0 ? (
            <p className="mt-1 text-caption text-muted-foreground">
              {t(($) => $.runs.acceptance.criteria_empty)}
            </p>
          ) : (
            <ul className="mt-1.5 flex flex-col gap-1">
              {acceptance.criteria.map((criterion, index) => (
                // Index key: criteria are a positional list of strings with no
                // identity of their own, and two criteria may legitimately read
                // the same.
                <li
                  key={index}
                  className="flex gap-2 text-caption leading-snug text-foreground"
                >
                  <span aria-hidden="true" className="text-muted-foreground">
                    •
                  </span>
                  <span className="min-w-0">{criterion}</span>
                </li>
              ))}
            </ul>
          )}
        </div>

        {/* The evidence, inline. A reviewer asked to judge work against criteria
            needs the work in the same frame; making them scroll into the trace
            to find the verdict they are ruling on is how a gate becomes a
            rubber stamp. */}
        <div>
          <h3 className="flex items-center gap-1.5 text-caption font-semibold tracking-wide text-muted-foreground uppercase">
            <ScrollText className="size-3.5" aria-hidden="true" />
            {t(($) => $.runs.acceptance.evidence, {
              node: evidenceStep?.node_key ?? "",
            })}
          </h3>
          {submission === null ? (
            <p className="mt-1 text-caption text-muted-foreground">
              {t(($) => $.runs.acceptance.evidence_empty)}
            </p>
          ) : (
            <div className="mt-1.5 flex flex-col gap-1.5">
              <WorkflowVerdictBadge verdict={submission.verdict} />
              {summary ? (
                <p className="text-caption leading-snug whitespace-pre-wrap">
                  {summary}
                </p>
              ) : null}
              {submission.rationale ? (
                <p className="text-caption leading-snug whitespace-pre-wrap text-muted-foreground">
                  {submission.rationale}
                </p>
              ) : null}
            </div>
          )}
        </div>

        {rejecting ? (
          <div className="flex flex-col gap-3 rounded-md border border-border bg-background p-3">
            <label className="flex flex-col gap-1.5">
              <span className="text-caption font-medium">
                {t(($) => $.runs.acceptance.reason_label)}
              </span>
              <Textarea
                value={reason}
                rows={4}
                className="min-h-24 resize-y"
                placeholder={t(($) => $.runs.acceptance.reason_placeholder)}
                onChange={(event) => setReason(event.target.value)}
              />
              <span className="text-caption leading-snug text-muted-foreground">
                {t(($) => $.runs.acceptance.reason_required)}
              </span>
            </label>

            {targets.length > 0 ? (
              <div className="flex flex-col gap-1.5">
                <span className="text-caption font-medium">
                  {t(($) => $.runs.acceptance.target_label)}
                </span>
                <Select<string>
                  items={targets.map((node) => ({ value: node, label: node }))}
                  value={target}
                  onValueChange={(next) => {
                    if (next === null) return;
                    setTarget(next);
                  }}
                >
                  <SelectTrigger
                    size="sm"
                    aria-label={t(($) => $.runs.acceptance.target_label)}
                    className="w-full min-w-0"
                  >
                    <SelectValue
                      placeholder={t(($) => $.runs.acceptance.target_unset)}
                    />
                  </SelectTrigger>
                  <SelectContent>
                    {targets.map((node) => (
                      <SelectItem key={node} value={node}>
                        {node}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>
            ) : null}

            <div className="flex items-center justify-end gap-2">
              <Button
                size="sm"
                variant="ghost"
                disabled={decide.isPending}
                onClick={() => setRejecting(false)}
              >
                {t(($) => $.runs.acceptance.reject_cancel)}
              </Button>
              <Button
                size="sm"
                variant="destructive"
                disabled={!canConfirmReject}
                onClick={() => void handleReject()}
              >
                {decide.isPending
                  ? t(($) => $.runs.acceptance.rejecting)
                  : acceptance.can_reject_without_rework
                    ? t(($) => $.graph_v2.reject)
                    : t(($) => $.runs.acceptance.reject)}
              </Button>
            </div>
          </div>
        ) : (
          <div className="flex flex-wrap items-center gap-2">
            <Button
              size="sm"
              disabled={decide.isPending}
              onClick={() => void handleAccept()}
            >
              {decide.isPending
                ? t(($) => $.runs.acceptance.accepting)
                : t(($) => $.runs.acceptance.accept)}
            </Button>
            {canReject ? (
              <Button
                size="sm"
                variant="outline"
                disabled={decide.isPending}
                onClick={() => setRejecting(true)}
              >
                <Undo2 className="mr-1 size-3.5" aria-hidden="true" />
                {acceptance.can_reject_without_rework
                  ? t(($) => $.graph_v2.reject)
                  : t(($) => $.runs.acceptance.reject)}
              </Button>
            ) : (
              // Stated rather than silently absent: a reviewer who expects a
              // reject button and cannot find one would assume the page is
              // broken, when in fact the graph pinned no rework targets.
              <span className="text-caption leading-snug text-muted-foreground">
                {t(($) => $.runs.acceptance.targets_empty)}
              </span>
            )}
          </div>
        )}
      </div>
    </section>
  );
}

/**
 * A gate that has already been decided.
 *
 * Kept visible after the decision, and not folded into the trace: the reason a
 * reviewer gave is the only record of *why* a rework round exists, and a
 * subsequent reader looking at three attempts at the same node needs it to make
 * sense of them.
 */
export function WorkflowAcceptanceDecided({
  acceptance,
}: {
  acceptance: WorkflowAcceptance;
}) {
  const { t } = useT("workflows");
  return (
    <section className="rounded-lg border p-4">
      <h2 className="text-body font-medium">
        {t(($) => $.runs.acceptance.decided, { status: acceptance.status })}
      </h2>
      {acceptance.reason ? (
        <p className="mt-1.5 text-caption leading-snug whitespace-pre-wrap text-muted-foreground">
          {t(($) => $.runs.acceptance.decided_reason, {
            reason: acceptance.reason,
          })}
        </p>
      ) : null}
      {acceptance.rework_target_node_key ? (
        <p className="mt-1 font-mono text-caption text-muted-foreground">
          {t(($) => $.runs.acceptance.decided_target, {
            node: acceptance.rework_target_node_key,
          })}
        </p>
      ) : null}
    </section>
  );
}
