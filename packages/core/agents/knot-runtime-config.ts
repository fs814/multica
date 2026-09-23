// Knot-specific `runtime_config` schema.
//
// Stored under `agent.runtime_config` as freeform JSONB; only meaningful for
// agents whose runtime provider is knot or knot-http. The daemon decodes the same schema
// in `server/internal/daemon/knot_runtime_config.go` — keep both sides in
// lockstep when changing field names.
//
// Why this exists at all: knot-http puts the Knot agent id in its request URL,
// so a run cannot start without one. The daemon-wide MULTICA_KNOT_AGENT_ID
// cannot be set per agent (the daemon strips the whole MULTICA_* namespace from
// custom_env), so runtime_config is the per-agent channel. Resolution order at
// dispatch is:
//
//   KNOT_AGENT_ID (custom_env)  >  runtime_config  >  MULTICA_KNOT_AGENT_ID
//
// Note an Agent Builder carrier agent can use none of this: CreateAgentBuilder
// hardcodes runtime_config and custom_env to '{}', so those runs always fall
// through to the daemon-wide default.

export interface KnotRuntimeConfig {
  agentId?: string;
  // Which registered machine runs the agent's tools (chat_extra.agent_client_uuid,
  // knot-http only). "remote" = the agent's own machine; a UUIDv4 = a specific
  // one; absent = pin the local host. See resolveKnotHTTPClientUUID in
  // server/pkg/agent/knot_http.go — keep both sides in lockstep.
  clientUuid?: string;
}

// The sentinel that tells the backend to omit agent_client_uuid so Knot runs
// the agent on its own registered machine. Mirrors knotHTTPClientUUIDRemote.
export const KNOT_CLIENT_UUID_REMOTE = "remote";

// Length of the hex ids `knot-cli list-agents` reports.
const KNOT_AGENT_ID_LENGTH = 32;

// Mirrors knotLooksLikeAgentID in server/pkg/agent/knot.go: 32 lowercase hex
// characters. Validating in the UI matters because a wrong id fails quietly —
// knot-cli substitutes its own default agent for an unknown one, so a typo would
// bill and behave as a different agent instead of erroring.
export function looksLikeKnotAgentId(value: string): boolean {
  return (
    value.length === KNOT_AGENT_ID_LENGTH && /^[0-9a-f]+$/.test(value)
  );
}

// Parse an arbitrary runtime_config payload into the typed schema. Unknown keys
// are dropped and malformed payloads collapse to an empty object, so an invalid
// config renders as "nothing selected" rather than blocking the form with a
// parse error.
//
// An id that is present but malformed is deliberately KEPT here, unlike in the
// daemon which discards it. The form needs to show what is currently stored so
// the user can see and correct it; hiding it would make a bad saved value look
// like an empty field that mysteriously fails at dispatch.
export function parseKnotRuntimeConfig(raw: unknown): KnotRuntimeConfig {
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) return {};
  const root = raw as Record<string, unknown>;
  const knot = root.knot;
  if (!knot || typeof knot !== "object" || Array.isArray(knot)) return {};
  const block = knot as Record<string, unknown>;
  const out: KnotRuntimeConfig = {};
  if (typeof block.agent_id === "string" && block.agent_id.trim() !== "") {
    out.agentId = block.agent_id.trim();
  }
  // Kept even when malformed, for the same reason as agent_id: the form must
  // show what is stored so a bad value can be seen and corrected, not hidden.
  if (typeof block.client_uuid === "string" && block.client_uuid.trim() !== "") {
    out.clientUuid = block.client_uuid.trim();
  }
  return out;
}

// Render the typed form state back into the wire shape the API accepts.
//
// An unset id produces `{}` rather than `{knot:{agent_id:""}}` so clearing the
// picker actually falls back to the daemon-wide default instead of pinning an
// empty id that the daemon would have to special-case. The same holds for
// client_uuid: only fields the user actually set survive, and an all-empty
// selection drops the knot block entirely.
export function serializeKnotRuntimeConfig(
  cfg: KnotRuntimeConfig,
): Record<string, unknown> {
  const agentId = cfg.agentId?.trim() ?? "";
  const clientUuid = cfg.clientUuid?.trim() ?? "";
  const knot: Record<string, unknown> = {};
  if (agentId !== "") knot.agent_id = agentId;
  if (clientUuid !== "") knot.client_uuid = clientUuid;
  if (Object.keys(knot).length === 0) return {};
  return { knot };
}

// Stable equality across two parsed configs, for the form's dirty detector.
// Absent and empty are the same thing (both mean "use the default"), so
// toggling a selection on and off again is not a change.
export function knotRuntimeConfigEquals(
  a: KnotRuntimeConfig,
  b: KnotRuntimeConfig,
): boolean {
  return (
    (a.agentId ?? "") === (b.agentId ?? "") &&
    (a.clientUuid ?? "") === (b.clientUuid ?? "")
  );
}

// Merge a fully-formed Knot selection into an agent's existing runtime_config,
// preserving any other provider's block.
//
// Needed because the settings tab PUTs runtime_config wholesale: writing only
// the knot block would silently drop an openclaw gateway pin (or anything added
// later) from the same agent. An empty selection removes the knot block rather
// than storing blank fields.
function mergeKnotBlock(
  existing: unknown,
  next: KnotRuntimeConfig,
): Record<string, unknown> {
  const base: Record<string, unknown> =
    existing && typeof existing === "object" && !Array.isArray(existing)
      ? { ...(existing as Record<string, unknown>) }
      : {};
  const wire = serializeKnotRuntimeConfig(next);
  if (wire.knot) {
    base.knot = wire.knot;
  } else {
    delete base.knot;
  }
  return base;
}

// Set the Knot agent id while PRESERVING any client_uuid already stored on the
// same agent. Replacing the whole knot block (the old behavior) would silently
// wipe a configured client_uuid every time the agent picker changed.
export function withKnotAgentId(
  existing: unknown,
  agentId: string,
): Record<string, unknown> {
  const prev = parseKnotRuntimeConfig(existing);
  return mergeKnotBlock(existing, { ...prev, agentId: agentId.trim() });
}

// Set the Knot client_uuid (which machine runs the tools) while preserving any
// agent_id already stored. Pass "" to clear it (falls back to pinning the local
// host); "remote" to dispatch to the agent's own registered machine.
export function withKnotClientUuid(
  existing: unknown,
  clientUuid: string,
): Record<string, unknown> {
  const prev = parseKnotRuntimeConfig(existing);
  return mergeKnotBlock(existing, { ...prev, clientUuid: clientUuid.trim() });
}

// This is the saved requested target; custom_env may override it at dispatch.
// It never establishes the actual execution location of Knot file tools.
export function knotToolTarget(raw: unknown): "local" | "remote" | "client" | "invalid" {
  if (raw != null && (typeof raw !== "object" || Array.isArray(raw))) return "invalid";
  if (raw && typeof raw === "object" && "knot" in raw && raw.knot != null) {
    const knot = raw.knot;
    if (typeof knot !== "object" || Array.isArray(knot)) return "invalid";
    if ("client_uuid" in knot && knot.client_uuid != null && typeof knot.client_uuid !== "string") return "invalid";
  }
  const value = parseKnotRuntimeConfig(raw).clientUuid;
  if (!value) return "local";
  if (value.toLowerCase() === "remote") return "remote";
  return /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(value) ? "client" : "invalid";
}
