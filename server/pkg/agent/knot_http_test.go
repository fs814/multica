package agent

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testKnotAgentID = "ec4633074fe4413c83218e1f36b8e24d"

// knotHTTPTestLogger keeps backend logging out of test output.
func knotHTTPTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// knotHTTPTestBackend builds a backend pointed at srv with a valid token, so
// each test states only what it is actually varying.
func knotHTTPTestBackend(t *testing.T, srv *httptest.Server, env map[string]string) *knotHTTPBackend {
	t.Helper()
	merged := map[string]string{
		KnotHTTPTokenEnv:  "test-token",
		KnotAgentIDEnv:    testKnotAgentID,
		KnotClientUUIDEnv: "remote",
	}
	for k, v := range env {
		merged[k] = v
	}
	return &knotHTTPBackend{
		cfg:        Config{Env: merged, Logger: knotHTTPTestLogger(), ExecutablePath: filepath.Join(t.TempDir(), "missing-knot-cli")},
		baseURL:    srv.URL,
		httpClient: srv.Client(),
	}
}

// serveSSEFixture replays a captured SSE stream and records the request the
// backend sent, so request-shape assertions do not need a second server.
func serveSSEFixture(t *testing.T, fixture string, gotReq *http.Request, gotBody *[]byte) *httptest.Server {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", fixture))
	if err != nil {
		t.Fatal(err)
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if gotBody != nil {
			*gotBody = body
		}
		if gotReq != nil {
			*gotReq = *r
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
	}))
}

// drainKnotHTTP runs one turn to completion.
func drainKnotHTTP(t *testing.T, b *knotHTTPBackend, opts ExecOptions) (Result, []Message) {
	t.Helper()
	session, err := b.Execute(context.Background(), "do the thing", opts)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var messages []Message
	for message := range session.Messages {
		messages = append(messages, message)
	}
	select {
	case res := <-session.Result:
		return res, messages
	case <-time.After(30 * time.Second):
		t.Fatal("timed out waiting for result")
		return Result{}, nil
	}
}

// TestKnotHTTPReplaysAGUIStream is the headline case: the SSE transport must
// reach the same verdict as the CLI transport on the same event stream, since
// both share knot.go's parser.
func TestKnotHTTPReplaysAGUIStream(t *testing.T) {
	t.Parallel()
	srv := serveSSEFixture(t, "knot-http-agui-sse.txt", nil, nil)
	defer srv.Close()

	res, messages := drainKnotHTTP(t, knotHTTPTestBackend(t, srv, nil), ExecOptions{Model: "claude-4.8-opus"})

	if res.Status != "completed" {
		t.Fatalf("status = %q, error = %q; want completed", res.Status, res.Error)
	}
	if res.Output != "Created hello.txt with the text hi." {
		t.Fatalf("output = %q", res.Output)
	}
	if res.SessionID != "conversation-redacted" {
		t.Fatalf("sessionID = %q, want the conversation_id from rawEvent", res.SessionID)
	}
	// Usage accumulates across both STEP_FINISHED events, exactly as on the CLI.
	if usage := res.Usage["claude-4.8-opus"]; usage.InputTokens != 3000 || usage.OutputTokens != 52 || usage.CacheReadTokens != 300 {
		t.Fatalf("usage = %+v, want accumulated across both steps", usage)
	}
	// Fragmented TOOL_CALL_ARGS must be reassembled, not emitted per-fragment.
	// Reading any single JSON-Patch op yields a truncated path.
	var toolUse *Message
	for i := range messages {
		if messages[i].Type == MessageToolUse && messages[i].Tool == "write_to_file" {
			toolUse = &messages[i]
		}
	}
	if toolUse == nil {
		t.Fatal("write_to_file tool use never emitted")
	}
	if got := toolUse.Input["file_path"]; got != `C:\redacted\workdir\hello.txt` {
		t.Fatalf("file_path = %q, want the fully reassembled path", got)
	}
}

// TestKnotHTTPSendsDocumentedRequestShape pins the wire contract: agent id in
// the path, auth headers, and the workspace/uuid fields that decide WHERE the
// agent's file tools run. A regression here silently runs the task against the
// wrong machine or the wrong directory.
func TestKnotHTTPSendsDocumentedRequestShape(t *testing.T) {
	t.Parallel()
	var gotReq http.Request
	var gotBody []byte
	srv := serveSSEFixture(t, "knot-http-agui-sse.txt", &gotReq, &gotBody)
	defer srv.Close()

	b := knotHTTPTestBackend(t, srv, map[string]string{KnotHTTPUserEnv: "shengfeng", KnotClientUUIDEnv: "11111111-1111-4111-8111-111111111111"})
	workdir := t.TempDir()
	res, _ := drainKnotHTTP(t, b, ExecOptions{
		Model:           "claude-4.8-opus",
		Cwd:             workdir,
		ResumeSessionID: "prior-conversation",
		ThinkingLevel:   "high",
	})
	if res.Status != "completed" {
		t.Fatalf("status = %q, error = %q", res.Status, res.Error)
	}

	if want := knotHTTPAGUIPath + testKnotAgentID; gotReq.URL.Path != want {
		t.Errorf("request path = %q, want %q (the agent id selects the agent server-side)", gotReq.URL.Path, want)
	}
	if got := gotReq.Header.Get(knotHTTPTokenHeader); got != "test-token" {
		t.Errorf("%s = %q, want the resolved token", knotHTTPTokenHeader, got)
	}
	if got := gotReq.Header.Get(knotHTTPUserHeader); got != "shengfeng" {
		t.Errorf("%s = %q, want the acting user", knotHTTPUserHeader, got)
	}

	var sent knotHTTPRequest
	if err := json.Unmarshal(gotBody, &sent); err != nil {
		t.Fatalf("request body is not the documented envelope: %v", err)
	}
	if sent.Input.Message != "do the thing" {
		t.Errorf("message = %q", sent.Input.Message)
	}
	if !sent.Input.Stream {
		t.Error("stream = false; the backend parses an SSE stream and must request one")
	}
	if sent.Input.ConversationID != "prior-conversation" {
		t.Errorf("conversation_id = %q, want the resume session id", sent.Input.ConversationID)
	}
	if sent.Input.Model != "claude-4.8-opus" {
		t.Errorf("model = %q", sent.Input.Model)
	}
	// The workdir must be the agent's working directory, or the task's diff
	// would be collected from a directory the agent never touched.
	if len(sent.Input.ChatExtra.Workspace) != 1 || sent.Input.ChatExtra.Workspace[0] != workdir {
		t.Errorf("chat_extra.workspace = %v, want [%q]", sent.Input.ChatExtra.Workspace, workdir)
	}
	// A bare reasoning_effort is ignored by the API unless thinking is enabled.
	if !sent.Input.ChatExtra.EnableThinking || sent.Input.ChatExtra.ReasoningEffort != "high" {
		t.Errorf("thinking = %v/%q, want enabled with the requested effort",
			sent.Input.ChatExtra.EnableThinking, sent.Input.ChatExtra.ReasoningEffort)
	}
}

// TestKnotHTTPStripsDataPrefixWithoutEatingPayload guards the specific bug the
// vendor's own sample invites: .lstrip("data:") strips CHARACTERS, so a payload
// whose first byte is d/a/t/: gets silently corrupted. The parser must trim the
// prefix, not the character set.
func TestKnotHTTPStripsDataPrefixWithoutEatingPayload(t *testing.T) {
	t.Parallel()
	// "data:" is followed immediately by a payload starting with 'd' (no space),
	// and the text content itself begins with characters from the prefix set.
	stream := "data:{\"type\":\"RUN_STARTED\",\"rawEvent\":{\"conversation_id\":\"c1\"}}\n" +
		"data: {\"type\":\"TEXT_MESSAGE_START\",\"rawEvent\":{}}\n" +
		"data: {\"type\":\"TEXT_MESSAGE_CONTENT\",\"rawEvent\":{\"content\":\"data: at::a\"}}\n" +
		"data: {\"type\":\"TEXT_MESSAGE_END\",\"rawEvent\":{}}\n" +
		": keep-alive comment\n" +
		"\n" +
		"data: {\"type\":\"RUN_FINISHED\",\"rawEvent\":{}}\n" +
		"data: [DONE]\n" +
		"data: {\"type\":\"TEXT_MESSAGE_CONTENT\",\"rawEvent\":{\"content\":\"after done\"}}\n"

	state := newKnotStreamState("m")
	ch := make(chan Message, 64)
	if err := scanKnotEventStream(strings.NewReader(stream), ch, state, true); err != nil {
		t.Fatalf("scan: %v", err)
	}
	close(ch)

	if !state.sawRunFinished {
		t.Fatal("RUN_FINISHED not parsed")
	}
	if got := state.finalResultText(); got != "data: at::a" {
		t.Fatalf("content = %q, want the payload preserved byte-for-byte", got)
	}
	if state.sessionID != "c1" {
		t.Fatalf("sessionID = %q, want c1 from the no-space `data:` line", state.sessionID)
	}
	// Everything after [DONE] must be ignored, and an SSE comment is framing.
	if strings.Contains(state.finalResultText(), "after done") {
		t.Error("events after [DONE] were parsed; the sentinel must stop the stream")
	}
	if state.invalidEventCount != 0 {
		t.Errorf("invalidEventCount = %d, want 0 (comments/blanks are framing, not bad events)", state.invalidEventCount)
	}
}

// TestKnotHTTPMissingTokenFailsBeforeRequest asserts the misconfiguration is
// named as such. Without this the request goes out and the gateway answers
// HTTP 200 with a Chinese auth error, which reads as a protocol fault.
func TestKnotHTTPMissingTokenFailsBeforeRequest(t *testing.T) {
	t.Parallel()
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer srv.Close()

	b := &knotHTTPBackend{
		cfg:        Config{Env: map[string]string{KnotAgentIDEnv: testKnotAgentID}, Logger: knotHTTPTestLogger()},
		baseURL:    srv.URL,
		httpClient: srv.Client(),
	}
	_, err := b.Execute(context.Background(), "p", ExecOptions{})
	if err == nil {
		t.Fatal("Execute succeeded without a token")
	}
	if !strings.Contains(err.Error(), KnotHTTPTokenEnv) || !strings.Contains(err.Error(), knotHTTPTokenCustomEnv) {
		t.Errorf("error does not name both token settings: %v", err)
	}
	if called {
		t.Error("an unauthenticated request was sent; it must fail before the call")
	}
}

// TestKnotHTTPPerAgentTokenWins covers the override precedence that exists
// because isBlockedEnvKey drops MULTICA_* from custom_env: the un-prefixed
// per-agent key is the only one a user can set per agent, so it must win.
func TestKnotHTTPPerAgentTokenWins(t *testing.T) {
	t.Parallel()
	var gotReq http.Request
	srv := serveSSEFixture(t, "knot-http-agui-sse.txt", &gotReq, nil)
	defer srv.Close()

	b := knotHTTPTestBackend(t, srv, map[string]string{
		knotHTTPTokenCustomEnv: "per-agent-token",
		knotHTTPUserCustomEnv:  "per-agent-user",
		KnotHTTPUserEnv:        "daemon-user",
	})
	if res, _ := drainKnotHTTP(t, b, ExecOptions{}); res.Status != "completed" {
		t.Fatalf("status = %q", res.Status)
	}
	if got := gotReq.Header.Get(knotHTTPTokenHeader); got != "per-agent-token" {
		t.Errorf("token = %q, want the per-agent override", got)
	}
	if got := gotReq.Header.Get(knotHTTPUserHeader); got != "per-agent-user" {
		t.Errorf("user = %q, want the per-agent override", got)
	}
}

// TestKnotHTTPPerAgentAgentIDWins covers the override that makes two knot-http
// agents on one machine able to serve DIFFERENT Knot identities. The daemon-wide
// MULTICA_KNOT_AGENT_ID cannot be set per agent (isBlockedEnvKey strips the whole
// MULTICA_* namespace from custom_env), so KNOT_AGENT_ID is the only per-agent
// route — and it must beat the daemon default, not merely fill in for it.
func TestKnotHTTPPerAgentAgentIDWins(t *testing.T) {
	t.Parallel()
	const perAgentID = "384328a66c52440b93ae811a6ce3a08f"
	var gotReq http.Request
	srv := serveSSEFixture(t, "knot-http-agui-sse.txt", &gotReq, nil)
	defer srv.Close()

	// The helper seeds KnotAgentIDEnv with testKnotAgentID; the per-agent key
	// must win over it.
	b := knotHTTPTestBackend(t, srv, map[string]string{knotHTTPAgentIDCustomEnv: perAgentID})
	if res, _ := drainKnotHTTP(t, b, ExecOptions{}); res.Status != "completed" {
		t.Fatalf("status = %q", res.Status)
	}
	if want := knotHTTPAGUIPath + perAgentID; gotReq.URL.Path != want {
		t.Fatalf("request path = %q, want %q (the per-agent id must select the agent)", gotReq.URL.Path, want)
	}
}

// TestKnotHTTPAgentIDFallsBackToDaemonWide is the other half of the precedence
// rule: with no per-agent key, the daemon-wide default still applies, so a
// single-identity setup needs no per-agent config at all.
func TestKnotHTTPAgentIDFallsBackToDaemonWide(t *testing.T) {
	t.Parallel()
	for name, env := range map[string]map[string]string{
		"unset":           nil,
		"empty":           {knotHTTPAgentIDCustomEnv: ""},
		"whitespace only": {knotHTTPAgentIDCustomEnv: "   "},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var gotReq http.Request
			srv := serveSSEFixture(t, "knot-http-agui-sse.txt", &gotReq, nil)
			defer srv.Close()
			b := knotHTTPTestBackend(t, srv, env)
			if res, _ := drainKnotHTTP(t, b, ExecOptions{}); res.Status != "completed" {
				t.Fatalf("status = %q", res.Status)
			}
			if want := knotHTTPAGUIPath + testKnotAgentID; gotReq.URL.Path != want {
				t.Fatalf("request path = %q, want the daemon-wide id %q", gotReq.URL.Path, want)
			}
		})
	}
}

// TestKnotHTTPMissingAgentIDNamesBothKeys guards the error text. The message is
// the only guidance a user gets at the moment of failure, so it must name the
// per-agent key too — pointing only at the daemon-wide env var sends someone
// editing machine-level settings when they wanted one agent changed.
func TestKnotHTTPMissingAgentIDNamesBothKeys(t *testing.T) {
	t.Parallel()
	b := &knotHTTPBackend{cfg: Config{
		Env:    map[string]string{KnotHTTPTokenEnv: "t"},
		Logger: knotHTTPTestLogger(),
	}}
	_, err := b.Execute(context.Background(), "p", ExecOptions{})
	if err == nil {
		t.Fatal("Execute succeeded with no agent id")
	}
	for _, want := range []string{knotHTTPAgentIDCustomEnv, KnotAgentIDEnv, "list-agents"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q: %v", want, err)
		}
	}
}

// TestKnotHTTPRejectsMalformedAgentID pins the fail-closed choice. knot-cli
// silently substitutes a default agent for an unknown id, so a typo bills and
// behaves as a different agent; over HTTP the id is in the URL, so we can and
// must reject it up front.
func TestKnotHTTPRejectsMalformedAgentID(t *testing.T) {
	t.Parallel()
	for name, id := range map[string]string{
		"truncated":     "ec4633074fe4413c",
		"non-hex":       "zzzz33074fe4413c83218e1f36b8e24d",
		"too long":      testKnotAgentID + "ab",
		"embedded dash": "ec4633074fe4413c-3218e1f36b8e24d",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			// Supplied through the per-agent key: a hand-typed id is exactly
			// where a typo comes from.
			b := &knotHTTPBackend{cfg: Config{
				Env:    map[string]string{KnotHTTPTokenEnv: "t", knotHTTPAgentIDCustomEnv: id},
				Logger: knotHTTPTestLogger(),
			}}
			if _, err := b.Execute(context.Background(), "p", ExecOptions{}); err == nil {
				t.Fatalf("Execute accepted agent id %q", id)
			}
		})
	}
}

// TestKnotHTTPSurfacesGatewayRefusal checks that a non-200 carries the
// gateway's own message. finalizeStreamResult would otherwise report "stream
// ended without terminal result", which describes the symptom, not the cause.
func TestKnotHTTPSurfacesGatewayRefusal(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"msg":"you have no permission to access agent","code":190004}`))
	}))
	defer srv.Close()

	_, err := knotHTTPTestBackend(t, srv, nil).Execute(context.Background(), "p", ExecOptions{})
	if err == nil {
		t.Fatal("Execute succeeded on an HTTP 403")
	}
	if !strings.Contains(err.Error(), "no permission") || !strings.Contains(err.Error(), "190004") {
		t.Errorf("error drops the gateway's own diagnosis: %v", err)
	}
}

// TestKnotHTTPTruncatedStreamFailsClosed guards the property knot.go documents:
// the protocol has no terminal result event, so a stream cut short mid-answer
// must fail rather than deliver a partial answer as a success.
func TestKnotHTTPTruncatedStreamFailsClosed(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"type\":\"RUN_STARTED\",\"rawEvent\":{\"conversation_id\":\"c1\"}}\n" +
			"data: {\"type\":\"TEXT_MESSAGE_START\",\"rawEvent\":{}}\n" +
			"data: {\"type\":\"TEXT_MESSAGE_CONTENT\",\"rawEvent\":{\"content\":\"partial\"}}\n"))
	}))
	defer srv.Close()

	res, _ := drainKnotHTTP(t, knotHTTPTestBackend(t, srv, nil), ExecOptions{})
	if res.Status != "failed" {
		t.Fatalf("status = %q, want failed for a stream with no RUN_FINISHED", res.Status)
	}
	if res.Output != "" {
		t.Errorf("output = %q, want empty on failure", res.Output)
	}
	if !strings.Contains(res.Error, "knot-http") {
		t.Errorf("error is not attributed to knot-http: %q", res.Error)
	}
}

// TestParseKnotClientUUID covers the banner-then-JSON shape of
// `knot-cli client-status`, and the failure modes that must degrade to "" so a
// broken probe never dispatches the run to a guessed machine.
//
// The headline case is field selection: client-status reports BOTH `uuid`
// ("894300") and `connection_uuid` (a UUIDv4), and only the latter is what
// chat_extra.agent_client_uuid accepts. Reading `uuid` compiles, parses, and
// looks plausible — then Knot silently picks its own machine and the task's
// file edits never reach the workdir.
func TestParseKnotClientUUID(t *testing.T) {
	t.Parallel()
	// Verbatim shape of real `knot-cli client-status` output (v0.26.2).
	real := "✅ success\n\nAgent Status Details:\n  {\n" +
		"    \"arch\": \"amd64\",\n" +
		"    \"connection_uuid\": \"68b7d6d7-8eb5-4598-830e-d71bcc739672\",\n" +
		"    \"host_user\": \"SHENGFENG-PC4-5\\\\shengfeng\",\n" +
		"    \"instance_id\": \"c48c329751ad6529\",\n" +
		"    \"os\": \"windows\",\n" +
		"    \"status\": \"ready\",\n" +
		"    \"uuid\": \"894300\"\n" +
		"  }\n"
	for name, tc := range map[string]struct{ in, want string }{
		"real output":             {real, "68b7d6d7-8eb5-4598-830e-d71bcc739672"},
		"no json":                 {"service not running", ""},
		"empty":                   {"", ""},
		"missing connection uuid": {"{\"os\":\"windows\",\"uuid\":\"894300\"}", ""},
		"malformed":               {"{\"connection_uuid\": ", ""},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := parseKnotClientUUID(tc.in); got != tc.want {
				t.Fatalf("parseKnotClientUUID(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestNewReturnsKnotHTTPBackend pins the factory wiring; the lockstep tests
// only prove New() returns SOMETHING for every supported type.
func TestNewReturnsKnotHTTPBackend(t *testing.T) {
	t.Parallel()
	b, err := New("knot-http", Config{Logger: knotHTTPTestLogger()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, ok := b.(*knotHTTPBackend); !ok {
		t.Fatalf("New returned %T, want *knotHTTPBackend", b)
	}
}

// sentClientUUID drives one turn and returns the agent_client_uuid the backend
// actually put on the wire, so the three client-uuid modes can be asserted end
// to end rather than only in the resolver unit.
func sentClientUUID(t *testing.T, env map[string]string) string {
	t.Helper()
	var gotBody []byte
	srv := serveSSEFixture(t, "knot-http-agui-sse.txt", nil, &gotBody)
	defer srv.Close()
	b := knotHTTPTestBackend(t, srv, env)
	res, _ := drainKnotHTTP(t, b, ExecOptions{Model: "claude-4.8-opus"})
	if res.Status != "completed" {
		t.Fatalf("status = %q, error = %q", res.Status, res.Error)
	}
	var sent knotHTTPRequest
	if err := json.Unmarshal(gotBody, &sent); err != nil {
		t.Fatalf("request body is not the documented envelope: %v", err)
	}
	return sent.Input.ChatExtra.AgentClientUUID
}

// TestKnotHTTPClientUUIDRemoteOmitsField is the headline case for this feature:
// "remote" must send NO agent_client_uuid, which is what makes Knot dispatch the
// run to the agent's own registered machine instead of this host.
func TestKnotHTTPClientUUIDRemoteOmitsField(t *testing.T) {
	t.Parallel()
	for _, v := range []string{"remote", "Remote", "REMOTE", "  remote  "} {
		if got := sentClientUUID(t, map[string]string{knotHTTPClientUUIDCustomEnv: v}); got != "" {
			t.Fatalf("client uuid %q: sent agent_client_uuid = %q, want empty (remote dispatch)", v, got)
		}
	}
}

// TestKnotHTTPClientUUIDExplicitTargetsMachine pins that an explicit UUIDv4 is
// forwarded verbatim, so an operator can name a specific registered client.
func TestKnotHTTPClientUUIDExplicitTargetsMachine(t *testing.T) {
	t.Parallel()
	const uuid = "68b7d6d7-8eb5-4598-830e-d71bcc739672"
	if got := sentClientUUID(t, map[string]string{knotHTTPClientUUIDCustomEnv: uuid}); got != uuid {
		t.Fatalf("sent agent_client_uuid = %q, want %q", got, uuid)
	}
}

// TestKnotHTTPClientUUIDSetting pins the precedence the daemon relies on:
// the un-prefixed custom_env key wins over the MULTICA_-prefixed runtime_config
// slot, exactly like the token and agent-id overrides.
func TestKnotHTTPClientUUIDSetting(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		env  map[string]string
		want string
	}{
		"custom_env wins over runtime_config": {
			map[string]string{knotHTTPClientUUIDCustomEnv: "remote", KnotClientUUIDEnv: "runtime-value"},
			"remote",
		},
		"runtime_config used when no custom_env": {
			map[string]string{KnotClientUUIDEnv: "runtime-value"},
			"runtime-value",
		},
		"blank custom_env falls through": {
			map[string]string{knotHTTPClientUUIDCustomEnv: "   ", KnotClientUUIDEnv: "runtime-value"},
			"runtime-value",
		},
		"neither set": {map[string]string{}, ""},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := knotHTTPClientUUIDSetting(tc.env); got != tc.want {
				t.Fatalf("knotHTTPClientUUIDSetting = %q, want %q", got, tc.want)
			}
		})
	}
}

// Invalid or unavailable local targets must fail before any HTTP request.
func TestKnotHTTPClientTargetFailsClosed(t *testing.T) {
	for _, setting := range []string{"", "not-a-uuid"} {
		t.Run(setting, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				t.Error("unexpected HTTP request for an unresolved target")
			}))
			defer srv.Close()
			b := knotHTTPTestBackend(t, srv, map[string]string{KnotClientUUIDEnv: setting})
			session, err := b.Execute(context.Background(), "test", ExecOptions{})
			if err == nil || session != nil {
				t.Fatal("expected a pre-request target error")
			}
		})
	}
}

func TestLooksLikeKnotClientUUID(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]bool{
		"68b7d6d7-8eb5-4598-830e-d71bcc739672": true,
		"68B7D6D7-8EB5-4598-830E-D71BCC739672": true, // UUIDs may be upper-case
		"":                                     false,
		"remote":                               false,
		"68b7d6d7-8eb5-4598-830e-d71bcc73967":  false, // one short
		"68b7d6d78eb545988300ed71bcc739672xxx": false, // no separators
		"68b7d6d7x8eb5-4598-830e-d71bcc739672": false, // wrong separator
		"zzzzzzzz-8eb5-4598-830e-d71bcc739672": false, // non-hex
	} {
		if got := LooksLikeKnotClientUUID(in); got != want {
			t.Fatalf("LooksLikeKnotClientUUID(%q) = %v, want %v", in, got, want)
		}
	}
}

// Remote execution must not receive a path from the dispatcher's filesystem.
func TestKnotHTTPRemoteOmitsLocalWorkspace(t *testing.T) {
	t.Parallel()
	for _, setting := range []string{"remote", "REMOTE"} {
		t.Run(setting, func(t *testing.T) {
			var body []byte
			srv := serveSSEFixture(t, "knot-http-agui-sse.txt", nil, &body)
			defer srv.Close()
			backend := knotHTTPTestBackend(t, srv, map[string]string{KnotClientUUIDEnv: setting})
			result, _ := drainKnotHTTP(t, backend, ExecOptions{Cwd: `D:\dispatch-host\task-workspace`})
			if result.Status != "completed" {
				t.Fatalf("execution failed: %s", result.Error)
			}
			var sent knotHTTPRequest
			if err := json.Unmarshal(body, &sent); err != nil {
				t.Fatal(err)
			}
			if len(sent.Input.ChatExtra.Workspace) != 0 {
				t.Errorf("remote received local workspace: %v", sent.Input.ChatExtra.Workspace)
			}
			if sent.Input.ChatExtra.AgentClientUUID != "" {
				t.Errorf("remote received client UUID: %q", sent.Input.ChatExtra.AgentClientUUID)
			}
			if strings.Contains(string(body), "dispatch-host") {
				t.Error("request leaked dispatcher's local path")
			}
		})
	}
}
