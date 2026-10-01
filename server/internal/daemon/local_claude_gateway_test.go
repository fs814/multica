package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/cli"
)

func TestLocalClaudeGatewayRunTaskDoesNotUploadCredential(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX fake executable")
	}
	config, path, task := gatewayFixture(t)
	t.Setenv("ANTHROPIC_API_KEY", "inherited-fixture-key")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "inherited-fixture-token")
	var uploadedSecret atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "local-fixture-secret") || strings.Contains(r.Header.Get("Authorization"), "local-fixture-secret") {
			uploadedSecret.Store(true)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	config.Gateways[0].CenterURL = srv.URL
	writeGatewayFixture(t, path, config)
	fake := filepath.Join(t.TempDir(), "claude-fixture")
	writeTestExecutable(t, fake, []byte(`#!/bin/sh
case "$*" in *--version*) echo '2.1.0'; exit 0;; esac
test "$ANTHROPIC_AUTH_TOKEN" = 'local-fixture-secret' || exit 42
test "$ANTHROPIC_BASE_URL" = 'https://gateway.example.test/anthropic' || exit 43
test -z "$ANTHROPIC_API_KEY" || exit 44
printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"result":"configured","session_id":"fixture-session"}'
`))
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	d := &Daemon{
		client: NewClient(srv.URL), logger: logger,
		workspaces: make(map[string]*workspaceState), activeEnvRoots: make(map[string]int),
		runtimeIndex: map[string]Runtime{"test-rt": {ID: "test-rt", Provider: "claude"}},
		cfg:          Config{Profile: "gateway-test", ServerBaseURL: srv.URL, WorkspacesRoot: t.TempDir(), AgentTimeout: 5 * time.Second, Agents: map[string]AgentEntry{"claude": {Path: fake}}},
	}
	task.ID, task.RuntimeID, task.IssueID, task.AuthToken = uuid.NewString(), "test-rt", uuid.NewString(), "mat_fixture"
	task.Agent = &AgentData{ID: task.AgentID, Name: "fixture", Model: "deepseek-v4.1-flash", CustomEnv: map[string]string{"ANTHROPIC_BASE_URL": "https://center-override.example.test", "ANTHROPIC_API_KEY": "remote-key"}}
	result, err := d.runTask(context.Background(), task, "claude", 0, logger)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "completed" || result.Comment != "configured" {
		t.Fatalf("fake agent failed: %s", result.Status)
	}
	encoded, _ := json.Marshal(task)
	if uploadedSecret.Load() || strings.Contains(logs.String(), "local-fixture-secret") || strings.Contains(string(encoded), "local-fixture-secret") {
		t.Fatal("local credential escaped child environment")
	}
}

func gatewayFixture(t *testing.T) (localClaudeGateways, string, Task) {
	t.Helper()
	setTestHome(t, t.TempDir())
	t.Setenv("MULTICA_TEST_GATEWAY_TOKEN", "local-fixture-secret")
	dir, err := cli.ProfileDir("gateway-test")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	task := Task{WorkspaceID: uuid.NewString(), AgentID: uuid.NewString()}
	config := localClaudeGateways{Version: 1, Gateways: []localClaudeGateway{{CenterURL: "https://center.example.test", WorkspaceID: task.WorkspaceID, AgentID: task.AgentID, BaseURL: "https://gateway.example.test/anthropic", AuthTokenEnv: "MULTICA_TEST_GATEWAY_TOKEN"}}}
	path := filepath.Join(dir, localClaudeGatewayFile)
	writeGatewayFixture(t, path, config)
	return config, path, task
}

func writeGatewayFixture(t *testing.T, path string, config localClaudeGateways) {
	t.Helper()
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestLocalClaudeGatewayExactScope(t *testing.T) {
	_, path, task := gatewayFixture(t)
	for _, tc := range []struct {
		name, profile, center, provider, workspace, agent string
		match                                             bool
	}{
		{"match", "gateway-test", "https://center.example.test", "claude", task.WorkspaceID, task.AgentID, true},
		{"other profile", "other", "https://center.example.test", "claude", task.WorkspaceID, task.AgentID, false},
		{"other center", "gateway-test", "https://other.example.test", "claude", task.WorkspaceID, task.AgentID, false},
		{"other scheme", "gateway-test", "http://center.example.test", "claude", task.WorkspaceID, task.AgentID, false},
		{"other workspace", "gateway-test", "https://center.example.test", "claude", uuid.NewString(), task.AgentID, false},
		{"other agent", "gateway-test", "https://center.example.test", "claude", task.WorkspaceID, uuid.NewString(), false},
		{"other provider", "gateway-test", "https://center.example.test", "codex", task.WorkspaceID, task.AgentID, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := map[string]string{"ANTHROPIC_BASE_URL": "https://remote-override.example.test", "MULTICA_TEST_GATEWAY_TOKEN": "remote-secret"}
			if err := applyLocalClaudeGateway(tc.profile, tc.center, tc.provider, Task{WorkspaceID: tc.workspace, AgentID: tc.agent}, env); err != nil {
				t.Fatal(err)
			}
			if tc.match {
				if env["ANTHROPIC_AUTH_TOKEN"] != "local-fixture-secret" || env["ANTHROPIC_BASE_URL"] != "https://gateway.example.test/anthropic" || env["CLAUDE_CODE_USE_BEDROCK"] != "0" {
					t.Fatal("local gateway not applied")
				}
			} else if len(env) != 2 {
				t.Fatal("credential escaped configured scope")
			}
		})
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "local-fixture-secret") {
		t.Fatal("resolved secret persisted")
	}
}

func TestLocalClaudeGatewayMissingSecretFailsClosed(t *testing.T) {
	_, _, task := gatewayFixture(t)
	t.Setenv("MULTICA_TEST_GATEWAY_TOKEN", "")
	env := map[string]string{"MULTICA_TEST_GATEWAY_TOKEN": "remote-secret"}
	err := applyLocalClaudeGateway("gateway-test", "https://center.example.test", "claude", task, env)
	if err == nil || strings.Contains(err.Error(), "remote-secret") || len(env) != 1 {
		t.Fatal("missing local secret did not fail closed")
	}
}

func TestLocalClaudeGatewayValidation(t *testing.T) {
	for _, name := range []string{"duplicate", "unknown field", "trailing JSON", "secret URL", "query URL", "public file", "symlink", "oversize", "http", "allowed http"} {
		t.Run(name, func(t *testing.T) {
			config, path, _ := gatewayFixture(t)
			switch name {
			case "duplicate":
				config.Gateways = append(config.Gateways, config.Gateways[0])
				writeGatewayFixture(t, path, config)
			case "secret URL":
				config.Gateways[0].BaseURL = "https://secret:password@gateway.example.test"
				writeGatewayFixture(t, path, config)
			case "query URL":
				config.Gateways[0].BaseURL += "?token=fixture-secret"
				writeGatewayFixture(t, path, config)
			case "http", "allowed http":
				config.Gateways[0].BaseURL = "http://21.214.198.175:4000"
				config.Gateways[0].AllowInsecureHTTP = name == "allowed http"
				writeGatewayFixture(t, path, config)
			case "public file":
				if runtime.GOOS == "windows" {
					t.Skip("Unix file permissions")
				}
				if err := os.Chmod(path, 0644); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				other := path + ".link"
				if err := os.Symlink(path, other); err != nil {
					t.Skip(err)
				}
				path = other
			default:
				data, _ := os.ReadFile(path)
				if name == "unknown field" {
					data = []byte(`{"version":1,"secret":"fixture-secret"}`)
				}
				if name == "trailing JSON" {
					data = append(data, []byte(` {"secret":"fixture-secret"}`)...)
				}
				if name == "oversize" {
					data = []byte(strings.Repeat(" ", maxLocalClaudeGatewayBytes+1))
				}
				if err := os.WriteFile(path, data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			_, err := readLocalClaudeGateways(path)
			if name == "allowed http" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil {
				t.Fatal("unsafe configuration accepted")
			}
			if strings.Contains(err.Error(), "fixture-secret") || strings.Contains(err.Error(), "password") {
				t.Fatal("error exposed file contents")
			}
		})
	}
}
