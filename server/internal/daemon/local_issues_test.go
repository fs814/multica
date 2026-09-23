package daemon

import (
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

	"github.com/multica-ai/multica/server/internal/cli"
)

func localIssueTestDaemon(t *testing.T) *Daemon {
	t.Helper()
	t.Setenv(cli.TaskConfigRootEnv, t.TempDir())
	d := New(Config{Profile: "desktop-local-test", WorkspacesRoot: t.TempDir(), Agents: map[string]AgentEntry{}}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := d.initLocalIssues(); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestLocalIssuesStayInProfileAndRequireBearer(t *testing.T) {
	d := localIssueTestDaemon(t)
	now := time.Now().UTC()
	issue := LocalIssue{ID: strings.Repeat("a", 32), Title: "Local only", Machine: "local", Directory: t.TempDir(), Provider: "codex", Status: "completed", CreatedAt: now, UpdatedAt: now}
	if err := d.writeLocalIssue(issue); err != nil {
		t.Fatal(err)
	}
	dir, err := d.localIssuesDir()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, issue.ID+".json")); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodGet, "/local/issues", nil)
	unauthorized := httptest.NewRecorder()
	d.localIssuesHandler()(unauthorized, request)
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized response: %d", unauthorized.Code)
	}

	request.Header.Set("Authorization", "Bearer "+d.localIssueToken)
	response := httptest.NewRecorder()
	d.localIssuesHandler()(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("list response: %d %s", response.Code, response.Body.String())
	}
	var listed []LocalIssue
	if err := json.Unmarshal(response.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].Machine != "local" || listed[0].ID != issue.ID {
		t.Fatalf("unexpected list: %+v", listed)
	}
}

func TestLocalIssueAPIRejectsRemoteMachine(t *testing.T) {
	d := localIssueTestDaemon(t)
	request := httptest.NewRequest(http.MethodPost, "/local/issues", strings.NewReader(`{"title":"remote","machine":"another-daemon","directory":"/tmp"}`))
	request.Header.Set("Authorization", "Bearer "+d.localIssueToken)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	d.localIssuesHandler()(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("remote response: %d %s", response.Code, response.Body.String())
	}
}

func TestLocalIssueRestartMarksRunningInterrupted(t *testing.T) {
	d := localIssueTestDaemon(t)
	now := time.Now().UTC()
	issue := LocalIssue{ID: strings.Repeat("b", 32), Title: "Running", Machine: "local", Status: "running", CreatedAt: now, UpdatedAt: now}
	if err := d.writeLocalIssue(issue); err != nil {
		t.Fatal(err)
	}
	if err := d.initLocalIssues(); err != nil {
		t.Fatal(err)
	}
	read, err := d.readLocalIssue(issue.ID)
	if err != nil {
		t.Fatal(err)
	}
	if read.Status != "interrupted" {
		t.Fatalf("restart status = %q", read.Status)
	}
}

func TestLocalIssueAgentEnvironmentKeepsNativeConfigAndIsolatesCenter(t *testing.T) {
	d := localIssueTestDaemon(t)
	env, err := d.localIssueAgentEnv(strings.Repeat("c", 32))
	if err != nil {
		t.Fatal(err)
	}
	root := env[cli.TaskConfigRootEnv]
	if root == "" || !filepath.IsAbs(root) {
		t.Fatalf("task-local CLI config root = %q", root)
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"HOME", "CODEX_HOME", "XDG_CONFIG_HOME", "MULTICA_TOKEN", "MULTICA_SERVER_URL"} {
		if _, ok := env[key]; ok {
			t.Fatalf("local issue agent env unexpectedly overrides %s", key)
		}
	}
}

func TestLocalCapabilitiesUseCenterInventoryScannerWithoutLeakingMCPSecrets(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	t.Setenv("CODEX_HOME", "")
	writeTestLocalSkill(t, filepath.Join(home, ".codex", "skills"), "review", map[string]string{
		"SKILL.md": "---\nname: Review\ndescription: Review code\n---\n# Review\n",
	})
	configDir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(configDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte("[mcp_servers.fetch]\ncommand = \"secret-command\"\nargs = [\"secret-token\"]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	d := localIssueTestDaemon(t)
	agents := map[string]AgentEntry{"codex": {Path: "/unused"}}
	d.agentsAvailable.Store(&agents)
	req := httptest.NewRequest(http.MethodGet, "/local/capabilities?provider=codex", nil)
	req.Header.Set("Authorization", "Bearer "+d.localIssueToken)
	response := httptest.NewRecorder()
	d.localCapabilitiesHandler()(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("capabilities response: %d %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "secret-command") || strings.Contains(response.Body.String(), "secret-token") {
		t.Fatalf("MCP secret leaked in capability inventory: %s", response.Body.String())
	}
	var got struct {
		Skills []runtimeLocalSkillSummary     `json:"skills"`
		MCP    []runtimeLocalMcpServerSummary `json:"mcp_servers"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Skills) != 1 || got.Skills[0].Name != "Review" || len(got.MCP) != 1 || got.MCP[0].Name != "fetch" {
		t.Fatalf("wrong local capability inventory: %+v", got)
	}
}
