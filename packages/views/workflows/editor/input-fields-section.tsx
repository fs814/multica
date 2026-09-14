"use client";
import { ScriptPipelineFields } from "../components/script-pipeline-fields";
import { scriptPipelineConfig } from "@multica/core/workflows";

/**
 * The `INTAKE FIELDS` section: what an input node asks a human for.
 *
 * This is the only part of the properties panel whose edits are consumed by a
 * *different surface* - the Run dialog reads the entry input node's declaration
 * and renders one control per field. That makes the rules here unusually
 * consequential: every one of them protects the property that a declared field is
 * one the dialog can render and a human can satisfy. A field that breaks any of
 * them is worse than a missing field, because the canvas advertises an input the
 * run can never receive, and the server's required-field check then rejects every
 * run of a template that looks correct.
 *
 * So each rule the server's `validateInputFields` enforces is surfaced *in place*
 * rather than left to the validate button at the other end of the page:
 *
 *  - a field with no key (the key is where the value is stored);
 *  - two fields with the same key (the second would overwrite the first);
 *  - a `select` with no options (a dropdown with nothing in it);
 *  - a blank option (indistinguishable from "nothing selected").
 *
 * ## Why ORDER is editable and not incidental
 *
 * Declaration order is the order the Run dialog lays the form out AND the order
 * the values are appended to the first agent's prompt (`ParseRunInputFor` walks
 * `InputFields` and preserves it). It is therefore authored content, not a storage
 * detail, and needs explicit controls - dragging is not available on this panel and
 * "delete and re-add" would lose the field's other three properties.
 *
 * ## Why field keys ARE editable, when node keys are not
 *
 * A node key is addressed by five other places in the same graph (see the
 * properties panel's header), and the `onChange(next: WorkflowNode)` signature
 * cannot rewrite them - so renaming there would save dangling references. A field
 * key is addressed by nothing inside the graph: it names a slot in the Run's input
 * bag, and the whole declaration is emitted as one node in one call, so a rename
 * here is complete and consistent by construction.
 *
 * It is still not free, and the hint says so: a *published* version is immutable
 * and every Run pins one, so an already-started Run keeps reading the key it was
 * published with - which is the good direction. What a rename does break is an
 * external caller (the intake endpoint, plan section 9) that was written against
 * the old key: it will keep sending it, the new declaration will not read it, and
 * the value silently arrives as absent rather than as an error. That is the same
 * class of consequence the node-key hint warns about, stated where the edit is.
 */

import { useRef, useState, type ChangeEvent } from "react";
import {
  ArrowDown,
  ArrowUp,
  ImagePlus,
  Loader2,
  Plus,
  Trash2,
  X,
} from "lucide-react";
import { api } from "@multica/core/api";
import { MAX_FILE_SIZE } from "@multica/core/constants/upload";
import { useFileUpload } from "@multica/core/hooks/use-file-upload";
import { attachmentDownloadPath } from "@multica/core/types";
import { resolvePublicFileUrlWithBase } from "@multica/core/workspace/avatar-url";
import type { WorkflowInputField, WorkflowNode } from "@multica/core/workflows";
import { Button } from "@multica/ui/components/ui/button";
import { Checkbox } from "@multica/ui/components/ui/checkbox";
import { Input } from "@multica/ui/components/ui/input";
import {
  WORKFLOW_INPUT_FIELD_TYPES,
  blankWorkflowInputField,
  toWorkflowInputFieldType,
} from "../graph";
import { useT } from "../../i18n";
import {
  PanelHint,
  PanelProblem,
  PanelSection,
  PanelSelect,
  type PanelOption,
} from "./panel-controls";

const WORKFLOW_IMAGE_TYPES = new Set([
  "image/jpeg",
  "image/png",
  "image/webp",
  "image/gif",
]);

export function InputFieldsSection({
  node,
  readOnly,
  onChange,
}: {
  node: WorkflowNode;
  readOnly: boolean;
  onChange(next: WorkflowNode): void;
}) {
  const { t } = useT("workflows");
  const fields = node.input_fields;
  const inputMode = node.input_mode === "scripts" ? "scripts" : node.input_mode === "image" ? "image" : "text";
  const imageAttachmentId = node.image_attachment_id ?? "";
  const imageInputRef = useRef<HTMLInputElement>(null);
  const latestNodeRef = useRef(node);
  latestNodeRef.current = node;
  const [imageError, setImageError] = useState<string | null>(null);
  const { upload, uploading } = useFileUpload(api);
  const stableImageUrl =
    imageAttachmentId === "" ? null : attachmentDownloadPath(imageAttachmentId);
  const imagePreviewUrl =
    stableImageUrl === null
      ? null
      : (resolvePublicFileUrlWithBase(
          stableImageUrl,
          api.getBaseUrl?.() ?? "",
        ) ?? stableImageUrl);

  const replace = (next: WorkflowInputField[]) =>
    onChange({ ...node, input_fields: next });

  const handleImagePick = async (event: ChangeEvent<HTMLInputElement>) => {
    const file = event.target.files?.[0];
    event.target.value = "";
    if (!file) return;
    if (!WORKFLOW_IMAGE_TYPES.has(file.type)) {
      setImageError(t(($) => $.panel.input_mode.image_invalid_type));
      return;
    }
    if (file.size > MAX_FILE_SIZE) {
      setImageError(t(($) => $.panel.input_mode.image_too_large));
      return;
    }
    setImageError(null);
    try {
      const result = await upload(file);
      if (!result) return;
      // The upload can finish after another part of the panel edited the node.
      // Spread the latest render, not the node captured when the picker opened,
      // so a slow upload cannot restore an older name or instruction.
      onChange({ ...latestNodeRef.current, image_attachment_id: result.id });
    } catch (error) {
      setImageError(
        error instanceof Error
          ? error.message
          : t(($) => $.panel.input_mode.image_upload_failed),
      );
    }
  };

  const patch = (index: number, next: Partial<WorkflowInputField>) =>
    replace(
      fields.map((existing, at) =>
        // Spread the existing field so a forward-compatible key a newer server
        // put on it survives being edited here - the wire schema is `.loose()`.
        at === index ? { ...existing, ...next } : existing,
      ),
    );

  /** Swaps a row with its neighbour, which is what "reorder" means for a list. */
  const move = (index: number, by: -1 | 1) => {
    const to = index + by;
    if (to < 0 || to >= fields.length) return;
    const next = [...fields];
    const moved = next[index]!;
    next[index] = next[to]!;
    next[to] = moved;
    replace(next);
  };

  // Duplicate detection is over the whole list, so BOTH offending rows are
  // flagged rather than only the later one: the author has to choose which key to
  // change, and marking only the second implies the first is the correct one.
  const keyCounts = new Map<string, number>();
  for (const field of fields) {
    if (field.key === "") continue;
    keyCounts.set(field.key, (keyCounts.get(field.key) ?? 0) + 1);
  }

  const typeLabels: Record<
    (typeof WORKFLOW_INPUT_FIELD_TYPES)[number],
    string
  > = {
    text: t(($) => $.panel.input_fields.type_text),
    textarea: t(($) => $.panel.input_fields.type_textarea),
    select: t(($) => $.panel.input_fields.type_select),
  };

  return (
    <PanelSection title={t(($) => $.panel.section.input_fields)}>
      <PanelSelect
        value={inputMode}
        options={[
          { value: "text", label: t(($) => $.panel.input_mode.text) },
          { value: "image", label: t(($) => $.panel.input_mode.image) },
          { value: "scripts", label: t(($) => $.scripts.mode) },
        ]}
        onChange={(next) =>
          // Fields belong to the text form. Clearing them on a mode change keeps
          // an image intake from carrying invisible required fields; switching
          // back deliberately starts with a valid, empty text declaration.
          onChange({
            ...node,
            input_mode: next,
            script_pipeline: next === "scripts" ? scriptPipelineConfig(node.script_pipeline) : undefined,
            input_fields: [],
            image_attachment_id: "",
          })
        }
        disabled={readOnly}
        ariaLabel={t(($) => $.panel.input_mode.aria)}
      />
      {inputMode === "scripts" ? (<ScriptPipelineFields value={scriptPipelineConfig(node.script_pipeline)} disabled={readOnly} onChange={(value) => onChange({ ...node, script_pipeline: value })} />) : inputMode === "image" ? (
        <>
          <input
            ref={imageInputRef}
            type="file"
            accept="image/jpeg,image/png,image/webp,image/gif"
            className="hidden"
            onChange={(event) => void handleImagePick(event)}
          />
          {imagePreviewUrl ? (
            <div className="flex flex-col gap-2 rounded-lg border border-input p-2">
              <img
                src={imagePreviewUrl}
                alt={t(($) => $.panel.input_mode.image_preview_alt)}
                className="h-36 w-full rounded object-contain"
              />
              {!readOnly ? (
                <div className="flex items-center gap-1.5">
                  <Button
                    type="button"
                    variant="outline"
                    size="sm"
                    disabled={uploading}
                    onClick={() => imageInputRef.current?.click()}
                  >
                    {uploading ? (
                      <Loader2
                        className="size-3.5 animate-spin"
                        aria-hidden="true"
                      />
                    ) : (
                      <ImagePlus className="size-3.5" aria-hidden="true" />
                    )}
                    {t(($) => $.panel.input_mode.image_replace)}
                  </Button>
                  <Button
                    type="button"
                    variant="ghost"
                    size="sm"
                    onClick={() =>
                      onChange({ ...node, image_attachment_id: "" })
                    }
                  >
                    <X className="size-3.5" aria-hidden="true" />
                    {t(($) => $.panel.input_mode.image_clear)}
                  </Button>
                </div>
              ) : null}
            </div>
          ) : !readOnly ? (
            <Button
              type="button"
              variant="outline"
              size="sm"
              className="self-start"
              disabled={uploading}
              onClick={() => imageInputRef.current?.click()}
            >
              {uploading ? (
                <Loader2 className="size-3.5 animate-spin" aria-hidden="true" />
              ) : (
                <ImagePlus className="size-3.5" aria-hidden="true" />
              )}
              {t(($) => $.panel.input_mode.image_choose)}
            </Button>
          ) : null}
          {imageError ? (
            <PanelProblem>{imageError}</PanelProblem>
          ) : (
            <PanelHint>{t(($) => $.panel.input_mode.image_hint)}</PanelHint>
          )}
        </>
      ) : (
        <>
          <PanelHint>{t(($) => $.panel.input_fields.hint)}</PanelHint>
          {/* The rename caution is shown once, above the list, rather than on each
          row: four copies of the same paragraph in a 320px panel would push the
          fields themselves off screen. See the file header for why renaming is
          offered here at all when the node key is not. */}
          {fields.length > 0 ? (
            <PanelHint>{t(($) => $.panel.input_fields.key_hint)}</PanelHint>
          ) : null}

          {fields.length === 0 ? (
            // A hint, not a problem: an input node with no declared fields is a legal
            // graph. It documents where work enters, and the Run dialog falls back to
            // the freeform Title + Description pair - exactly what every template
            // published before input nodes existed still does.
            <PanelHint>{t(($) => $.panel.input_fields.empty)}</PanelHint>
          ) : (
            <ul className="flex flex-col gap-3">
              {fields.map((field, index) => {
                const type = toWorkflowInputFieldType(field.type);
                const duplicate =
                  field.key !== "" && (keyCounts.get(field.key) ?? 0) > 1;

                const typeOptions: PanelOption[] =
                  WORKFLOW_INPUT_FIELD_TYPES.map((candidate) => ({
                    value: candidate,
                    label: typeLabels[candidate],
                  }));
                // A kind a newer server added is offered back verbatim, for the same
                // reason an unknown routing strategy is: otherwise merely selecting
                // the node would rewrite a field type the server accepts. `""` is the
                // server's own spelling of "text", so it is folded into that option
                // rather than shown as a fourth choice.
                if (
                  field.type !== "" &&
                  !(WORKFLOW_INPUT_FIELD_TYPES as readonly string[]).includes(
                    field.type,
                  )
                ) {
                  typeOptions.push({
                    value: field.type,
                    label: t(($) => $.panel.input_fields.type_unknown, {
                      type: field.type,
                    }),
                  });
                }

                return (
                  <li
                    // Index key: the rows are positional (a field's identity IS its
                    // place in the declaration, which is what the dialog and the
                    // prompt read), and keying on `field.key` would remount the input
                    // the author is typing a key into on every keystroke.
                    key={index}
                    className="flex flex-col gap-1.5 rounded-lg border border-input p-2"
                  >
                    <div className="flex items-center gap-1.5">
                      <Input
                        value={field.key}
                        disabled={readOnly}
                        className="font-mono"
                        aria-label={t(($) => $.panel.input_fields.key_aria, {
                          index: index + 1,
                        })}
                        placeholder={t(
                          ($) => $.panel.input_fields.key_placeholder,
                        )}
                        onChange={(event) =>
                          patch(index, { key: event.target.value })
                        }
                      />
                      {!readOnly ? (
                        <>
                          {/* Both arrows are always rendered and the end ones are
                          disabled, rather than omitted: a row whose control set
                          changes with its position makes the buttons move under
                          the pointer as the author reorders. */}
                          <Button
                            type="button"
                            variant="ghost"
                            size="icon-sm"
                            className="shrink-0"
                            disabled={index === 0}
                            aria-label={t(
                              ($) => $.panel.input_fields.move_up_aria,
                              {
                                index: index + 1,
                              },
                            )}
                            onClick={() => move(index, -1)}
                          >
                            <ArrowUp className="size-3.5" aria-hidden="true" />
                          </Button>
                          <Button
                            type="button"
                            variant="ghost"
                            size="icon-sm"
                            className="shrink-0"
                            disabled={index === fields.length - 1}
                            aria-label={t(
                              ($) => $.panel.input_fields.move_down_aria,
                              { index: index + 1 },
                            )}
                            onClick={() => move(index, 1)}
                          >
                            <ArrowDown
                              className="size-3.5"
                              aria-hidden="true"
                            />
                          </Button>
                          <Button
                            type="button"
                            variant="ghost"
                            size="icon-sm"
                            className="shrink-0"
                            aria-label={t(
                              ($) => $.panel.input_fields.remove_aria,
                              {
                                index: index + 1,
                              },
                            )}
                            onClick={() =>
                              replace(fields.filter((_, at) => at !== index))
                            }
                          >
                            <Trash2 className="size-3.5" aria-hidden="true" />
                          </Button>
                        </>
                      ) : null}
                    </div>

                    {field.key === "" ? (
                      <PanelProblem>
                        {t(($) => $.panel.input_fields.key_required)}
                      </PanelProblem>
                    ) : null}
                    {duplicate ? (
                      <PanelProblem>
                        {t(($) => $.panel.input_fields.key_duplicate, {
                          key: field.key,
                        })}
                      </PanelProblem>
                    ) : null}

                    <Input
                      value={field.label}
                      disabled={readOnly}
                      aria-label={t(($) => $.panel.input_fields.label_aria, {
                        index: index + 1,
                      })}
                      placeholder={t(
                        ($) => $.panel.input_fields.label_placeholder,
                      )}
                      onChange={(event) =>
                        patch(index, { label: event.target.value })
                      }
                    />

                    <PanelSelect
                      value={field.type}
                      options={typeOptions}
                      onChange={(next) => patch(index, { type: next })}
                      disabled={readOnly}
                      ariaLabel={t(($) => $.panel.input_fields.type_aria, {
                        index: index + 1,
                      })}
                    />

                    <label
                      className={`flex items-center gap-2 text-caption ${
                        readOnly ? "opacity-50" : "cursor-pointer"
                      }`}
                    >
                      <Checkbox
                        checked={field.required}
                        disabled={readOnly}
                        onCheckedChange={(checked) =>
                          patch(index, { required: checked === true })
                        }
                      />
                      <span>{t(($) => $.panel.input_fields.required)}</span>
                    </label>

                    {/* Options only exist for a select. Rendering them for every kind
                    would invite an author to fill in a list the dialog will never
                    read, and the server ignores. */}
                    {type === "select" ? (
                      <OptionsEditor
                        field={field}
                        index={index}
                        readOnly={readOnly}
                        onPatch={(next) => patch(index, next)}
                      />
                    ) : null}
                  </li>
                );
              })}
            </ul>
          )}

          {!readOnly ? (
            <Button
              type="button"
              variant="outline"
              size="sm"
              className="self-start"
              // A blank field, with an empty key on purpose: the validator rejects an
              // empty key, and inventing `field_1` would publish a key nobody chose
              // and that every downstream prompt would then label meaninglessly. See
              // `blankWorkflowInputField`.
              onClick={() => replace([...fields, blankWorkflowInputField()])}
            >
              <Plus className="size-3.5" aria-hidden="true" />
              {t(($) => $.panel.input_fields.add)}
            </Button>
          ) : null}
        </>
      )}
    </PanelSection>
  );
}

/**
 * The permitted values of one `select` field.
 *
 * One input per option rather than a comma-separated string: an option may
 * legitimately contain a comma ("blocked, needs info"), and a delimiter would make
 * that value unrepresentable while looking like it worked. It is also the shape the
 * blank-option rule can be reported against per row.
 */
function OptionsEditor({
  field,
  index,
  readOnly,
  onPatch,
}: {
  field: WorkflowInputField;
  /** The field's 1-based position, for aria labels that name the right row. */
  index: number;
  readOnly: boolean;
  onPatch(next: Partial<WorkflowInputField>): void;
}) {
  const { t } = useT("workflows");
  const options = field.options;

  return (
    <div className="flex flex-col gap-1.5">
      <span className="text-caption font-medium">
        {t(($) => $.panel.input_fields.options)}
      </span>
      {options.length === 0 ? (
        // A problem, not a hint: a select with no options is a graph the server
        // rejects, and a required one would make the template unrunnable.
        <PanelProblem>
          {t(($) => $.panel.input_fields.options_required)}
        </PanelProblem>
      ) : (
        <ul className="flex flex-col gap-1.5">
          {options.map((option, at) => (
            <li key={at} className="flex flex-col gap-1">
              <div className="flex items-center gap-1.5">
                <Input
                  value={option}
                  disabled={readOnly}
                  aria-label={t(($) => $.panel.input_fields.option_aria, {
                    field: index + 1,
                    index: at + 1,
                  })}
                  placeholder={t(
                    ($) => $.panel.input_fields.option_placeholder,
                  )}
                  onChange={(event) =>
                    onPatch({
                      options: options.map((existing, i) =>
                        i === at ? event.target.value : existing,
                      ),
                    })
                  }
                />
                {!readOnly ? (
                  <Button
                    type="button"
                    variant="ghost"
                    size="icon-sm"
                    className="shrink-0"
                    aria-label={t(
                      ($) => $.panel.input_fields.option_remove_aria,
                      { field: index + 1, index: at + 1 },
                    )}
                    onClick={() =>
                      onPatch({ options: options.filter((_, i) => i !== at) })
                    }
                  >
                    <Trash2 className="size-3.5" aria-hidden="true" />
                  </Button>
                ) : null}
              </div>
              {option === "" ? (
                <PanelProblem>
                  {t(($) => $.panel.input_fields.option_blank)}
                </PanelProblem>
              ) : null}
            </li>
          ))}
        </ul>
      )}
      {!readOnly ? (
        <Button
          type="button"
          variant="outline"
          size="sm"
          className="self-start"
          onClick={() => onPatch({ options: [...options, ""] })}
        >
          <Plus className="size-3.5" aria-hidden="true" />
          {t(($) => $.panel.input_fields.option_add)}
        </Button>
      ) : null}
    </div>
  );
}
