"use client";

import { Badge } from "@multica/ui/components/ui/badge";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../../../i18n";

/**
 * Status badges for a Run and for one Step of a Run.
 *
 * Both are deliberately lenient in the same way `WorkflowStatusBadge` is: the
 * engine owns these state machines and can add a state in any server release
 * while an installed desktop build keeps running, so an unrecognized value
 * renders as its raw string in the neutral tone rather than as a blank cell.
 *
 * The two vocabularies are kept apart even though they overlap (`blocked`,
 * `failed`, `cancelled`, `waiting_acceptance` appear in both). A Run's
 * `running` is not a Step's `running`, and `passed` / `submitted` / `skipped`
 * exist only on a Step - folding them into one map would let a Run render a
 * status it can never hold and hide the difference from a reader scanning a
 * trace next to a header.
 */

const RUN_STATUSES = [
  "pending",
  "running",
  "waiting_acceptance",
  "blocked",
  "completed",
  "failed",
  "cancelled",
] as const;
type KnownRunStatus = (typeof RUN_STATUSES)[number];

const STEP_STATUSES = [
  "pending",
  "ready",
  "queued",
  "running",
  "submitted",
  "passed",
  "failed",
  "blocked",
  "waiting_acceptance",
  "skipped",
  "cancelled",
] as const;
type KnownStepStatus = (typeof STEP_STATUSES)[number];

type BadgeVariant = "default" | "outline" | "secondary" | "destructive";

// Tone rules, in one place so a Run header and a Step row cannot disagree:
//   - a state that needs a person (`waiting_acceptance`, `blocked`) is the only
//     one that gets emphasis, because it is the only one where nothing will
//     happen until someone acts;
//   - `failed` is destructive; `cancelled` / `skipped` are muted, since neither
//     is a defect and colouring them red would inflate a reader's sense of how
//     often runs break;
//   - everything in flight is `outline` - provisional, not an outcome.
const RUN_VARIANT: Record<KnownRunStatus, BadgeVariant> = {
  pending: "outline",
  running: "outline",
  waiting_acceptance: "default",
  blocked: "default",
  completed: "secondary",
  failed: "destructive",
  cancelled: "secondary",
};

const STEP_VARIANT: Record<KnownStepStatus, BadgeVariant> = {
  pending: "outline",
  ready: "outline",
  queued: "outline",
  running: "outline",
  submitted: "outline",
  passed: "secondary",
  failed: "destructive",
  blocked: "default",
  waiting_acceptance: "default",
  skipped: "secondary",
  cancelled: "secondary",
};

// `completed` and `passed` are the states a reader most wants to pick out of a
// long trace, and `secondary` alone does not carry that. The colour rides in a
// className rather than a badge variant because it is a *semantic* success tone
// this design system does not otherwise have.
const RUN_ACCENT: Partial<Record<KnownRunStatus, string>> = {
  completed: "text-emerald-700 dark:text-emerald-400",
  running: "text-blue-700 dark:text-blue-400",
};

const STEP_ACCENT: Partial<Record<KnownStepStatus, string>> = {
  passed: "text-emerald-700 dark:text-emerald-400",
  running: "text-blue-700 dark:text-blue-400",
};

function known<T extends string>(
  values: readonly T[],
  candidate: string,
): T | null {
  return (values as readonly string[]).includes(candidate)
    ? (candidate as T)
    : null;
}

export function WorkflowRunStatusBadge({
  status,
  className,
}: {
  status: string;
  className?: string;
}) {
  const { t } = useT("workflows");
  const value = known(RUN_STATUSES, status);
  return (
    <Badge
      variant={value ? RUN_VARIANT[value] : "outline"}
      className={cn(value ? RUN_ACCENT[value] : undefined, className)}
    >
      {value ? t(($) => $.runs.status[value]) : status}
    </Badge>
  );
}

export function WorkflowStepStatusBadge({
  status,
  className,
}: {
  status: string;
  className?: string;
}) {
  const { t } = useT("workflows");
  const value = known(STEP_STATUSES, status);
  return (
    <Badge
      variant={value ? STEP_VARIANT[value] : "outline"}
      className={cn(value ? STEP_ACCENT[value] : undefined, className)}
    >
      {value ? t(($) => $.runs.step_status[value]) : status}
    </Badge>
  );
}

/**
 * Verdict pill for a step's submission.
 *
 * An empty verdict renders as "no verdict" and never as a pass. That mirrors
 * the server's own contract - prose never implies a pass - and the schema's
 * refusal to default the field, so a contract drift cannot make a client show
 * a green pass for work nobody approved.
 */
export function WorkflowVerdictBadge({ verdict }: { verdict: string }) {
  const { t } = useT("workflows");
  if (verdict === "pass") {
    return (
      <Badge
        variant="secondary"
        className="text-emerald-700 dark:text-emerald-400"
      >
        {t(($) => $.runs.verdict.pass)}
      </Badge>
    );
  }
  if (verdict === "fail") {
    return <Badge variant="destructive">{t(($) => $.runs.verdict.fail)}</Badge>;
  }
  if (verdict === "blocked") {
    return <Badge variant="default">{t(($) => $.runs.verdict.blocked)}</Badge>;
  }
  // Includes `""`. An unknown verdict from a newer server shows its own token
  // so an operator can at least search for it; only the empty case is named.
  return (
    <Badge variant="outline">
      {verdict || t(($) => $.runs.verdict.unknown)}
    </Badge>
  );
}
