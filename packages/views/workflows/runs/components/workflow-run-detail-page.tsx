"use client";

import { useState } from "react";
import { AlertCircle, Ban, XCircle } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import {
  useCancelWorkflowRun,
  workflowRunDetailOptions,
} from "@multica/core/workflows";
import type { WorkflowStep } from "@multica/core/workflows";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import type { AgentTask } from "@multica/core/types/agent";
import { Badge } from "@multica/ui/components/ui/badge";
import { Button } from "@multica/ui/components/ui/button";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { AppLink } from "../../../navigation";
import { BreadcrumbHeader } from "../../../layout/breadcrumb-header";
import { TranscriptButton } from "../../../common/task-transcript";
import { formatInTimeZone } from "../../../common/format-in-time-zone";
import { formatDuration } from "../../../dashboard/utils";
import { useT } from "../../../i18n";
import { runElapsedSeconds, useExplainReason } from "../run-reason";
import {
  artifactHasExtraKeys,
  artifactReferences,
  artifactSummary,
  artifactType,
} from "../submission";
import {
  WorkflowAcceptanceDecided,
  WorkflowAcceptancePanel,
} from "./workflow-acceptance-panel";
import {
  WorkflowRunStatusBadge,
  WorkflowStepStatusBadge,
  WorkflowVerdictBadge,
} from "./run-status-badge";

/**
 * The Run detail page: Run -> Step -> Task, plus the human gate.
 *
 * ## Why the terminal reason is the most prominent thing on the page
 *
 * Every run that stops short carries a *classified* reason - `blocked_reason`
 * or `failure_reason`, drawn from a fixed vocabulary the engine owns. That
 * classification is the entire value of stopping instead of guessing: a run
 * blocked on `routing_no_candidate` needs an agent brought online, one blocked
 * on `submission_contract_invalid` needs the node's contract looked at, and a
 * page that renders both as "blocked" throws away the distinction the engine
 * paid for. So the reason gets its own banner above the trace, translated into
 * a sentence (see run-reason.ts) rather than shown as a token.
 *
 * ## Why the trace is a history, not a current-state list
 *
 * A rework round produces a NEW step with the same `node_key` and a higher
 * `attempt`. Rendering only the latest attempt per node - which reads as the
 * tidier choice - would erase exactly the thing a reader opens this page to
 * understand: that `implement` ran three times and why the first two came back.
 * Every step row is shown, in server order, with its attempt number.
 *
 * ## Why this page gates on `status`, not on `id` or `steps.length`
 *
 * `getWorkflowRun` spreads the requested id onto its parse-miss fallback so the
 * page keeps its breadcrumb and cancel target, which makes a non-empty `id` say
 * nothing about whether the response was readable. `steps: []` is equally
 * ambiguous - a genuinely fresh run has no steps either. `status` is the one
 * field the schema refuses to default and the server always emits, so `""`
 * means "unreadable" and nothing else. Without this gate an unreadable run
 * renders as a live run that has done nothing, and offers a Cancel button for
 * something we cannot describe.
 */

/** Run states where cancelling is still meaningful. */
const CANCELLABLE = new Set([
  "pending",
  "running",
  "waiting_acceptance",
  "blocked",
]);

export function WorkflowRunDetailPage({ runId }: { runId: string }) {
  const { t, i18n } = useT("workflows");
  const wsId = useWorkspaceId();
  const wsPaths = useWorkspacePaths();
  const explainReason = useExplainReason();

  const { data, isLoading, error } = useQuery(
    workflowRunDetailOptions(wsId, runId),
  );

  const cancelRun = useCancelWorkflowRun();
  const [cancelling, setCancelling] = useState(false);

  if (isLoading) return <RunSkeleton />;

  // See the header: `status` is the only field that separates a real run from a
  // fallback the client spread an id onto.
  if (error || !data || !data.status) {
    return (
      <div className="flex h-full flex-col">
        <BreadcrumbHeader
          segments={[
            {
              href: wsPaths.workflowRuns(),
              label: t(($) => $.runs.page.title),
            },
          ]}
          leaf={
            <h1 className="min-w-0 truncate text-body font-medium">
              {t(($) => $.runs.detail.not_found)}
            </h1>
          }
        />
        <div className="flex flex-1 items-center justify-center px-6">
          <p className="max-w-md text-center text-body text-muted-foreground">
            {error
              ? t(($) => $.runs.detail.not_found)
              : t(($) => $.runs.detail.unreadable)}
          </p>
        </div>
      </div>
    );
  }

  const run = data;
  const elapsed = runElapsedSeconds(run.started_at, run.completed_at);
  const inputTitle =
    typeof run.input.title === "string" ? run.input.title.trim() : "";
  const inputDescription =
    typeof run.input.description === "string"
      ? run.input.description.trim()
      : "";

  const handleCancel = async () => {
    setCancelling(true);
    try {
      await cancelRun.mutateAsync(runId);
      toast.success(t(($) => $.runs.detail.toast_cancelled));
    } catch (err) {
      toast.error(
        err instanceof Error && err.message
          ? err.message
          : t(($) => $.runs.detail.toast_cancel_failed),
      );
    } finally {
      setCancelling(false);
    }
  };

  // The acceptance row names its step by id; resolving it here rather than in
  // the panel keeps the panel free of trace-shaped knowledge. A null result is
  // a legitimate state (the step could not be read, see the schema's local
  // degradation) and the panel says so instead of rendering a blank quote.
  const acceptance = run.acceptance;
  const evidenceStep = acceptance
    ? (upstreamEvidence(run.steps, acceptance.step_id) ?? null)
    : null;

  return (
    <div className="flex h-full flex-col">
      <BreadcrumbHeader
        segments={[
          { href: wsPaths.workflowRuns(), label: t(($) => $.runs.page.title) }
        ]}
        leaf={
          <>
            <h1 className="min-w-0 truncate text-body font-medium">
              {inputTitle || run.template_name}
            </h1>
            <div className="ml-1 flex shrink-0 items-center gap-1.5">
              <WorkflowRunStatusBadge status={run.status} />
              {run.source ? (
                <Badge variant="outline" className="hidden sm:inline-flex">
                  {t(($) => $.runs.detail.source, { source: run.source })}
                </Badge>
              ) : null}
            </div>
          </>
        }
        actions={
          CANCELLABLE.has(run.status) ? (
            <Button
              size="sm"
              variant="outline"
              disabled={cancelling}
              onClick={() => void handleCancel()}
              className="px-2 sm:px-2.5"
              aria-label={t(($) => $.runs.detail.cancel)}
            >
              <Ban className="size-3.5 sm:mr-1" aria-hidden="true" />
              <span className="hidden sm:inline">
                {cancelling
                  ? t(($) => $.runs.detail.cancelling)
                  : t(($) => $.runs.detail.cancel)}
              </span>
            </Button>
          ) : null
        }
      />

      <div className="flex-1 overflow-y-auto">
        <div className="mx-auto flex max-w-4xl flex-col gap-6 p-6">
          {/* Terminal reason first — see the header comment. */}
          {run.blocked_reason ? (
            <TerminalReason
              tone="warning"
              title={t(($) => $.runs.detail.blocked_title)}
              reason={run.blocked_reason}
              detail={run.failure_detail}
              explain={explainReason}
            />
          ) : null}
          {run.failure_reason ? (
            <TerminalReason
              tone="destructive"
              title={
                run.status === "cancelled"
                  ? t(($) => $.runs.detail.cancelled_title)
                  : t(($) => $.runs.detail.failed_title)
              }
              reason={run.failure_reason}
              detail={run.failure_detail}
              explain={explainReason}
            />
          ) : null}

          {acceptance && acceptance.status === "pending" ? (
            <WorkflowAcceptancePanel
              runId={runId}
              acceptance={acceptance}
              evidenceStep={evidenceStep}
            />
          ) : acceptance ? (
            <WorkflowAcceptanceDecided acceptance={acceptance} />
          ) : null}

          <section className="flex flex-col gap-3">
            <h2 className="text-body font-medium tracking-wider text-muted-foreground uppercase">
              {t(($) => $.runs.detail.section_input)}
            </h2>
            {run.source_event_id ? (
              <div className="flex min-w-0 items-baseline gap-2 text-caption">
                <span className="shrink-0 text-muted-foreground">
                  {t(($) => $.runs.detail.source_event)}
                </span>
                <code className="min-w-0 break-all text-foreground">
                  {run.source_event_id}
                </code>
              </div>
            ) : null}
            {inputTitle === "" && inputDescription === "" ? (
              <p className="text-caption text-muted-foreground">
                {t(($) => $.runs.detail.input_empty)}
              </p>
            ) : (
              <div className="flex flex-col gap-3 rounded-lg border p-4 text-body">
                {inputTitle ? (
                  <div>
                    <span className="text-caption text-muted-foreground">
                      {t(($) => $.runs.detail.input_title)}
                    </span>
                    <p className="mt-0.5">{inputTitle}</p>
                  </div>
                ) : null}
                {inputDescription ? (
                  <div>
                    <span className="text-caption text-muted-foreground">
                      {t(($) => $.runs.detail.input_description)}
                    </span>
                    {/* `whitespace-pre-wrap`, not a markdown renderer: this is
                        the literal text the agents were given, and rendering it
                        richer here than they saw it would misrepresent the
                        prompt. */}
                    <p className="mt-0.5 leading-relaxed whitespace-pre-wrap">
                      {inputDescription}
                    </p>
                  </div>
                ) : null}
              </div>
            )}
            {run.issue_id ? (
              <AppLink
                href={wsPaths.issueDetail(run.issue_id)}
                className="self-start text-caption text-muted-foreground underline decoration-muted-foreground/30 underline-offset-4 transition-colors hover:text-foreground"
              >
                {t(($) => $.runs.detail.issue_link)}
              </AppLink>
            ) : null}
          </section>

          <section className="flex flex-col gap-3">
            <div className="flex items-baseline justify-between gap-3">
              <h2 className="text-body font-medium tracking-wider text-muted-foreground uppercase">
                {t(($) => $.runs.detail.section_trace)}
              </h2>
              <div className="flex shrink-0 flex-col items-end gap-0.5 text-caption text-muted-foreground">
                {run.started_at ? (
                  <span className="tabular-nums">
                    {t(($) => $.runs.detail.started_at, {
                      date: formatInTimeZone(
                        run.started_at,
                        undefined,
                        i18n.language,
                      ),
                    })}
                  </span>
                ) : null}
                {run.completed_at ? (
                  <span className="tabular-nums">
                    {t(($) => $.runs.detail.completed_at, {
                      date: formatInTimeZone(
                        run.completed_at,
                        undefined,
                        i18n.language,
                      ),
                    })}
                  </span>
                ) : null}
                {elapsed === null ? null : (
                  <span className="tabular-nums">
                    {run.completed_at
                      ? formatDuration(elapsed, "<1m")
                      : t(($) => $.runs.page.duration_running, {
                          duration: formatDuration(elapsed, "<1m"),
                        })}
                  </span>
                )}
              </div>
            </div>

            {run.steps.length === 0 ? (
              // The two empty traces are NOT the same claim, and conflating them
              // is how "we lost the trace" reads as "nothing has happened".
              // A non-zero step_count with an empty array is the tell that the
              // schema's per-step degradation fired.
              <p className="text-caption text-muted-foreground">
                {run.step_count > 0
                  ? t(($) => $.runs.detail.trace_lost, {
                      count: run.step_count,
                    })
                  : t(($) => $.runs.detail.trace_empty)}
              </p>
            ) : (
              <ol className="flex flex-col gap-2">
                {run.steps.map((step) => (
                  <li key={step.id}>
                    <StepRow step={step} runStatus={run.status} />
                  </li>
                ))}
              </ol>
            )}
          </section>
        </div>
      </div>
    </div>
  );
}

/**
 * Locate the submission the acceptance gate is judging.
 *
 * The acceptance row points at its OWN step (the gate), which carries no
 * submission of its own - an acceptance node produces a decision, not a
 * verdict. The evidence is the last agent step to finish before it, which in
 * server order is the last step preceding the gate that has a submission. Falls
 * back to the gate's own step so a graph that does attach one still shows it.
 */
function upstreamEvidence(
  steps: WorkflowStep[],
  gateStepId: string,
): WorkflowStep | undefined {
  const gateIndex = steps.findIndex((step) => step.id === gateStepId);
  if (gateIndex === -1) return undefined;
  const gate = steps[gateIndex];
  if (gate?.submission) return gate;
  for (let i = gateIndex - 1; i >= 0; i -= 1) {
    const candidate = steps[i];
    if (candidate?.submission) return candidate;
  }
  return undefined;
}

function TerminalReason({
  tone,
  title,
  reason,
  detail,
  explain,
}: {
  tone: "warning" | "destructive";
  title: string;
  reason: string;
  detail?: string | null;
  explain: (reason: string) => string;
}) {
  return (
    <section
      role="alert"
      className={
        tone === "destructive"
          ? "flex items-start gap-2.5 rounded-lg border border-destructive/40 bg-destructive/5 p-4"
          : "flex items-start gap-2.5 rounded-lg border border-amber-500/40 bg-amber-500/5 p-4"
      }
    >
      {tone === "destructive" ? (
        <XCircle
          className="mt-0.5 size-4 shrink-0 text-destructive"
          aria-hidden="true"
        />
      ) : (
        <AlertCircle
          className="mt-0.5 size-4 shrink-0 text-amber-600 dark:text-amber-500"
          aria-hidden="true"
        />
      )}
      <div className="min-w-0 flex-1">
        <p className="text-body font-medium">{title}</p>
        <p className="mt-1 text-caption leading-relaxed">{explain(reason)}</p>
        {/* The server's own diagnosis, verbatim and untranslated. The sentence
            above says what CLASS of problem this was; only this says which agent
            to fix - for routing_no_candidate it is the per-candidate refusal list
            ("Ada: runtime offline; Bob: not permitted"), which is the difference
            between an actionable banner and "something went wrong somewhere". */}
        {detail ? (
          <p className="mt-1 whitespace-pre-wrap text-caption leading-relaxed text-muted-foreground">
            {detail}
          </p>
        ) : null}
        {/* The raw identifier stays visible under the sentence. It is what an
            operator greps a server log for and what a bug report should quote;
            the sentence is for the reader, the token is for the diagnosis. */}
        <p className="mt-1 font-mono text-micro text-muted-foreground">
          {reason}
        </p>
      </div>
    </section>
  );
}

/**
 * One attempt at one node.
 *
 * The routing reason is rendered verbatim and unconditionally when present: it
 * is the *only* record of why this agent got this step, and a run that quietly
 * picked the wrong specialist has to be diagnosable after the fact. Translating
 * it is not possible - the router composes it server-side from the strategy it
 * used - and hiding it behind a disclosure would mean the one question an
 * operator asks of a surprising trace needs a click to answer.
 */
function StepRow({
  step,
  runStatus,
}: {
  step: WorkflowStep;
  runStatus: string;
}) {
  const { t } = useT("workflows");
  const explainReason = useExplainReason();

  // TranscriptButton needs an AgentTask; the trace carries only the task id, so
  // a minimal synthetic one is built here - the same approach the autopilot run
  // history takes. Only `id` is load-bearing (it keys the message fetch and the
  // live cache); the status drives the dialog's live/terminal presentation.
  const task: AgentTask | null = step.task_id
    ? {
        id: step.task_id,
        agent_id: step.agent_id ?? "",
        runtime_id: "",
        issue_id: "",
        status:
          step.status === "running"
            ? "running"
            : step.status === "passed" || step.status === "submitted"
              ? "completed"
              : step.status === "failed" || step.status === "blocked"
                ? "failed"
                : step.status === "cancelled"
                  ? "cancelled"
                  : "queued",
        priority: 0,
        dispatched_at: null,
        started_at: step.started_at,
        completed_at: step.completed_at,
        result: null,
        error: step.failure_reason,
        created_at: step.started_at ?? "",
      }
    : null;

  // Live only while BOTH the step and the run are moving. A step left `running`
  // on a cancelled run is a stale row, and telling the transcript dialog it is
  // live would leave it waiting on a WS stream that will never emit again.
  const isLive =
    step.status === "running" &&
    (runStatus === "running" || runStatus === "waiting_acceptance");

  const submission = step.submission;
  const summary = submission ? artifactSummary(submission.artifact) : "";
  const references = submission ? artifactReferences(submission.artifact) : [];
  const type = submission ? artifactType(submission.artifact) : "";

  return (
    <div className="rounded-lg border p-3">
      <div className="flex flex-wrap items-center gap-2">
        <span className="font-mono text-caption font-medium">{step.node_key}</span>
        <Badge variant="outline">{step.node_type}</Badge>
        <WorkflowStepStatusBadge status={step.status} />
        {/* Attempt 1 is unremarkable; a second attempt is the whole story of a
            rework round, so only those are called out. */}
        {step.attempt > 1 ? (
          <span className="text-caption text-muted-foreground">
            {t(($) => $.runs.detail.attempt, { attempt: step.attempt })}
          </span>
        ) : null}
        <span className="flex-1" />
        {step.agent_name ? (
          <span className="min-w-0 truncate text-caption text-muted-foreground">
            {step.agent_name}
          </span>
        ) : step.node_type === "agent" ? (
          <span className="text-caption text-muted-foreground">
            {t(($) => $.runs.detail.no_agent)}
          </span>
        ) : null}
        {task ? (
          <TranscriptButton
            task={task}
            agentName={step.agent_name ?? ""}
            isLive={isLive}
            title={t(($) => $.runs.detail.view_log)}
          />
        ) : null}
      </div>

      {step.routing_reason ? (
        <p className="mt-1.5 text-caption leading-snug text-muted-foreground">
          {t(($) => $.runs.detail.routing_reason, {
            reason: step.routing_reason,
          })}
        </p>
      ) : null}

      {step.failure_reason ? (
        <p className="mt-1.5 text-caption leading-snug text-destructive">
          {explainReason(step.failure_reason)}
        </p>
      ) : null}
      {/* The server's diagnosis for THIS step, verbatim. On a step blocked with
          routing_no_candidate this is the per-candidate refusal list, which is
          the only place that names which agent needs attention. */}
      {step.failure_detail ? (
        <p className="mt-0.5 whitespace-pre-wrap text-caption leading-snug text-muted-foreground">
          {step.failure_detail}
        </p>
      ) : null}

      {submission ? (
        <div className="mt-2.5 flex flex-col gap-1.5 border-t pt-2.5">
          <div className="flex flex-wrap items-center gap-2">
            <WorkflowVerdictBadge verdict={submission.verdict} />
            {type ? (
              <span className="font-mono text-micro text-muted-foreground">
                {type}
              </span>
            ) : null}
            {submission.confidence === null ? null : (
              <span className="text-caption text-muted-foreground">
                {t(($) => $.runs.detail.submission_confidence, {
                  value: submission.confidence,
                })}
              </span>
            )}
          </div>
          {summary ? (
            <div>
              <span className="text-caption text-muted-foreground">
                {t(($) => $.runs.detail.submission_artifact)}
              </span>
              <p className="text-caption leading-snug whitespace-pre-wrap">
                {summary}
              </p>
            </div>
          ) : null}
          {submission.rationale ? (
            <div>
              <span className="text-caption text-muted-foreground">
                {t(($) => $.runs.detail.submission_rationale)}
              </span>
              <p className="text-caption leading-snug whitespace-pre-wrap">
                {submission.rationale}
              </p>
            </div>
          ) : null}
          {submission.root_cause ? (
            <div>
              <span className="text-caption text-muted-foreground">
                {t(($) => $.runs.detail.submission_root_cause)}
              </span>
              <p className="text-caption leading-snug whitespace-pre-wrap">
                {submission.root_cause}
              </p>
            </div>
          ) : null}
          {references.length > 0 ? (
            <ul className="flex flex-col gap-0.5">
              {references.map((ref, index) => (
                // Index key: references are a positional list of plain strings.
                <li
                  key={index}
                  className="font-mono text-micro break-words text-muted-foreground"
                >
                  {ref}
                </li>
              ))}
            </ul>
          ) : null}
          {/* Only when the node contract added keys of its own — an empty `{}`
              block would be noise, but a key this build has never heard of is
              exactly what an operator diagnosing a new node type needs. */}
          {artifactHasExtraKeys(submission.artifact) ? (
            <pre className="overflow-x-auto rounded-md bg-muted/50 p-2 font-mono text-micro leading-snug">
              {JSON.stringify(submission.artifact, null, 2)}
            </pre>
          ) : null}
          {submission.validation_errors &&
          submission.validation_errors.length > 0 ? (
            <div>
              <p className="text-caption font-medium text-destructive">
                {t(($) => $.runs.detail.submission_validation_errors)}
              </p>
              <ul className="mt-0.5 flex flex-col gap-0.5">
                {submission.validation_errors.map((message, index) => (
                  // Rendered verbatim and untranslated, matching the graph
                  // validator: these are the server's own strings and the only
                  // actionable part of a rejected submission.
                  <li
                    key={index}
                    className="font-mono text-micro leading-snug break-words text-muted-foreground"
                  >
                    {message}
                  </li>
                ))}
              </ul>
            </div>
          ) : null}
        </div>
      ) : null}
    </div>
  );
}

/** Mirrors the page's frame so the trace does not jump in beneath the header. */
function RunSkeleton() {
  return (
    <div className="flex h-full flex-col">
      <div className="flex shrink-0 items-center gap-2 border-b px-5 py-2.5">
        <Skeleton className="h-3.5 w-16" />
        <Skeleton className="h-4 w-48" />
        <Skeleton className="h-5 w-20 rounded-full" />
      </div>
      <div className="flex-1 overflow-hidden">
        <div className="mx-auto flex max-w-4xl flex-col gap-6 p-6">
          <div className="flex flex-col gap-3">
            <Skeleton className="h-3 w-16" />
            <Skeleton className="h-24 w-full rounded-lg" />
          </div>
          <div className="flex flex-col gap-3">
            <Skeleton className="h-3 w-16" />
            {Array.from({ length: 3 }).map((_, i) => (
              <Skeleton key={i} className="h-20 w-full rounded-lg" />
            ))}
          </div>
        </div>
      </div>
    </div>
  );
}
