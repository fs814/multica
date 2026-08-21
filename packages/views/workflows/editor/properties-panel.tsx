"use client";

import { Plus, Trash2 } from "lucide-react";
import type {
  WorkflowBranch,
  WorkflowDefinition,
  WorkflowNode,
} from "@multica/core/workflows";
import { Badge } from "@multica/ui/components/ui/badge";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { isWorkflowNodeType, legalReworkTargetNodes } from "../graph";
import { useT } from "../../i18n";
import { AgentRoutingSection } from "./agent-routing-fields";
import { InputFieldsSection } from "./input-fields-section";
import {
  PanelCheckList,
  PanelField,
  PanelHint,
  PanelNotice,
  PanelProblem,
  PanelSection,
  PanelSelect,
  PanelSelectField,
  type PanelOption,
} from "./panel-controls";

/**
 * The graph editor's right-hand inspector: everything about the selected node
 * that is *not* an edge.
 *
 * Three rules shape the whole file.
 *
 * **1. Edges belong to the canvas, not to this panel.** `next`, `branches`
 * targets and `rework_targets` are all drawn as connections, and the model's
 * `graphToDefinition` reconstructs those three fields from the canvas edge list
 * (see graph/from-graph.ts) - it overrides whatever the node object carried. So
 * a `next` editor here would appear to work and then be silently discarded on
 * save. The two exceptions are deliberate and are the fields where the *set
 * membership* is the semantics rather than the drawing: `rework_targets` and a
 * branch's verdict. Editing a rework target here writes the node's array, the
 * canvas re-derives its rework edges from it, and the round trip closes.
 *
 * **2. The panel makes invalid graphs hard to express, and names the rest.**
 * Where the validator's rule can be encoded in the control - only agent nodes in
 * the `from_node` picker, at most one default branch, never routing on an
 * acceptance node - it is. Where it cannot (an acceptance node needs at least one
 * rework target, and the author may not have decided yet), the requirement is
 * stated in place rather than left for the validate button to discover, because
 * the fix is here and the button is at the other end of the page.
 *
 * **3. Only the fields the panel renders are touched.** Every edit is
 * `{...node, field: value}`, so `join_sources`, `max_attempts`, `fan_out_max`
 * and any key a newer server added ride through untouched - the same
 * spread-then-override rule that makes the graph round trip lossless. The JSON
 * view is the escape hatch for the rest.
 *
 * ## Why `key` is read-only after creation
 *
 * A node key is referenced by five *other* places: every other node's `next`,
 * `branches[].target`, `rework_targets`, `join_sources`, and the graph's
 * `entry_node`. The frozen `onChange(next: WorkflowNode)` signature can emit
 * exactly one node, so a rename here could not rewrite any of them - the save
 * would land a graph full of dangling references, and the validator would blame
 * the node the author never touched. Widening the callback to emit a whole
 * definition was the alternative, but the key is also an external identifier
 * (intake resolves templates and steps by key, plan section 9) and a Step already
 * pinned by a running Run records the key it ran, so renaming is not a rename -
 * it is "delete a node and add another one with the same box on screen".
 * Presenting it as an inert value with an explanation is honest about that; the
 * JSON view remains available for an author who really does want to restructure
 * a draft graph wholesale.
 */

/** Mirrors `DefaultSchemaRegistry` in server/internal/workflow. */
const SUBMISSION_SCHEMAS = [
  "analysis",
  "code_change",
  "test_report",
  "review_report",
  "generic",
] as const;

/** Mirrors `FailurePolicy`; `""` is the wire form of the server's default. */
const FAILURE_POLICIES = ["fail", "block", "rework"] as const;

/** Mirrors `validVerdicts`; a branch with no verdict is the default branch. */
const VERDICTS = ["pass", "fail", "blocked"] as const;

/**
 * Node kinds that can report a failure, and therefore have an `on_failure`.
 *
 * `condition` routes on a verdict another node already produced and `end` is
 * terminal, so neither has a failure of its own to police - the validator does
 * accept a policy on them, but offering one would suggest a behaviour the engine
 * has no place to run.
 */
const FAILABLE_TYPES = new Set(["agent", "acceptance", "fan_out", "join"]);

export function WorkflowPropertiesPanel({
  node,
  definition,
  readOnly,
  onChange,
}: {
  node: WorkflowNode | null;
  definition: WorkflowDefinition;
  readOnly: boolean;
  onChange(next: WorkflowNode): void;
}) {
  const { t } = useT("workflows");

  return (
    <aside
      className="flex w-80 shrink-0 flex-col overflow-y-auto border-l border-border bg-background"
      aria-label={t(($) => $.panel.aria)}
    >
      {node === null ? (
        <PanelNotice
          title={t(($) => $.panel.empty.title)}
          hint={t(($) => $.panel.empty.hint)}
        />
      ) : (
        <PanelBody
          node={node}
          definition={definition}
          readOnly={readOnly}
          onChange={onChange}
        />
      )}
    </aside>
  );
}

function PanelBody({
  node,
  definition,
  readOnly,
  onChange,
}: {
  node: WorkflowNode;
  definition: WorkflowDefinition;
  readOnly: boolean;
  onChange(next: WorkflowNode): void;
}) {
  const { t } = useT("workflows");

  // A node kind this build has never heard of gets identity plus a notice and
  // nothing else. Rendering the generic fields would be worse than refusing:
  // the fields a *newer* server attached to that kind are exactly the ones this
  // build cannot see, so an edit made through them would look complete and save
  // half a node. Same rule as the canvas's read-only `unknown` renderer.
  const known = isWorkflowNodeType(node.type);

  const showRouting = node.type === "agent";
  const showFailure = FAILABLE_TYPES.has(node.type);
  // Rework targets are surfaced exactly where the validator can demand them, so
  // the control appears in the same moment the requirement does.
  const showReworkTargets =
    node.type === "acceptance" ||
    (showFailure && node.on_failure === "rework") ||
    (node.type === "join" && node.join_policy === "rework");

  return (
    <>
      <BasicSection
        node={node}
        definition={definition}
        readOnly={readOnly}
        onChange={onChange}
      />

      {!known ? (
        <PanelNotice
          title={t(($) => $.panel.unknown.title, { type: node.type })}
          hint={t(($) => $.panel.unknown.hint)}
        />
      ) : node.type === "end" ? (
        // End is terminal and carries no routing, no prompt and no failure
        // policy. Saying so beats four empty sections an author would read as a
        // rendering bug.
        <PanelNotice
          title={t(($) => $.panel.end.title)}
          hint={t(($) => $.panel.end.hint)}
        />
      ) : node.type === "input" ? (
        // An input node's whole configuration is its field declaration plus its
        // instruction. It carries no routing, no submission schema and no rework
        // targets - and those are not "not implemented yet", they are rules the
        // validator enforces (intake is filled in by a human, never dispatches an
        // Agent Task, never submits, and cannot be sent back to because the engine
        // cannot re-prompt a human mid-run). Saying so beats three empty sections
        // an author would read as a rendering bug and then hunt for in the JSON.
        <>
          <InputFieldsSection
            node={node}
            readOnly={readOnly}
            onChange={onChange}
          />
          {/* The instruction is kept: on an input node it is the note the *author*
              leaves about what the intake is for, and it still rides into the
              definition untouched. It is the only free-text field the node has. */}
          <PromptSection node={node} readOnly={readOnly} onChange={onChange} />
          <PanelNotice
            title={t(($) => $.panel.input.title)}
            hint={t(($) => $.panel.input.hint)}
          />
        </>
      ) : (
        <>
          {showRouting ? (
            <AgentRoutingSection
              node={node}
              definition={definition}
              readOnly={readOnly}
              onChange={onChange}
            />
          ) : null}

          <PromptSection node={node} readOnly={readOnly} onChange={onChange} />

          {node.type === "agent" ? (
            <SubmissionSection
              node={node}
              readOnly={readOnly}
              onChange={onChange}
            />
          ) : null}

          {node.type === "acceptance" ? (
            <AcceptanceCriteriaSection
              node={node}
              readOnly={readOnly}
              onChange={onChange}
            />
          ) : null}

          {node.type === "condition" ? (
            <BranchesSection
              node={node}
              definition={definition}
              readOnly={readOnly}
              onChange={onChange}
            />
          ) : null}

          {showFailure ? (
            <FailureSection
              node={node}
              definition={definition}
              readOnly={readOnly}
              onChange={onChange}
              showReworkTargets={showReworkTargets}
            />
          ) : null}
        </>
      )}
    </>
  );
}

// ---------------------------------------------------------------------------
// 基本信息 - basic info
// ---------------------------------------------------------------------------

function BasicSection({
  node,
  definition,
  readOnly,
  onChange,
}: {
  node: WorkflowNode;
  definition: WorkflowDefinition;
  readOnly: boolean;
  onChange(next: WorkflowNode): void;
}) {
  const { t } = useT("workflows");
  const typeLabel = useNodeTypeLabel(node.type);
  const isEntry = definition.entry_node === node.key && node.key !== "";

  return (
    <PanelSection title={t(($) => $.panel.section.basic)}>
      {/* Rendered as a value, not a disabled input: a greyed-out text box invites
          the author to keep clicking it. See the file header for why renaming is
          not offered here. */}
      <div className="flex flex-col gap-1.5">
        <span className="text-caption font-medium">
          {t(($) => $.panel.basic.key)}
        </span>
        <div className="flex items-center gap-2">
          <code className="min-w-0 flex-1 truncate rounded-lg border border-input bg-muted/40 px-2.5 py-1.5 font-mono text-caption text-muted-foreground">
            {node.key}
          </code>
          {isEntry ? (
            <Badge variant="secondary" className="shrink-0">
              {t(($) => $.panel.basic.entry_badge)}
            </Badge>
          ) : null}
        </div>
        <PanelHint>{t(($) => $.panel.basic.key_locked_hint)}</PanelHint>
      </div>

      <PanelField label={t(($) => $.panel.basic.name)}>
        <Input
          value={node.name}
          disabled={readOnly}
          placeholder={t(($) => $.panel.basic.name_placeholder)}
          onChange={(event) => onChange({ ...node, name: event.target.value })}
        />
      </PanelField>

      <div className="flex flex-col gap-1.5">
        <span className="text-caption font-medium">
          {t(($) => $.panel.basic.type)}
        </span>
        <span className="text-body">{typeLabel}</span>
      </div>
    </PanelSection>
  );
}

/**
 * Human label for a node type, reusing the viewer's existing `detail.node_type`
 * strings rather than adding a second set. Two labels for one concept would
 * eventually disagree, and the canvas badge, the read-only viewer and this row
 * all name the same thing.
 */
function useNodeTypeLabel(type: string): string {
  const { t } = useT("workflows");
  const labels: Record<string, string> = {
    input: t(($) => $.detail.node_type.input),
    agent: t(($) => $.detail.node_type.agent),
    condition: t(($) => $.detail.node_type.condition),
    fan_out: t(($) => $.detail.node_type.fan_out),
    join: t(($) => $.detail.node_type.join),
    acceptance: t(($) => $.detail.node_type.acceptance),
    end: t(($) => $.detail.node_type.end),
  };
  // An unrecognised kind shows its raw token: "Unknown" alone would hide the one
  // piece of information an author could act on.
  return labels[type] ?? type;
}

// ---------------------------------------------------------------------------
// 提示词配置 - prompt config
// ---------------------------------------------------------------------------

/**
 * The node's instruction.
 *
 * This doubles as the step's description because the graph contract has no
 * separate one: `ParseDefinition` rejects unknown fields, so a `description` key
 * invented here would make every save fail with "definition is not valid JSON"
 * rather than being ignored. The instruction is also the field that actually
 * reaches the agent, so splitting the author's intent across two boxes - one
 * executed, one decorative - would be the worse of the two shapes.
 */
function PromptSection({
  node,
  readOnly,
  onChange,
}: {
  node: WorkflowNode;
  readOnly: boolean;
  onChange(next: WorkflowNode): void;
}) {
  const { t } = useT("workflows");

  return (
    <PanelSection title={t(($) => $.panel.section.prompt)}>
      <PanelField
        label={t(($) => $.panel.prompt.instruction)}
        hint={t(($) => $.panel.prompt.instruction_hint)}
      >
        <Textarea
          value={node.instruction}
          disabled={readOnly}
          rows={6}
          placeholder={t(($) => $.panel.prompt.instruction_placeholder)}
          onChange={(event) =>
            onChange({ ...node, instruction: event.target.value })
          }
        />
      </PanelField>
    </PanelSection>
  );
}

// ---------------------------------------------------------------------------
// Submission schema
// ---------------------------------------------------------------------------

function SubmissionSection({
  node,
  readOnly,
  onChange,
}: {
  node: WorkflowNode;
  readOnly: boolean;
  onChange(next: WorkflowNode): void;
}) {
  const { t } = useT("workflows");

  const labels: Record<(typeof SUBMISSION_SCHEMAS)[number], string> = {
    analysis: t(($) => $.panel.submission.analysis),
    code_change: t(($) => $.panel.submission.code_change),
    test_report: t(($) => $.panel.submission.test_report),
    review_report: t(($) => $.panel.submission.review_report),
    generic: t(($) => $.panel.submission.generic),
  };

  const options: PanelOption[] = [
    { value: "", label: t(($) => $.panel.submission.none) },
    ...SUBMISSION_SCHEMAS.map((schema) => ({
      value: schema,
      label: labels[schema],
    })),
  ];
  // A registry entry a newer server added is offered back verbatim, for the same
  // reason as an unknown routing strategy: otherwise merely selecting the node
  // would clear a schema the server accepts.
  if (
    node.submission_schema !== "" &&
    !(SUBMISSION_SCHEMAS as readonly string[]).includes(node.submission_schema)
  ) {
    options.push({
      value: node.submission_schema,
      label: t(($) => $.panel.submission.unknown, {
        schema: node.submission_schema,
      }),
    });
  }

  return (
    <PanelSection title={t(($) => $.panel.section.submission)}>
      <PanelSelectField
        label={t(($) => $.panel.submission.label)}
        hint={t(($) => $.panel.submission.hint)}
      >
        <PanelSelect
          value={node.submission_schema}
          options={options}
          onChange={(next) => onChange({ ...node, submission_schema: next })}
          disabled={readOnly}
          ariaLabel={t(($) => $.panel.submission.aria)}
        />
      </PanelSelectField>
    </PanelSection>
  );
}

// ---------------------------------------------------------------------------
// 验收 - acceptance criteria
// ---------------------------------------------------------------------------

/**
 * One criterion per line, added and removed explicitly.
 *
 * Not a single newline-separated textarea, even though that is less code: each
 * criterion is a separate array element the reviewer evaluates one at a time, and
 * a textarea would make "blank line" and "criterion that happens to be empty"
 * indistinguishable - the difference between a graph that means what it says and
 * one that ships an unevaluatable empty rule.
 */
function AcceptanceCriteriaSection({
  node,
  readOnly,
  onChange,
}: {
  node: WorkflowNode;
  readOnly: boolean;
  onChange(next: WorkflowNode): void;
}) {
  const { t } = useT("workflows");
  const criteria = node.acceptance_criteria;

  const replace = (next: string[]) =>
    onChange({ ...node, acceptance_criteria: next });

  return (
    <PanelSection title={t(($) => $.panel.section.acceptance)}>
      {criteria.length === 0 ? (
        <PanelHint>{t(($) => $.panel.acceptance.empty)}</PanelHint>
      ) : (
        <ul className="flex flex-col gap-1.5">
          {criteria.map((criterion, index) => (
            // Index key: the rows are positional (a criterion's identity *is*
            // its position in the array), and keying by text would remount the
            // input the author is typing into on every keystroke.
            <li key={index} className="flex items-start gap-1.5">
              <Input
                value={criterion}
                disabled={readOnly}
                aria-label={t(($) => $.panel.acceptance.criterion_aria, {
                  index: index + 1,
                })}
                placeholder={t(($) => $.panel.acceptance.placeholder)}
                onChange={(event) =>
                  replace(
                    criteria.map((existing, at) =>
                      at === index ? event.target.value : existing,
                    ),
                  )
                }
              />
              {!readOnly ? (
                <Button
                  type="button"
                  variant="ghost"
                  size="icon-sm"
                  aria-label={t(($) => $.panel.acceptance.remove_aria, {
                    index: index + 1,
                  })}
                  onClick={() =>
                    replace(criteria.filter((_, at) => at !== index))
                  }
                >
                  <Trash2 className="size-3.5" aria-hidden="true" />
                </Button>
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
          onClick={() => replace([...criteria, ""])}
        >
          <Plus className="size-3.5" aria-hidden="true" />
          {t(($) => $.panel.acceptance.add)}
        </Button>
      ) : null}
    </PanelSection>
  );
}

// ---------------------------------------------------------------------------
// Condition branches
// ---------------------------------------------------------------------------

/**
 * The branch editor.
 *
 * Verdict and target are edited together because a branch is the pair: the
 * canvas draws the edge and labels it, but which verdict fires it is not
 * something a dragged connection can express. `branches[].target` is still
 * canvas-owned on save (see the file header), so the target select here is the
 * second way to say the same thing rather than the only one - which is why it
 * offers every declared node and reports a dangling target instead of hiding it.
 */
function BranchesSection({
  node,
  definition,
  readOnly,
  onChange,
}: {
  node: WorkflowNode;
  definition: WorkflowDefinition;
  readOnly: boolean;
  onChange(next: WorkflowNode): void;
}) {
  const { t } = useT("workflows");
  const branches = node.branches;

  const replace = (next: WorkflowBranch[]) =>
    onChange({ ...node, branches: next });

  const verdictLabels: Record<(typeof VERDICTS)[number], string> = {
    pass: t(($) => $.panel.branches.verdict_pass),
    fail: t(($) => $.panel.branches.verdict_fail),
    blocked: t(($) => $.panel.branches.verdict_blocked),
  };

  const defaultCount = branches.filter(
    (branch) => branch.when_verdict === "",
  ).length;

  const targetOptions = (target: string): PanelOption[] => {
    const options: PanelOption[] = [
      { value: "", label: t(($) => $.panel.branches.target_unset) },
      ...definition.nodes.map((candidate) => ({
        value: candidate.key,
        label: candidate.name || candidate.key,
      })),
    ];
    if (
      target !== "" &&
      !definition.nodes.some((candidate) => candidate.key === target)
    ) {
      options.push({
        value: target,
        label: t(($) => $.panel.branches.target_missing, { key: target }),
      });
    }
    return options;
  };

  return (
    <PanelSection title={t(($) => $.panel.section.branches)}>
      {branches.length === 0 ? (
        // Not a hint: a condition with no branches is a graph the server will
        // reject, not an unfinished detail.
        <PanelProblem>{t(($) => $.panel.branches.required)}</PanelProblem>
      ) : (
        <ul className="flex flex-col gap-3">
          {branches.map((branch, index) => {
            const isDefault = branch.when_verdict === "";
            const verdictOptions: PanelOption[] = [
              {
                value: "",
                label: t(($) => $.panel.branches.verdict_default),
                // At most one default branch. Disabling the option on the other
                // rows enforces it in the control; letting the author pick a
                // second one and then reporting it would make them undo an edit
                // the panel could have refused.
                disabled: !isDefault && defaultCount > 0,
              },
              ...VERDICTS.map((verdict) => ({
                value: verdict,
                label: verdictLabels[verdict],
              })),
            ];
            if (
              branch.when_verdict !== "" &&
              !(VERDICTS as readonly string[]).includes(branch.when_verdict)
            ) {
              verdictOptions.push({
                value: branch.when_verdict,
                label: t(($) => $.panel.branches.verdict_unknown, {
                  verdict: branch.when_verdict,
                }),
              });
            }

            const patch = (next: Partial<WorkflowBranch>) =>
              replace(
                branches.map((existing, at) =>
                  // Spread the existing branch so a forward-compatible key a
                  // newer server put on it survives being edited here.
                  at === index ? { ...existing, ...next } : existing,
                ),
              );

            return (
              <li
                key={index}
                className="flex flex-col gap-1.5 rounded-lg border border-input p-2"
              >
                <div className="flex items-center gap-1.5">
                  <PanelSelect
                    value={branch.when_verdict}
                    options={verdictOptions}
                    onChange={(next) => patch({ when_verdict: next })}
                    disabled={readOnly}
                    ariaLabel={t(($) => $.panel.branches.verdict_aria, {
                      index: index + 1,
                    })}
                  />
                  {!readOnly ? (
                    <Button
                      type="button"
                      variant="ghost"
                      size="icon-sm"
                      className="shrink-0"
                      aria-label={t(($) => $.panel.branches.remove_aria, {
                        index: index + 1,
                      })}
                      onClick={() =>
                        replace(branches.filter((_, at) => at !== index))
                      }
                    >
                      <Trash2 className="size-3.5" aria-hidden="true" />
                    </Button>
                  ) : null}
                </div>
                <PanelSelect
                  value={branch.target}
                  options={targetOptions(branch.target)}
                  onChange={(next) => patch({ target: next })}
                  disabled={readOnly}
                  ariaLabel={t(($) => $.panel.branches.target_aria, {
                    index: index + 1,
                  })}
                />
                {branch.target === "" ? (
                  <PanelProblem>
                    {t(($) => $.panel.branches.target_required)}
                  </PanelProblem>
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
          onClick={() =>
            // A new branch defaults to the `pass` verdict rather than to the
            // default branch: `""` may already be taken, and a second default is
            // the one branch shape the validator rejects outright.
            replace([...branches, { when_verdict: "pass", target: "" }])
          }
        >
          <Plus className="size-3.5" aria-hidden="true" />
          {t(($) => $.panel.branches.add)}
        </Button>
      ) : null}
    </PanelSection>
  );
}

// ---------------------------------------------------------------------------
// on_failure + rework targets
// ---------------------------------------------------------------------------

function FailureSection({
  node,
  definition,
  readOnly,
  onChange,
  showReworkTargets,
}: {
  node: WorkflowNode;
  definition: WorkflowDefinition;
  readOnly: boolean;
  onChange(next: WorkflowNode): void;
  showReworkTargets: boolean;
}) {
  const { t } = useT("workflows");

  const policyLabels: Record<(typeof FAILURE_POLICIES)[number], string> = {
    fail: t(($) => $.panel.failure.fail),
    block: t(($) => $.panel.failure.block),
    rework: t(($) => $.panel.failure.rework),
  };

  const options: PanelOption[] = [
    // `""` is offered as its own choice rather than folded into "block": the
    // stored value decides whether a later change to the server default follows
    // this graph or is pinned out of it, and that is the author's call.
    { value: "", label: t(($) => $.panel.failure.unset) },
    ...FAILURE_POLICIES.map((policy) => ({
      value: policy,
      label: policyLabels[policy],
    })),
  ];
  if (
    node.on_failure !== "" &&
    !(FAILURE_POLICIES as readonly string[]).includes(node.on_failure)
  ) {
    options.push({
      value: node.on_failure,
      label: t(($) => $.panel.failure.unknown, { policy: node.on_failure }),
    });
  }

  const candidates = legalReworkTargetNodes(definition, node.key);
  const candidateKeys = new Set(candidates.map((candidate) => candidate.key));
  // A hand-edited or older draft may already contain a target this build would
  // no longer offer. Keep it visible and removable instead of hiding the only
  // control that can repair it; it is never offered unchecked.
  const invalidSelected = node.rework_targets.filter(
    (target) => !candidateKeys.has(target),
  );
  const byKey = new Map(
    definition.nodes.map((candidate) => [candidate.key, candidate] as const),
  );
  const targetOptions: PanelOption[] = [
    ...candidates.map((candidate) => ({
      value: candidate.key,
      label: candidate.name || candidate.key,
    })),
    ...invalidSelected.map((target) => ({
      value: target,
      label: t(($) => $.panel.failure.rework_targets_invalid_option, {
        name: byKey.get(target)?.name || target,
      }),
    })),
  ];

  const missing =
    showReworkTargets &&
    !node.rework_targets.some((target) => candidateKeys.has(target));
  const targetProblem =
    invalidSelected.length > 0
      ? t(($) => $.panel.failure.rework_targets_invalid, {
          targets: invalidSelected.join(", "),
        })
      : missing
        ? node.type === "acceptance"
          ? t(($) => $.panel.failure.rework_targets_required_acceptance)
          : t(($) => $.panel.failure.rework_targets_required)
        : undefined;

  return (
    <PanelSection title={t(($) => $.panel.section.failure)}>
      <PanelSelectField
        label={t(($) => $.panel.failure.label)}
        hint={t(($) => $.panel.failure.hint)}
      >
        <PanelSelect
          value={node.on_failure}
          options={options}
          // Choosing `rework` does not pre-select a target: which step the work
          // goes back to is a design decision, and guessing (the entry node? the
          // previous one?) would author a cycle in the author's name.
          onChange={(next) => onChange({ ...node, on_failure: next })}
          disabled={readOnly}
          ariaLabel={t(($) => $.panel.failure.aria)}
        />
      </PanelSelectField>

      {showReworkTargets ? (
        <PanelSelectField
          label={t(($) => $.panel.failure.rework_targets)}
          hint={t(($) => $.panel.failure.rework_targets_hint)}
          problem={targetProblem}
        >
          <PanelCheckList
            options={targetOptions}
            selected={node.rework_targets}
            onToggle={(value, checked) =>
              onChange({
                ...node,
                rework_targets: checked
                  ? [...node.rework_targets, value]
                  : node.rework_targets.filter((target) => target !== value),
              })
            }
            disabled={readOnly}
            emptyLabel={t(($) => $.panel.failure.rework_targets_empty)}
            ariaLabel={t(($) => $.panel.failure.rework_targets_aria)}
          />
        </PanelSelectField>
      ) : null}
    </PanelSection>
  );
}
