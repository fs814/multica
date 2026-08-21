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

// TestKnotBackendAgainstRealCLI drives the REAL knot-cli binary end to end.
// Everything else in knot_test.go replays a captured stream, which proves the
// parser matches a recording — not that the recording still matches the CLI, and
// not that the argv we build is one knot-cli accepts. Only this closes that gap.
//
// Skipped unless MULTICA_KNOT_E2E=1 and knot-cli is on PATH: it needs a
// logged-in CLI and makes a billed model call, so it must never run in CI by
// default. Run it after a knot-cli upgrade — a protocol change shows up here as
// an empty Output or a missing tool event while every fixture test still passes.
//
//	MULTICA_KNOT_E2E=1 go test ./pkg/agent/ -run TestKnotBackendAgainstRealCLI -v
func TestKnotBackendAgainstRealCLI(t *testing.T) {
	if os.Getenv("MULTICA_KNOT_E2E") != "1" {
		t.Skip("set MULTICA_KNOT_E2E=1 to run the live knot-cli test (billed model call)")
	}
	execPath, err := exec.LookPath(knotDefaultBinary)
	if err != nil {
		t.Skipf("knot-cli not on PATH: %v", err)
	}

	env := map[string]string{}
	if agentID := strings.TrimSpace(os.Getenv(KnotAgentIDEnv)); agentID != "" {
		env[KnotAgentIDEnv] = agentID
	}
	backend := &knotBackend{cfg: Config{
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
		t.Fatalf("status = %q, error = %q; the argv we build may no longer be accepted", result.Status, result.Error)
	}
	// The headline claim: knot-cli has no terminal result event, so a non-empty
	// Output proves the accumulate-from-deltas strategy still works.
	if !strings.Contains(result.Output, "MULTICA") {
		t.Fatalf("output = %q, want the model's answer accumulated from TEXT_MESSAGE_CONTENT", result.Output)
	}
	if !sawText {
		t.Fatal("no text message streamed; live progress would be blank in the UI")
	}
	// A session id is what makes a follow-up turn resumable.
	if result.SessionID == "" {
		t.Fatal("no session id captured; resume would silently start a fresh conversation")
	}
	if len(result.Usage) == 0 {
		t.Fatal("no token usage recorded; STEP_FINISHED.token_usage may have moved")
	}
	t.Logf("live knot-cli run ok: session=%s usage=%+v", result.SessionID, result.Usage)
}
