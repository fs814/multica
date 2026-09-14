"use client";

import { scriptPipelineConfig } from "@multica/core/workflows";
import { ScriptPipelineFields } from "../../components/script-pipeline-fields";
import type { WorkflowNode } from "@multica/core/workflows";
import { Input } from "@multica/ui/components/ui/input";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { useT } from "../../../i18n";
import { InputFieldsSection } from "../../editor/input-fields-section";

/** Edits the source node through the page's undoable draft, without a local copy. */
export function InputNodeEditor({ node, onChange }: {
  node: WorkflowNode;
  onChange?: (node: WorkflowNode) => void;
}) {
  const { t } = useT("workflows");
  return (
    <div
      className="nodrag nopan nowheel flex flex-col gap-3"
      onClick={(event) => event.stopPropagation()}
      onKeyDown={(event) => event.stopPropagation()}
    >
      {onChange && (
        <label className="flex flex-col gap-1 text-caption">
          {t(($) => $.canvas.input_editor.name)}
          <Input
            value={node.name}
            onChange={(event) => onChange({ ...node, name: event.target.value })}
            className="text-caption"
          />
        </label>
      )}
      <label className="flex flex-col gap-1 text-caption">
        {t(($) => $.panel.input_mode.aria)}
        <select className="rounded-md border bg-background p-2" disabled={!onChange} value={node.input_mode || "text"} onChange={(event) => onChange?.({ ...node, input_mode: event.target.value, input_fields: [], image_attachment_id: "", script_pipeline: event.target.value === "scripts" ? scriptPipelineConfig(node.script_pipeline) : undefined })}>
          <option value="text">{t(($) => $.panel.input_mode.text)}</option>
          <option value="image">{t(($) => $.panel.input_mode.image)}</option>
          <option value="scripts">{t(($) => $.scripts.mode)}</option>
        </select>
      </label>
      {node.input_mode === "scripts" && <ScriptPipelineFields value={scriptPipelineConfig(node.script_pipeline)} onChange={onChange ? (value) => onChange({ ...node, script_pipeline: value }) : undefined} />}
      <label className="flex flex-col gap-1 text-caption">
        {t(($) => $.canvas.input_editor.content)}
        {onChange ? (
          <Textarea
            value={node.instruction}
            onChange={(event) => onChange({ ...node, instruction: event.target.value })}
            placeholder={t(($) => $.canvas.input_editor.placeholder)}
            rows={5}
            className="max-h-64 min-h-28 resize-y text-caption"
          />
        ) : (
          <span className="max-h-64 overflow-y-auto whitespace-pre-wrap break-words text-muted-foreground">
            {node.instruction || t(($) => $.canvas.input_editor.empty)}
          </span>
        )}
      </label>
      {onChange && node.input_mode !== "scripts" && (
        <details className="text-caption">
          <summary className="cursor-pointer text-muted-foreground">
            {t(($) => $.canvas.input_editor.configure)}
          </summary>
          <div className="max-h-80 overflow-y-auto">
            <InputFieldsSection node={node} readOnly={false} onChange={onChange} />
          </div>
        </details>
      )}
    </div>
  );
}