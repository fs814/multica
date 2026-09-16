package agent

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCodexWindowsSandboxFailureStopsBeforeTurn(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "fake_codex.go")
	exePath := filepath.Join(dir, "fake_codex.exe")
	marker := filepath.Join(dir, "model-started")
	const source = `package main
import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
)
func main() {
	if len(os.Args) > 1 && os.Args[1] == "--version" { fmt.Println("codex-cli windows-test"); return }
	s := bufio.NewScanner(os.Stdin)
	for s.Scan() {
		var request struct { ID json.RawMessage; Method string }
		if json.Unmarshal(s.Bytes(), &request) != nil { return }
		switch request.Method {
		case "initialize":
			fmt.Printf("{\"id\":%s,\"result\":{}}\n", request.ID)
		case "command/exec":
			fmt.Printf("{\"id\":%s,\"error\":{\"code\":-32603,\"message\":\"CreateProcessWithLogonW failed: 1385\"}}\n", request.ID)
		case "thread/start", "turn/start":
			_ = os.WriteFile(os.Getenv("MODEL_STARTED_FILE"), []byte(request.Method), 0600)
			fmt.Printf("{\"id\":%s,\"result\":{}}\n", request.ID)
		}
	}
}`
	if err := os.WriteFile(sourcePath, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("go", "build", "-o", exePath, sourcePath).CombinedOutput(); err != nil {
		t.Fatalf("build fake app-server: %v: %s", err, output)
	}
	backend, err := New("codex", Config{
		ExecutablePath: exePath,
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		Env:            map[string]string{"MODEL_STARTED_FILE": marker},
	})
	if err != nil {
		t.Fatal(err)
	}
	session, err := backend.Execute(context.Background(), "prompt", ExecOptions{
		Cwd:              dir,
		Timeout:          5 * time.Second,
		HandshakeTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for range session.Messages {
		}
	}()
	result := <-session.Result
	if result.Status != "failed" || !strings.Contains(result.Error, "1385") {
		t.Fatalf("expected sandbox failure, got %+v", result)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("model request must not start after sandbox failure: %v", err)
	}
}
