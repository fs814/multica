package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os/exec"
	"strings"
	"time"
)

// knotHTTPBackend drives Knot through its HTTP AG-UI endpoint
//
//	POST {base}/apigw/api/v1/agents/agui/{agent_id}
//
// rather than by spawning `knot-cli`. Both transports carry the SAME AG-UI
// event vocabulary — SCREAMING_CASE `type` with the payload under `rawEvent` —
// so this backend reuses knot.go's parser wholesale (newKnotStreamState,
// handleKnotEvent, scanKnotEventStream) and differs only in framing: the CLI
// writes bare JSONL to stdout, the endpoint writes Server-Sent Events
// terminated by a [DONE] sentinel.
//
// Why a separate family rather than a flag on knot: the endpoint is absent from
// the knot-cli binary entirely (the binary only calls
// /apigw/api/v1/agents/knot-cli/{chat,download_plugins,download_skills}), it
// authenticates with its own API token instead of the CLI's ambient login, and
// it accepts per-request knobs the CLI has no flags for. The two are different
// wire protocols to different endpoints that happen to share an event schema.
//
// Execution model — the part that is easy to get wrong: Knot's server runs the
// LLM loop, but the agent's file tools execute on a REGISTERED CLIENT MACHINE
// selected by chat_extra.agent_client_uuid. By default this backend requires
// the LOCAL client UUID and requests the Multica workdir as chat_extra.workspace.
// This is a requested target; actual file placement still needs verification. Pointing
// the uuid at another machine would run the tools on that machine's filesystem
// and produce a task that completes having changed nothing locally.
type knotHTTPBackend struct {
	cfg Config
	// baseURL overrides the endpoint host. Tests point this at an
	// httptest.Server; production leaves it empty and resolves from env.
	baseURL string
	// httpClient overrides the transport. Zero value means a client with no
	// global timeout: an agent turn is long-lived and its liveness is owned by
	// the daemon's inactivity watchdog, exactly as the CLI transport's is.
	httpClient *http.Client
}

const (
	// KnotHTTPTokenEnv is the daemon-wide default API token
	// (https://knot.woa.com/settings/token). Exported because the daemon must
	// copy it into the per-task env itself — isBlockedEnvKey drops the entire
	// MULTICA_* namespace from an agent's custom_env, so a user-set value would
	// silently never arrive.
	KnotHTTPTokenEnv = "MULTICA_KNOT_HTTP_TOKEN"
	// KnotHTTPUserEnv is the daemon-wide default for the x-knot-api-user
	// header (the acting user's WeCom English name). Required when the token is
	// a TEAM token; ignored by the server for personal tokens.
	KnotHTTPUserEnv = "MULTICA_KNOT_HTTP_USER"
	// KnotHTTPBaseURLEnv overrides the API host, for a region or a proxy.
	KnotHTTPBaseURLEnv = "MULTICA_KNOT_HTTP_BASE_URL"
	// KnotClientUUIDEnv is the slot the daemon writes an agent's
	// runtime_config.knot.client_uuid into. It is MULTICA_-prefixed so it cannot
	// arrive from custom_env (isBlockedEnvKey strips the namespace), which is
	// what lets the un-prefixed knotHTTPClientUUIDCustomEnv override outrank it —
	// mirroring the token and agent-id split above. Resolution order:
	//   KNOT_CLIENT_UUID (custom_env) > runtime_config (this key) > local probe.
	KnotClientUUIDEnv = "MULTICA_KNOT_CLIENT_UUID"

	// knotHTTPTokenCustomEnv is the PER-AGENT token override. It carries no
	// MULTICA_ prefix precisely so it survives isBlockedEnvKey and can be set
	// per agent through the existing custom_env settings UI, the same way
	// ANTHROPIC_API_KEY is. It wins over KnotHTTPTokenEnv.
	knotHTTPTokenCustomEnv = "KNOT_API_TOKEN"
	// knotHTTPUserCustomEnv is the matching per-agent acting-user override.
	knotHTTPUserCustomEnv = "KNOT_API_USER"
	// knotHTTPAgentIDCustomEnv is the PER-AGENT Knot agent id override. Like the
	// two above it carries no MULTICA_ prefix so it survives isBlockedEnvKey and
	// can be set per agent from the settings UI's Env tab — the daemon-wide
	// MULTICA_KNOT_AGENT_ID cannot, so this is the ONLY way to give two
	// knot-http agents different Knot identities on one machine. List valid ids
	// with `knot-cli list-agents`.
	knotHTTPAgentIDCustomEnv = "KNOT_AGENT_ID"

	// knotHTTPClientUUIDCustomEnv is the PER-AGENT override for WHICH registered
	// machine runs the agent's tools (chat_extra.agent_client_uuid). Like the
	// keys above it carries no MULTICA_ prefix so it survives isBlockedEnvKey and
	// can be set per agent from the settings UI's Env tab. It wins over the
	// runtime_config value the daemon writes into KnotClientUUIDEnv.
	//
	// Two special forms, plus the default:
	//   - unset            -> pin THIS host (knotLocalClientUUID), the default.
	//                         The agent's file edits land in the task workdir
	//                         multica will diff — the safe choice for local work.
	//   - "remote"         -> send NO agent_client_uuid so Knot dispatches to the
	//                         agent's OWN registered machine (e.g. a macbook).
	//                         File edits execute there, so multica's local diff is
	//                         empty; intended for chat/ops/remote-host agents.
	//   - a UUIDv4         -> target that specific registered client verbatim.
	knotHTTPClientUUIDCustomEnv = "KNOT_CLIENT_UUID"
	// knotHTTPClientUUIDRemote is the sentinel (case-insensitive) that means
	// "omit agent_client_uuid and let Knot pick the agent's home machine".
	knotHTTPClientUUIDRemote = "remote"

	// knotHTTPDefaultBaseURL is the production host. HTTPS is not optional:
	// http:// answers 307 and net/http would not carry the auth headers
	// through a cross-scheme redirect.
	knotHTTPDefaultBaseURL = "https://knot.woa.com"
	// knotHTTPAGUIPath is the AG-UI chat endpoint; the agent id is appended.
	knotHTTPAGUIPath = "/apigw/api/v1/agents/agui/"

	knotHTTPTokenHeader = "x-knot-api-token"
	knotHTTPUserHeader  = "x-knot-api-user"

	// knotHTTPClientStatusTimeout bounds the local `knot-cli client-status`
	// call that resolves this host's uuid. It is a local IPC round-trip to an
	// already-running daemon, so it is quick or it is broken.
	knotHTTPClientStatusTimeout = 10 * time.Second
)

// knotHTTPRequest is the request envelope. Everything rides under `input`.
type knotHTTPRequest struct {
	Input knotHTTPInput `json:"input"`
}

type knotHTTPInput struct {
	Message string `json:"message"`
	// ConversationID resumes a prior conversation. Empty starts a new one —
	// the field is always sent because the API distinguishes "" (new) from a
	// value, and omitting it entirely is not documented as equivalent.
	ConversationID string        `json:"conversation_id"`
	Model          string        `json:"model,omitempty"`
	Stream         bool          `json:"stream"`
	ChatExtra      knotHTTPExtra `json:"chat_extra"`
	UseMemory      bool          `json:"use_memory,omitempty"`
}

type knotHTTPExtra struct {
	// AgentClientUUID selects WHICH registered machine executes the agent's
	// tools. See the type comment: this backend pins it to the local host.
	AgentClientUUID string `json:"agent_client_uuid,omitempty"`
	// Workspace are absolute paths on that machine used as working
	// directories. Multica passes the prepared task workdir.
	Workspace []string `json:"workspace,omitempty"`
	// ExtraHeaders are forwarded to MCP servers the agent calls. Left empty
	// here; it is the documented channel for an internal-taihu-token when an
	// agent's MCP tools need the user's own credentials.
	ExtraHeaders map[string]string `json:"extra_headers,omitempty"`

	EnableThinking   bool   `json:"enable_thinking,omitempty"`
	ReasoningEffort  string `json:"reasoning_effort,omitempty"`
	MaxContextTokens int64  `json:"max_context_tokens,omitempty"`
}

// knotHTTPErrorResponse is the non-stream JSON body the gateway returns when it
// refuses the request (bad/absent token, no permission, malformed body). It
// arrives with HTTP 200 for auth failures, so the body — not the status — is
// what identifies a refusal.
type knotHTTPErrorResponse struct {
	Msg  string `json:"msg"`
	Code int    `json:"code"`
}

func (b *knotHTTPBackend) baseURLOrDefault() string {
	if b.baseURL != "" {
		return strings.TrimRight(b.baseURL, "/")
	}
	if fromEnv := strings.TrimSpace(b.cfg.Env[KnotHTTPBaseURLEnv]); fromEnv != "" {
		return strings.TrimRight(fromEnv, "/")
	}
	return knotHTTPDefaultBaseURL
}

// knotHTTPToken resolves the API token, per-agent override first. Config.Env is
// consulted rather than the process environment because buildEnv strips the
// inherited MULTICA_* namespace — only what the daemon assembled for this task
// is visible.
func knotHTTPToken(env map[string]string) string {
	if perAgent := strings.TrimSpace(env[knotHTTPTokenCustomEnv]); perAgent != "" {
		return perAgent
	}
	return strings.TrimSpace(env[KnotHTTPTokenEnv])
}

// knotHTTPUser resolves the acting user for x-knot-api-user, per-agent first.
func knotHTTPUser(env map[string]string) string {
	if perAgent := strings.TrimSpace(env[knotHTTPUserCustomEnv]); perAgent != "" {
		return perAgent
	}
	return strings.TrimSpace(env[KnotHTTPUserEnv])
}

// knotHTTPAgentID resolves WHICH registered Knot agent serves this task,
// per-agent override first.
//
// Full resolution order, of which this function sees only the ends:
//
//	KNOT_AGENT_ID (custom_env)  >  runtime_config  >  MULTICA_KNOT_AGENT_ID
//
// The middle one is not visible here — the daemon decodes an agent's
// runtime_config and writes the result into KnotAgentIDEnv, so from the
// backend's side it arrives as the daemon-wide key having been overwritten. See
// decodeKnotRuntimeConfig in internal/daemon.
//
// The per-agent key exists because the daemon-wide MULTICA_KNOT_AGENT_ID cannot
// be set per agent at all: isBlockedEnvKey strips the whole MULTICA_* namespace
// from custom_env. Without KNOT_AGENT_ID every knot-http agent on a machine
// would be forced to share one Knot identity, which defeats the point of having
// several registered agents (each with its own plugins, skills and machine).
//
// The knot CLI family solves the same problem with `custom_args -a <id>`, but
// this backend spawns no process and has no argv to carry it — the id goes in
// the URL path — so an env key is the honest equivalent.
func knotHTTPAgentID(env map[string]string) string {
	if perAgent := strings.TrimSpace(env[knotHTTPAgentIDCustomEnv]); perAgent != "" {
		return perAgent
	}
	return knotAgentID(env)
}

// knotHTTPClientUUIDSetting returns the configured per-agent client-uuid
// selector, "" when none is set. Precedence mirrors the agent id:
//
//	KNOT_CLIENT_UUID (custom_env)  >  MULTICA_KNOT_CLIENT_UUID (runtime_config)
//
// The middle of that order is invisible here: the daemon decodes an agent's
// runtime_config.knot.client_uuid and writes it into KnotClientUUIDEnv (see
// decodeKnotClientUUID in internal/daemon), so from the backend it arrives as a
// pre-populated env key.
func knotHTTPClientUUIDSetting(env map[string]string) string {
	if perAgent := strings.TrimSpace(env[knotHTTPClientUUIDCustomEnv]); perAgent != "" {
		return perAgent
	}
	return strings.TrimSpace(env[KnotClientUUIDEnv])
}

// resolveKnotHTTPClientUUID resolves a requested tool target, not a verified
// execution location. Only explicit remote mode may omit the target UUID.
func resolveKnotHTTPClientUUID(ctx context.Context, env map[string]string, executablePath string, logger *slog.Logger) (string, error) {
	setting := knotHTTPClientUUIDSetting(env)
	switch {
	case setting == "":
		clientUUID := knotLocalClientUUID(ctx, executablePath)
		if !LooksLikeKnotClientUUID(clientUUID) {
			return "", fmt.Errorf("knot-http local tool target unavailable: start the local Knot client and verify `knot-cli client-status`; remote dispatch requires explicit client_uuid=remote")
		}
		return clientUUID, nil
	case strings.EqualFold(setting, knotHTTPClientUUIDRemote):
		if logger != nil {
			logger.Info("knot-http requested tool target: platform selection; actual execution location is unverified", "provider", "knot-http")
		}
		return "", nil
	case LooksLikeKnotClientUUID(setting):
		return setting, nil
	default:
		return "", fmt.Errorf("knot-http client_uuid must be remote or a UUID; refusing to change the requested tool target")
	}
}

// LooksLikeKnotClientUUID reports whether s has the shape of the UUIDv4 that
// chat_extra.agent_client_uuid expects: 8-4-4-4-12 hex (either case). Exported
// so the daemon can reject a typo'd machine id out of runtime_config before it
// dispatches a run to nowhere. The "remote" sentinel is handled by callers, not
// here.
func LooksLikeKnotClientUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
			continue
		}
		isHex := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
		if !isHex {
			return false
		}
	}
	return true
}

// knotLocalClientUUID reads this host's registered agent-client uuid from
// `knot-cli client-status`, which prints a JSON object after a status banner.
//
// Returns "" on any failure. The caller must reject local execution when
// discovery fails; it must never interpret this as permission for remote dispatch.
func knotLocalClientUUID(ctx context.Context, executablePath string) string {
	if executablePath == "" {
		executablePath = knotDefaultBinary
	}
	if _, err := exec.LookPath(executablePath); err != nil {
		return ""
	}
	runCtx, cancel := context.WithTimeout(ctx, knotHTTPClientStatusTimeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, executablePath, "client-status")
	hideAgentWindow(cmd)
	// Discovery must not inherit the daemon's MULTICA_* namespace and needs no
	// task environment of its own.
	cmd.Env = buildEnv(nil)
	out, err := cmd.Output()
	if err != nil && len(out) == 0 {
		return ""
	}
	return parseKnotClientUUID(string(out))
}

// parseKnotClientUUID extracts the agent-client uuid from `knot-cli
// client-status` output. The command prints human banner lines ("✅ success",
// "Agent Status Details:") before an indented JSON object, so the JSON is
// located by its first brace rather than by parsing the whole stream.
//
// It reads `connection_uuid`, NOT `uuid`. Both are present and they are
// different things: `uuid` is a short numeric id ("894300") while the value the
// API's chat_extra.agent_client_uuid expects is a UUIDv4
// ("68b7d6d7-8eb5-4598-830e-d71bcc739672") — verified against this host's own
// client log, where outbound requests carry connection_uuid under exactly that
// key. Sending `uuid` makes Knot fall back to choosing a machine itself, which
// silently runs the agent's file tools somewhere other than the task workdir.
//
// The name is a trap: "connection" suggests something transient, and it IS
// regenerated when the background service reconnects — which is precisely why it
// must be read fresh per run rather than cached or configured.
func parseKnotClientUUID(out string) string {
	start := strings.Index(out, "{")
	end := strings.LastIndex(out, "}")
	if start < 0 || end <= start {
		return ""
	}
	var status struct {
		ConnectionUUID string `json:"connection_uuid"`
	}
	if err := json.Unmarshal([]byte(out[start:end+1]), &status); err != nil {
		return ""
	}
	return strings.TrimSpace(status.ConnectionUUID)
}

// buildKnotHTTPRequest assembles the request body for one turn.
//
// It takes clientUUID explicitly rather than resolving it, so the caller owns
// the (possibly slow, possibly failing) local probe and this stays pure and
// directly testable.
func buildKnotHTTPRequest(prompt string, opts ExecOptions, clientUUID string) knotHTTPRequest {
	extra := knotHTTPExtra{
		AgentClientUUID: clientUUID,
	}
	// A remote tool host cannot use the dispatching daemon's local directory.
	if opts.Cwd != "" && clientUUID != "" {
		extra.Workspace = []string{opts.Cwd}
	}
	if opts.ThinkingLevel != "" {
		// Mirrors buildKnotArgs: the endpoint, like the CLI, ignores an effort
		// level unless thinking is also switched on.
		extra.EnableThinking = true
		extra.ReasoningEffort = opts.ThinkingLevel
	}
	return knotHTTPRequest{Input: knotHTTPInput{
		Message:        prompt,
		ConversationID: opts.ResumeSessionID,
		Model:          opts.Model,
		Stream:         true,
		ChatExtra:      extra,
	}}
}

func (b *knotHTTPBackend) Execute(ctx context.Context, prompt string, opts ExecOptions) (*Session, error) {
	token := knotHTTPToken(b.cfg.Env)
	if token == "" {
		// Fail before the request rather than after: an unauthenticated call
		// returns HTTP 200 with a Chinese error body, which would otherwise be
		// diagnosed as a protocol fault instead of a missing setting.
		return nil, fmt.Errorf("knot-http requires an API token: set %s (daemon-wide) or %s in the agent's custom_env — obtain one at %s/settings/token",
			KnotHTTPTokenEnv, knotHTTPTokenCustomEnv, knotHTTPDefaultBaseURL)
	}
	agentID := knotHTTPAgentID(b.cfg.Env)
	if agentID == "" {
		// Unlike the CLI, the agent id is part of the URL path — there is no
		// endpoint to call without one.
		return nil, fmt.Errorf("knot-http requires an agent id: set %s in the agent's custom_env (per-agent) or %s (daemon-wide) — list them with `knot-cli list-agents`",
			knotHTTPAgentIDCustomEnv, KnotAgentIDEnv)
	}
	if !knotLooksLikeAgentID(agentID) {
		// The CLI silently substitutes a default agent for an unknown id, which
		// is how a typo bills and behaves as a different agent. Over HTTP the id
		// is in the URL, so reject a malformed one up front.
		return nil, fmt.Errorf("knot-http agent id %q is not a 32-character hex id (from `knot-cli list-agents`)", agentID)
	}

	timeout := opts.Timeout
	runCtx, cancel := runContext(ctx, timeout)

	clientUUID, err := resolveKnotHTTPClientUUID(runCtx, b.cfg.Env, b.cfg.ExecutablePath, b.cfg.Logger)
	if err != nil {
		cancel()
		return nil, err
	}

	body, err := json.Marshal(buildKnotHTTPRequest(prompt, opts, clientUUID))
	if err != nil {
		cancel()
		return nil, fmt.Errorf("knot-http marshal request: %w", err)
	}
	endpoint := b.baseURLOrDefault() + knotHTTPAGUIPath + agentID
	req, err := http.NewRequestWithContext(runCtx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		cancel()
		return nil, fmt.Errorf("knot-http build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set(knotHTTPTokenHeader, token)
	if user := knotHTTPUser(b.cfg.Env); user != "" {
		req.Header.Set(knotHTTPUserHeader, user)
	}

	client := b.httpClient
	if client == nil {
		// No client-level timeout: it would cap the whole streamed turn, not
		// just connection setup. Wall-clock bounds come from runCtx (opts.Timeout)
		// and liveness from the daemon's inactivity watchdog.
		client = &http.Client{}
	}
	// The prompt is never logged; the endpoint and selectors are.
	b.cfg.Logger.Info("agent command", "provider", "knot-http", "endpoint", endpoint,
		"knot_agent_id", agentID, "knot_client_uuid", clientUUID)

	resp, err := client.Do(req)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("knot-http POST %s: %w", endpoint, err)
	}
	if resp.StatusCode != http.StatusOK {
		detail := knotHTTPErrorDetail(resp)
		resp.Body.Close()
		cancel()
		return nil, fmt.Errorf("knot-http POST %s: HTTP %d%s", endpoint, resp.StatusCode, detail)
	}

	msgCh := make(chan Message, 256)
	resCh := make(chan Result, 1)
	go func() {
		defer cancel()
		defer close(msgCh)
		defer close(resCh)
		defer resp.Body.Close()

		started := time.Now()
		state := newKnotStreamState(opts.Model)
		go func() {
			<-runCtx.Done()
			_ = resp.Body.Close()
		}()

		scanErr := scanKnotEventStream(resp.Body, msgCh, state, true)
		duration := time.Since(started)

		status, output, errMsg := finalizeStreamResult("knot-http", timeout, runCtx.Err(), nil, nil, state.sessionID, streamTerminalState{
			lastAssistantText:   state.lastAssistantText(),
			finalResultText:     state.finalResultText(),
			sawResult:           state.sawRunFinished,
			resultIsError:       state.runError != "",
			scanErr:             scanErr,
			terminalReasonError: state.terminalReasonError(),
		}, "")
		logStreamProtocolObservation(b.cfg.Logger, streamProtocolObservation{
			provider: "knot-http", cliVersion: b.cfg.CLIVersion, model: state.model,
			eventCount:        state.eventCount,
			invalidEventCount: state.invalidEventCount, assistantEventCount: state.assistantEventCount,
			toolUseCount: state.toolUseCount, sawResult: state.sawRunFinished,
			resultIsError: state.runError != "",
			resultBytes:   len(state.finalResultText()), lastAssistantBytes: len(state.lastAssistantText()),
			scannerError: scanErr != nil, lastEventType: state.lastEventType,
			unhandledEventTypeCount: len(state.unhandledTypes), unhandledEventTypes: state.unhandledTypeList(),
		})
		b.cfg.Logger.Info("knot-http finished", "status", status, "duration", duration.Round(time.Millisecond).String())
		resCh <- Result{
			Status: status, Output: output, Error: errMsg, DurationMs: duration.Milliseconds(),
			SessionID:      resolveSessionID(opts.ResumeSessionID, state.sessionID, status == "failed", errMsg),
			Usage:          state.usage,
			ResumeRejected: resumeWasRejected(opts.ResumeSessionID, state.sessionID, status == "failed", errMsg),
		}
	}()
	return &Session{Messages: msgCh, Result: resCh}, nil
}

// knotHTTPErrorDetail renders the gateway's JSON refusal body as a short suffix
// for an error message, falling back to raw text when it is not the documented
// shape. Bounded because the body on a proxy error can be an HTML page.
func knotHTTPErrorDetail(resp *http.Response) string {
	const maxDetailBytes = 512
	buf := make([]byte, maxDetailBytes)
	n, _ := resp.Body.Read(buf)
	if n == 0 {
		return ""
	}
	raw := strings.TrimSpace(string(buf[:n]))
	var parsed knotHTTPErrorResponse
	if err := json.Unmarshal([]byte(raw), &parsed); err == nil && parsed.Msg != "" {
		return fmt.Sprintf(": %s (code %d)", parsed.Msg, parsed.Code)
	}
	if raw == "" {
		return ""
	}
	return ": " + sanitizeAgentDiagnostic(raw)
}
