"use client";
import { canRenameWorkflowPort } from "@multica/core/workflows";
import { useState } from "react";
import { ArrowUp, Plus, Trash2 } from "lucide-react";
import type {
  WorkflowDefinition,
  WorkflowNode,
  WorkflowPort,
  WorkflowBranch,
} from "@multica/core/workflows";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { useT } from "../../i18n";
import { PanelField, PanelSection, PanelSelect } from "./panel-controls";

export type PortActions = {
  onRenamePort?(
    direction: "input_ports" | "output_ports",
    previous: string,
    next: string,
  ): void;
  onBindPort?(source: string, sourcePort: string, targetPort: string): void;
  onRemoveBinding?(edgeId: string): void;
};

function PortName({
  id,
  disabled,
  duplicate,
  canRename,
  onRename,
}: {
  id: string;
  disabled: boolean;
  duplicate(value: string): boolean;
  canRename(value: string): boolean;
  onRename(value: string): void;
}) {
  const { t } = useT("workflows");
  const [value, setValue] = useState(id);
  const contractBlocked = !canRename(value);
  const invalid =
    !value.trim() ||
    value !== value.trim() ||
    (value !== id && duplicate(value));
  return (
    <div className="flex flex-col gap-1">
      <Input
        aria-label={t(($) => $.graph_v2.port_id)}
        value={value}
        disabled={disabled}
        aria-invalid={invalid || contractBlocked}
        onChange={(event) => setValue(event.target.value)}
      />
      {value !== id && (
        <>
          <Button
            size="sm"
            variant="outline"
            disabled={disabled || invalid || contractBlocked}
            onClick={() => {
              if (!contractBlocked) onRename(value);
            }}
          >
            {t(($) => $.authoring.rename)}
          </Button>
          <Button size="sm" variant="ghost" onClick={() => setValue(id)}>
            {t(($) => $.instances.cancel)}
          </Button>
        </>
      )}
      {contractBlocked && (
        <p role="alert" className="text-caption text-muted-foreground">
          {t(($) => $.authoring.output_contract)}
        </p>
      )}
      {invalid && (
        <p role="alert" className="text-caption text-destructive">
          {t(($) => $.authoring.invalid_port)}
        </p>
      )}
    </div>
  );
}

export function PortsSection({
  node,
  definition,
  onRenamePort,
  onBindPort,
  onRemoveBinding,
  readOnly,
  onChange,
}: PortActions & {
  node: WorkflowNode;
  definition: WorkflowDefinition;
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
                <PortName
                  key={`${node.key}:${direction}:${port.id}`}
                  id={port.id}
                  disabled={
                    readOnly ||
                    !onRenamePort ||
                    !canRenameWorkflowPort(node, direction, port.id)
                  }
                  canRename={(value) =>
                    canRenameWorkflowPort(node, direction, port.id, value)
                  }
                  duplicate={(value) =>
                    Boolean(node[direction]?.some((p) => p.id === value))
                  }
                  onRename={(value) =>
                    onRenamePort?.(direction, port.id, value)
                  }
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
                {direction === "input_ports" && (
                  <div className="flex flex-col gap-2">
                    {(definition.data_edges ?? [])
                      .filter(
                        (edge) =>
                          edge.target === node.key &&
                          edge.target_port === port.id,
                      )
                      .map((edge) => (
                        <div
                          key={edge.id}
                          className="flex items-center gap-1 break-all text-caption"
                        >
                          <span>
                            {edge.source} / {edge.source_port} → {port.id} ·{" "}
                            {edge.order}
                          </span>
                          {!readOnly && (
                            <Button
                              variant="ghost"
                              size="icon-sm"
                              aria-label={t(($) => $.authoring.disconnect)}
                              onClick={() => onRemoveBinding?.(edge.id)}
                            >
                              <Trash2 className="size-3" />
                            </Button>
                          )}
                        </div>
                      ))}
                    <PanelSelect
                      value=""
                      ariaLabel={`${t(($) => $.authoring.source)}: ${port.id}`}
                      disabled={readOnly || !onBindPort}
                      options={[
                        {
                          value: "",
                          label: `${t(($) => $.authoring.source)} → ${port.type}`,
                        },
                        ...definition.nodes
                          .filter((source) => source.key !== node.key)
                          .flatMap((source) =>
                            (source.output_ports ?? []).map((output) => ({
                              value: JSON.stringify([source.key, output.id]),
                              label: `${source.name || source.key} / ${output.id} · ${output.type}`,
                              disabled:
                                (port.type !== "any" &&
                                  output.type !== port.type) ||
                                Boolean(
                                  (definition.data_edges ?? []).some(
                                    (edge) =>
                                      edge.target === node.key &&
                                      edge.target_port === port.id &&
                                      (!port.multiple ||
                                        (edge.source === source.key &&
                                          edge.source_port === output.id)),
                                  ),
                                ),
                            })),
                          ),
                      ]}
                      onChange={(value) => {
                        if (value) {
                          const [source, sourcePort] = JSON.parse(value) as [
                            string,
                            string,
                          ];
                          onBindPort?.(source, sourcePort, port.id);
                        }
                      }}
                    />
                  </div>
                )}
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
      {node.type === "join" && (
        <p className="text-caption text-muted-foreground">
          {t(($) => $.authoring.join_hint)}
        </p>
      )}
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
