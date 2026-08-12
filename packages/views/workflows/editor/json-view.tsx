"use client";

import { useMemo, useState } from "react";
import { CircleAlert, Check, RotateCcw } from "lucide-react";
import {
  WorkflowDefinitionSchema,
  type WorkflowDefinition,
} from "@multica/core/workflows";
import { Button } from "@multica/ui/components/ui/button";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { useT } from "../../i18n";

/**
 * The JSON escape hatch.
 *
 * The canvas models edges, entry and a node's headline fields; a definition
 * carries more than that (`submission_schema`, `acceptance_criteria`,
 * `join_sources`, `limits`, and whatever a newer server added - the wire schema
 * is `.loose()` on purpose). This panel is how an author edits the parts no
 * control has been built for yet, and how they paste a graph wholesale.
 *
 * Two rules follow from the fact that the text in this box is the user's work,
 * not a rendering of ours:
 *
 *  1. **A bad parse never costs the text.** A 200-line graph with one stray
 *     comma must stay on screen with the error beside it. So the textarea holds
 *     local draft state and `onApply` only fires on a clean parse - there is no
 *     path where a keystroke is swallowed.
 *  2. **An edit made elsewhere never silently overwrites the draft.** If the
 *     canvas changes while there are unapplied edits here, re-seeding the
 *     textarea would discard them; keeping the stale text and applying it later
 *     would silently revert the canvas. Neither is acceptable, so the panel says
 *     so and lets the author choose.
 */

/** Discriminated parse outcome, so a failure carries its message rather than throwing. */
type ParseResult =
  | { ok: true; definition: WorkflowDefinition }
  /** `syntax`: not JSON at all. `shape`: JSON, but not a workflow graph. */
  | { ok: false; kind: "syntax" | "shape"; message: string };

/**
 * Text -> `WorkflowDefinition`, the one place untyped input becomes the typed
 * model.
 *
 * The zod schema does the second half rather than a cast: `JSON.parse` is happy
 * to return `42` or a node array missing every `key`, and handing that to the
 * canvas as a `WorkflowDefinition` would move the failure somewhere unrelated
 * and much later. Parsing here also fills the defaults the server would have
 * filled (an absent `on_failure` means block, absent limits mean the server's),
 * so an author may paste the abbreviated graph they actually wrote.
 */
function parseDefinition(text: string): ParseResult {
  let value: unknown;
  try {
    value = JSON.parse(text);
  } catch (error) {
    return {
      ok: false,
      kind: "syntax",
      message: error instanceof Error ? error.message : String(error),
    };
  }
  const parsed = WorkflowDefinitionSchema.safeParse(value);
  if (!parsed.success) {
    // The first issue only: a single wrong node type cascades into a dozen
    // downstream complaints, and a wall of them buries the one that is real.
    const issue = parsed.error.issues[0];
    const path = issue?.path.join(".") ?? "";
    return {
      ok: false,
      kind: "shape",
      message: path ? `${path}: ${issue?.message ?? ""}` : (issue?.message ?? ""),
    };
  }
  return { ok: true, definition: parsed.data };
}

export function WorkflowJsonView({
  definition,
  readOnly,
  onApply,
}: {
  definition: WorkflowDefinition;
  readOnly: boolean;
  onApply(next: WorkflowDefinition): void;
}) {
  const { t } = useT("workflows");

  /** The canonical serialization of what the editor currently holds. */
  const seed = useMemo(() => JSON.stringify(definition, null, 2), [definition]);

  /**
   * The user's text, plus the seed it was based on. Both halves are needed to
   * tell "the author typed something" apart from "the graph moved underneath
   * them", and they must be one state object so the two can never disagree.
   */
  const [draft, setDraft] = useState<{ text: string; seed: string } | null>(
    null,
  );
  const [error, setError] = useState<{
    kind: "syntax" | "shape";
    message: string;
  } | null>(null);

  // Derived, not stored: a draft that matches its own seed is not an edit, so
  // the box keeps tracking the live definition. Typing and then undoing the
  // typing therefore returns to following the canvas, with no reset button.
  const untouched = draft === null || draft.text === draft.seed;
  const text = untouched ? seed : draft.text;
  // Unapplied edits, and meanwhile the definition changed elsewhere. Applying
  // now would revert that change, so the author is told before they can.
  const stale = !untouched && draft.seed !== seed;

  const handleChange = (value: string) => {
    setDraft({ text: value, seed });
    // A stale error outlives the typo that caused it and reads as if the fix
    // did not take. Clear on the next keystroke and let Apply re-decide.
    setError(null);
  };

  const handleApply = () => {
    const result = parseDefinition(text);
    if (!result.ok) {
      setError({ kind: result.kind, message: result.message });
      return;
    }
    setError(null);
    // Drop the draft so the textarea re-renders from the canonical
    // serialization: the applied graph is now the editor's graph, and leaving a
    // draft behind would make the freshly applied text look like a pending edit.
    setDraft(null);
    onApply(result.definition);
  };

  /** Abandon the draft and follow the live definition again. */
  const handleReset = () => {
    setDraft(null);
    setError(null);
  };

  return (
    <div className="flex min-h-0 flex-col gap-2">
      <p className="text-xs text-muted-foreground">
        {readOnly
          ? t(($) => $.toolbar.json_view.read_only_hint)
          : t(($) => $.toolbar.json_view.hint)}
      </p>

      <Textarea
        value={text}
        onChange={(event) => handleChange(event.target.value)}
        readOnly={readOnly}
        spellCheck={false}
        autoComplete="off"
        translate="no"
        aria-label={t(($) => $.toolbar.json_view.textarea_aria)}
        aria-invalid={error !== null}
        className="min-h-64 flex-1 resize-none font-mono text-xs leading-5"
      />

      {stale ? (
        <div className="flex items-start gap-2 rounded-md border border-amber-500/40 bg-amber-500/5 p-2 text-xs">
          <CircleAlert
            className="mt-0.5 size-3.5 shrink-0 text-amber-600 dark:text-amber-500"
            aria-hidden="true"
          />
          <span className="min-w-0 flex-1">
            {t(($) => $.toolbar.json_view.stale_notice)}
          </span>
        </div>
      ) : null}

      {error ? (
        <div
          role="alert"
          className="flex items-start gap-2 rounded-md border border-destructive/40 bg-destructive/5 p-2 text-xs text-destructive"
        >
          <CircleAlert className="mt-0.5 size-3.5 shrink-0" aria-hidden="true" />
          <span className="min-w-0 flex-1 break-words">
            {error.kind === "syntax"
              ? t(($) => $.toolbar.json_view.invalid_json, {
                  error: error.message,
                })
              : t(($) => $.toolbar.json_view.invalid_graph, {
                  error: error.message,
                })}
          </span>
        </div>
      ) : null}

      {/* No Apply at all while read-only: the server answers PATCH on a
          published or built-in template with 409, so a button here could only
          ever fail. The text stays selectable so a graph can still be copied. */}
      {readOnly ? null : (
        <div className="flex shrink-0 items-center justify-end gap-2">
          {untouched ? null : (
            <Button
              type="button"
              variant="ghost"
              size="sm"
              onClick={handleReset}
              aria-label={t(($) => $.toolbar.json_view.reset)}
            >
              <RotateCcw className="size-3.5" aria-hidden="true" />
              {t(($) => $.toolbar.json_view.reset)}
            </Button>
          )}
          <Button
            type="button"
            size="sm"
            onClick={handleApply}
            disabled={untouched}
            title={
              untouched
                ? t(($) => $.toolbar.json_view.nothing_to_apply)
                : t(($) => $.toolbar.json_view.apply)
            }
          >
            <Check className="size-3.5" aria-hidden="true" />
            {t(($) => $.toolbar.json_view.apply)}
          </Button>
        </div>
      )}
    </div>
  );
}
