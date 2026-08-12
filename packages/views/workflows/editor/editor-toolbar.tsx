"use client";

import {
  Braces,
  LayoutGrid,
  Loader2,
  Redo2,
  Save,
  ShieldCheck,
  Undo2,
} from "lucide-react";
import { Button } from "@multica/ui/components/ui/button";
import { Separator } from "@multica/ui/components/ui/separator";
import { useT } from "../../i18n";

/**
 * The graph editor's action cluster: undo / redo, then validate, auto-layout,
 * JSON and save.
 *
 * Which actions `readOnly` disables is decided by *what the action can change*,
 * not by how it looks:
 *
 *  - **Save / undo / redo** mutate the draft definition. A published or
 *    built-in template cannot accept a draft edit at all - the server answers
 *    PATCH with 409 for both - so offering them would promise a write the API
 *    refuses.
 *  - **Validate** only asks a question. Running it against a published graph is
 *    how an author finds out *why* a template they inherited misbehaves, so it
 *    stays live.
 *  - **Auto-layout** moves pixels. Canvas positions are deliberately not part of
 *    the definition (see graph/layout.ts and the "does not write canvas
 *    positions into the definition" test), so pressing it on a read-only
 *    template changes nothing that could be saved. Disabling it would only stop
 *    a reader from untangling a graph in order to read it.
 *
 * `dirty` gates save rather than the component tracking it: the graph lives in
 * the page's editor state, and a toolbar that guessed at dirtiness would either
 * offer a no-op save or hide a real one.
 */
export function WorkflowEditorToolbar({
  dirty,
  saving,
  validating,
  readOnly,
  onValidate,
  onAutoLayout,
  onToggleJson,
  onSave,
  onUndo,
  onRedo,
  canUndo,
  canRedo,
}: {
  dirty: boolean;
  saving: boolean;
  validating: boolean;
  readOnly: boolean;
  onValidate(): void;
  onAutoLayout(): void;
  onToggleJson(): void;
  onSave(): void;
  onUndo(): void;
  onRedo(): void;
  canUndo: boolean;
  canRedo: boolean;
}) {
  const { t } = useT("workflows");

  const undoLabel = t(($) => $.toolbar.undo);
  const redoLabel = t(($) => $.toolbar.redo);
  const validateLabel = validating
    ? t(($) => $.toolbar.validating)
    : t(($) => $.toolbar.validate);
  const autoLayoutLabel = t(($) => $.toolbar.auto_layout);
  const saveLabel = saving
    ? t(($) => $.toolbar.saving)
    : t(($) => $.toolbar.save);

  // A disabled primary action is only fair if it explains itself. The three
  // reasons save is unavailable are distinct and the user can act on two of
  // them, so they get distinct tooltips instead of a shared grey button.
  const saveHint = readOnly
    ? t(($) => $.toolbar.read_only)
    : saving
      ? t(($) => $.toolbar.saving)
      : dirty
        ? saveLabel
        : t(($) => $.toolbar.no_changes);

  return (
    <div className="flex shrink-0 items-center gap-1">
      <Button
        variant="ghost"
        size="icon-sm"
        onClick={onUndo}
        disabled={readOnly || !canUndo}
        title={undoLabel}
        aria-label={undoLabel}
      >
        <Undo2 className="size-3.5" aria-hidden="true" />
      </Button>
      <Button
        variant="ghost"
        size="icon-sm"
        onClick={onRedo}
        disabled={readOnly || !canRedo}
        title={redoLabel}
        aria-label={redoLabel}
      >
        <Redo2 className="size-3.5" aria-hidden="true" />
      </Button>

      <Separator orientation="vertical" className="mx-1 h-5" />

      <Button
        variant="outline"
        size="sm"
        onClick={onValidate}
        disabled={validating}
        className="px-2 sm:px-2.5"
        aria-label={validateLabel}
      >
        {validating ? (
          <Loader2
            className="size-3.5 animate-spin motion-reduce:animate-none sm:mr-1"
            aria-hidden="true"
          />
        ) : (
          <ShieldCheck className="size-3.5 sm:mr-1" aria-hidden="true" />
        )}
        <span className="hidden sm:inline">{validateLabel}</span>
      </Button>

      <Button
        variant="outline"
        size="sm"
        onClick={onAutoLayout}
        className="px-2 sm:px-2.5"
        aria-label={autoLayoutLabel}
      >
        <LayoutGrid className="size-3.5 sm:mr-1" aria-hidden="true" />
        <span className="hidden sm:inline">{autoLayoutLabel}</span>
      </Button>

      <Button
        variant="outline"
        size="sm"
        onClick={onToggleJson}
        className="px-2 sm:px-2.5"
        aria-label={t(($) => $.toolbar.json_aria)}
      >
        <Braces className="size-3.5 sm:mr-1" aria-hidden="true" />
        <span className="hidden sm:inline">{t(($) => $.toolbar.json)}</span>
      </Button>

      <Button
        size="sm"
        onClick={onSave}
        disabled={readOnly || saving || !dirty}
        className="px-2 sm:px-2.5"
        title={saveHint}
        aria-label={saveLabel}
      >
        {saving ? (
          <Loader2
            className="size-3.5 animate-spin motion-reduce:animate-none sm:mr-1"
            aria-hidden="true"
          />
        ) : (
          <Save className="size-3.5 sm:mr-1" aria-hidden="true" />
        )}
        <span className="hidden sm:inline">{saveLabel}</span>
      </Button>
    </div>
  );
}
