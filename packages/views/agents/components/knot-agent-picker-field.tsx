"use client";

import { useState } from "react";
import { Bot, ChevronDown } from "lucide-react";
import { looksLikeKnotAgentId } from "@multica/core/agents";
import type { KnotAgent } from "@multica/core/types";
import { Input } from "@multica/ui/components/ui/input";
import { Label } from "@multica/ui/components/ui/label";
import {
  PickerItem,
  PropertyPicker,
} from "../../issues/components/pickers";
import { useT } from "../../i18n";

export function KnotAgentPickerField({
  value,
  onChange,
  agents,
  loading,
  disabled = false,
  required = false,
  showLabel = true,
}: {
  value: string;
  onChange: (value: string) => void;
  agents: KnotAgent[];
  loading: boolean;
  disabled?: boolean;
  required?: boolean;
  showLabel?: boolean;
}) {
  const { t } = useT("agents");
  const [open, setOpen] = useState(false);
  const trimmed = value.trim();
  const idValid =
    (!required && trimmed === "") || looksLikeKnotAgentId(trimmed);
  const selected = agents.find((agent) => agent.id === trimmed);
  const triggerLabel = selected
    ? selected.name
    : trimmed ||
      t(($) =>
        required
          ? $.tab_body.knot_config.agent_required_unset
          : $.tab_body.knot_config.agent_unset,
      );

  return (
    <fieldset className="space-y-2" disabled={disabled}>
      {showLabel && (
        <Label className="text-caption font-medium">
          {t(($) => $.tab_body.knot_config.agent_label)}
        </Label>
      )}

      {agents.length > 0 ? (
        <PropertyPicker
          open={open}
          onOpenChange={setOpen}
          width="w-[var(--anchor-width)] min-w-[16rem] max-w-md"
          align="start"
          triggerRender={
            <button
              type="button"
              disabled={disabled}
              className="flex min-h-10 w-full min-w-0 items-center gap-2 rounded-lg border border-input bg-transparent px-3 text-left text-body transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-3 focus-visible:ring-ring/50 disabled:cursor-not-allowed disabled:opacity-50"
              aria-label={t(($) => $.tab_body.knot_config.agent_label)}
            />
          }
          trigger={
            <>
              <Bot
                className="h-4 w-4 shrink-0 text-muted-foreground"
                aria-hidden="true"
              />
              <span className="min-w-0 flex-1 truncate">{triggerLabel}</span>
              <ChevronDown
                className={`h-4 w-4 shrink-0 text-muted-foreground transition-transform ${
                  open ? "rotate-180" : ""
                }`}
                aria-hidden="true"
              />
            </>
          }
        >
          {agents.map((agent) => (
            <PickerItem
              key={agent.id}
              selected={agent.id === trimmed}
              onClick={() => {
                setOpen(false);
                onChange(agent.id);
              }}
            >
              <span className="block min-w-0 flex-1 text-left">
                <span className="truncate text-label font-medium">
                  {agent.name}
                </span>
                <span className="mt-0.5 block truncate font-mono text-micro leading-snug text-muted-foreground">
                  {agent.id}
                </span>
              </span>
            </PickerItem>
          ))}

          {trimmed !== "" && !required && (
            <button
              type="button"
              onClick={() => {
                setOpen(false);
                onChange("");
              }}
              className="mt-1 flex w-full items-center border-t px-3 py-2 text-left text-caption text-muted-foreground transition-colors hover:bg-accent/50"
            >
              {t(($) => $.tab_body.knot_config.agent_clear)}
            </button>
          )}
        </PropertyPicker>
      ) : (
        <>
          <Input
            value={value}
            onChange={(event) => onChange(event.target.value)}
            placeholder={t(($) => $.tab_body.knot_config.agent_placeholder)}
            className="font-mono"
            aria-invalid={!idValid}
            disabled={disabled}
          />
          <p className="text-caption text-muted-foreground">
            {loading
              ? t(($) => $.tab_body.knot_config.agent_discovering)
              : t(($) => $.tab_body.knot_config.agent_manual_hint)}
          </p>
        </>
      )}

      {!idValid && (
        <p className="text-caption text-destructive">
          {t(($) => $.tab_body.knot_config.agent_invalid)}
        </p>
      )}
      {required && trimmed === "" && (
        <p className="text-caption text-destructive">
          {t(($) => $.tab_body.knot_config.agent_required)}
        </p>
      )}
      {!required && idValid && trimmed === "" && (
        <p className="text-caption text-muted-foreground">
          {t(($) => $.tab_body.knot_config.agent_default_hint)}
        </p>
      )}
    </fieldset>
  );
}
