/**
 * Per-node-type colour + icon, in one table.
 *
 * The accent is not decoration: a workflow graph is read by scanning for the
 * *kind* of a step before reading its name, and the seven kinds behave completely
 * differently (an input step is the form a human fills in before anything runs,
 * an agent step runs work, a condition only routes, an acceptance step is the one
 * that can send work backwards, an end step terminates the run). Giving each kind
 * a fixed hue means the badge, the card's left border and the matching "+" pill in
 * the add-node toolbar all agree, so a card can be classified without reading a
 * word of it.
 *
 * Keyed on {@link EditorNodeType} rather than on the raw server string so a node
 * kind this build has never heard of resolves to the `unknown` row instead of
 * falling through to a blank card. See graph/types.ts on why `unknown` exists at
 * all.
 *
 * Colours are Tailwind palette classes with an explicit dark variant, matching
 * the convention in issues/components/pull-request-list.tsx: semantic tokens
 * (`--color-primary` and friends) carry only one accent, and seven node kinds need
 * seven distinguishable hues. Everything that is *not* the accent - card surface,
 * border, text, ring - uses the semantic tokens so the card follows the theme.
 */

import {
  Bot,
  CircleCheckBig,
  Flag,
  GitBranch,
  Inbox,
  Merge,
  Split,
  Workflow,
  type LucideIcon,
} from "lucide-react";
import { UNKNOWN_NODE_TYPE, type EditorNodeType } from "../graph";

export type NodeAccent = {
  /** Type badge: tinted fill + accent text. */
  badge: string;
  /** The card's 4px left border, so the accent survives at minimap zoom. */
  border: string;
  /**
   * The minimap rectangle. A `fill-*` utility rather than `bg-*` because the
   * minimap renders SVG `<rect>` elements - `bg-*` would apply to nothing and
   * every kind would come out the library's default grey, which is the one thing
   * that would make the minimap useless: at thumbnail scale the hue is the only
   * remaining signal about what a node is.
   */
  minimap: string;
  /** Icon shown in the badge. */
  icon: LucideIcon;
};

export const NODE_ACCENT: Record<EditorNodeType, NodeAccent> = {
  // Rose, and deliberately not a shade of any of the six below: an input node is
  // the only kind whose content comes from a *person* rather than from the engine,
  // so it should read as belonging to a different family at a glance. The Inbox
  // icon says the same thing - work arriving from outside - which is exactly the
  // question ("where does the bug report go in?") this node exists to answer on
  // the canvas.
  input: {
    badge: "bg-rose-500/10 text-rose-600 dark:text-rose-400",
    border: "border-l-rose-500",
    minimap: "fill-rose-500",
    icon: Inbox,
  },
  agent: {
    badge: "bg-indigo-500/10 text-indigo-600 dark:text-indigo-400",
    border: "border-l-indigo-500",
    minimap: "fill-indigo-500",
    icon: Bot,
  },
  condition: {
    badge: "bg-purple-500/10 text-purple-600 dark:text-purple-400",
    border: "border-l-purple-500",
    minimap: "fill-purple-500",
    icon: GitBranch,
  },
  acceptance: {
    badge: "bg-emerald-500/10 text-emerald-600 dark:text-emerald-400",
    border: "border-l-emerald-500",
    minimap: "fill-emerald-500",
    icon: CircleCheckBig,
  },
  fan_out: {
    badge: "bg-sky-500/10 text-sky-600 dark:text-sky-400",
    border: "border-l-sky-500",
    minimap: "fill-sky-500",
    icon: Split,
  },
  join: {
    badge: "bg-amber-500/10 text-amber-600 dark:text-amber-400",
    border: "border-l-amber-500",
    minimap: "fill-amber-500",
    icon: Merge,
  },
  end: {
    badge: "bg-slate-500/10 text-slate-600 dark:text-slate-400",
    border: "border-l-slate-400",
    minimap: "fill-slate-400",
    icon: Flag,
  },
  // A kind from a newer server gets the neutral tokens rather than a borrowed
  // hue: inventing an accent would assert a family membership this build cannot
  // actually verify.
  [UNKNOWN_NODE_TYPE]: {
    badge: "bg-muted text-muted-foreground",
    border: "border-l-border",
    minimap: "fill-muted-foreground",
    icon: Workflow,
  },
};
