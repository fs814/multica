"use client";

import { Checkbox } from "@multica/ui/components/ui/checkbox";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@multica/ui/components/ui/select";
import { cn } from "@multica/ui/lib/utils";

/**
 * The small controls the properties panel is built from.
 *
 * They exist as a separate file for one reason: the panel edits roughly twenty
 * fields across six node types, and the difference between a *field* and a
 * *validator requirement* has to be visible at a glance in that file. Inlining
 * the Select/Trigger/Content/Item ceremony twelve times would bury the rules
 * these controls exist to surface (which node types need rework targets, which
 * routing field the chosen strategy makes mandatory) under markup.
 *
 * Every control here takes `disabled` explicitly rather than reading a context.
 * The panel's `readOnly` comes from whether the *server* will accept a PATCH at
 * all (a built-in or published template answers 409), so it is a property of the
 * template rather than of the control tree, and threading it means a new control
 * cannot silently forget it.
 */

/** One choice in a {@link PanelSelect}. `value` is the stored token, not a label. */
export type PanelOption = {
  /** The exact string written into the definition. `""` is a legal token: the
   *  engine reads an empty `on_failure` / `when_verdict` as its own default. */
  value: string;
  label: string;
  /** Renders the option unselectable - used for "at most one default branch". */
  disabled?: boolean;
};

/** Section heading + body. Uppercase small caps, matching the screenshot. */
export function PanelSection({
  title,
  children,
}: {
  title: string;
  children: React.ReactNode;
}) {
  return (
    <section className="flex flex-col gap-3 border-b border-border/60 px-4 py-4 last:border-b-0">
      <h3 className="text-caption font-semibold tracking-wide text-muted-foreground uppercase">
        {title}
      </h3>
      {children}
    </section>
  );
}

/**
 * A labelled text/number/multiline field.
 *
 * Rendered as a `<label>` wrapping its control so the association is implicit:
 * an explicit `htmlFor` would need a generated id per field, and a hand-written
 * one would eventually collide with a second panel mounted for a comparison
 * view. Only valid for labelable controls - a {@link PanelSelect} renders a
 * button, so it carries an `aria-label` instead (see {@link PanelSelectField}).
 */
export function PanelField({
  label,
  hint,
  problem,
  children,
}: {
  label: string;
  /** Explanatory text. Muted - it describes, it does not warn. */
  hint?: string;
  /** A rule this field currently violates. Rendered in the destructive colour. */
  problem?: string;
  children: React.ReactNode;
}) {
  return (
    <label className="flex flex-col items-stretch gap-1.5">
      <PanelLabelText>{label}</PanelLabelText>
      {children}
      {hint ? <PanelHint>{hint}</PanelHint> : null}
      {problem ? <PanelProblem>{problem}</PanelProblem> : null}
    </label>
  );
}

/** Same layout as {@link PanelField}, for controls that are not labelable. */
export function PanelSelectField({
  label,
  hint,
  problem,
  children,
}: {
  label: string;
  hint?: string;
  problem?: string;
  children: React.ReactNode;
}) {
  return (
    <div className="flex flex-col items-stretch gap-1.5">
      <PanelLabelText>{label}</PanelLabelText>
      {children}
      {hint ? <PanelHint>{hint}</PanelHint> : null}
      {problem ? <PanelProblem>{problem}</PanelProblem> : null}
    </div>
  );
}

export function PanelLabelText({ children }: { children: React.ReactNode }) {
  return <span className="text-caption font-medium">{children}</span>;
}

export function PanelHint({ children }: { children: React.ReactNode }) {
  return (
    <span className="text-caption leading-snug text-muted-foreground">
      {children}
    </span>
  );
}

/**
 * A rule the current node breaks.
 *
 * Deliberately not `role="alert"`: these are steady-state descriptions of an
 * unfinished graph (an acceptance node the author has not given rework targets
 * yet), not events. Announcing every one on every keystroke would make the
 * panel unusable with a screen reader, and the authoritative report is the
 * validate button's problem list.
 */
export function PanelProblem({ children }: { children: React.ReactNode }) {
  return (
    <span className="text-caption leading-snug text-destructive">{children}</span>
  );
}

/**
 * Single-choice select over string tokens.
 *
 * `onChange` fires with `""` when the "not set" option is chosen, so callers
 * must never test the incoming value for truthiness - clearing a routing agent
 * or a branch verdict is a legitimate edit, and `""` is what the engine stores
 * for "use the default". Only a `null` (Base UI's "selection cleared without a
 * value") is ignored, because it does not correspond to any option.
 */
export function PanelSelect({
  value,
  options,
  onChange,
  disabled,
  ariaLabel,
  className,
}: {
  value: string;
  options: readonly PanelOption[];
  onChange(next: string): void;
  disabled: boolean;
  ariaLabel: string;
  className?: string;
}) {
  return (
    <Select<string>
      items={options}
      value={value}
      disabled={disabled}
      onValueChange={(next) => {
        if (next === null) return;
        onChange(next);
      }}
    >
      <SelectTrigger
        size="sm"
        aria-label={ariaLabel}
        className={cn("w-full min-w-0", className)}
      >
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        {options.map((option) => (
          <SelectItem
            key={option.value}
            value={option.value}
            disabled={option.disabled}
          >
            {option.label}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  );
}

/**
 * Multi-select over node keys, for `rework_targets` and `join_sources`.
 *
 * A checkbox list rather than a popover picker because both fields are sets of
 * *node keys*: the candidate list is the graph itself (never more than a screen
 * of items), and the author is checking their answer against the canvas beside
 * them. A popover would hide the current selection behind a click at exactly the
 * moment the validator is complaining that the selection is empty.
 */
export function PanelCheckList({
  options,
  selected,
  onToggle,
  disabled,
  emptyLabel,
  ariaLabel,
}: {
  options: readonly PanelOption[];
  selected: readonly string[];
  onToggle(value: string, checked: boolean): void;
  disabled: boolean;
  /** Shown when the graph offers no candidates at all - see the note above. */
  emptyLabel: string;
  ariaLabel: string;
}) {
  if (options.length === 0) return <PanelHint>{emptyLabel}</PanelHint>;

  const selectedSet = new Set(selected);
  return (
    <div
      role="group"
      aria-label={ariaLabel}
      className="flex flex-col gap-1.5 rounded-lg border border-input p-2"
    >
      {options.map((option) => (
        <label
          key={option.value}
          className={cn(
            "flex items-center gap-2 text-caption",
            disabled ? "opacity-50" : "cursor-pointer",
          )}
        >
          <Checkbox
            checked={selectedSet.has(option.value)}
            disabled={disabled}
            onCheckedChange={(checked) =>
              onToggle(option.value, checked === true)
            }
          />
          <span className="min-w-0 truncate">{option.label}</span>
        </label>
      ))}
    </div>
  );
}

/**
 * The panel's own empty / terminal states: no selection, an End node, a node
 * kind this build does not know.
 *
 * One component for all three because they are the same claim - "there is
 * nothing to edit here" - and the difference that matters is *why*. Rendering
 * collapsed sections instead would leave an author hunting for a control that
 * does not exist.
 */
export function PanelNotice({
  title,
  hint,
}: {
  title: string;
  hint: string;
}) {
  return (
    <div className="flex flex-col gap-1.5 px-4 py-6">
      <p className="text-body font-medium">{title}</p>
      <PanelHint>{hint}</PanelHint>
    </div>
  );
}
