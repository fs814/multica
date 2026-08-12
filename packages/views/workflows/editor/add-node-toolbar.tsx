"use client";

import {
  Bot,
  CircleCheckBig,
  Flag,
  GitBranch,
  Inbox,
  Plus,
  type LucideIcon,
} from "lucide-react";
import type { WorkflowDefinition } from "@multica/core/workflows";
import { Button } from "@multica/ui/components/ui/button";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../../i18n";
import type { WorkflowNodeType } from "../graph";

/**
 * The node kinds a human may add, in the screenshot's order.
 *
 * `fan_out` and `join` are deliberately absent even though the engine's node
 * vocabulary includes them and the validator accepts them. Their executors are
 * not implemented yet - a run reaching one passes straight through - so a node
 * added here would be a step the author designed, the server accepted, and the
 * run silently skipped. That is worse than the feature being missing, because
 * the graph *looks* correct. An existing graph that already declares them still
 * renders and still saves: the omission is only in what a human may newly
 * author, never in what the model can carry.
 *
 * `input` IS offered, and it leads the row because it is the step that comes
 * first in every graph that has one: it declares what the Run dialog collects, so
 * an author reading the canvas can see what the workflow takes.
 *
 * `agent` is labelled "Issue" because that is what an agent step operates on in
 * product language (see the screenshot); `onAdd` still emits `agent`, and the
 * label lives in i18n so the wording can change without touching the model.
 *
 * Accent colours mirror the canvas node badges so a pill and the node it creates
 * read as the same thing.
 */
const ADDABLE_NODES: {
  type: Extract<
    WorkflowNodeType,
    "input" | "agent" | "acceptance" | "condition" | "end"
  >;
  icon: LucideIcon;
  /** Matches the canvas badge accent for this node type. */
  accent: string;
}[] = [
  { type: "input", icon: Inbox, accent: "text-rose-500" },
  { type: "agent", icon: Bot, accent: "text-indigo-500" },
  { type: "acceptance", icon: CircleCheckBig, accent: "text-emerald-500" },
  { type: "condition", icon: GitBranch, accent: "text-purple-500" },
  { type: "end", icon: Flag, accent: "text-slate-500" },
];

export function AddNodeToolbar({
  readOnly,
  /**
   * The working graph. Read for exactly one thing - whether an input node already
   * exists - so the rule that at most one is legal lives next to the control that
   * would break it rather than in the page that mounts it.
   */
  definition,
  onAdd,
}: {
  readOnly: boolean;
  definition: WorkflowDefinition;
  onAdd(type: WorkflowNodeType): void;
}) {
  const { t } = useT("workflows");

  // A published or built-in template cannot take a draft edit (the server
  // answers PATCH with 409 for both), so the pills are hidden rather than
  // disabled - a row of dead buttons reads as a broken editor rather than
  // as a template nobody is allowed to edit.
  if (readOnly) return null;

  // The validator permits AT MOST ONE input node (a Run has one input bag and one
  // entry node, so a second could only be dead), so a second pill press would
  // author a graph the server refuses to publish. Disabled with the reason stated,
  // not hidden: a pill that vanishes once used looks like a rendering bug, and the
  // author would not learn that the limit is one.
  const hasInputNode = definition.nodes.some((node) => node.type === "input");
  const inputRefusal = hasInputNode
    ? t(($) => $.add_node.input_exists)
    : undefined;

  // Keyed on the addable types only, so a label can never exist for a node kind
  // the toolbar refuses to create.
  const labels: Record<(typeof ADDABLE_NODES)[number]["type"], string> = {
    input: t(($) => $.add_node.input),
    agent: t(($) => $.add_node.agent),
    acceptance: t(($) => $.add_node.acceptance),
    condition: t(($) => $.add_node.condition),
    end: t(($) => $.add_node.end),
  };

  return (
    <div
      className="flex flex-wrap items-center gap-1.5"
      role="group"
      aria-label={t(($) => $.add_node.group_aria)}
    >
      {ADDABLE_NODES.map(({ type, icon: Icon, accent }) => {
        const refusal = type === "input" ? inputRefusal : undefined;
        return (
          <Button
            key={type}
            type="button"
            variant="outline"
            size="sm"
            className="rounded-full"
            disabled={refusal !== undefined}
            title={refusal}
            onClick={() => onAdd(type)}
            aria-label={t(($) => $.add_node.add_aria, { type: labels[type] })}
          >
            <Plus className="size-3 text-muted-foreground" aria-hidden="true" />
            <Icon className={cn("size-3.5", accent)} aria-hidden="true" />
            {labels[type]}
          </Button>
        );
      })}
      {/* The reason is also rendered as text, not left to the `title` tooltip:
          a disabled button's tooltip is unreachable by keyboard and unread by a
          screen reader, and "why is this greyed out" is the only question the
          state raises. Mirrors how the page states its read-only reason in this
          same row. */}
      {inputRefusal ? (
        <span className="truncate text-xs text-muted-foreground">
          {inputRefusal}
        </span>
      ) : null}
    </div>
  );
}
