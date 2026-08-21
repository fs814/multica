"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import { Loader2, Save } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import type { Agent, KnotAgent } from "@multica/core/types";
import {
  type KnotRuntimeConfig,
  knotRuntimeConfigEquals,
  looksLikeKnotAgentId,
  parseKnotRuntimeConfig,
  withKnotAgentId,
} from "@multica/core/agents";
import { runtimeModelsOptions } from "@multica/core/runtimes";
import { Button } from "@multica/ui/components/ui/button";
import { toast } from "sonner";
import { KnotAgentPickerField } from "../knot-agent-picker-field";
import { useT } from "../../../i18n";

/**
 * Per-agent Knot agent picker for the knot and knot-http runtimes.
 *
 * knot-cli passes the id with `-a`; knot-http puts it in its request URL. The
 * daemon-wide MULTICA_KNOT_AGENT_ID cannot be set per agent
 * (the daemon strips the whole MULTICA_* namespace from custom_env), which is
 * why the selection lives in runtime_config. Leaving it unset is valid and
 * means "use the daemon-wide default".
 *
 * The dropdown's options come from `knot-cli list-agents`, discovered on the
 * same daemon round trip as the model catalog. When that list is empty — CLI
 * missing, runtime offline, older daemon — the form falls back to a validated
 * text field rather than becoming unusable.
 */
export function KnotConfigTab({
  agent,
  runtimeId,
  runtimeOnline,
  onSave,
  onDirtyChange,
  compact = false,
}: {
  agent: Agent;
  runtimeId: string | null;
  runtimeOnline: boolean;
  onSave: (updates: {
    runtime_config: Record<string, unknown>;
  }) => Promise<void>;
  onDirtyChange?: (dirty: boolean) => void;
  /** Embed the picker in General → Execution config without repeating its
   *  section copy or field label. */
  compact?: boolean;
}) {
  const { t } = useT("agents");
  const [saving, setSaving] = useState(false);

  const original = useMemo<KnotRuntimeConfig>(
    () => parseKnotRuntimeConfig(agent.runtime_config),
    [agent.runtime_config],
  );
  const [agentId, setAgentId] = useState(original.agentId ?? "");

  // Adopt a new server value only when the user has no in-flight edit relative
  // to the PREVIOUS original — same rule as RuntimeConfigTab, so a background
  // refetch cannot silently discard typing.
  const previousRef = useRef(original.agentId ?? "");
  useEffect(() => {
    const next = original.agentId ?? "";
    setAgentId((current) => (current === previousRef.current ? next : current));
    previousRef.current = next;
  }, [original.agentId]);

  // Reuses the model-catalog query: knot_agents rides that same response, so
  // opening this tab costs no extra round trip when the picker was already open.
  const modelsQuery = useQuery(
    runtimeModelsOptions(runtimeOnline ? runtimeId : null),
  );
  const knotAgents: KnotAgent[] = modelsQuery.data?.knotAgents ?? [];

  // This tab only edits agent_id; carry the stored client_uuid through the
  // comparison so an agent that has one does not read as perpetually dirty.
  const dirty = !knotRuntimeConfigEquals(original, {
    agentId,
    clientUuid: original.clientUuid,
  });
  useEffect(() => {
    onDirtyChange?.(dirty);
  }, [dirty, onDirtyChange]);

  const trimmed = agentId.trim();
  const idValid = trimmed === "" || looksLikeKnotAgentId(trimmed);
  // An empty id is valid: it clears the override and defers to the daemon-wide
  // default. Only a non-empty malformed id blocks saving.
  const canSave = dirty && idValid && !saving;

  const handleSave = async () => {
    if (!canSave) return;
    setSaving(true);
    try {
      // Merge rather than replace: another provider's block (or anything added
      // later) must survive a knot-only edit.
      await onSave({
        runtime_config: withKnotAgentId(agent.runtime_config, trimmed),
      });
      toast.success(t(($) => $.tab_body.knot_config.saved_toast));
    } catch (err) {
      toast.error(
        err instanceof Error && err.message
          ? err.message
          : t(($) => $.tab_body.knot_config.save_failed_toast),
      );
    } finally {
      setSaving(false);
    }
  };

  return (
    <div className="flex h-full flex-col space-y-4">
      {!compact && (
        <p className="text-caption text-muted-foreground">
          {t(($) => $.tab_body.knot_config.intro)}
        </p>
      )}

      <KnotAgentPickerField
        value={agentId}
        onChange={setAgentId}
        agents={knotAgents}
        loading={modelsQuery.isLoading}
        showLabel={!compact}
      />

      <div className="flex items-center gap-2">
        <Button size="sm" onClick={() => void handleSave()} disabled={!canSave}>
          {saving ? (
            <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />
          ) : (
            <Save className="h-4 w-4" aria-hidden="true" />
          )}
          {t(($) => $.tab_body.knot_config.save)}
        </Button>
      </div>
    </div>
  );
}
