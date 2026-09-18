package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestCodexSandboxPreflight(t *testing.T) {
	for _, tc := range []struct {
		name, platform, response string
		rpcErr                   error
		want                     string
	}{
		{"windows success", "windows", `{"exitCode":0}`, nil, ""},
		{"logon denied", "windows", "", errors.New("CreateProcessWithLogonW failed: 1385 secret-value"), "logon error 1385"},
		{"RPC failure", "windows", "", errors.New("secret-value"), "could not run"},
		{"nonzero", "windows", `{"exitCode":1,"stderr":"secret-value"}`, nil, "exited with code 1"},
		{"missing code", "windows", `{}`, nil, "no valid exit code"},
		{"null response", "windows", `null`, nil, "no valid exit code"},
		{"invalid JSON", "windows", "{", nil, "no valid exit code"},
		{"Linux skips", "linux", "", nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			err := codexSandboxPreflight(context.Background(), tc.platform, "task-dir", func(ctx context.Context, method string, params any) (json.RawMessage, error) {
				called = true
				if method != "command/exec" {
					t.Fatalf("method = %s", method)
				}
				p := params.(map[string]any)
				if p["cwd"] != "task-dir" || p["timeoutMs"] != 10000 {
					t.Fatalf("params = %v", p)
				}
				if _, ok := p["sandboxPolicy"]; ok {
					t.Fatal("probe must inherit policy")
				}
				if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 15*time.Second {
					t.Fatal("unbounded probe")
				}
				return json.RawMessage(tc.response), tc.rpcErr
			})
			if called != (tc.platform == "windows") {
				t.Fatal("incorrect platform gate")
			}
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %s", err, tc.want)
			}
			if strings.Contains(err.Error(), "secret-value") {
				t.Fatal("leaked RPC output")
			}
		})
	}
}

func TestCodexSandboxPreflightCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := codexSandboxPreflight(ctx, "windows", "task-dir", func(ctx context.Context, _ string, _ any) (json.RawMessage, error) { return nil, ctx.Err() })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
}
