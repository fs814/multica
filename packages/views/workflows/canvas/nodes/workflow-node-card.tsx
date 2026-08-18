"use client";

/**
 * The card every workflow node type is drawn as.
 *
 * All seven node kinds are the same object on the canvas - a rounded surface with
 * a type badge, a key, a title and one line of the field that decides what the
 * step actually does - so they share one shell and differ only in accent and in
 * that last line. Seven independently written cards would drift on padding and
 * corner radius, and the graph would stop reading as a single diagram.
 *
 * The shell owns the invariants that are not per-type:
 *
 *  1. **Left target handle, right source handle.** Layout is strictly
 *     left-to-right (graph/layout.ts ranks by longest path), so a port on any
 *     other side would draw an edge that doubles back and read as a cycle.
 *     Condition nodes pass their own source handles because a branch needs one
 *     port per verdict, and an input node passes an empty target handle because
 *     nothing precedes the point where work enters the graph.
 *
 *  2. **Selection is a ring, not a border swap.** The left border carries the
 *     node's *type*; overloading it with selection would make a selected agent
 *     node indistinguishable from an unselected one of another kind. The ring is
 *     also what `readOnly` keeps working, since selecting a node to inspect it is
 *     reading, not editing.
 *
 *  3. **The entry node is marked.** Exactly one node starts the run, and
 *     `entry_node` is a graph-level field with no visual consequence anywhere
 *     else on the canvas - a graph whose entry is not the leftmost card (legal,
 *     and what a rework loop produces) would otherwise be unreadable.
 */

import { Handle, Position } from "@xyflow/react";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../../../i18n";
import { NODE_ACCENT } from "../node-accent";
import type { EditorNode } from "../../graph";

export function WorkflowNodeCard({
  data,
  selected,
  typeLabel,
  /** The routing / behaviour line. Absent for kinds that have nothing to say. */
  summary,
  /**
   * A multi-line block under the summary. Only the input node uses it, to list
   * the fields it collects: a single truncating summary line cannot answer "what
   * does this workflow ask for?", which is the whole reason that node is on the
   * canvas.
   */
  body,
  /**
   * Extra source ports, for condition nodes. When given, the shell's own single
   * right-hand source handle is omitted: two source handles where the model has
   * one branch would let a user attach an edge to a port that maps to nothing.
   */
  sourceHandles,
  /**
   * Replaces the shell's left target port. Pass `<></>` to remove it entirely,
   * which the input node does: it is the graph's entry, so an edge *into* it
   * would describe a step running before the run's input was collected.
   */
  targetHandle,
  className,
}: {
  data: EditorNode;
  selected: boolean;
  typeLabel: string;
  summary?: React.ReactNode;
  body?: React.ReactNode;
  sourceHandles?: React.ReactNode;
  targetHandle?: React.ReactNode;
  /** Extra classes on the card surface. Used only by the `unknown` renderer. */
  className?: string;
}) {
  const { t } = useT("workflows");
  const accent = NODE_ACCENT[data.type];
  const Icon = accent.icon;

  return (
    <div
      className={cn(
        // Fixed width so a layer of cards lines up: COL_GAP (320) is chosen
        // against this, and a content-sized card would make the gap between
        // columns vary with the length of a step's name.
        "relative w-60 rounded-lg border border-l-4 bg-card text-card-foreground shadow-sm transition-shadow",
        accent.border,
        selected
          ? "ring-2 ring-ring ring-offset-2 ring-offset-background"
          : "hover:shadow-md",
        className,
      )}
    >
      {targetHandle ?? <Handle type="target" position={Position.Left} />}

      <div className="flex flex-col gap-1.5 px-3 py-2.5">
        <div className="flex items-center gap-1.5">
          <span
            className={cn(
              "inline-flex shrink-0 items-center gap-1 rounded px-1.5 py-0.5 text-micro font-semibold uppercase tracking-wide",
              accent.badge,
            )}
          >
            <Icon className="size-3" aria-hidden="true" />
            {typeLabel}
          </span>
          {/* The key is right-aligned and muted because it is an identifier the
              author only needs when cross-referencing the properties panel or a
              validator message - not part of the sentence the card reads as. */}
          <code className="ml-auto min-w-0 truncate font-mono text-micro text-muted-foreground">
            {data.nodeKey}
          </code>
        </div>

        {data.isEntry ? (
          <span className="inline-flex w-fit items-center rounded border border-dashed px-1 py-px text-micro text-muted-foreground">
            {t(($) => $.canvas.entry_badge)}
          </span>
        ) : null}

        {/* Falls back to the key so a node the author has not named yet is still
            identifiable; `name` is optional in the wire schema. */}
        <div className="truncate text-body font-semibold leading-tight">
          {data.node.name || data.nodeKey}
        </div>

        {summary ? (
          <div className="flex min-w-0 items-center gap-1 text-micro text-muted-foreground">
            {summary}
          </div>
        ) : null}

        {body ?? null}
      </div>

      {sourceHandles ?? <Handle type="source" position={Position.Right} />}
    </div>
  );
}

/** One line of `summary`: a small icon plus a truncating label. */
export function WorkflowNodeSummary({
  icon: Icon,
  children,
}: {
  icon: React.ComponentType<{ className?: string; "aria-hidden"?: boolean }>;
  children: React.ReactNode;
}) {
  return (
    <>
      <Icon className="size-3 shrink-0" aria-hidden={true} />
      <span className="min-w-0 truncate">{children}</span>
    </>
  );
}
