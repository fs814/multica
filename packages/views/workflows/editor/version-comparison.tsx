"use client";
import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  compareInputFields,
  compareWorkflowVersions,
  workflowInstanceVersionOptions,
  type WorkflowDefinition,
  type WorkflowNode,
} from "@multica/core/workflows";
import { useT } from "../../i18n";
import { PanelSelect } from "./panel-controls";
import { inputNode } from "../instances/instance-form";

export function InputVersionDiff({
  before,
  after,
}: {
  before?: WorkflowNode | null;
  after?: WorkflowNode | null;
}) {
  const { t } = useT("workflows");
  const changes = compareInputFields(before, after);
  const labels = {
    added: t(($) => $.authoring.added),
    removed: t(($) => $.authoring.removed),
    type: t(($) => $.authoring.type),
    options: t(($) => $.authoring.options),
    required: t(($) => $.authoring.required),
    label: t(($) => $.authoring.label),
  };
  return (
    <div className="flex flex-col gap-2" data-testid="input-version-diff">
      {before?.input_mode !== after?.input_mode && (
        <p className="text-caption">
          {before?.input_mode === "image"
            ? t(($) => $.instances.image)
            : t(($) => $.instances.text)}{" "}
          →{" "}
          {after?.input_mode === "image"
            ? t(($) => $.instances.image)
            : t(($) => $.instances.text)}
        </p>
      )}
      {changes.length === 0 && (
        <p className="text-caption">{t(($) => $.authoring.no_field_changes)}</p>
      )}
      {changes.map((change) => (
        <div
          key={change.key}
          className="rounded border p-2 text-caption break-words"
        >
          <strong>{change.key}</strong> ·{" "}
          {change.changes.map((kind) => labels[kind]).join(" · ")}
          <div className="grid grid-cols-2 gap-2">
            {[change.before, change.after].map((field, index) => (
              <p key={index}>
                {field
                  ? `${field.label || field.key} · ${field.type || "text"} ${field.required ? "*" : ""} · ${field.options.join(", ")}`
                  : "—"}
              </p>
            ))}
          </div>
        </div>
      ))}
    </div>
  );
}

export function WorkflowVersionComparison({
  wsId,
  templateId,
  versions,
  draft,
}: {
  wsId: string;
  templateId: string;
  versions: { id: string; version: number; status: string }[];
  draft: WorkflowDefinition;
}) {
  const { t } = useT("workflows");
  const published = versions.filter(
    (version) => version.status === "published",
  );
  const [from, setFrom] = useState(published[0]?.id ?? "");
  const [to, setTo] = useState("");
  const before = useQuery(
    workflowInstanceVersionOptions(wsId, templateId, from),
  );
  const after = useQuery(workflowInstanceVersionOptions(wsId, templateId, to));
  const target = to ? after.data?.definition : draft;
  const summary =
    before.data && target
      ? compareWorkflowVersions(before.data.definition, target)
      : null;
  const options = published.map((version) => ({
    value: version.id,
    label: `v${version.version}`,
  }));
  return (
    <section
      className="max-h-80 shrink-0 overflow-y-auto border-b p-4"
      aria-label={t(($) => $.authoring.compare)}
    >
      <div className="mb-3 flex gap-3">
        <PanelSelect
          ariaLabel={t(($) => $.authoring.from)}
          value={from}
          disabled={false}
          options={options}
          onChange={setFrom}
        />
        <span>→</span>
        <PanelSelect
          ariaLabel={t(($) => $.authoring.to)}
          value={to}
          disabled={false}
          options={[{ value: "", label: t(($) => $.status.draft) }, ...options]}
          onChange={setTo}
        />
      </div>
      {(before.isError || after.isError) && (
        <p role="alert">{t(($) => $.authoring.version_failed)}</p>
      )}
      {!summary && !before.isError && !after.isError && (
        <p>{t(($) => $.instances.loading)}</p>
      )}
      {summary && target && (
        <>
          <p className="mb-2 text-caption break-words">
            {t(($) => $.authoring.added)}: {summary.added.join(", ") || "—"} ·{" "}
            {t(($) => $.authoring.removed)}: {summary.removed.join(", ") || "—"}{" "}
            · {t(($) => $.authoring.changed)}:{" "}
            {summary.changed.join(", ") || "—"}
          </p>
          <p className="mb-2 text-caption">
            {[
              summary.schemaChanged && t(($) => $.authoring.schema),
              summary.entryChanged && t(($) => $.authoring.entry),
              summary.limitsChanged && t(($) => $.authoring.limits),
              summary.dataChanged && t(($) => $.authoring.bindings),
            ]
              .filter(Boolean)
              .join(" · ")}
          </p>
          <InputVersionDiff
            before={inputNode(before.data?.definition)}
            after={inputNode(target)}
          />
        </>
      )}
    </section>
  );
}
