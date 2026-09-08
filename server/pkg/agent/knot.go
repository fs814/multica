package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"sort"
	"strings"
	"time"
)

// knotBackend drives the Knot background-agent CLI (`knot-cli chat -p <prompt>
// --output-format stream-json`). Unlike the Claude-shaped stream-json backends,
// knot-cli speaks an AG-UI style event stream: SCREAMING_CASE `type` at the top
// level with the payload nested under `rawEvent`. The schema is based on
// knot-cli v0.26.2 captures in testdata/knot-cli-0.26.2-stream-json.jsonl.
//
// Two properties of that stream drive the design below and are the reason this
// backend cannot reuse another family's parser:
//
//   - There is NO terminal event carrying the final answer. RUN_FINISHED is a
//     bare marker with no result/is_error payload, so the deliverable text must
//     be accumulated from TEXT_MESSAGE_CONTENT deltas and RUN_FINISHED serves
//     only as the "protocol completed" proof finalizeStreamResult requires.
//   - TOOL_CALL_ARGS streams JSON-Patch fragments whose `value` is a partial
//     string chunk. Reading any single op yields a truncated argument (a
//     `/file_path` observed as 7 ops: "C:\\Users", "\\sh", "engf", …), so ops
//     are concatenated per (tool_call_id, path) and only flushed at
//     TOOL_CALL_END.
type knotBackend struct {
	cfg Config
}

// knotDefaultBinary is the CLI name probed on PATH when no explicit executable
// path is configured. The provider key is "knot" while the binary is
// "knot-cli", matching the kiro/kiro-cli and cursor/cursor-agent precedent.
const knotDefaultBinary = "knot-cli"

// KnotAgentIDEnv names the environment variable that selects which registered
// Knot agent (`knot-cli list-agents`) serves a task. It is delivered through
// the environment rather than agent.model because knot-cli has BOTH an agent
// identity (-a) and a model (-m), while Multica's config carries one model
// field — which is bound to -m so the model picker offers the real catalog.
//
// Exported because the daemon must copy it into the per-task env itself: its
// custom_env blocklist drops the whole MULTICA_* namespace, so a user-set value
// would never reach the child. Empty means send no -a at all and let knot-cli
// resolve its own default agent.
const KnotAgentIDEnv = "MULTICA_KNOT_AGENT_ID"

// knotBlockedArgs are owned by Multica. The prompt, the stream protocol, and
// the daemon-managed defaults must survive custom_args or the daemon cannot
// read the run: -o/--output-format switches away from the JSONL this parser
// requires and -p replaces the task. Model is Multica-selected (agent.model),
// and
// --status-out collapses the stream to a one-line status summary.
//
// --sessionId is deliberately allowed: a per-agent pin is the supported way
// to give one Knot-backed Multica agent a stable conversation across otherwise
// unrelated Multica issues. buildKnotArgs makes that pin authoritative over
// the issue/chat-derived ResumeSessionID so the CLI never receives two values.
//
// Deliberately NOT blocked: --sandbox, --spending-limit, --reasoning-effort,
// --enable-thinking, --max-context-tokens, --enable-web-search. Those are
// policy knobs a user may legitimately want to set per agent, and unlike the
// flags above they cannot break the daemon↔CLI protocol.
var knotBlockedArgs = map[string]blockedArgMode{
	"-p":              blockedWithValue,
	"--prompt":        blockedWithValue,
	"-o":              blockedWithValue,
	"--output-format": blockedWithValue,
	"-m":              blockedWithValue,
	"--model":         blockedWithValue,
	"-i":              blockedStandalone,
	"--interactive":   blockedStandalone,
	"--status-out":    blockedStandalone,
	"-h":              blockedStandalone,
	"--help":          blockedStandalone,
}

// knotAgentID resolves the configured Knot agent id for this execution.
// Config.Env is consulted rather than the daemon's own environment because
// buildEnv strips the inherited MULTICA_* namespace — only values the daemon
// explicitly assembled for the task reach the child.
func knotAgentID(env map[string]string) string {
	return strings.TrimSpace(env[KnotAgentIDEnv])
}

// knotArgsSelectAgent reports whether a user's own args already pin an agent.
// -a is deliberately NOT blocked: per-agent custom_env cannot carry
// MULTICA_KNOT_AGENT_ID (the daemon blocks the whole MULTICA_* namespace), so
// custom_args is the only per-agent override available. When the user supplies
// one, the daemon-wide env default must stand down rather than emit -a twice.
func knotArgsSelectAgent(args []string) bool {
	for _, arg := range args {
		arg = unshellQuoteArg(arg)
		if arg == "-a" || arg == "--agentId" ||
			strings.HasPrefix(arg, "-a=") || strings.HasPrefix(arg, "--agentId=") {
			return true
		}
	}
	return false
}

// knotArgsSelectSession reports whether a fixed/profile or per-agent argument
// already pins the Knot conversation. There is no short form for --sessionId.
func knotArgsSelectSession(args []string) bool {
	for _, arg := range args {
		arg = unshellQuoteArg(arg)
		if arg == "--sessionId" || strings.HasPrefix(arg, "--sessionId=") {
			return true
		}
	}
	return false
}

func buildKnotArgs(prompt string, opts ExecOptions, agentID string, logger *slog.Logger) []string {
	args := []string{"chat", "-p", prompt, "--output-format", "stream-json"}
	extra := filterCustomArgs(opts.ExtraArgs, knotBlockedArgs, logger)
	custom := filterCustomArgs(opts.CustomArgs, knotBlockedArgs, logger)
	// A user-pinned agent wins over the daemon-wide default.
	if agentID != "" && !knotArgsSelectAgent(extra) && !knotArgsSelectAgent(custom) {
		args = append(args, "-a", agentID)
	}
	if opts.Model != "" {
		args = append(args, "-m", opts.Model)
	}
	if opts.ResumeSessionID != "" &&
		!knotArgsSelectSession(extra) && !knotArgsSelectSession(custom) {
		args = append(args, "--sessionId", opts.ResumeSessionID)
	}
	if opts.ThinkingLevel != "" {
		// knot-cli splits reasoning into a boolean gate plus a level, and the
		// level alone is ignored unless thinking is enabled. `knot-cli model
		// list` advertises the per-model vocabulary this value comes from.
		args = append(args, "--enable-thinking", "--reasoning-effort", opts.ThinkingLevel)
	}
	args = append(args, extra...)
	args = append(args, custom...)
	return args
}

func (b *knotBackend) Execute(ctx context.Context, prompt string, opts ExecOptions) (*Session, error) {
	execPath := b.cfg.ExecutablePath
	if execPath == "" {
		execPath = knotDefaultBinary
	}
	if _, err := exec.LookPath(execPath); err != nil {
		return nil, fmt.Errorf("knot-cli executable not found at %q: %w", execPath, err)
	}
	timeout := opts.Timeout
	runCtx, cancel := runContext(ctx, timeout)
	agentID := knotAgentID(b.cfg.Env)
	args := buildKnotArgs(prompt, opts, agentID, b.cfg.Logger)

	cmd := exec.CommandContext(runCtx, execPath, args...)
	hideAgentWindow(cmd)
	// args carry the task prompt; never expose it in daemon logs.
	b.cfg.Logger.Info("agent command", "exec", execPath, "provider", "knot", "knot_agent_id", agentID)
	cmd.WaitDelay = 10 * time.Second
	if opts.Cwd != "" {
		cmd.Dir = opts.Cwd
	}
	cmd.Env = buildEnv(b.cfg.Env)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("knot stdout pipe: %w", err)
	}
	stderrBuf := newStderrTail(newLogWriter(b.cfg.Logger, "[knot:stderr] "), agentStderrTailBytes)
	cmd.Stderr = stderrBuf
	if err := startAgentProcess(cmd); err != nil {
		cancel()
		return nil, fmt.Errorf("start knot-cli: %w", err)
	}
	b.cfg.Logger.Info("knot-cli started", "pid", cmd.Process.Pid, "cwd", opts.Cwd, "model", opts.Model)

	msgCh := make(chan Message, 256)
	resCh := make(chan Result, 1)
	go func() {
		defer cancel()
		defer close(msgCh)
		defer close(resCh)

		started := time.Now()
		state := newKnotStreamState(opts.Model)
		go func() {
			<-runCtx.Done()
			_ = stdout.Close()
		}()

		scanErr := scanKnotEventStream(stdout, msgCh, state, false)
		if scanErr != nil {
			_ = stdout.Close()
		}
		exitErr := cmd.Wait()
		duration := time.Since(started)

		status, output, errMsg := finalizeStreamResult("knot", timeout, runCtx.Err(), nil, exitErr, state.sessionID, streamTerminalState{
			lastAssistantText:   state.lastAssistantText(),
			finalResultText:     state.finalResultText(),
			sawResult:           state.sawRunFinished,
			resultIsError:       state.runError != "",
			scanErr:             scanErr,
			terminalReasonError: state.terminalReasonError(),
		}, "")
		if errMsg != "" {
			errMsg = withAgentStderr(errMsg, "knot", stderrBuf.Tail())
		}
		logStreamProtocolObservation(b.cfg.Logger, streamProtocolObservation{
			provider: "knot", cliVersion: b.cfg.CLIVersion, model: state.model,
			exitCode: streamProcessExitCode(exitErr), eventCount: state.eventCount,
			invalidEventCount: state.invalidEventCount, assistantEventCount: state.assistantEventCount,
			toolUseCount: state.toolUseCount, sawResult: state.sawRunFinished,
			resultIsError: state.runError != "",
			resultBytes:   len(state.finalResultText()), lastAssistantBytes: len(state.lastAssistantText()),
			scannerError: scanErr != nil, lastEventType: state.lastEventType,
			unhandledEventTypeCount: len(state.unhandledTypes), unhandledEventTypes: state.unhandledTypeList(),
		})
		b.cfg.Logger.Info("knot-cli finished", "pid", cmd.Process.Pid, "status", status, "duration", duration.Round(time.Millisecond).String())
		resCh <- Result{
			Status: status, Output: output, Error: errMsg, DurationMs: duration.Milliseconds(),
			SessionID:      resolveSessionID(opts.ResumeSessionID, state.sessionID, status == "failed", errMsg),
			Usage:          state.usage,
			ResumeRejected: resumeWasRejected(opts.ResumeSessionID, state.sessionID, status == "failed", errMsg),
		}
	}()
	return &Session{Messages: msgCh, Result: resCh}, nil
}

// knotStreamEvent is one line of `--output-format stream-json`. Every payload
// field lives under rawEvent; the envelope carries only the discriminator, a
// wall-clock timestamp and a monotonic offset.
type knotStreamEvent struct {
	Type     string       `json:"type"`
	RawEvent knotRawEvent `json:"rawEvent"`
	Offset   int64        `json:"offset"`
	// Content is the top-level tool-result body. TOOL_CALL_RESULT carries the
	// human-readable output here, OUTSIDE rawEvent, while rawEvent.result holds
	// a structured echo of the call.
	Content string `json:"content,omitempty"`
}

type knotRawEvent struct {
	MessageID      string          `json:"message_id,omitempty"`
	ConversationID string          `json:"conversation_id,omitempty"`
	ToolCallID     string          `json:"tool_call_id,omitempty"`
	Name           string          `json:"name,omitempty"`
	DisplayName    string          `json:"display_name,omitempty"`
	Content        string          `json:"content,omitempty"`
	StepName       string          `json:"step_name,omitempty"`
	Model          string          `json:"model,omitempty"`
	Patchs         []knotArgsPatch `json:"patchs,omitempty"`
	TokenUsage     *knotTokenUsage `json:"token_usage,omitempty"`
	// Error/Message carry a mid-run failure on the event shapes that report
	// one. knot-cli v0.26.2 was not observed emitting these, so they are read
	// defensively: a future terminal error must not be mistaken for success.
	Error   json.RawMessage `json:"error,omitempty"`
	Message string          `json:"message,omitempty"`
}

// knotArgsPatch is one JSON-Patch fragment from TOOL_CALL_ARGS. Value is a
// PARTIAL chunk of the argument at Path, not the whole value.
type knotArgsPatch struct {
	Op    string          `json:"op"`
	Path  string          `json:"path"`
	Value json.RawMessage `json:"value"`
}

type knotTokenUsage struct {
	PromptTokens        int64 `json:"prompt_tokens"`
	CompletionTokens    int64 `json:"completion_tokens"`
	TotalTokens         int64 `json:"total_tokens"`
	PromptTokensDetails *struct {
		CachedTokens int64 `json:"cached_tokens"`
	} `json:"prompt_tokens_details,omitempty"`
}

// knotToolCall accumulates one in-flight tool call. Args fragments arrive
// between TOOL_CALL_START and TOOL_CALL_END and are only assembled once the
// call closes, because a fragment boundary can fall mid-escape-sequence.
type knotToolCall struct {
	name      string
	argChunks map[string]*strings.Builder
	argOrder  []string
	emitted   bool
}

type knotStreamState struct {
	sessionID, model, lastEventType string
	sawRunFinished                  bool
	// runError holds a mid-run failure read out of a structured field.
	runError string
	// unparsedLines keeps the first few non-JSON stdout lines. knot-cli prints
	// a rejected model as prose on stdout with no JSON at all, and that prose
	// is the only description of the failure the run will ever produce.
	unparsedLines []string

	// answer accumulates TEXT_MESSAGE_CONTENT deltas for the CURRENT message,
	// and completed holds messages already closed by TEXT_MESSAGE_END.
	answer    strings.Builder
	completed []string

	toolCalls map[string]*knotToolCall

	usage                                                            map[string]TokenUsage
	eventCount, invalidEventCount, assistantEventCount, toolUseCount int
	unhandledTypes                                                   map[string]struct{}
}

func newKnotStreamState(model string) *knotStreamState {
	return &knotStreamState{
		model:          model,
		toolCalls:      make(map[string]*knotToolCall),
		usage:          make(map[string]TokenUsage),
		unhandledTypes: make(map[string]struct{}),
	}
}

// knotMaxUnparsedLines bounds retained non-JSON stdout. A CLI that abandons the
// protocol entirely could otherwise stream unbounded prose into a task row.
const knotMaxUnparsedLines = 5

// knotSSEDoneSentinel terminates the HTTP AG-UI stream. The CLI's JSONL
// transport has no equivalent — it ends when stdout closes.
const knotSSEDoneSentinel = "[DONE]"

// scanKnotEventStream drives the AG-UI event loop over any line-delimited
// reader, so the CLI's stdout pipe and the HTTP transport's response body share
// one parser. It returns the scanner's error, if any.
//
// unwrapSSE selects the framing. false is the CLI's bare JSONL: every non-empty
// line is an event, and anything unparseable is retained by noteUnparsedLine
// because knot-cli reports a rejected model as bare prose with no JSON at all.
// true is the HTTP endpoint's Server-Sent Events: lines carry a `data:` prefix,
// the stream ends at a [DONE] sentinel, and SSE comments are framing rather
// than protocol content.
func scanKnotEventStream(r io.Reader, ch chan<- Message, state *knotStreamState, unwrapSSE bool) error {
	scanner := newAgentStreamScanner(r)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if unwrapSSE {
			// SSE comments (":" keep-alives) and non-data fields ("event:",
			// "id:", "retry:") are transport framing, never AG-UI events.
			if strings.HasPrefix(line, ":") {
				continue
			}
			data, isData := strings.CutPrefix(line, "data:")
			if !isData {
				continue
			}
			// TrimPrefix, NOT strings.Trim/lstrip: the vendor's own Python
			// sample uses .lstrip("data:"), which strips the CHARACTERS d/a/t/:
			// and would eat the leading brace-less bytes of any payload that
			// happens to start with one.
			line = strings.TrimSpace(data)
			if line == "" {
				continue
			}
			if line == knotSSEDoneSentinel {
				break
			}
		}
		var event knotStreamEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			state.noteUnparsedLine(line)
			continue
		}
		state.eventCount++
		state.lastEventType = event.Type
		handleKnotEvent(event, ch, state)
	}
	return scanner.Err()
}

// knotPreamblePrefixes are the plugin/skill sync lines knot-cli prints on stdout
// BEFORE any JSON event ("正在检查智能体Id为 … 缺少的 插件 并下载",
// "智能体Id为 … 的 skill 下载检查完成"). They are normal startup chatter, and
// retaining them would hand a failed run the preamble as its diagnosis instead
// of the actual error — the fixture opens with four of them.
var knotPreamblePrefixes = []string{"正在检查智能体Id", "智能体Id为"}

func (s *knotStreamState) noteUnparsedLine(line string) {
	s.invalidEventCount++
	// The trailing `requestID:`/`sessionID:` footer is normal output, not a
	// failure, so it must not be reported as one. sessionID there is a fallback
	// source for the conversation id when no event carried it.
	if rest, ok := strings.CutPrefix(line, "sessionID:"); ok {
		if s.sessionID == "" {
			s.sessionID = strings.TrimSpace(rest)
		}
		return
	}
	if strings.HasPrefix(line, "requestID:") {
		return
	}
	for _, prefix := range knotPreamblePrefixes {
		if strings.HasPrefix(line, prefix) {
			return
		}
	}
	// Keep the LAST few, not the first: when knot-cli abandons the protocol it
	// prints the reason last, and an earlier unrecognised banner must not
	// crowd it out.
	s.unparsedLines = append(s.unparsedLines, line)
	if len(s.unparsedLines) > knotMaxUnparsedLines {
		s.unparsedLines = s.unparsedLines[len(s.unparsedLines)-knotMaxUnparsedLines:]
	}
}

func (s *knotStreamState) unhandledTypeList() string {
	if len(s.unhandledTypes) == 0 {
		return ""
	}
	types := make([]string, 0, len(s.unhandledTypes))
	for t := range s.unhandledTypes {
		types = append(types, t)
	}
	sort.Strings(types)
	return strings.Join(types, ",")
}

// finalResultText is the answer knot-cli streamed, standing in for the terminal
// result event the protocol does not have.
func (s *knotStreamState) finalResultText() string {
	if len(s.completed) > 0 {
		return strings.TrimSpace(strings.Join(s.completed, "\n\n"))
	}
	// An unterminated message still holds the answer when the stream was cut
	// after the deltas but before TEXT_MESSAGE_END.
	return strings.TrimSpace(s.answer.String())
}

// lastAssistantText intentionally returns nothing. finalResultText already
// reports the accumulated answer, and finalizeStreamResult only consults this
// fallback when the terminal text is empty — which for this protocol means the
// assistant genuinely streamed no text, not that we failed to find it.
func (s *knotStreamState) lastAssistantText() string { return "" }

// terminalReasonError names a failure read from a structured field, plus any
// bare prose knot-cli printed instead of JSON (a rejected model). Empty keeps
// the shared contract's own exit-code and missing-terminal handling.
func (s *knotStreamState) terminalReasonError() string {
	if s.runError != "" {
		return s.runError
	}
	// Only speak up when the protocol never completed. A run that reached
	// RUN_FINISHED succeeded, and stray unparsed output must not fail it.
	if !s.sawRunFinished && len(s.unparsedLines) > 0 {
		return "knot-cli reported: " + sanitizeAgentDiagnostic(strings.Join(s.unparsedLines, " "))
	}
	return ""
}

func handleKnotEvent(event knotStreamEvent, ch chan<- Message, state *knotStreamState) {
	raw := event.RawEvent
	if raw.ConversationID != "" {
		state.sessionID = raw.ConversationID
	}
	if raw.Model != "" {
		state.model = raw.Model
	}

	switch event.Type {
	case "RUN_STARTED":
		trySend(ch, Message{Type: MessageStatus, Status: "running", SessionID: state.sessionID})
	case "RUN_FINISHED":
		state.sawRunFinished = true
	case "RUN_ERROR", "ERROR", "RUN_FAILED":
		// Not observed on v0.26.2; handled so a future terminal error fails the
		// run instead of passing as a silent success.
		state.sawRunFinished = true
		state.runError = knotErrorText(raw)
	case "TEXT_MESSAGE_START":
		state.assistantEventCount++
		state.answer.Reset()
	case "TEXT_MESSAGE_CONTENT":
		if raw.Content != "" {
			state.answer.WriteString(raw.Content)
			trySend(ch, Message{Type: MessageText, Content: raw.Content})
		}
	case "TEXT_MESSAGE_END":
		if text := strings.TrimSpace(state.answer.String()); text != "" {
			state.completed = append(state.completed, text)
		}
		state.answer.Reset()
	case "THINKING_TEXT_MESSAGE_CONTENT":
		if raw.Content != "" {
			trySend(ch, Message{Type: MessageThinking, Content: raw.Content})
		}
	case "TOOL_CALL_START":
		state.toolCalls[raw.ToolCallID] = &knotToolCall{
			name:      knotToolName(raw),
			argChunks: make(map[string]*strings.Builder),
		}
	case "TOOL_CALL_ARGS":
		knotAccumulateArgs(state, raw)
	case "TOOL_CALL_END":
		knotFlushToolCall(state, raw.ToolCallID, ch)
	case "TOOL_CALL_RESULT":
		// A result can arrive for a call whose END we never saw; flush first so
		// the tool-use message always precedes its result.
		knotFlushToolCall(state, raw.ToolCallID, ch)
		trySend(ch, Message{
			Type:   MessageToolResult,
			CallID: raw.ToolCallID,
			Output: knotToolResultOutput(event),
		})
	case "HEARTBEAT", "STEP_STARTED", "CUSTOM",
		"THINKING_TEXT_MESSAGE_START", "THINKING_TEXT_MESSAGE_END":
		// Liveness, step framing, and thinking-block delimiters carry no
		// content of their own. The daemon's watchdog reads liveness from the
		// message channel, which the surrounding content events already feed.
	case "STEP_FINISHED":
		// Usage is per step, so a turn reports several; accumulate rather than
		// overwrite or the recorded cost is only the final step's.
		knotAccumulateUsage(state, raw)
	default:
		state.unhandledTypes[event.Type] = struct{}{}
	}
}

// knotToolName prefers the machine tool name and falls back to the localized
// display label, so an unnamed call still reads as something in the transcript.
func knotToolName(raw knotRawEvent) string {
	if raw.Name != "" {
		return raw.Name
	}
	return raw.DisplayName
}

func knotAccumulateArgs(state *knotStreamState, raw knotRawEvent) {
	call := state.toolCalls[raw.ToolCallID]
	if call == nil {
		// Args before START: keep them rather than drop the call entirely.
		call = &knotToolCall{argChunks: make(map[string]*strings.Builder)}
		state.toolCalls[raw.ToolCallID] = call
	}
	for _, patch := range raw.Patchs {
		if patch.Op == "remove" {
			continue
		}
		key := strings.TrimPrefix(patch.Path, "/")
		if key == "" {
			continue
		}
		chunk, ok := call.argChunks[key]
		if !ok {
			chunk = &strings.Builder{}
			call.argChunks[key] = chunk
			call.argOrder = append(call.argOrder, key)
		}
		chunk.WriteString(knotPatchValueText(patch.Value))
	}
}

// knotPatchValueText renders one patch value as the text to append. A JSON
// string decodes to its contents (the fragments concatenate into the real
// value); any other shape is appended as compact JSON so structured arguments
// survive without pretending they are text.
func knotPatchValueText(value json.RawMessage) string {
	if len(value) == 0 {
		return ""
	}
	var text string
	if json.Unmarshal(value, &text) == nil {
		return text
	}
	return string(value)
}

// knotFlushToolCall emits the assembled tool-use message exactly once, after
// every fragment for the call has been concatenated.
func knotFlushToolCall(state *knotStreamState, callID string, ch chan<- Message) {
	call := state.toolCalls[callID]
	if call == nil || call.emitted {
		return
	}
	call.emitted = true
	state.toolUseCount++
	input := make(map[string]any, len(call.argOrder))
	for _, key := range call.argOrder {
		assembled := call.argChunks[key].String()
		// A fragmented value is a string by construction. Re-parse it only when
		// it is self-contained JSON so structured args round-trip, and keep the
		// raw text otherwise.
		var decoded any
		if json.Unmarshal([]byte(assembled), &decoded) == nil {
			input[key] = decoded
		} else {
			input[key] = assembled
		}
	}
	trySend(ch, Message{Type: MessageToolUse, Tool: call.name, CallID: callID, Input: input})
}

func knotToolResultOutput(event knotStreamEvent) string {
	if event.Content != "" {
		return event.Content
	}
	if event.RawEvent.Content != "" {
		return event.RawEvent.Content
	}
	// Fall back to the localized completion label so the transcript records
	// that the call closed even when no body was sent.
	return event.RawEvent.DisplayName
}

func knotAccumulateUsage(state *knotStreamState, raw knotRawEvent) {
	if raw.TokenUsage == nil {
		return
	}
	model := state.model
	if model == "" {
		// Usage must still be recorded when no model was pinned; key it to the
		// provider so the row is attributable rather than silently dropped.
		model = "knot"
	}
	entry := state.usage[model]
	entry.InputTokens += raw.TokenUsage.PromptTokens
	entry.OutputTokens += raw.TokenUsage.CompletionTokens
	if details := raw.TokenUsage.PromptTokensDetails; details != nil {
		entry.CacheReadTokens += details.CachedTokens
	}
	state.usage[model] = entry
}

func knotErrorText(raw knotRawEvent) string {
	if raw.Message != "" {
		return raw.Message
	}
	var body struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(raw.Error, &body) == nil && body.Message != "" {
		return body.Message
	}
	if len(raw.Error) > 0 {
		return string(raw.Error)
	}
	return "knot-cli returned an error event without details"
}

// discoverKnotModels enumerates `knot-cli model list`, whose output is
// space-padded columns:
//
//	claude-4.8-opus  supports-thinking  context=200K,1M  thinking_effort=low,…  non_thinking_effort=…
//
// The catalog is account-scoped rather than agent-scoped (all registered agents
// returned an identical list on v0.26.2), so it is discovered once without an
// agent id. A failure returns an empty list so the picker degrades to manual
// entry instead of advertising a guess.
func discoverKnotModels(ctx context.Context, executablePath string) ([]Model, error) {
	if executablePath == "" {
		executablePath = knotDefaultBinary
	}
	if _, err := exec.LookPath(executablePath); err != nil {
		return []Model{}, nil
	}
	runCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(runCtx, executablePath, "model", "list")
	hideAgentWindow(cmd)
	// Discovery must not inherit the daemon's MULTICA_* namespace, and needs no
	// task environment of its own.
	cmd.Env = buildEnv(nil)
	out, err := cmd.Output()
	if err != nil && len(out) == 0 {
		return []Model{}, nil
	}
	return parseKnotModels(string(out)), nil
}

// parseKnotModels reads the column layout of `knot-cli model list`. It is
// deliberately strict: a line must start with an id-shaped token, otherwise it
// is banner or prompt text and is skipped. A wrong entry here becomes a model
// string the CLI rejects, which is worse than an empty picker.
func parseKnotModels(out string) []Model {
	var models []Model
	seen := make(map[string]bool)
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(strings.TrimSpace(strings.ReplaceAll(line, "\r", "")))
		if len(fields) == 0 {
			continue
		}
		id := fields[0]
		if !knotLooksLikeModelID(id) || seen[id] {
			continue
		}
		seen[id] = true
		model := Model{ID: id, Label: id}
		var thinkingLevels, nonThinkingLevels []string
		supportsThinking := false
		for _, field := range fields[1:] {
			switch {
			case field == "supports-thinking":
				supportsThinking = true
			case strings.HasPrefix(field, "thinking_effort="):
				thinkingLevels = knotSplitLevels(strings.TrimPrefix(field, "thinking_effort="))
			case strings.HasPrefix(field, "non_thinking_effort="):
				nonThinkingLevels = knotSplitLevels(strings.TrimPrefix(field, "non_thinking_effort="))
			}
		}
		// Advertise a thinking catalog only for a model that declares it
		// supports thinking AND names levels. non_thinking_effort exists on
		// models with no reasoning mode at all, so treating it as a thinking
		// catalog would show a reasoning picker that does nothing.
		levels := thinkingLevels
		if len(levels) == 0 && supportsThinking {
			levels = nonThinkingLevels
		}
		if supportsThinking && len(levels) > 0 {
			model.Thinking = &ModelThinking{SupportedLevels: knotThinkingLevels(levels)}
		}
		models = append(models, model)
	}
	return models
}

func knotSplitLevels(value string) []string {
	var levels []string
	for _, part := range strings.Split(value, ",") {
		if part = strings.TrimSpace(part); part != "" {
			levels = append(levels, part)
		}
	}
	return levels
}

func knotThinkingLevels(values []string) []ThinkingLevel {
	levels := make([]ThinkingLevel, 0, len(values))
	for _, value := range values {
		levels = append(levels, ThinkingLevel{Value: value, Label: value})
	}
	return levels
}

// knotLooksLikeModelID accepts the lowercase, punctuation-separated ids
// knot-cli uses (claude-4.8-opus, gpt-5.6-sol, tokenhub_deepseek-v4-pro) and
// rejects table headers, Chinese banner text and TUI decoration.
func knotLooksLikeModelID(token string) bool {
	if len(token) < 3 || len(token) > 96 {
		return false
	}
	hasLetter := false
	for _, r := range token {
		switch {
		case r >= 'a' && r <= 'z':
			hasLetter = true
		case r >= '0' && r <= '9', r == '-', r == '_', r == '.', r == '/':
		default:
			return false
		}
	}
	return hasLetter
}

// knotKnownAgentIDs lists the agent ids `knot-cli list-agents` reports, mapped
// to their display names, so a configured id can be checked before it is used.
// An invalid -a does NOT make knot-cli fail: it silently falls back to a
// default agent, so a typo would otherwise run as an agent nobody chose.
func knotKnownAgentIDs(ctx context.Context, executablePath string) (map[string]string, error) {
	if executablePath == "" {
		executablePath = knotDefaultBinary
	}
	if _, err := exec.LookPath(executablePath); err != nil {
		return nil, err
	}
	runCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(runCtx, executablePath, "list-agents")
	hideAgentWindow(cmd)
	cmd.Env = buildEnv(nil)
	out, err := cmd.Output()
	if err != nil && len(out) == 0 {
		return nil, err
	}
	return parseKnotAgents(string(out)), nil
}

// knotAgentIDLen is the length of the hex agent ids knot-cli reports.
const knotAgentIDLen = 32

// parseKnotAgents reads the `list-agents` stanza layout, keyed by id:
//
//	智能体名称: 全能选手
//	id: 384328a66c52440b93ae811a6ce3a08f
func parseKnotAgents(out string) map[string]string {
	agents := make(map[string]string)
	name := ""
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(strings.ReplaceAll(line, "\r", ""))
		_, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		switch {
		case strings.HasPrefix(line, "id:"):
			if knotLooksLikeAgentID(value) {
				agents[value] = name
				name = ""
			}
		case strings.Contains(line, "名称") || strings.HasPrefix(line, "name"):
			name = value
		}
	}
	return agents
}

// discoverKnotAgents lists the Knot agents registered on this machine for the
// settings UI's agent picker, sorted by display name for a stable dropdown.
//
// Returns nil rather than an error when the CLI is missing or the call fails:
// an empty list means "offer manual entry instead of a dropdown", which keeps a
// runtime whose binary is absent or offline configurable. Failing would
// otherwise take the whole model catalog down with it, since both share one
// discovery round.
func discoverKnotAgents(ctx context.Context, executablePath string) []KnotAgentEntry {
	known, err := knotKnownAgentIDs(ctx, executablePath)
	if err != nil || len(known) == 0 {
		return nil
	}
	entries := make([]KnotAgentEntry, 0, len(known))
	for id, name := range known {
		if name == "" {
			// An unnamed agent still has to be selectable; the id is the only
			// label left.
			name = id
		}
		entries = append(entries, KnotAgentEntry{ID: id, Name: name})
	}
	// knotKnownAgentIDs returns a map, so without this the dropdown would
	// reshuffle on every discovery round.
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Name != entries[j].Name {
			return entries[i].Name < entries[j].Name
		}
		return entries[i].ID < entries[j].ID
	})
	return entries
}

// LooksLikeKnotAgentID reports whether token has the shape of an id from
// `knot-cli list-agents` (32 lowercase hex characters).
//
// Exported for the daemon, which validates a user-supplied per-agent id out of
// runtime_config before it reaches a backend. That check cannot be left to the
// backend alone: knot-cli silently substitutes its own default agent for an
// unknown id, so a typo would quietly bill and behave as a different agent
// rather than failing.
func LooksLikeKnotAgentID(token string) bool {
	return knotLooksLikeAgentID(token)
}

func knotLooksLikeAgentID(token string) bool {
	if len(token) != knotAgentIDLen {
		return false
	}
	for _, r := range token {
		if !(r >= '0' && r <= '9') && !(r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

// VerifyKnotAgentID reports whether the MULTICA_KNOT_AGENT_ID configured for a
// knot runtime names an agent the CLI actually knows. It returns ok=true when no
// id is configured (knot-cli picks its own default) and when the check itself
// could not run — an unreachable CLI is not evidence of a bad id.
//
// This exists because knot-cli treats an unknown -a as "use the default"
// instead of an error, so nothing downstream would ever surface the mistake.
func VerifyKnotAgentID(ctx context.Context, executablePath string, env map[string]string) (ok bool, configured string, known map[string]string) {
	configured = knotAgentID(env)
	if configured == "" {
		return true, "", nil
	}
	agents, err := knotKnownAgentIDs(ctx, executablePath)
	if err != nil || len(agents) == 0 {
		return true, configured, nil
	}
	_, found := agents[configured]
	return found, configured, agents
}

// KnownKnotAgentNames returns the display names of every agent knot-cli reports,
// for inclusion in an operator-facing diagnostic. Sorted so the message is
// stable across rounds.
func KnownKnotAgentNames(known map[string]string) []string {
	names := make([]string, 0, len(known))
	for id, name := range known {
		if name == "" {
			name = id
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
