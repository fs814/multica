"use client";

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
      {onChange && (
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