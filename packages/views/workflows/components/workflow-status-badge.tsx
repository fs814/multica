"use client";

import { Badge } from "@multica/ui/components/ui/badge";
import { useT } from "../../i18n";

// Status is a server-driven string (schemas stay lenient — see
// packages/core/api/schema.ts and CLAUDE.md on contract drift), so the UI
// must render a value it has never heard of instead of crashing or showing
// a blank cell. Known values get a translated label and a tone; anything
// else falls through to the raw string in the neutral tone.
const KNOWN_STATUSES = ["draft", "published", "archived"] as const;
type KnownWorkflowStatus = (typeof KNOWN_STATUSES)[number];

function asKnownStatus(status: string): KnownWorkflowStatus | null {
  return (KNOWN_STATUSES as readonly string[]).includes(status)
    ? (status as KnownWorkflowStatus)
    : null;
}

// Published is the only state a Run can pin, so it is the only one that gets
// emphasis. Draft reads as provisional (outline) and archived as retired
// (muted), which keeps the list scannable when all three mix.
const STATUS_VARIANT: Record<
  KnownWorkflowStatus,
  "default" | "outline" | "secondary"
> = {
  draft: "outline",
  published: "default",
  archived: "secondary",
};

export function WorkflowStatusBadge({
  status,
  className,
}: {
  status: string;
  className?: string;
}) {
  const { t } = useT("workflows");
  const known = asKnownStatus(status);
  return (
    <Badge
      variant={known ? STATUS_VARIANT[known] : "outline"}
      className={className}
    >
      {known ? t(($) => $.status[known]) : status}
    </Badge>
  );
}
