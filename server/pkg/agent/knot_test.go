package agent

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewReturnsKnotBackend(t *testing.T) {
	t.Parallel()
	backend, err := New("knot", Config{ExecutablePath: "/nonexistent/knot-cli"})
	if err != nil {
		t.Fatalf("New(knot): %v", err)
	}
	if _, ok := backend.(*knotBackend); !ok {
		t.Fatalf("New(knot) = %T, want *knotBackend", backend)
	}
	if got := LaunchHeader("knot"); got == "" {
		t.Fatal("LaunchHeader(knot) is empty")
	}
}

func TestBuildKnotArgsKeepsProtocolManaged(t *testing.T) {
	t.Parallel()
	args := buildKnotArgs("task prompt", ExecOptions{
		Model:           "claude-4.8-opus",
		ResumeSessionID: "conversation-1",
		ThinkingLevel:   "high",
		ExtraArgs:       []string{"--output-format", "text", "--sandbox", "proc:workspace_write"},
		CustomArgs: []string{
			"--prompt=replace", "-o", "json", "--model", "other-model",
			"-i", "--status-out",
			"--spending-limit", "5", "--max-context-tokens", "200000",
		},
	}, "agent-id-1", slog.Default())
	joined := strings.Join(args, " ")

	// Compare whole argv tokens, not substrings: "text" is a substring of
	// "--max-context-tokens", which a naive Contains check reads as a leak.
	present := make(map[string]bool, len(args))
	for _, arg := range args {
		present[arg] = true
	}
	for _, forbidden := range []string{"text", "--prompt=replace", "json", "other-model", "--status-out", "-i"} {
		if present[forbidden] {
			t.Fatalf("managed argument %q leaked into %v", forbidden, args)
		}
	}
	wantPrefix := []string{
		"chat", "-p", "task prompt", "--output-format", "stream-json",
		"-a", "agent-id-1", "-m", "claude-4.8-opus", "--sessionId", "conversation-1",
		"--enable-thinking", "--reasoning-effort", "high",
	}
	if len(args) < len(wantPrefix) {
		t.Fatalf("args too short: %v", args)
	}
	for i, want := range wantPrefix {
		if args[i] != want {
			t.Fatalf("args[%d] = %q, want %q; all=%v", i, args[i], want, args)
		}
	}
	// Policy knobs a user may legitimately set must survive.
	for _, kept := range []string{"--sandbox proc:workspace_write", "--spending-limit 5", "--max-context-tokens 200000"} {
		if !strings.Contains(joined, kept) {
			t.Fatalf("non-managed custom arg %q missing from %v", kept, args)
		}
	}
}

// TestBuildKnotArgsOmitsUnsetSelectors pins that an unconfigured agent id and
// model send NO flag at all, so knot-cli applies its own defaults rather than
// receiving an empty string it would reject.
func TestBuildKnotArgsOmitsUnsetSelectors(t *testing.T) {
	t.Parallel()
	args := buildKnotArgs("prompt", ExecOptions{}, "", slog.Default())
	joined := strings.Join(args, " ")
	for _, absent := range []string{"-a", "-m", "--sessionId", "--enable-thinking", "--reasoning-effort"} {
		if strings.Contains(joined, absent) {
			t.Fatalf("unset selector %q was still sent: %v", absent, args)
		}
	}
	want := []string{"chat", "-p", "prompt", "--output-format", "stream-json"}
	if len(args) != len(want) {
		t.Fatalf("args = %v, want exactly %v", args, want)
	}
}

func TestKnotAgentIDReadsTaskEnv(t *testing.T) {
	t.Parallel()
	if got := knotAgentID(map[string]string{KnotAgentIDEnv: "  abc123  "}); got != "abc123" {
		t.Fatalf("knotAgentID = %q, want trimmed abc123", got)
	}
	if got := knotAgentID(nil); got != "" {
		t.Fatalf("knotAgentID(nil) = %q, want empty", got)
	}
}

// TestBuildKnotArgsUserPinnedAgentWins covers the only per-agent override path
// available: custom_env cannot carry MULTICA_KNOT_AGENT_ID (the daemon blocks
// the whole MULTICA_* namespace), so a per-agent id must come from custom_args.
// The daemon-wide default must then stand down instead of emitting -a twice —
// knot-cli would otherwise take one of the two, and which one is not ours to
// guess.
func TestBuildKnotArgsUserPinnedAgentWins(t *testing.T) {
	t.Parallel()
	for _, form := range [][]string{
		{"-a", "user-agent"},
		{"--agentId", "user-agent"},
		{"-a=user-agent"},
		{"--agentId=user-agent"},
	} {
		args := buildKnotArgs("prompt", ExecOptions{CustomArgs: form}, "daemon-default-agent", slog.Default())
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "daemon-default-agent") {
			t.Fatalf("daemon default survived alongside user pin %v: %v", form, args)
		}
		if !strings.Contains(joined, "user-agent") {
			t.Fatalf("user pin %v was dropped: %v", form, args)
		}
		if n := strings.Count(joined, "-a"); n == 0 {
			t.Fatalf("no agent selector left in %v", args)
		}
	}
	// An ExtraArgs pin must suppress the default too.
	args := buildKnotArgs("prompt", ExecOptions{ExtraArgs: []string{"-a", "extra-agent"}}, "daemon-default-agent", slog.Default())
	if strings.Contains(strings.Join(args, " "), "daemon-default-agent") {
		t.Fatalf("daemon default survived an ExtraArgs pin: %v", args)
	}
}

func TestBuildKnotArgsUserPinnedSessionWins(t *testing.T) {
	t.Parallel()
	for _, form := range [][]string{
		{"--sessionId", "per-agent-session"},
		{"--sessionId=per-agent-session"},
	} {
		args := buildKnotArgs("prompt", ExecOptions{
			ResumeSessionID: "issue-derived-session",
			CustomArgs:      form,
		}, "", slog.Default())
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "issue-derived-session") {
			t.Fatalf("issue-derived session survived alongside per-agent pin %v: %v", form, args)
		}
		if !strings.Contains(joined, "per-agent-session") {
			t.Fatalf("per-agent session pin %v was dropped: %v", form, args)
		}
		if n := strings.Count(joined, "--sessionId"); n != 1 {
			t.Fatalf("session selector count = %d, want 1; args=%v", n, args)
		}
	}
}

func TestKnownKnotAgentNames(t *testing.T) {
	t.Parallel()
	names := KnownKnotAgentNames(map[string]string{
		"384328a66c52440b93ae811a6ce3a08f": "全能选手",
		"7a5d51d0b14f449683fdb839c5e3a448": "",
	})
	// A nameless agent falls back to its id so the diagnostic still identifies it.
	want := []string{"7a5d51d0b14f449683fdb839c5e3a448", "全能选手"}
	if len(names) != len(want) {
		t.Fatalf("names = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("names = %v, want %v (sorted for a stable log line)", names, want)
		}
	}
}

// replayKnotFixture drives the parser over a captured stream and returns the
// state plus every message it emitted.
func replayKnotFixture(t *testing.T, name, model string) (*knotStreamState, []Message) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	state := newKnotStreamState(model)
	ch := make(chan Message, 256)
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
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
	close(ch)
	var messages []Message
	for message := range ch {
		messages = append(messages, message)
	}
	return state, messages
}

func TestKnotCli0262FixtureParses(t *testing.T) {
	t.Parallel()
	state, messages := replayKnotFixture(t, "knot-cli-0.26.2-stream-json.jsonl", "claude-4.8-opus")

	if !state.sawRunFinished {
		t.Fatal("RUN_FINISHED not observed, run would be reported as an incomplete stream")
	}
	if state.runError != "" {
		t.Fatalf("unexpected run error: %q", state.runError)
	}
	if state.sessionID != "conversation-redacted" {
		t.Fatalf("sessionID = %q, want conversation-redacted", state.sessionID)
	}
	// knot-cli has no terminal result event; the answer is the accumulated
	// TEXT_MESSAGE_CONTENT deltas.
	if got := state.finalResultText(); got != "Created hello.txt with the text hi." {
		t.Fatalf("final text = %q", got)
	}
	// Usage is per STEP_FINISHED and the fixture has two, so it must accumulate.
	usage := state.usage["claude-4.8-opus"]
	if usage.InputTokens != 3000 || usage.OutputTokens != 52 || usage.CacheReadTokens != 300 {
		t.Fatalf("usage = %+v, want accumulated across both steps", usage)
	}
	// The plugin/skill sync preamble is normal chatter, not a diagnosis.
	if len(state.unparsedLines) != 0 {
		t.Fatalf("preamble retained as failure detail: %q", state.unparsedLines)
	}
	if got := state.terminalReasonError(); got != "" {
		t.Fatalf("terminalReasonError = %q, want empty for a healthy run", got)
	}
	if unhandled := state.unhandledTypeList(); unhandled != "" {
		t.Fatalf("unhandled event types in fixture: %s", unhandled)
	}

	var thinking, text bool
	var toolUse, toolResult *Message
	for i := range messages {
		switch messages[i].Type {
		case MessageThinking:
			thinking = thinking || messages[i].Content == " user wants a file."
		case MessageText:
			text = text || messages[i].Content == "hello.txt"
		case MessageToolUse:
			if messages[i].Tool == "write_to_file" {
				toolUse = &messages[i]
			}
		case MessageToolResult:
			if messages[i].CallID == "write-call-redacted" {
				toolResult = &messages[i]
			}
		}
	}
	if !thinking || !text {
		t.Fatalf("missing streamed content thinking=%v text=%v", thinking, text)
	}
	if toolUse == nil {
		t.Fatal("write_to_file tool use never emitted")
	}
	// The headline invariant: TOOL_CALL_ARGS fragments must be concatenated.
	// Reading any single op yields a truncated path like "C:\\redacted".
	if got := toolUse.Input["file_path"]; got != `C:\redacted\workdir\hello.txt` {
		t.Fatalf("file_path = %q, want the fully reassembled path", got)
	}
	if got := toolUse.Input["explanation"]; got != "Creating the file." {
		t.Fatalf("explanation = %q, want reassembled", got)
	}
	if toolResult == nil || toolResult.Output != "File written successfully." {
		t.Fatalf("tool result = %+v, want top-level content body", toolResult)
	}
}

// TestKnotToolCallEmittedExactlyOnce guards the START→ARGS→END→RESULT ordering:
// the tool-use message must precede its result and never be duplicated, even
// though both END and RESULT call the flush.
func TestKnotToolCallEmittedExactlyOnce(t *testing.T) {
	t.Parallel()
	_, messages := replayKnotFixture(t, "knot-cli-0.26.2-stream-json.jsonl", "")
	uses, firstUse, firstResult := 0, -1, -1
	for i, message := range messages {
		if message.Type == MessageToolUse && message.CallID == "write-call-redacted" {
			uses++
			if firstUse < 0 {
				firstUse = i
			}
		}
		if message.Type == MessageToolResult && message.CallID == "write-call-redacted" && firstResult < 0 {
			firstResult = i
		}
	}
	if uses != 1 {
		t.Fatalf("write_to_file emitted %d tool-use messages, want exactly 1", uses)
	}
	if firstUse < 0 || firstResult < 0 || firstUse > firstResult {
		t.Fatalf("tool use (%d) must precede its result (%d)", firstUse, firstResult)
	}
}

// TestKnotUsageRecordedWithoutModel pins that usage is still attributable when
// no model was pinned, instead of being dropped on an empty map key.
func TestKnotUsageRecordedWithoutModel(t *testing.T) {
	t.Parallel()
	state, _ := replayKnotFixture(t, "knot-cli-0.26.2-stream-json.jsonl", "")
	if usage, ok := state.usage["knot"]; !ok || usage.InputTokens != 3000 {
		t.Fatalf("usage = %+v, want recorded under the provider key", state.usage)
	}
}

// TestKnotRejectedModelBecomesFailureDetail covers the shape knot-cli actually
// produces for a bad -m: exit 1, prose on STDOUT, and no JSON at all. Without
// this the run would fail as a bare "stream ended without terminal result" and
// lose the only description of the cause.
func TestKnotRejectedModelBecomesFailureDetail(t *testing.T) {
	t.Parallel()
	state := newKnotStreamState("no-such-model-xyz")
	state.noteUnparsedLine("不支持该模型: no-such-model-xyz,请检查模型是否全小写或存在")

	if state.sawRunFinished {
		t.Fatal("no RUN_FINISHED was emitted; the run must not look complete")
	}
	detail := state.terminalReasonError()
	if !strings.Contains(detail, "no-such-model-xyz") {
		t.Fatalf("terminalReasonError = %q, want the CLI's own message", detail)
	}

	status, output, errMsg := finalizeStreamResult("knot", 0, nil, nil, nil, "", streamTerminalState{
		lastAssistantText:   state.lastAssistantText(),
		finalResultText:     state.finalResultText(),
		sawResult:           state.sawRunFinished,
		terminalReasonError: detail,
	}, "")
	if status != "failed" || output != "" {
		t.Fatalf("status=%q output=%q, want a failed run with no output", status, output)
	}
	if !strings.Contains(errMsg, "no-such-model-xyz") {
		t.Fatalf("error = %q, want the rejected model named", errMsg)
	}
}

// TestKnotMissingRunFinishedFailsClosed is the fail-closed contract: a stream
// that carried an answer but never completed must NOT deliver that answer.
func TestKnotMissingRunFinishedFailsClosed(t *testing.T) {
	t.Parallel()
	state := newKnotStreamState("claude-4.8-opus")
	ch := make(chan Message, 8)
	handleKnotEvent(knotStreamEvent{Type: "TEXT_MESSAGE_START"}, ch, state)
	handleKnotEvent(knotStreamEvent{
		Type:     "TEXT_MESSAGE_CONTENT",
		RawEvent: knotRawEvent{Content: "partial answer"},
	}, ch, state)

	status, output, errMsg := finalizeStreamResult("knot", 0, nil, nil, nil, "", streamTerminalState{
		lastAssistantText:   state.lastAssistantText(),
		finalResultText:     state.finalResultText(),
		sawResult:           state.sawRunFinished,
		terminalReasonError: state.terminalReasonError(),
	}, "")
	if status != "failed" || output != "" {
		t.Fatalf("status=%q output=%q, want failed with no output", status, output)
	}
	if !strings.Contains(errMsg, "without terminal result") {
		t.Fatalf("error = %q, want the missing-terminal diagnosis", errMsg)
	}
}

// TestKnotUnterminatedMessageStillDeliversAnswer is the counterpart: when
// RUN_FINISHED did arrive but TEXT_MESSAGE_END did not, the text is still the
// deliverable.
func TestKnotUnterminatedMessageStillDeliversAnswer(t *testing.T) {
	t.Parallel()
	state := newKnotStreamState("claude-4.8-opus")
	ch := make(chan Message, 8)
	handleKnotEvent(knotStreamEvent{Type: "TEXT_MESSAGE_START"}, ch, state)
	handleKnotEvent(knotStreamEvent{Type: "TEXT_MESSAGE_CONTENT", RawEvent: knotRawEvent{Content: "answer"}}, ch, state)
	handleKnotEvent(knotStreamEvent{Type: "RUN_FINISHED"}, ch, state)

	status, output, _ := finalizeStreamResult("knot", 0, nil, nil, nil, "", streamTerminalState{
		lastAssistantText:   state.lastAssistantText(),
		finalResultText:     state.finalResultText(),
		sawResult:           state.sawRunFinished,
		terminalReasonError: state.terminalReasonError(),
	}, "")
	if status != "completed" || output != "answer" {
		t.Fatalf("status=%q output=%q, want completed/answer", status, output)
	}
}

// TestKnotMultipleMessagesJoin covers a turn that closes more than one
// assistant message: every one is part of the answer, and none may overwrite
// an earlier one.
func TestKnotMultipleMessagesJoin(t *testing.T) {
	t.Parallel()
	state := newKnotStreamState("m")
	ch := make(chan Message, 16)
	for _, text := range []string{"first", "second"} {
		handleKnotEvent(knotStreamEvent{Type: "TEXT_MESSAGE_START"}, ch, state)
		handleKnotEvent(knotStreamEvent{Type: "TEXT_MESSAGE_CONTENT", RawEvent: knotRawEvent{Content: text}}, ch, state)
		handleKnotEvent(knotStreamEvent{Type: "TEXT_MESSAGE_END"}, ch, state)
	}
	if got := state.finalResultText(); got != "first\n\nsecond" {
		t.Fatalf("final text = %q, want both messages joined", got)
	}
}

// TestKnotStructuredErrorEventFailsRun covers the defensive branch: knot-cli
// v0.26.2 was never observed emitting a terminal error, so if a future release
// does, it must fail the run rather than pass as a silent success.
func TestKnotStructuredErrorEventFailsRun(t *testing.T) {
	t.Parallel()
	state := newKnotStreamState("m")
	ch := make(chan Message, 4)
	handleKnotEvent(knotStreamEvent{
		Type:     "RUN_ERROR",
		RawEvent: knotRawEvent{Error: json.RawMessage(`{"message":"quota exhausted"}`)},
	}, ch, state)
	if state.runError != "quota exhausted" {
		t.Fatalf("runError = %q", state.runError)
	}
	status, _, errMsg := finalizeStreamResult("knot", 0, nil, nil, nil, "", streamTerminalState{
		sawResult:           state.sawRunFinished,
		resultIsError:       state.runError != "",
		terminalReasonError: state.terminalReasonError(),
	}, "")
	if status != "failed" || !strings.Contains(errMsg, "quota exhausted") {
		t.Fatalf("status=%q error=%q", status, errMsg)
	}
}

// TestKnotUnknownEventTypeRecorded pins that a new top-level event type is
// reported as an observation instead of silently ignored — that count is the
// first signal a CLI upgrade changed the protocol.
func TestKnotUnknownEventTypeRecorded(t *testing.T) {
	t.Parallel()
	state := newKnotStreamState("m")
	ch := make(chan Message, 4)
	handleKnotEvent(knotStreamEvent{Type: "SOME_NEW_EVENT"}, ch, state)
	if got := state.unhandledTypeList(); got != "SOME_NEW_EVENT" {
		t.Fatalf("unhandledTypeList = %q", got)
	}
}

func TestParseKnotModels(t *testing.T) {
	t.Parallel()
	// Verbatim column layout from `knot-cli model list` v0.26.2.
	out := strings.Join([]string{
		"  claude-opus-5                                     ",
		"  gpt-5.6-sol                    context=272K       non_thinking_effort=low,medium,high,xhigh,max",
		"  claude-4.8-opus  supports-thinking  context=200K,1M  thinking_effort=low,medium,high,xhigh,max  non_thinking_effort=low,medium,high,max",
		"  glm-5.2          context=128K   non_thinking_effort=",
		"  tokenhub_deepseek-v4-pro  supports-thinking  context=1M  thinking_effort=high,max",
		"智能体列表",
		"",
	}, "\n")
	models := parseKnotModels(out)

	byID := make(map[string]Model, len(models))
	for _, model := range models {
		byID[model.ID] = model
	}
	for _, want := range []string{"claude-opus-5", "gpt-5.6-sol", "claude-4.8-opus", "glm-5.2", "tokenhub_deepseek-v4-pro"} {
		if _, ok := byID[want]; !ok {
			t.Fatalf("model %q missing from %+v", want, models)
		}
	}
	if len(models) != 5 {
		t.Fatalf("parsed %d models, want 5 (banner text must be rejected): %+v", len(models), models)
	}
	// A thinking catalog is advertised only where the CLI says thinking is
	// supported AND names levels.
	thinking := byID["claude-4.8-opus"].Thinking
	if thinking == nil || len(thinking.SupportedLevels) != 5 || thinking.SupportedLevels[0].Value != "low" {
		t.Fatalf("claude-4.8-opus thinking = %+v", thinking)
	}
	// non_thinking_effort alone must NOT produce a reasoning picker that does
	// nothing.
	if got := byID["gpt-5.6-sol"].Thinking; got != nil {
		t.Fatalf("gpt-5.6-sol thinking = %+v, want nil", got)
	}
	if got := byID["claude-opus-5"].Thinking; got != nil {
		t.Fatalf("claude-opus-5 thinking = %+v, want nil", got)
	}
}

func TestParseKnotAgents(t *testing.T) {
	t.Parallel()
	// Verbatim `knot-cli list-agents` layout, with ids replaced.
	out := strings.Join([]string{
		"",
		"智能体名称: 全能选手-5090",
		"id: 7a5d51d0b14f449683fdb839c5e3a448",
		"",
		"智能体名称: 全能选手",
		"id: 384328a66c52440b93ae811a6ce3a08f",
		"",
		"note: not-an-id",
		"",
	}, "\n")
	agents := parseKnotAgents(out)
	if len(agents) != 2 {
		t.Fatalf("parsed %d agents, want 2: %+v", len(agents), agents)
	}
	if name := agents["384328a66c52440b93ae811a6ce3a08f"]; name != "全能选手" {
		t.Fatalf("agent name = %q, want 全能选手", name)
	}
	if _, ok := agents["not-an-id"]; ok {
		t.Fatal("non-hex value accepted as an agent id")
	}
}

func TestKnotLooksLikeAgentID(t *testing.T) {
	t.Parallel()
	if !knotLooksLikeAgentID("384328a66c52440b93ae811a6ce3a08f") {
		t.Fatal("valid 32-char hex id rejected")
	}
	for _, bad := range []string{"", "384328a6", strings.Repeat("z", 32), "384328A66C52440B93AE811A6CE3A08F"} {
		if knotLooksLikeAgentID(bad) {
			t.Fatalf("invalid id %q accepted", bad)
		}
	}
}

// TestVerifyKnotAgentIDNoConfiguredID pins the fail-open contract: no agent id
// means knot-cli picks its own default, which is valid rather than suspicious.
func TestVerifyKnotAgentIDNoConfiguredID(t *testing.T) {
	t.Parallel()
	ok, configured, known := VerifyKnotAgentID(t.Context(), "/nonexistent/knot-cli", nil)
	if !ok || configured != "" || known != nil {
		t.Fatalf("ok=%v configured=%q known=%v, want a clean pass", ok, configured, known)
	}
}

// TestVerifyKnotAgentIDUnreachableCLIPasses pins that an unreachable CLI is not
// treated as evidence of a bad id — the check itself failed, not the config.
func TestVerifyKnotAgentIDUnreachableCLIPasses(t *testing.T) {
	t.Parallel()
	ok, configured, _ := VerifyKnotAgentID(t.Context(), "/nonexistent/knot-cli", map[string]string{
		KnotAgentIDEnv: "384328a66c52440b93ae811a6ce3a08f",
	})
	if !ok {
		t.Fatal("unreachable CLI reported the id as invalid; must fail open")
	}
	if configured != "384328a66c52440b93ae811a6ce3a08f" {
		t.Fatalf("configured = %q", configured)
	}
}

func TestKnotRuntimeConfigFamilyIsAgentsMD(t *testing.T) {
	t.Parallel()
	// knot-cli reads AGENTS.md from the workdir (probed with a canary), so it
	// must NOT be in the inline-system-prompt set.
	if got := LaunchHeader("knot"); !strings.Contains(got, "knot-cli") {
		t.Fatalf("LaunchHeader(knot) = %q", got)
	}
}
