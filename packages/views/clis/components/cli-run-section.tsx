"use client";

import { useState } from "react";
import { Loader2, Play } from "lucide-react";
import type {
  RuntimeCLIParamDescriptor,
  RuntimeCLIRunRequest,
  RuntimeCLISummary,
} from "@multica/core/types";
import { runRuntimeCLI } from "@multica/core/clis";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Label } from "@multica/ui/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@multica/ui/components/ui/select";
import { useT } from "../../i18n";

/**
 * One registry entry: its parameter form, its Run button, and the output of
 * the run it produced.
 *
 * The form is generated from the entry's declared `params`, so the panel can
 * never send a value shape the machine did not agree to accept — and a
 * parameter the registry does not declare has nowhere to be typed.
 */

interface CLIRunSectionProps {
  runtimeId: string;
  entry: RuntimeCLISummary;
}

export function CLIRunSection({ runtimeId, entry }: CLIRunSectionProps) {
  const { t } = useT("clis");
  const [values, setValues] = useState<Record<string, string>>({});
  const [running, setRunning] = useState(false);
  const [result, setResult] = useState<RuntimeCLIRunRequest | null>(null);
  const [error, setError] = useState<string | null>(null);

  const params = entry.params ?? [];

  function setValue(name: string, value: string) {
    setValues((prev) => ({ ...prev, [name]: value }));
  }

  // A required slot with no value would be rejected by the machine anyway;
  // blocking it here saves a round trip through the heartbeat.
  const missingRequired = params.some((p) => p.required && !values[p.name]);

  async function handleRun() {
    setRunning(true);
    setError(null);
    setResult(null);
    try {
      const run = await runRuntimeCLI(runtimeId, entry.key, {
        params: values,
        timeout_seconds: entry.timeout_seconds,
      });
      setResult(run);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setRunning(false);
    }
  }

  return (
    <div className="rounded-lg border p-4">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          <div className="flex items-center gap-2">
            <span className="font-medium">{entry.label || entry.key}</span>
            <code className="text-muted-foreground text-xs">{entry.key}</code>
            {!entry.available && (
              <span className="text-muted-foreground text-xs">
                {t(($) => $.page.unavailable)}
              </span>
            )}
          </div>
          {entry.description && (
            <p className="text-muted-foreground mt-1 text-sm">
              {entry.description}
            </p>
          )}
          {!entry.available && entry.unavailable_reason && (
            <p className="text-muted-foreground mt-1 text-sm">
              {entry.unavailable_reason}. {t(($) => $.page.unavailable_hint)}
            </p>
          )}
        </div>
        <Button
          size="sm"
          onClick={handleRun}
          disabled={running || !entry.available || missingRequired}
        >
          {running ? (
            <Loader2 className="size-4 animate-spin" />
          ) : (
            <Play className="size-4" />
          )}
          {running ? t(($) => $.entry.running) : t(($) => $.entry.run)}
        </Button>
      </div>

      {params.length > 0 && (
        <div className="mt-4 grid gap-3 sm:grid-cols-2">
          {params.map((param) => (
            <CLIParamField
              key={param.name}
              param={param}
              value={values[param.name] ?? ""}
              disabled={running}
              onChange={(value) => setValue(param.name, value)}
            />
          ))}
        </div>
      )}

      {running && (
        <p className="text-muted-foreground mt-3 text-sm">
          {t(($) => $.entry.awaiting_machine)}
        </p>
      )}

      {error && <p className="text-destructive mt-3 text-sm">{error}</p>}

      {result && <CLIRunOutput run={result} />}
    </div>
  );
}

function CLIParamField({
  param,
  value,
  disabled,
  onChange,
}: {
  param: RuntimeCLIParamDescriptor;
  value: string;
  disabled: boolean;
  onChange: (value: string) => void;
}) {
  const { t } = useT("clis");
  const id = `cli-param-${param.name}`;

  return (
    <div className="grid gap-1.5">
      <Label htmlFor={id} className="text-xs">
        {param.name}
        {param.required ? "" : ` · ${t(($) => $.entry.optional)}`}
      </Label>
      {param.type === "enum" ? (
        <Select
          items={(param.values ?? []).map((option) => ({
            value: option,
            label: option,
          }))}
          value={value}
          onValueChange={(next) => onChange(next ?? "")}
          disabled={disabled}
        >
          <SelectTrigger id={id}>
            <SelectValue>{value}</SelectValue>
          </SelectTrigger>
          <SelectContent>
            {(param.values ?? []).map((option) => (
              <SelectItem key={option} value={option}>
                {option}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      ) : (
        <Input
          id={id}
          value={value}
          disabled={disabled}
          maxLength={param.max_len || undefined}
          onChange={(event) => onChange(event.target.value)}
        />
      )}
    </div>
  );
}

function CLIRunOutput({ run }: { run: RuntimeCLIRunRequest }) {
  const { t } = useT("clis");

  return (
    <div className="mt-4 border-t pt-3">
      <div className="text-muted-foreground flex flex-wrap items-center gap-3 text-xs">
        {run.resolved_argv && run.resolved_argv.length > 0 && (
          <span className="min-w-0 truncate">
            {t(($) => $.result.command)}: <code>{run.resolved_argv.join(" ")}</code>
          </span>
        )}
        {typeof run.exit_code === "number" && (
          <span>{t(($) => $.result.exit_code, { code: run.exit_code })}</span>
        )}
        {typeof run.duration_ms === "number" && (
          <span>{t(($) => $.result.duration, { ms: run.duration_ms })}</span>
        )}
      </div>

      {/* A silent truncation would read as "that was everything"; say it
          instead, and say how much was dropped. */}
      {run.truncated && (
        <p className="text-muted-foreground mt-2 text-xs">
          {t(($) => $.result.truncated, { bytes: run.output_bytes ?? 0 })}
        </p>
      )}

      <pre className="bg-muted mt-2 max-h-96 overflow-auto rounded-md p-3 text-xs whitespace-pre-wrap">
        {run.output && run.output.length > 0
          ? run.output
          : t(($) => $.result.no_output)}
      </pre>
    </div>
  );
}
