package daemon

import (
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/multica-ai/multica/server/pkg/agent"
)

// knotRuntimeConfig is the schema the daemon expects under a knot or knot-http agent's
// `runtime_config` JSONB column. Every field is optional; an absent block (or
// the agent's whole runtime_config being null/empty) leaves the daemon-wide
// MULTICA_KNOT_AGENT_ID default in charge, so existing agents are unaffected.
//
// Schema:
//
//	{
//	  "knot": {
//	    "agent_id":    "ec4633074fe4413c83218e1f36b8e24d", // from `knot-cli list-agents`
//	    "client_uuid": "remote"                            // or a UUIDv4, or omit
//	  }
//	}
//
// It is nested under a "knot" key rather than sitting at the top level so one
// agent's runtime_config can carry blocks for unrelated concerns without the
// keys colliding — the same reason openclaw namespaces its gateway settings.
//
// Other providers' runtime_config payloads pass through untouched; this decoder
// only reads keys that have meaning for the knot backends.
type knotRuntimeConfig struct {
	Knot struct {
		AgentID string `json:"agent_id"`
		// ClientUUID selects WHICH registered machine runs the agent's tools
		// (chat_extra.agent_client_uuid). "remote" means "let Knot pick the
		// agent's own machine"; a UUIDv4 targets a specific one; empty pins the
		// local host. Only meaningful for knot-http.
		ClientUUID string `json:"client_uuid"`
	} `json:"knot"`
}

func supportsKnotRuntimeConfig(provider string) bool {
	return provider == "knot" || provider == "knot-http"
}

// decodeKnotRuntimeConfig extracts the per-agent Knot agent id from an agent's
// runtime_config payload, or "" when none is configured.
//
// Fails soft, deliberately: a malformed payload or a bad id logs a warning and
// returns "" so the daemon-wide default still applies, rather than blocking
// dispatch. The alternative would let one bad save break every task the agent
// runs — the same rule decodeOpenclawRuntimeConfig follows.
//
// The id is validated here rather than only in the backend because a wrong id
// is not a loud failure downstream: knot-cli silently substitutes its own
// default agent for an unknown id, so a typo would quietly bill and behave as a
// different agent. Rejecting it here means the run falls back to the configured
// default instead of an arbitrary one.
func decodeKnotRuntimeConfig(raw json.RawMessage, logger *slog.Logger) string {
	if len(raw) == 0 {
		return ""
	}
	var cfg knotRuntimeConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		if logger != nil {
			logger.Warn("knot runtime_config: parse failed; falling back to the daemon-wide agent id", "error", err)
		}
		return ""
	}
	agentID := strings.TrimSpace(cfg.Knot.AgentID)
	if agentID == "" {
		return ""
	}
	if !agent.LooksLikeKnotAgentID(agentID) {
		if logger != nil {
			// The id is not a secret (it comes from `knot-cli list-agents`), so
			// logging it is what makes the typo findable.
			logger.Warn("knot runtime_config: agent_id is not a 32-character hex id; falling back to the daemon-wide agent id",
				"agent_id", agentID)
		}
		return ""
	}
	return agentID
}

// decodeKnotClientUUID extracts the per-agent client-uuid selector from an
// agent's runtime_config, or "" when none is configured. The value chooses
// which registered machine runs the agent's tools — see knotRuntimeConfig.
//
// Accepts the "remote" sentinel (dispatch to the agent's own machine) or a
// UUIDv4 (a specific machine). Like the agent id it fails soft: an unrecognized
// value logs a warning and returns "", so the backend falls back to pinning the
// local host rather than dispatching the run to a machine that does not exist.
// This is knot-http-only (the CLI transport has no agent_client_uuid), but the
// decoder is transport-agnostic; the caller gates it on provider == "knot-http".
func decodeKnotClientUUID(raw json.RawMessage, logger *slog.Logger) string {
	if len(raw) == 0 {
		return ""
	}
	var cfg knotRuntimeConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		if logger != nil {
			logger.Warn("knot runtime_config: parse failed; ignoring client_uuid and pinning the local host", "error", err)
		}
		return ""
	}
	clientUUID := strings.TrimSpace(cfg.Knot.ClientUUID)
	if clientUUID == "" {
		return ""
	}
	if strings.EqualFold(clientUUID, "remote") {
		// Normalize so the backend's case-insensitive sentinel check is trivial.
		return "remote"
	}
	if !agent.LooksLikeKnotClientUUID(clientUUID) {
		if logger != nil {
			logger.Warn("knot runtime_config: client_uuid is neither \"remote\" nor a UUIDv4; pinning the local host instead",
				"client_uuid", clientUUID)
		}
		return ""
	}
	return clientUUID
}
