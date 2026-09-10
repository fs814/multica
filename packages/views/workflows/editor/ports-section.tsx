"use client";
import { useState } from "react";
import { ArrowUp, Plus, Trash2 } from "lucide-react";
import type {
  WorkflowNode,
  WorkflowPort,
  WorkflowBranch,
} from "@multica/core/workflows";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { useT } from "../../i18n";
import { PanelField, PanelSection, PanelSelect } from "./panel-controls";

export function PortsSection({
  node,
  readOnly,
  onChange,
}: {
  node: WorkflowNode;
  readOnly: boolean;
  onChange(node: WorkflowNode): void;
}) {
  const { t } = useT("workflows");
  return (
    <PanelSection title={t(($) => $.graph_v2.ports)}>
      {(["input_ports", "output_ports"] as const).map((direction) => (
        <div key={direction} className="flex flex-col gap-2">
          <span className="text-caption">
            {direction === "input_ports"
              ? t(($) => $.graph_v2.inputs)
              : t(($) => $.graph_v2.outputs)}
          </span>
          {(node[direction] ?? []).map((port, index) => {
            const patch = (value: Partial<WorkflowPort>) =>
              onChange({
                ...node,
                [direction]: node[direction]?.map((p, i) =>
                  i === index ? { ...p, ...value } : p,
                ),
              });
            return (
              <div
                key={index}
                className="flex flex-col gap-1 rounded border p-2"
              >
                <Input
                  aria-label={t(($) => $.graph_v2.port_id)}
                  value={port.id}
                  disabled={readOnly}
                  onChange={(e) => patch({ id: e.target.value })}
                />
                <PanelSelect
                  ariaLabel={t(($) => $.graph_v2.port_type)}
                  value={port.type}
                  disabled={readOnly}
                  options={[
                    "string",
                    "number",
                    "boolean",
                    "object",
                    "array",
                    "any",
                  ].map((value) => ({ value, label: value }))}
                  onChange={(type) => patch({ type })}
                />
                {direction === "input_ports" ? (
                  <div className="flex flex-wrap gap-2 text-caption">
                    <label>
                      <input
                        type="checkbox"
                        checked={port.required ?? false}
                        disabled={readOnly}
                        onChange={(e) => patch({ required: e.target.checked })}
                      />{" "}
                      {t(($) => $.graph_v2.required)}
                    </label>
                    <label>
                      <input
                        type="checkbox"
                        checked={port.multiple ?? false}
                        disabled={readOnly}
                        onChange={(e) => patch({ multiple: e.target.checked })}
                      />{" "}
                      {t(($) => $.graph_v2.collection)}
                    </label>
                  </div>
                ) : null}
                {!readOnly ? (
                  <Button
                    variant="ghost"
                    size="icon-sm"
                    aria-label={t(($) => $.graph_v2.remove_port)}
                    onClick={() =>
                      onChange({
                        ...node,
                        [direction]: node[direction]?.filter(
                          (_, i) => i !== index,
                        ),
                      })
                    }
                  >
                    <Trash2 className="size-3" />
                  </Button>
                ) : null}
              </div>
            );
          })}
          {!readOnly ? (
            <Button
              variant="outline"
              size="sm"
              onClick={() =>
                onChange({
                  ...node,
                  [direction]: [
                    ...(node[direction] ?? []),
                    {
                      id: `port_${crypto.randomUUID().slice(0, 8)}`,
                      type: "string",
                    },
                  ],
                })
              }
            >
              <Plus className="size-3" />
              {t(($) => $.graph_v2.add_port)}
            </Button>
          ) : null}
        </div>
      ))}
      {node.type === "agent" ? (
        <PanelField label={t(($) => $.graph_v2.attempts)}>
          <Input
            type="number"
            min={1}
            max={10}
            value={node.max_attempts || 1}
            disabled={readOnly}
            onChange={(e) =>
              onChange({ ...node, max_attempts: Number(e.target.value) })
            }
          />
        </PanelField>
      ) : null}
    </PanelSection>
  );
}
export function PredicateFields({
  node,
  branch,
  index,
  readOnly,
  onChange,
  onMoveUp,
}: {
  node: WorkflowNode;
  branch: WorkflowBranch;
  index: number;
  readOnly: boolean;
  onChange(branch: Partial<WorkflowBranch>): void;
  onMoveUp(): void;
}) {
  const { t } = useT("workflows");
  const [invalid, setInvalid] = useState(false);
  return (
    <div className="flex flex-col gap-1">
      <PanelSelect
        ariaLabel={t(($) => $.graph_v2.compare_input)}
        value={branch.predicate?.input_port ?? ""}
        disabled={readOnly}
        options={[
          { value: "", label: t(($) => $.graph_v2.verdict) },
          ...(node.input_ports ?? []).map((p) => ({
            value: p.id,
            label: p.id,
          })),
        ]}
        onChange={(input_port) =>
          onChange({
            predicate: input_port ? { input_port, equals: "" } : undefined,
          })
        }
      />
      {branch.predicate ? (
        <Input
          key={`${branch.predicate.input_port}:${JSON.stringify(branch.predicate.equals)}`}
          aria-label={t(($) => $.graph_v2.equals)}
          aria-invalid={invalid}
          defaultValue={JSON.stringify(branch.predicate.equals)}
          disabled={readOnly}
          onBlur={(e) => {
            try {
              const equals: unknown = JSON.parse(e.target.value);
              onChange({
                predicate: { input_port: branch.predicate!.input_port, equals },
              });
              setInvalid(false);
            } catch {
              setInvalid(true);
            }
          }}
        />
      ) : null}
      {!readOnly && index > 0 ? (
        <Button variant="ghost" size="sm" onClick={onMoveUp}>
          <ArrowUp className="size-3" />
          {t(($) => $.graph_v2.priority)}
        </Button>
      ) : null}
    </div>
  );
}
