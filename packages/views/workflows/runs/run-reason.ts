"use client";

import { useT } from "../../i18n";

/**
 * Server-side classification identifiers, mirroring the `Reason*` constants in
 * `server/internal/workflow/errors.go`.
 *
 * These are *identifiers*, not prose: the engine writes `routing_no_candidate`
 * into `blocked_reason`, and showing that token to the person who filed the
 * work is a non-answer - they cannot tell whether they should wait, edit the
 * graph, or bring an agent online. So this module maps each one to a sentence
 * that says what happened and what it implies.
 *
 * An unrecognized reason falls through to the raw token rather than to a
 * generic "something went wrong". A newer server's classification is still a
 * searchable, reportable string, whereas a swallowed one leaves an operator
 * with a stopped run and nothing to go on. This is the same rule the graph
 * validator's messages follow: never invent a reason, never hide one.
 */
const KNOWN_REASONS = [
  "routing_no_candidate",
  "submission_contract_invalid",
  "workflow_invariant_violation",
  "attempt_limit_exceeded",
  "rework_limit_exceeded",
  "step_limit_exceeded",
  "duration_limit_exceeded",
  "cost_limit_exceeded",
  "agent_task_failed",
  "agent_verdict_fail",
  "agent_verdict_blocked",
  "acceptance_rejected",
  "activation_timeout",
  "runtime_offline",
  "cancelled",
] as const;

type KnownReason = (typeof KNOWN_REASONS)[number];

function asKnownReason(reason: string): KnownReason | null {
  return (KNOWN_REASONS as readonly string[]).includes(reason)
    ? (reason as KnownReason)
    : null;
}

/** Returns a localized explanation for an engine reason identifier. */
export function useExplainReason(): (reason: string) => string {
  const { t } = useT("workflows");
  return (reason: string) => {
    const known = asKnownReason(reason);
    if (known) return t(($) => $.runs.reason[known]);
    return t(($) => $.runs.reason.unknown, { reason });
  };
}

/**
 * Elapsed wall-clock time for a run, in seconds, or null when it cannot be
 * computed.
 *
 * A run with no `started_at` has not been routed yet, which is a real state
 * (`pending`, or `blocked` before the first step activated) and must read as
 * "no duration" rather than as zero - zero would claim the run finished
 * instantly. A run with `started_at` and no `completed_at` is still going, so
 * it is measured against now; the caller labels that differently so a reader
 * can tell a final duration from a running total.
 *
 * Unparseable timestamps return null for the same reason: a NaN duration
 * rendered through a formatter becomes a confident-looking wrong number.
 */
export function runElapsedSeconds(
  startedAt: string | null,
  completedAt: string | null,
  now: number = Date.now(),
): number | null {
  if (!startedAt) return null;
  const start = new Date(startedAt).getTime();
  if (Number.isNaN(start)) return null;
  const end = completedAt ? new Date(completedAt).getTime() : now;
  if (Number.isNaN(end)) return null;
  const seconds = (end - start) / 1000;
  // A negative span means the two stamps disagree (clock skew between the
  // engine's writes). Reporting "-3s" would be worse than admitting we cannot
  // measure it.
  return seconds < 0 ? null : seconds;
}
