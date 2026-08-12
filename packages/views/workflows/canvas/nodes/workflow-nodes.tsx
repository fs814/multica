"use client";

/**
 * The seven node renderers, plus the `unknown` fallback.
 *
 * They live in one file because each is the shared card (workflow-node-card.tsx)
 * plus one decision: which field answers "what does this step do?". Splitting
 * eight eight-line components across eight files would hide the thing worth
 * reviewing - that the eight summary lines are the eight different behaviours -
 * behind eight identical import blocks.
 *
 * Every component is `memo`ised. xyflow re-renders the node layer on viewport
 * changes and on any node's selection, so an un-memoised card would re-render on
 * every pan.
 */

import { memo } from "react";
import { Handle, Position, type NodeProps } from "@xyflow/react";
import {
  Asterisk,
  CircleCheckBig,
  Flag,
  GitBranch,
  Merge,
  Split,
} from "lucide-react";
import { api } from "@multica/core/api";
import { attachmentDownloadPath } from "@multica/core/types";
import { resolvePublicFileUrlWithBase } from "@multica/core/workspace/avatar-url";
import { useT } from "../../../i18n";
import { toWorkflowInputFieldType, type FlowNode } from "../../graph";
import { RoutingSummary } from "./routing-summary";
import { WorkflowNodeCard, WorkflowNodeSummary } from "./workflow-node-card";

// ---------------------------------------------------------------------------
// input
// ---------------------------------------------------------------------------

/**
 * The intake card: where work enters the graph.
 *
 * Unlike every other kind, this one lists its *contents* rather than summarising
 * one field. The question an author has looking at a template is "what does this
 * workflow take?", and before input nodes existed the answer lived only in the Run
 * dialog's hardcoded Title + Description - invisible on the canvas. Printing the
 * declared keys is the entire reason this node type is drawn at all, so a
 * truncating one-liner ("3 fields") would defeat it.
 *
 * The declaration is capped at four rows with a "+n more" tail. A card is a fixed
 * 240px wide (see WorkflowNodeCard) sitting in a layered layout, so an
 * unbounded list would push a long form's card past its neighbours' rows and make
 * the graph unreadable - the properties panel is where the full list is edited.
 */
const MAX_LISTED_FIELDS = 4;

export const InputNode = memo(function InputNode({
  data,
  selected,
}: NodeProps<FlowNode>) {
  const { t } = useT("workflows");
  const fields = data.node.input_fields;
  const imageMode = data.node.input_mode === "image";
  const imageAttachmentId = data.node.image_attachment_id ?? "";
  const imagePath =
    imageAttachmentId === "" ? null : attachmentDownloadPath(imageAttachmentId);
  const imageUrl =
    imagePath === null
      ? null
      : (resolvePublicFileUrlWithBase(imagePath, api.getBaseUrl?.() ?? "") ??
        imagePath);
  const shown = fields.slice(0, MAX_LISTED_FIELDS);
  const hidden = fields.length - shown.length;

  return (
    <WorkflowNodeCard
      data={data}
      selected={selected}
      typeLabel={t(($) => $.canvas.node_type.input)}
      summary={
        <WorkflowNodeSummary icon={Asterisk}>
          {/* An empty declaration is legal - the node documents where work enters
              and the Run dialog falls back to the freeform pair - so this reads as
              a statement rather than as a problem. */}
          {imageMode
            ? t(($) => $.canvas.summary.input_image)
            : fields.length === 0
              ? t(($) => $.canvas.summary.input_freeform)
              : t(($) => $.canvas.summary.input_fields, {
                  count: fields.length,
                })}
        </WorkflowNodeSummary>
      }
      body={
        imageMode ? (
          imageUrl ? (
            <img
              src={imageUrl}
              alt={t(($) => $.canvas.summary.input_image_alt)}
              className="h-36 w-full rounded-md bg-muted/30 object-contain"
            />
          ) : (
            <p className="text-xs text-destructive">
              {t(($) => $.canvas.summary.input_image_missing)}
            </p>
          )
        ) : fields.length === 0 ? null : (
          <ul className="flex flex-col gap-0.5">
            {shown.map((field, index) => (
              // Index key: the rows are positional, exactly like the properties
              // panel's field list, and a duplicate key is a graph the validator
              // rejects but the canvas still has to draw.
              <li
                key={index}
                className="flex min-w-0 items-center gap-1 text-[10px] text-muted-foreground"
              >
                <span className="min-w-0 truncate">
                  {field.label || field.key}
                </span>
                {/* The type token, because "textarea" vs "select" is what tells an
                    author whether this asks for prose or for one of a fixed set -
                    which is the difference the downstream prompt is written
                    against. Narrowed, so a kind from a newer server reads as the
                    text input the dialog will actually render. */}
                <code className="shrink-0 font-mono opacity-60">
                  {toWorkflowInputFieldType(field.type)}
                </code>
                {/* Required is marked, not spelled out: it is the one property
                    that decides whether a run can start without the value, and it
                    has to survive at 10px. */}
                {field.required ? (
                  <span
                    className="shrink-0 text-rose-500"
                    aria-label={t(($) => $.canvas.summary.input_required_aria)}
                  >
                    *
                  </span>
                ) : null}
              </li>
            ))}
            {hidden > 0 ? (
              <li className="text-[10px] text-muted-foreground/70">
                {t(($) => $.canvas.summary.input_more, { count: hidden })}
              </li>
            ) : null}
          </ul>
        )
      }
      // No target handle at all. An input node must BE the entry node (the
      // validator rejects it anywhere else), so an incoming port could only ever
      // produce a graph the server refuses - and would suggest that a step can run
      // before the human has supplied the input it works from.
      targetHandle={<></>}
    />
  );
});

// ---------------------------------------------------------------------------
// agent
// ---------------------------------------------------------------------------

export const AgentNode = memo(function AgentNode({
  data,
  selected,
}: NodeProps<FlowNode>) {
  const { t } = useT("workflows");
  return (
    <WorkflowNodeCard
      data={data}
      selected={selected}
      typeLabel={t(($) => $.canvas.node_type.agent)}
      summary={<RoutingSummary node={data.node} />}
    />
  );
});

// ---------------------------------------------------------------------------
// condition
// ---------------------------------------------------------------------------

/**
 * One source handle per branch.
 *
 * The handle ids are the branch *index* (`branch-0`, `branch-1`, ...) rather than
 * the verdict, because a verdict is not unique: a malformed graph can declare two
 * `pass` branches, and the validator's job is to complain about that - the canvas
 * has to be able to draw it first. Indices also keep the ports stable while the
 * author is still choosing a verdict for a branch they just added.
 *
 * A condition with no branches still gets one port so the author has something to
 * drag from; the validator reports the missing branch.
 */
function ConditionHandles({ count }: { count: number }) {
  const ports = Math.max(count, 1);
  return (
    <>
      {Array.from({ length: ports }, (_, index) => (
        <Handle
          key={index}
          id={`branch-${index}`}
          type="source"
          position={Position.Right}
          // Evenly spaced down the right edge. Percentages rather than pixels so
          // the ports stay distributed however tall the card grows.
          style={{ top: `${((index + 1) / (ports + 1)) * 100}%` }}
        />
      ))}
    </>
  );
}

export const ConditionNode = memo(function ConditionNode({
  data,
  selected,
}: NodeProps<FlowNode>) {
  const { t } = useT("workflows");
  const count = data.node.branches?.length ?? 0;
  return (
    <WorkflowNodeCard
      data={data}
      selected={selected}
      typeLabel={t(($) => $.canvas.node_type.condition)}
      summary={
        <WorkflowNodeSummary icon={GitBranch}>
          {t(($) => $.canvas.summary.branches, { count })}
        </WorkflowNodeSummary>
      }
      sourceHandles={<ConditionHandles count={count} />}
    />
  );
});

// ---------------------------------------------------------------------------
// acceptance
// ---------------------------------------------------------------------------

export const AcceptanceNode = memo(function AcceptanceNode({
  data,
  selected,
}: NodeProps<FlowNode>) {
  const { t } = useT("workflows");
  // Criteria count, not routing: an acceptance step has no agent of its own, and
  // "how many things are being checked" is the field that decides whether the
  // step is meaningful. Zero criteria means a reviewer with nothing to check.
  const count = data.node.acceptance_criteria?.length ?? 0;
  return (
    <WorkflowNodeCard
      data={data}
      selected={selected}
      typeLabel={t(($) => $.canvas.node_type.acceptance)}
      summary={
        <WorkflowNodeSummary icon={CircleCheckBig}>
          {t(($) => $.canvas.summary.criteria, { count })}
        </WorkflowNodeSummary>
      }
    />
  );
});

// ---------------------------------------------------------------------------
// fan_out / join
// ---------------------------------------------------------------------------

export const FanOutNode = memo(function FanOutNode({
  data,
  selected,
}: NodeProps<FlowNode>) {
  const { t } = useT("workflows");
  const max = data.node.fan_out_max ?? 0;
  return (
    <WorkflowNodeCard
      data={data}
      selected={selected}
      typeLabel={t(($) => $.canvas.node_type.fan_out)}
      summary={
        <WorkflowNodeSummary icon={Split}>
          {/* 0 means "inherit the workspace ceiling" (Definition.EffectiveLimits
              has no unlimited sentinel), so it must not render as "max 0". */}
          {max > 0
            ? t(($) => $.canvas.summary.fan_out_max, { max })
            : t(($) => $.canvas.summary.fan_out_default)}
        </WorkflowNodeSummary>
      }
    />
  );
});

export const JoinNode = memo(function JoinNode({
  data,
  selected,
}: NodeProps<FlowNode>) {
  const { t } = useT("workflows");
  // Join sources are edges in the engine's sense but are NOT drawn as canvas
  // edges (the model maps only next/branches/rework_targets), so the count is the
  // only place a join's inputs are visible on the canvas at all.
  const count = data.node.join_sources?.length ?? 0;
  return (
    <WorkflowNodeCard
      data={data}
      selected={selected}
      typeLabel={t(($) => $.canvas.node_type.join)}
      summary={
        <WorkflowNodeSummary icon={Merge}>
          {t(($) => $.canvas.summary.join_sources, { count })}
        </WorkflowNodeSummary>
      }
    />
  );
});

// ---------------------------------------------------------------------------
// end
// ---------------------------------------------------------------------------

export const EndNode = memo(function EndNode({
  data,
  selected,
}: NodeProps<FlowNode>) {
  const { t } = useT("workflows");
  return (
    <WorkflowNodeCard
      data={data}
      selected={selected}
      typeLabel={t(($) => $.canvas.node_type.end)}
      summary={
        <WorkflowNodeSummary icon={Flag}>
          {t(($) => $.canvas.summary.end)}
        </WorkflowNodeSummary>
      }
      // No source handle at all: the validator rejects an End node with any
      // outgoing edge, so a port here would exist only to let the author build a
      // graph the server refuses to save.
      sourceHandles={<></>}
    />
  );
});

// ---------------------------------------------------------------------------
// unknown
// ---------------------------------------------------------------------------

/**
 * A node kind published by a newer server.
 *
 * Registered so the canvas *draws* it: xyflow drops a node whose `type` has no
 * renderer, and a dropped node would make the graph look smaller than it is
 * while `graphToDefinition` still saves it - the author would be publishing
 * around a step they cannot see. The dashed outline says "this build cannot
 * interpret this", and the raw server string is shown as the badge because
 * translating it is exactly what this build cannot do.
 *
 * Both handles are present: the *neighbours* of an unknown node are still
 * editable, and hiding the ports would prevent an author from re-pointing a
 * perfectly ordinary edge that happens to touch it.
 */
export const UnknownNode = memo(function UnknownNode({
  data,
  selected,
}: NodeProps<FlowNode>) {
  return (
    <WorkflowNodeCard
      data={data}
      selected={selected}
      typeLabel={data.node.type}
      className="border-dashed"
    />
  );
});
