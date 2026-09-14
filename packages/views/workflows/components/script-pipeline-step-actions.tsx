"use client";

import { scriptPipelineSteps } from "@multica/core/workflows";
import { Button } from "@multica/ui/components/ui/button";
import { useT } from "../../i18n";

export function ScriptPipelineStepActions({ disabled, onRun }: {
  disabled: boolean;
  onRun(step: typeof scriptPipelineSteps[number]): void;
}) {
  const { t } = useT("workflows");
  return (
    <div className="flex flex-wrap gap-2">
      {scriptPipelineSteps.map((step) => (
        <Button key={step} size="sm" variant="outline" disabled={disabled} onClick={() => onRun(step)}>
          {t(($) => $.scripts.run_step[step])}
        </Button>
      ))}
    </div>
  );
}
