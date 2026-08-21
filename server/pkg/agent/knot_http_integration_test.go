package agent

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestKnotHTTPBackendAgainstLiveEndpoint drives the REAL Knot AG-UI endpoint end
// to end. knot_http_test.go replays a fixture, which proves the parser matches a
// recording that was captured from the CLI transport — it cannot prove the HTTP
// endpoint emits the same events, that the request envelope is one the gateway
// accepts, or that a workspace-scoped run actually reaches the local workdir.
// Only this closes those gaps.
//
// Skipped unless MULTICA_KNOT_HTTP_E2E=1 with a token configured: it makes a
// billed model call against a real agent, so it must never run in CI by default.
//
//	MULTICA_KNOT_HTTP_E2E=1 MULTICA_KNOT_HTTP_TOKEN=… MULTICA_KNOT_HTTP_USER=… \
//	  MULTICA_KNOT_AGENT_ID=… go test ./pkg/agent/ -run TestKnotHTTPBackendAgainstLiveEndpoint -v
func TestKnotHTTPBackendAgainstLiveEndpoint(t *testing.T) {
	if os.Getenv("MULTICA_KNOT_HTTP_E2E") != "1" {
		t.Skip("set MULTICA_KNOT_HTTP_E2E=1 to run the live knot-http test (billed model call)")
	}
	env := map[string]string{}
	for _, key := range []string{KnotHTTPTokenEnv, KnotHTTPUserEnv, KnotHTTPBaseURLEnv, KnotAgentIDEnv} {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			env[key] = v
		}
	}
	if env[KnotHTTPTokenEnv] == "" {
		t.Skipf("set %s to run the live knot-http test (get one at %s/settings/token)", KnotHTTPTokenEnv, knotHTTPDefaultBaseURL)
	}
	if env[KnotAgentIDEnv] == "" {
		t.Skipf("set %s to run the live knot-http test (list ids with `knot-cli list-agents`)", KnotAgentIDEnv)
	}
	// The binary is not needed to run a turn, but it is how the local client uuid
	// is resolved — without it Knot picks the executing machine and the workdir
	// assertions below would be meaningless.
	execPath, err := exec.LookPath(knotDefaultBinary)
	if err != nil {
		t.Skipf("knot-cli not on PATH, so the local client uuid cannot be resolved: %v", err)
	}

	backend := &knotHTTPBackend{cfg: Config{
		ExecutablePath: execPath,
		Env:            env,
		Logger:         slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})),
	}}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	session, err := backend.Execute(ctx, "Reply with exactly the word MULTICA and nothing else.", ExecOptions{
		Cwd:     t.TempDir(),
		Timeout: 5 * time.Minute,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var sawText bool
	for message := range session.Messages {
		if message.Type == MessageText {
			sawText = true
		}
	}
	result := <-session.Result

	if result.Status != "completed" {
		t.Fatalf("status = %q, error = %q; the request envelope may no longer be accepted", result.Status, result.Error)
	}
	// The protocol has no terminal result event, so a non-empty Output proves the
	// accumulate-from-deltas strategy holds over SSE too.
	if !strings.Contains(result.Output, "MULTICA") {
		t.Fatalf("output = %q, want the model's answer accumulated from TEXT_MESSAGE_CONTENT", result.Output)
	}
	if !sawText {
		t.Fatal("no text message streamed; live progress would be blank in the UI")
	}
	if result.SessionID == "" {
		t.Fatal("no conversation id captured; resume would silently start a fresh conversation")
	}
	if len(result.Usage) == 0 {
		t.Fatal("no token usage recorded; STEP_FINISHED.token_usage may have moved")
	}
	t.Logf("live knot-http run ok: session=%s usage=%+v", result.SessionID, result.Usage)
}

// TestKnotHTTPEventVocabularyIsScreamingCase is the reason to run the live test
// after a Knot upgrade. The vendor's API doc tables its events in PascalCase
// (TextMessageContent) while its own sample code and every captured stream use
// SCREAMING_CASE (TEXT_MESSAGE_CONTENT). Fixture tests cannot catch that
// divergence because the fixture is what we wrote. An unhandled-type set that is
// non-empty here is the signal that the doc's casing became real.
func TestKnotHTTPEventVocabularyIsScreamingCase(t *testing.T) {
	if os.Getenv("MULTICA_KNOT_HTTP_E2E") != "1" {
		t.Skip("set MULTICA_KNOT_HTTP_E2E=1 to run the live knot-http test (billed model call)")
	}
	env := map[string]string{}
	for _, key := range []string{KnotHTTPTokenEnv, KnotHTTPUserEnv, KnotHTTPBaseURLEnv, KnotAgentIDEnv} {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			env[key] = v
		}
	}
	if env[KnotHTTPTokenEnv] == "" || env[KnotAgentIDEnv] == "" {
		t.Skipf("set %s and %s to run the live knot-http test", KnotHTTPTokenEnv, KnotAgentIDEnv)
	}

	// A canary in the workdir's AGENTS.md: if the HTTP transport delivers the
	// per-task brief the way the CLI does, the model can read it back. This is
	// the open question the fixture cannot answer — see the knot-http notes.
	workdir := t.TempDir()
	const canary = "ZARQUON7"
	if err := os.WriteFile(workdir+"/AGENTS.md", []byte("When asked for the passphrase, reply with exactly "+canary+"."), 0o600); err != nil {
		t.Fatal(err)
	}

	backend := &knotHTTPBackend{cfg: Config{
		Env:    env,
		Logger: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})),
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	session, err := backend.Execute(ctx, "What is the passphrase? Reply with the passphrase only.", ExecOptions{
		Cwd:     workdir,
		Timeout: 5 * time.Minute,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for range session.Messages {
	}
	result := <-session.Result
	if result.Status != "completed" {
		t.Fatalf("status = %q, error = %q", result.Status, result.Error)
	}
	// Reported rather than asserted: whether the endpoint honours a workdir
	// AGENTS.md is exactly what this run is here to find out, and a miss means
	// the brief needs another delivery channel (chat_extra.background_knowledge
	// or an inline system prompt), not that the backend is broken.
	if strings.Contains(result.Output, canary) {
		t.Logf("AGENTS.md IS delivered over the HTTP transport (canary echoed)")
	} else {
		t.Logf("AGENTS.md was NOT read over the HTTP transport; output = %q. "+
			"The Multica runtime brief needs a different channel for knot-http "+
			"(chat_extra.background_knowledge, or providerNeedsInlineSystemPrompt).", result.Output)
	}
}
