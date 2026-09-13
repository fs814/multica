"use client";
import { useEffect, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { api } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core/hooks";
import { useFileUpload } from "@multica/core/hooks/use-file-upload";
import { attachmentDownloadPath } from "@multica/core/types";
import { projectListOptions } from "@multica/core/projects/queries";
import type {
  WorkflowDefinition,
  WorkflowNode,
  SaveWorkflowInputInstance,
} from "@multica/core/workflows";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { useNavigation } from "../../navigation";
import { useT } from "../../i18n";
import {
  resolveRunFormFields,
  RunFormControl,
} from "../runs/components/workflow-run-dialog";

export function copiedInstanceName(name: string, suffix: string) {
  const ending = " " + suffix;
  return (
    Array.from(name)
      .slice(0, Math.max(0, 100 - Array.from(ending).length))
      .join("") + ending
  );
}

export function inputNode(
  definition?: WorkflowDefinition,
): WorkflowNode | null {
  return (
    definition?.nodes.find(
      (n) => n.key === definition.entry_node && n.type === "input",
    ) ?? null
  );
}
export function inputSchema(node: WorkflowNode | null) {
  return JSON.stringify(
    node
      ? {
          key: node.key,
          type: node.type,
          mode: node.input_mode || "text",
          fields: node.input_fields.map((f) => ({
            key: f.key,
            type: f.type || "text",
            label: f.label || "",
            placeholder: f.placeholder || "",
            required: f.required || false,
            options: f.options || [],
          })),
        }
      : null,
  );
}
export function useInstanceLeaveWarning(dirty: boolean, allowSamePage = false) {
  const navigation = useNavigation();
  const bypass = useRef(false);
  const { t } = useT("workflows");
  useEffect(() => {
    if (!dirty) return;
    const unload = (event: BeforeUnloadEvent) => {
      event.preventDefault();
      event.returnValue = "";
    };
    const navigate = (event: Event) => {
      const detail = (event as CustomEvent<{ pathname: string; destination?: string }>).detail;
      const target = detail?.pathname;
      if (allowSamePage && detail?.destination?.split(/[?#]/)[0] === navigation.pathname) return;
      if (target !== navigation.pathname || bypass.current) return;
      if (!window.confirm(t(($) => $.instances.leave))) event.preventDefault();
    };
    window.addEventListener("beforeunload", unload);
    window.addEventListener("multica:before-navigate", navigate);
    return () => {
      window.removeEventListener("beforeunload", unload);
      window.removeEventListener("multica:before-navigate", navigate);
    };
  }, [dirty, t, navigation.pathname, allowSamePage]);
  return (action: () => void) => {
    bypass.current = true;
    try {
      action();
    } finally {
      bypass.current = false;
    }
  };
}
export function InstanceFields({
  value,
  onChange,
  disabled = false,
  onUploadingChange,
}: {
  value: SaveWorkflowInputInstance;
  onChange: (value: SaveWorkflowInputInstance) => void;
  disabled?: boolean;
  onUploadingChange?: (uploading: boolean) => void;
}) {
  const { t } = useT("workflows");
  const wsId = useWorkspaceId();
  const projects = useQuery(projectListOptions(wsId));
  const { upload, uploading } = useFileUpload(api);
  const [imageError, setImageError] = useState("");
  const latest = useRef(value);
  latest.current = value;
  const fields = resolveRunFormFields(value.inputNode?.input_fields ?? [], {
    titleLabel: t(($) => $.runs.dialog.title_label),
    titlePlaceholder: t(($) => $.runs.dialog.title_placeholder),
    descriptionLabel: t(($) => $.runs.dialog.description_label),
    descriptionPlaceholder: t(($) => $.runs.dialog.description_placeholder),
  });
  const known = new Set(fields.map((f) => f.key));
  const unknown = Object.keys(value.input).filter((k) => !known.has(k));
  return (
    <div className="flex flex-col gap-4">
      <label className="text-caption">
        {t(($) => $.input_instances.name)}
        <Input
          value={value.name}
          maxLength={100}
          disabled={disabled}
          onChange={(e) => onChange({ ...value, name: e.target.value })}
        />
      </label>
      <label className="text-caption">
        {t(($) => $.instances.notes)}
        <Textarea
          value={value.description ?? ""}
          disabled={disabled}
          onChange={(e) => onChange({ ...value, description: e.target.value })}
        />
      </label>
      <label className="text-caption">
        {t(($) => $.instances.project)}
        <select
          className="mt-1 block w-full rounded-md border bg-background p-2"
          value={value.projectId ?? ""}
          disabled={disabled}
          onChange={(e) =>
            onChange({ ...value, projectId: e.target.value || null })
          }
        >
          <option value="">{t(($) => $.instances.no_project)}</option>
          {value.projectId &&
            !projects.data?.some((p) => p.id === value.projectId) && (
              <option value={value.projectId}>
                {t(($) => $.instances.unavailable_project)}
              </option>
            )}
          {projects.data?.map((p) => (
            <option key={p.id} value={p.id}>
              {p.title}
            </option>
          ))}
        </select>
      </label>
      {fields.map((field) => (
        <RunFormControl
          key={field.key}
          field={field}
          value={value.input[field.key] ?? ""}
          disabled={disabled}
          onChange={(next) =>
            onChange({ ...value, input: { ...value.input, [field.key]: next } })
          }
          onBlur={() => {}}
          selectPlaceholder={t(($) => $.instances.select)}
        />
      ))}
      {unknown.map((key) => (
        <div key={key} className="rounded border p-3">
          <p className="text-caption">
            {t(($) => $.instances.unknown, { field: key })}
          </p>
          <pre className="whitespace-pre-wrap break-words">
            {value.input[key]}
          </pre>
          <Button
            variant="outline"
            size="sm"
            disabled={disabled}
            onClick={() => {
              if (window.confirm(t(($) => $.instances.remove_field))) {
                const input = { ...value.input };
                delete input[key];
                onChange({ ...value, input });
              }
            }}
          >
            {t(($) => $.instances.remove)}
          </Button>
        </div>
      ))}
      {value.inputNode?.input_mode === "image" && (
        <div className="flex flex-col gap-2">
          <label className="text-caption">
            {t(($) => $.instances.image)}
            <Input
              type="file"
              accept="image/png,image/jpeg,image/webp,image/gif"
              disabled={disabled || uploading}
              onChange={async (e) => {
                const file = e.target.files?.[0];
                e.target.value = "";
                if (!file) return;
                if (
                  ![
                    "image/png",
                    "image/jpeg",
                    "image/webp",
                    "image/gif",
                  ].includes(file.type) ||
                  file.size > 100 * 1024 * 1024
                ) {
                  setImageError(
                    t(($) => $.panel.input_mode.image_invalid_type),
                  );
                  return;
                }
                try {
                  setImageError("");
                  onUploadingChange?.(true);
                  const result = await upload(file);
                  if (result)
                    onChange({
                      ...latest.current,
                      imageAttachmentId: result.id,
                    });
                } catch (error) {
                  setImageError(
                    error instanceof Error ? error.message : String(error),
                  );
                } finally {
                  onUploadingChange?.(false);
                }
              }}
            />
          </label>
          {value.imageAttachmentId && (
            <>
              <img
                className="max-h-48 max-w-full object-contain"
                alt={t(($) => $.instances.image)}
                src={
                  api.getBaseUrl() +
                  attachmentDownloadPath(value.imageAttachmentId)
                }
              />
              <Button
                variant="outline"
                disabled={disabled || uploading}
                onClick={() => onChange({ ...value, imageAttachmentId: "" })}
              >
                {t(($) => $.instances.remove)}
              </Button>
            </>
          )}
          {uploading && <p role="status">{t(($) => $.instances.uploading)}</p>}
          {imageError && <p role="alert">{imageError}</p>}
        </div>
      )}
    </div>
  );
}
