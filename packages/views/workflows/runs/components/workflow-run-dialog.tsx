"use client";
import { scriptPipelineFromInput, scriptPipelineInput, scriptPipelineInputKeys, scriptPipelineReady } from "@multica/core/workflows";
import { ScriptPipelineFields } from "../../components/script-pipeline-fields";

import { useRef, useState } from "react";
import { ChevronDown, ChevronRight, FolderKanban, Play } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import { useRunWorkflowTemplate, workflowRunInputDefaults } from "@multica/core/workflows";
import type {
  RunWorkflowTemplateRequest,
  WorkflowDefinition,
  WorkflowInputField,
} from "@multica/core/workflows";
import { useWorkspaceId } from "@multica/core/hooks";
import { useCurrentWorkspace, useWorkspacePaths } from "@multica/core/paths";
import { projectListOptions } from "@multica/core/projects/queries";
import { Button } from "@multica/ui/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import { Input } from "@multica/ui/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@multica/ui/components/ui/select";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { cn } from "@multica/ui/lib/utils";
import { toWorkflowInputFieldType } from "../../graph";
import { ProjectPicker } from "../../../projects/components/project-picker";
import { ProjectIcon } from "../../../projects/components/project-icon";
import { useNavigation } from "../../../navigation";
import { useT } from "../../../i18n";

/**
 * The Run dialog: the answer to "how do I give input to a workflow".
 *
 * ## Where the form comes from
 *
 * From the GRAPH, when the graph says so. A template whose `entry_node` is an
 * `input` node declares the fields this dialog collects, so the workflow is
 * self-documenting: the author sees `[INPUT|intake] -> [ISSUE|analyze]` on the
 * canvas and knows what starting a run will ask for. Before input nodes existed
 * the field list lived here, hardcoded, and was invisible on the canvas.
 *
 * The declaration is read off the *effective* definition - the graph a run started
 * now would pin - and not off the editor's unsaved draft. A run pins a published
 * version, so collecting fields from an unpublished draft would show a form whose
 * values the pinned graph never declared.
 *
 * ## Why the fallback is not optional
 *
 * A published version is IMMUTABLE and every in-flight run pins one, so every
 * template published before input nodes existed - and every run of one - keeps the
 * freeform Title + Description pair forever. `definition` is therefore allowed to
 * be absent (a caller with no graph to hand) and an entry node that is not an input
 * node is not an error: both take exactly today's form. That is also why this
 * component keeps working when the whole definition fails to parse - the wire
 * schema degrades a bad graph to an empty one, which lands here as "no declaration".
 *
 * ## Why Title and Description are always collected
 *
 * `POST /api/workflow-templates/{id}/run` rejects an empty title (400) and an
 * empty description (400) on every run, because every agent step's prompt is its
 * node instruction plus this input: without a description the first agent is told
 * to reproduce a defect nobody described, burns an attempt producing a guess, and
 * the acceptance gate then rejects work that was never briefed.
 *
 * So a declaration cannot REMOVE those two controls - it can only relabel them. A
 * declared field keyed `title` or `description` takes over the corresponding
 * control (its label, placeholder and kind are the author's); a declaration that
 * omits one gets the built-in control back. The alternative - rendering exactly
 * what was declared - would let an author publish a graph whose Run dialog is
 * structurally incapable of producing a body the endpoint accepts, and the
 * submitter would meet that as a 400 with no way to fix it.
 *
 * For the same reason those two are collected as REQUIRED whichever way they
 * arrive, even if the declaration marks them optional: honouring `required: false`
 * there would enable the Run button for a body the server is about to refuse.
 *
 * ## Why the unpublished case disables rather than fails
 *
 * A run pins a *published* version, so a template with none cannot start one:
 * the server answers 409. Letting the click through and surfacing that as an
 * error toast would teach nothing - the reader would not know that publishing
 * is the fix. The button is disabled and the dialog says which of the two
 * refusals applies, the same way the editor names its three read-only reasons.
 */

/** The two keys the run endpoint models as first-class body fields. */
const TITLE_KEY = "title";
const DESCRIPTION_KEY = "description";

/**
 * One control to render, resolved from the declaration plus the endpoint's own
 * two required fields.
 *
 * `declared` records where the control came from, and it is not cosmetic: a
 * built-in control is one this dialog supplied because the endpoint demands it, so
 * its label is a UI string from the locale bundle, while a declared control's label
 * is authored content that must be shown verbatim.
 */
type RunFormField = {
  key: string;
  label: string;
  placeholder: string;
  kind: "text" | "textarea" | "select";
  options: string[];
  required: boolean;
  declared: boolean;
};

/**
 * The graph's declared intake node, or null.
 *
 * Only the ENTRY node counts. The server's validator rejects an input node
 * anywhere else (a mid-graph one would be a form the engine walks past and this
 * dialog would never show), but a graph can still reach a client while being
 * invalid - it may be an unpublished draft, or authored by a newer server - and in
 * that case the honest reading is "this template has no intake declaration",
 * which is the freeform path. Mirrors `Definition.EntryInputNode` on the server.
 */
function entryInputFields(
  definition: WorkflowDefinition | undefined,
): WorkflowInputField[] {
  if (!definition || definition.entry_node === "") return [];
  const entry = definition.nodes.find(
    (node) => node.key === definition.entry_node,
  );
  if (!entry || entry.type !== "input") return [];
  return entry.input_fields;
}

export function WorkflowRunDialog(props: React.ComponentProps<typeof WorkflowRunDialogForm>) {
  const wsId = useWorkspaceId();
  return <WorkflowRunDialogForm key={`${wsId}:${props.templateId}:${props.open}`} {...props} />;
}

function WorkflowRunDialogForm({
  templateId,
  templateName,
  templateVersionId,
  /**
   * The template's effective graph - what a run started now would pin. Optional:
   * see "Why the fallback is not optional" above. Absent, or without an entry
   * input node, means the freeform Title + Description form.
   */
  definition,
  inputDefaults,
  /** True when the template has a published version to pin. */
  runnable,
  /** Set when `runnable` is false: which refusal applies. */
  refusal,
  open,
  onOpenChange,
}: {
  templateId: string;
  templateName: string;
  templateVersionId?: string;
  definition?: WorkflowDefinition;
  /** The editor may supply current intake text while the run keeps its published schema. */
  inputDefaults?: Record<string, string>;
  runnable: boolean;
  refusal?: "unpublished" | "archived";
  open: boolean;
  onOpenChange(next: boolean): void;
}) {
  const { t } = useT("workflows");
  const wsId = useWorkspaceId();
  const wsPaths = useWorkspacePaths();
  const navigation = useNavigation();
  const workspaceName = useCurrentWorkspace()?.name;
  const { data: projects = [] } = useQuery(projectListOptions(wsId));

  /**
   * User overrides by field key. Node content supplies defaults; explicit empty
   * strings stay empty, so clearing a field never restores the default.
   *
   * One map rather than a `useState` per control, because the control set is
   * decided by the pinned graph and is not known at compile time. A missing key
   * reads as `""`, so a field the author adds to a draft mid-session does not need
   * the map to be re-seeded.
   */
  const [overrides, setValues] = useState<Record<string, string>>({});
  const [useDefaults, setUseDefaults] = useState(true);
  const [loadedVersion, setLoadedVersion] = useState<string | null | undefined>(templateVersionId);
  const values = {
    ...(useDefaults ? inputDefaults ?? workflowRunInputDefaults(definition, templateName) : {}),
    ...overrides,
  };
  /**
   * Keys the submitter has left. Per-field problems appear on blur rather than
   * immediately, so an untouched form is not a wall of red before anyone has typed
   * anything - the empty state of a form is not an error, it is its beginning.
   */
  const [touched, setTouched] = useState<Record<string, boolean>>({});
  const [projectId, setProjectId] = useState<string | null>(null);
  const runTemplate = useRunWorkflowTemplate();
  const runAttempt = useRef<{ body: string; key: string } | null>(null);
  const submitting = useRef(false);

  const entry = definition?.nodes.find((node) => node.key === definition.entry_node);
  const scriptMode = entry?.input_mode === "scripts";
  const pipeline = scriptPipelineFromInput(entry?.script_pipeline, values);
  const declared = entryInputFields(definition);
  const fields = resolveRunFormFields(declared, {
    titleLabel: t(($) => $.runs.dialog.title_label),
    titlePlaceholder: t(($) => $.runs.dialog.title_placeholder),
    descriptionLabel: t(($) => $.runs.dialog.description_label),
    descriptionPlaceholder: t(($) => $.runs.dialog.description_placeholder),
  });

  const valueOf = (key: string) => values[key] ?? "";

  /**
   * The problem with one field's current value, or undefined.
   *
   * Returned per field rather than accumulated into one message: a submitter
   * fixing a form has to know WHICH control is the blocker, and a single toast
   * saying "fill in the required fields" makes them re-read the whole dialog.
   */
  const problemFor = (field: RunFormField): string | undefined => {
    const value = valueOf(field.key).trim();
    if (field.required && value === "") {
      return t(($) => $.runs.dialog.field_required, { label: field.label });
    }
    // An off-list select value is as unusable as a missing one - the author
    // enumerated the values the downstream steps are written against - and the
    // server refuses it with the same 422. Unreachable through the control itself,
    // but reachable when a declaration changes under a dialog left open.
    if (
      field.kind === "select" &&
      value !== "" &&
      !field.options.includes(value)
    ) {
      return t(($) => $.runs.dialog.field_option_invalid, {
        label: field.label,
      });
    }
    return undefined;
  };

  const problems = new Map<string, string>();
  for (const field of fields) {
    const problem = problemFor(field);
    if (problem !== undefined) problems.set(field.key, problem);
  }

  const fieldKeys = new Set([...fields.map((field) => field.key), ...(scriptMode ? scriptPipelineInputKeys : [])]);
  const removedKeys = Object.keys(values).filter((key) => !fieldKeys.has(key));
  const versionChanged = Boolean(templateVersionId && loadedVersion !== undefined && loadedVersion !== templateVersionId);
  const needsReview = versionChanged || removedKeys.length > 0;
  const canSubmit = (!scriptMode || scriptPipelineReady(pipeline)) && runnable && !needsReview && problems.size === 0 && !runTemplate.isPending;

  const reset = () => {
    runAttempt.current = null;
    setUseDefaults(true);
    setLoadedVersion(templateVersionId);
    setValues({});
    setTouched({});
    setProjectId(null);
  };

  const handleSubmit = async () => {
    if (!canSubmit || submitting.current) return;
    submitting.current = true;
    const body = { ...runRequestBody(fields, values, projectId), ...(scriptMode ? scriptPipelineInput(pipeline) : {}), ...(templateVersionId ? { templateVersionId } : {}) };
    const serialized = JSON.stringify(body);
    if (runAttempt.current?.body !== serialized) {
      runAttempt.current = { body: serialized, key: crypto.randomUUID() };
    }
    try {
      const run = await runTemplate.mutateAsync({
        templateId,
        ...body,
        idempotency_key: runAttempt.current.key,
      });
      // Both guards, and they are not redundant. This endpoint does NOT spread
      // the requested id onto its parse-miss fallback (the id is what the call
      // returns), so an empty id means "no run to navigate to" while an empty
      // status means "the payload was unreadable". Either way navigating would
      // land on a detail page for a run we cannot name - and the run itself did
      // start, so this is not an error, it is a lost handle. Say so and leave
      // the reader on the list where they can find it.
      if (!run.id || !run.status) {
        toast.warning(t(($) => $.runs.dialog.toast_unreadable));
        onOpenChange(false);
        reset();
        navigation.push(wsPaths.workflowRuns());
        return;
      }
      toast.success(t(($) => $.runs.dialog.toast_started));
      onOpenChange(false);
      reset();
      navigation.push(wsPaths.workflowRunDetail(run.id));
    } catch (err) {
      // The server's message is the useful part here: a 409 says the template
      // has no published version, a 422 names the graph rule the pinned
      // definition breaks or the declared field the values do not satisfy, a 503
      // says no engine is wired. A generic failure string would collapse four
      // different next actions into one.
      toast.error(
        err instanceof Error && err.message
          ? err.message
          : t(($) => $.runs.dialog.toast_failed),
      );
    } finally {
      submitting.current = false;
    }
  };

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        onOpenChange(next);
        // Closing ends this submission attempt; reopening starts a new run key.
        if (!next) reset();
      }}
    >
      <DialogContent
        showCloseButton={false}
        className="flex max-h-[calc(100vh-4rem)] w-[calc(100vw-2rem)] flex-col gap-0 overflow-hidden p-0 sm:max-w-xl"
      >
        <DialogTitle className="sr-only">
          {t(($) => $.runs.dialog.sr_title)}
        </DialogTitle>

        <div className="shrink-0 border-b px-5 pt-3 pb-2">
          <div className="flex items-center gap-2 text-caption">
            <span className="inline-flex size-5 items-center justify-center rounded-md bg-primary/15 text-primary">
              <Play className="size-3" aria-hidden="true" />
            </span>
            <span className="font-medium text-foreground">
              {t(($) => $.runs.dialog.header)}
            </span>
            <ChevronRight
              className="size-3 text-faint-foreground"
              aria-hidden="true"
            />
            <span className="min-w-0 truncate text-muted-foreground">
              {templateName}
            </span>
            {workspaceName ? (
              <>
                <ChevronRight
                  className="size-3 text-faint-foreground"
                  aria-hidden="true"
                />
                <span className="min-w-0 truncate text-muted-foreground">
                  {workspaceName}
                </span>
              </>
            ) : null}
          </div>
          <p className="mt-1 text-caption text-muted-foreground">
            {t(($) => $.runs.dialog.subtitle)}
          </p>
        </div>

        {/* The refusal notice sits above the fields rather than under the
            button: a submitter who cannot run this template should learn that
            before typing a report, not after. */}
        {runnable ? null : (
          <div
            role="status"
            className="shrink-0 border-b border-amber-500/30 bg-amber-500/5 px-5 py-2.5 text-caption leading-relaxed text-amber-700 dark:text-amber-400"
          >
            {refusal === "archived"
              ? t(($) => $.runs.dialog.archived)
              : t(($) => $.runs.dialog.unpublished)}
          </div>
        )}

        <div className="flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto px-5 py-4">
          {needsReview && (
            <div role="alert" className="flex flex-col gap-2 rounded-md border p-3 text-caption">
              <p>{t(($) => $.input_instances.changed)}</p>
              {removedKeys.length > 0 && <p>{t(($) => $.input_instances.removed, { fields: removedKeys.join(", ") })}</p>}
              <Button type="button" size="sm" variant="outline" onClick={() => {
                setUseDefaults(false);
                setValues(Object.fromEntries(Object.entries(values).filter(([key]) => fieldKeys.has(key))));
                setLoadedVersion(templateVersionId);
                setTouched(Object.fromEntries(fields.map((field) => [field.key, true])));
              }}>{t(($) => $.input_instances.review)}</Button>
            </div>
          )}
          {scriptMode && <ScriptPipelineFields value={pipeline} disabled={runTemplate.isPending} onChange={(next) => setValues({ ...overrides, ...scriptPipelineInput(next) })} />}
          {fields.map((field) => (
            <RunFormControl
              key={field.key}
              field={field}
              value={valueOf(field.key)}
              disabled={refusal === "archived" || runTemplate.isPending}
              // The problem is withheld until the control has been left: see
              // `touched`. It reappears on every render after that, so a field
              // emptied again does not go quiet.
              problem={touched[field.key] ? problems.get(field.key) : undefined}
              // The freeform description keeps its explanatory hint. A DECLARED
              // field does not get it: the author wrote the label and placeholder,
              // and a built-in sentence about what agents receive would sit under
              // their wording claiming to describe it.
              hint={
                field.key === DESCRIPTION_KEY && !field.declared
                  ? t(($) => $.runs.dialog.description_hint)
                  : undefined
              }
              onChange={(next) =>
                setValues((current) => ({ ...current, [field.key]: next }))
              }
              onBlur={() =>
                setTouched((current) => ({ ...current, [field.key]: true }))
              }
              selectPlaceholder={t(($) => $.runs.dialog.select_unset)}
            />
          ))}

          <div className="flex flex-col gap-1.5">
            <span className="text-caption font-medium">
              {t(($) => $.runs.dialog.project_label)}
            </span>
            <ProjectPicker
              projectId={projectId}
              disabled={!runnable}
              onUpdate={(updates) => setProjectId(updates.project_id ?? null)}
              align="start"
              triggerRender={
                <button
                  type="button"
                  className={cn(
                    "flex w-full items-center gap-2.5 rounded-md border bg-background px-3 py-2 text-left",
                    runnable
                      ? "cursor-pointer transition-colors hover:bg-accent/40"
                      : "opacity-50",
                  )}
                >
                  {selectedProjectOf(projects, projectId) ? (
                    <ProjectIcon
                      project={selectedProjectOf(projects, projectId)!}
                      size="md"
                    />
                  ) : (
                    <span className="inline-flex size-5 items-center justify-center rounded-md bg-muted text-muted-foreground">
                      <FolderKanban className="size-3.5" aria-hidden="true" />
                    </span>
                  )}
                  <span className="min-w-0 flex-1 truncate text-body">
                    {selectedProjectOf(projects, projectId)?.title ??
                      t(($) => $.runs.dialog.no_project)}
                  </span>
                  <ChevronDown
                    className="size-3.5 shrink-0 text-muted-foreground"
                    aria-hidden="true"
                  />
                </button>
              }
            />
          </div>
        </div>

        <div className="flex shrink-0 items-center justify-end gap-2 border-t bg-background px-5 py-3">
          <Button
            size="sm"
            variant="outline"
            onClick={() => { reset(); onOpenChange(false); }}
          >
            {t(($) => $.runs.dialog.cancel)}
          </Button>
          <Button
            size="sm"
            disabled={!canSubmit}
            onClick={() => void handleSubmit()}
          >
            {runTemplate.isPending
              ? t(($) => $.runs.dialog.submitting)
              : t(($) => $.runs.dialog.submit)}
          </Button>
        </div>
      </DialogContent>
    </Dialog>
  );
}

/**
 * The control set for one template: the declaration, plus whichever of Title and
 * Description it did not declare.
 *
 * Order is `title`, then the declaration in its authored order, then
 * `description`. Declaration order is authored content - it is the order the
 * properties panel laid the form out AND the order the values are appended to the
 * first agent's prompt - so it is never re-sorted; the two built-in controls
 * bracket it because the title names the work and the description is the long prose
 * box a form ends on.
 *
 * Exported for the dialog's tests, which is the point at which a declaration
 * becomes a submitted body.
 */
export function resolveRunFormFields(
  declared: readonly WorkflowInputField[],
  labels: {
    titleLabel: string;
    titlePlaceholder: string;
    descriptionLabel: string;
    descriptionPlaceholder: string;
  },
): RunFormField[] {
  const fields: RunFormField[] = [];
  const seen = new Set<string>();

  for (const field of declared) {
    // A field with no key has nowhere to store its value (the validator rejects
    // it, and the server's reader looks values up BY key), so rendering it would
    // collect something that is thrown away. A duplicate key is the same problem
    // one step later: two controls writing one slot, where the second silently
    // wins. Both are reported by the editor; here they are simply not collected.
    if (field.key === "" || seen.has(field.key)) continue;
    seen.add(field.key);

    const kind = toWorkflowInputFieldType(field.type);
    fields.push({
      key: field.key,
      // The author's label, falling back to the key - which is what
      // `InputField.DisplayLabel()` does on the server, so the dialog and the
      // agent's prompt name the same field the same way.
      label: field.label || field.key,
      placeholder: field.placeholder,
      kind,
      options: field.options,
      // Title and description are required by the ENDPOINT, not by the
      // declaration, so a declaration marking them optional is overridden here
      // rather than being allowed to enable a body the server will refuse with a
      // 400. Every other field is required exactly as declared.
      required:
        field.key === TITLE_KEY || field.key === DESCRIPTION_KEY
          ? true
          : field.required,
      declared: true,
    });
  }

  if (!seen.has(TITLE_KEY)) {
    fields.unshift({
      key: TITLE_KEY,
      label: labels.titleLabel,
      placeholder: labels.titlePlaceholder,
      kind: "text",
      options: [],
      required: true,
      declared: false,
    });
  }
  if (!seen.has(DESCRIPTION_KEY)) {
    fields.push({
      key: DESCRIPTION_KEY,
      label: labels.descriptionLabel,
      placeholder: labels.descriptionPlaceholder,
      kind: "textarea",
      options: [],
      required: true,
      declared: false,
    });
  }

  return fields;
}

/**
 * The request body, from whichever form produced the values.
 *
 * Both paths emit the same shape, and that is the property that matters: a
 * template whose intake declares `title` and `description` - which the seeded
 * `bug_fix` does - submits a body BYTE-IDENTICAL to the freeform form's, so the
 * first agent's prompt does not change shape for a template that only became
 * self-documenting. `title` and `description` are the two keys
 * `RunWorkflowTemplateRequest` models and `workflow.RunInput` reads back.
 *
 * A declared field beyond those two is sent under its own declared key, flat,
 * because flat-and-keyed-by-the-declared-key is exactly how the run's input JSONB
 * is shaped and how `ParseRunInputFor` looks values up. The endpoint decodes
 * both the standard fields and the raw bag so declared custom values reach the
 * run's independent input snapshot.
 *
 * Values are trimmed, matching the server (`strings.TrimSpace` on both fields), so
 * two submissions differing only in trailing whitespace resolve to one idempotency
 * key rather than to two runs. An empty optional field writes no key at all: a key
 * whose value is `""` is indistinguishable from an unanswered question, and the
 * reader skips it anyway.
 */
export function runRequestBody(
  fields: readonly RunFormField[],
  values: Readonly<Record<string, string>>,
  projectId: string | null,
): RunWorkflowTemplateRequest {
  // The index signature is what lets a declared key that is not one of the three
  // modelled ones be written; `RunWorkflowTemplateRequest` is the contract, and
  // this stays assignable to it.
  const body: RunWorkflowTemplateRequest & Record<string, unknown> = {
    title: "",
    description: "",
    project_id: projectId,
  };

  for (const field of fields) {
    const value = (values[field.key] ?? "").trim();
    if (field.key === TITLE_KEY) {
      body.title = value;
      continue;
    }
    if (field.key === DESCRIPTION_KEY) {
      body.description = value;
      continue;
    }
    if (value === "") continue;
    body[field.key] = value;
  }

  return body;
}

/** The picked project row, or null. Kept as a helper so the trigger reads once. */
function selectedProjectOf<T extends { id: string }>(
  projects: readonly T[],
  projectId: string | null,
): T | null {
  if (projectId === null) return null;
  return projects.find((project) => project.id === projectId) ?? null;
}

/**
 * One collected value.
 *
 * The three kinds are three different questions: `text` is a line, `textarea` is
 * prose the agent reads as the brief, and `select` is one of a set the downstream
 * steps were written against. A kind this build has never heard of renders as
 * `text` (see `toWorkflowInputFieldType`) - it accepts any string, so the human can
 * still answer and the run can still start, where refusing to render would make a
 * template the server considers valid unrunnable.
 */
export function RunFormControl({
  field,
  value,
  disabled,
  problem,
  hint,
  onChange,
  onBlur,
  selectPlaceholder,
}: {
  field: RunFormField;
  value: string;
  disabled: boolean;
  problem?: string;
  hint?: string;
  onChange(next: string): void;
  onBlur(): void;
  selectPlaceholder: string;
}) {
  // The asterisk is `aria-hidden` and the requirement is carried by
  // `aria-required` on the control instead. A visible "*" inside the label
  // element would be concatenated into the control's accessible NAME -
  // "Severity*" - which is the field's identity being corrupted by a property
  // of it, and it is the name a screen reader reads back on every focus.
  const label = (
    <span className="text-caption font-medium">
      {field.label}
      {field.required ? (
        <span aria-hidden="true" className="ml-0.5 text-destructive">
          *
        </span>
      ) : null}
    </span>
  );

  // The hint and the problem are SIBLINGS of the <label>, never inside it.
  // A <label> associates its control implicitly by containment, and everything it
  // contains becomes part of that control's accessible NAME - so a hint sentence
  // nested here would make the description field's name "Description Every agent
  // step gets its own instruction plus this text...", which is what a screen
  // reader would read on every focus. Same reason the required marker is
  // aria-hidden above.
  const messages = (
    <>
      {hint ? (
        <span className="text-caption leading-snug text-muted-foreground">
          {hint}
        </span>
      ) : null}
      {problem ? (
        <span role="alert" className="text-caption leading-snug text-destructive">
          {problem}
        </span>
      ) : null}
    </>
  );

  // A select renders a button, not a labelable control, so it carries an
  // aria-label and sits in a div - the same split panel-controls.tsx makes
  // between PanelField and PanelSelectField.
  if (field.kind === "select") {
    return (
      <div className="flex flex-col gap-1.5">
        {label}
        <Select<string>
          items={field.options.map((option) => ({
            value: option,
            label: option,
          }))}
          value={value}
          disabled={disabled}
          onValueChange={(next) => {
            if (next === null) return;
            onChange(next);
            // A select has no meaningful blur before a choice is made (opening the
            // popup moves focus), so choosing is what marks it touched.
            onBlur();
          }}
        >
          <SelectTrigger
            aria-label={field.label}
            aria-required={field.required}
            aria-invalid={problem !== undefined}
            className="w-full min-w-0"
            // Also on the trigger, not only on a choice: tabbing past a required
            // select without answering is exactly the case where the submitter
            // needs to be told it is still empty, and `onValueChange` never fires
            // for a field nobody touched.
            onBlur={onBlur}
          >
            <SelectValue placeholder={field.placeholder || selectPlaceholder} />
          </SelectTrigger>
          <SelectContent>
            {field.options.map((option) => (
              <SelectItem key={option} value={option}>
                {option}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        {messages}
      </div>
    );
  }

  return (
    <div className="flex flex-col gap-1.5">
      <label className="flex flex-col gap-1.5">
        {label}
        {field.kind === "textarea" ? (
          <Textarea
            value={value}
            disabled={disabled}
            rows={8}
            className="min-h-40 resize-y"
            required={field.required}
            aria-invalid={problem !== undefined}
            placeholder={field.placeholder}
            onChange={(event) => onChange(event.target.value)}
            onBlur={onBlur}
          />
        ) : (
          <Input
            value={value}
            disabled={disabled}
            required={field.required}
            aria-invalid={problem !== undefined}
            placeholder={field.placeholder}
            onChange={(event) => onChange(event.target.value)}
            onBlur={onBlur}
          />
        )}
      </label>
      {messages}
    </div>
  );
}
