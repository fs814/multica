"use client";
import type { WorkflowDebugLimits } from "@multica/core/workflows";
import { useT } from "../../i18n";

export function WorkflowDebugLimitsSummary({
  limits,
  retentionSeconds,
}: {
  limits: WorkflowDebugLimits;
  retentionSeconds: number;
}) {
  const { t } = useT("workflows");
  return (
    <div className="space-y-1 text-caption">
      <strong>{t(($) => $.detail.section_limits)}</strong>
      <dl className="grid grid-cols-2 gap-x-4 gap-y-1">
        {Object.entries(limits).map(([key, value]) => (
          <div key={key}>
            <dt className="inline">
              {t(($) => $.detail.limits[key as keyof WorkflowDebugLimits])}
              :{" "}
            </dt>
            <dd className="inline">
              {key === "max_duration_seconds"
                ? t(($) => $.detail.limits.duration_seconds, { seconds: value })
                : key === "max_cost_cents"
                  ? t(($) => $.detail.limits.cost_value, {
                      amount: value / 100,
                    })
                  : value}
            </dd>
          </div>
        ))}
        <div>
          <dt className="inline">{t(($) => $.debug.retention)}: </dt>
          <dd className="inline">{retentionSeconds}</dd>
        </div>
      </dl>
    </div>
  );
}
