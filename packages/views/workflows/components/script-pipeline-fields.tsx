"use client";

import { useRef, useState } from "react";
import { scriptPipelineSteps, type ScriptPipelineConfig } from "@multica/core/workflows";
import { Input } from "@multica/ui/components/ui/input";
import { Button } from "@multica/ui/components/ui/button";
import { isDesktopShell, pickDirectory } from "../../platform/local-directory";
import { useT } from "../../i18n";

export function ScriptPipelineFields({ value, onChange, disabled = false }: {
  value: ScriptPipelineConfig;
  onChange?: (value: ScriptPipelineConfig) => void;
  disabled?: boolean;
}) {
  const { t } = useT("workflows");
  const latest = useRef(value);
  latest.current = value;
  const [error, setError] = useState("");
  const readOnly = disabled || !onChange;
  const chooseDirectory = async () => {
    try {
      const picked = await pickDirectory(value.directory.trim() || undefined, "script_pipeline");
      if (picked.ok && picked.path) onChange?.({ ...latest.current, directory: picked.path });
      else if (picked.reason !== "cancelled") setError(t(($) => $.scripts.pick_failed));
    } catch { setError(t(($) => $.scripts.pick_failed)); }
  };
  return (
    <fieldset disabled={readOnly} className="nodrag nopan nowheel flex min-w-0 flex-col gap-3 text-caption">
      <label className="flex flex-col gap-1">
        {t(($) => $.scripts.directory)}
        <Input value={value.directory} placeholder={t(($) => $.scripts.directory_placeholder)} onChange={(event) => onChange?.({ ...value, directory: event.target.value })} />
      </label>
      {isDesktopShell() && <Button type="button" variant="outline" size="sm" onClick={() => void chooseDirectory()}>{t(($) => $.scripts.browse)}</Button>}
      {error && <p role="alert" className="text-destructive">{error}</p>}
      <p className="text-muted-foreground">{t(($) => $.scripts.directory_hint)}</p>
      <label className="flex flex-col gap-1">
        {t(($) => $.scripts.platform)}
        <select className="rounded-md border bg-background p-2" value={value.platform} onChange={(event) => onChange?.({ ...value, platform: event.target.value })}>
          <option value="auto">{t(($) => $.scripts.auto_platform)}</option>
          <option value="windows">{t(($) => $.scripts.windows_platform)}</option>
          <option value="darwin">{t(($) => $.scripts.macos_platform)}</option>
          <option value="linux">{t(($) => $.scripts.linux_platform)}</option>
        </select>
      </label>
      <div className="flex flex-wrap gap-3" role="group" aria-label={t(($) => $.scripts.steps)}>
        {scriptPipelineSteps.map((step) => <label key={step} className="flex items-center gap-1">
          <input type="checkbox" checked={value.steps.includes(step)} onChange={(event) => {
            const selected = new Set(value.steps);
            if (event.target.checked) selected.add(step); else selected.delete(step);
            onChange?.({ ...value, steps: scriptPipelineSteps.filter((candidate) => selected.has(candidate)) });
          }} />{step}
        </label>)}
      </div>
      {value.steps.length === 0 && <p role="alert" className="text-destructive">{t(($) => $.scripts.select_step)}</p>}
      <p className="text-muted-foreground">{t(($) => $.scripts.order_hint)}</p>
      {scriptPipelineSteps.filter((step) => value.steps.includes(step)).map((step) => <label key={step} className="flex flex-col gap-1">
        {t(($) => $.scripts.script_path, { step })}
        <Input value={value.scripts[step] ?? ""} placeholder={t(($) => $.scripts.auto_script)} onChange={(event) => onChange?.({ ...value, scripts: { ...value.scripts, [step]: event.target.value } })} />
      </label>)}
      <label className="flex flex-col gap-1">
        {t(($) => $.scripts.timeout)}
        <Input type="number" min={1} max={86400} value={Number.isFinite(value.timeout_seconds) ? value.timeout_seconds : ""} onChange={(event) => onChange?.({ ...value, timeout_seconds: Number(event.target.value) })} />
      </label>
      <p className="text-muted-foreground">{t(($) => $.scripts.runtime_hint)}</p>
    </fieldset>
  );
}