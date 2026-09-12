"use client";

import { useAgentPresenceDetail } from "@multica/core/agents";
import { ModelPicker } from "../../agents/components/inspector/model-picker";

interface Props {
  wsId: string;
  /**
   * The agent that owns the OPEN session. Must be the session's own agent, not
   * the controller's `activeAgent` — that one falls back to
   * `availableAgents[0]` when no session is selected, which would resolve the
   * catalog from an unrelated agent's runtime and offer, say, Claude Code's
   * models for a knot conversation.
   *
   * `runtime_id` is typed loosely because an unbound agent reports it as "" and
   * older servers may omit it — all three cases mean "no catalog to offer".
   */
  agent: { id: string; runtime_id?: string | null } | null | undefined;
  /** Current override; "" means the session follows the agent's own model. */
  value: string;
  /** Render read-only (archived session/agent, unbound runtime, write in flight). */
  disabled?: boolean;
  onChange: (model: string) => void;
}

/**
 * The composer's per-session model chip.
 *
 * Wraps the agent inspector's `ModelPicker` rather than growing a second picker:
 * both answer the same question against the same runtime catalog (search,
 * custom entry, clear, `supported=false`), and only the write target differs —
 * `agent.model` there, `chat_session.model` here.
 *
 * Presence is resolved from the SESSION's agent rather than taken as a prop, so
 * the runtime whose catalog is fetched and the runtime whose health gates the
 * fetch can never disagree. The controller's own `availability` is derived from
 * `activeAgent`, which is the fallback-prone value this component must avoid.
 *
 * Renders nothing unless that agent has a bound runtime that is ONLINE. Model
 * discovery is a round trip to the user's machine, so an offline runtime has no
 * catalog to offer and the picker would show a permanent empty dropdown plus a
 * manual-entry field nothing could validate. Both states are already explained
 * right above the composer by RuntimeRequiredBanner / OfflineBanner, so a silent
 * absence here reads as "not applicable" rather than as breakage.
 *
 * The runtime comes from the AGENT, not `chat_session.runtime_id`: the latter is
 * the daemon's resume pointer and is deliberately left stale after a runtime
 * switch, so resolving the catalog from it would offer models the runtime that
 * actually executes the next turn does not serve.
 */
export function ChatModelPicker({ wsId, agent, value, disabled, onChange }: Props) {
  const presence = useAgentPresenceDetail(wsId, agent?.id);
  const runtimeId = agent?.runtime_id?.trim() || null;
  const online = presence !== "loading" && presence.availability === "online";

  if (!runtimeId || !online) return null;

  return (
    <ModelPicker
      runtimeId={runtimeId}
      runtimeOnline
      value={value}
      canEdit={!disabled}
      variant="chip"
      showLabel={false}
      onChange={onChange}
    />
  );
}
