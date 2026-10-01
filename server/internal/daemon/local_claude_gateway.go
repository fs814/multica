package daemon

import (
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/cli"
)

// This file is machine-local, outside task worktrees and center configuration.
// It contains environment-variable references, never credential values.
const localClaudeGatewayFile = "local-claude-gateways.json"
const maxLocalClaudeGatewayBytes = 64 << 10

type localClaudeGateways struct {
	Version  int                  `json:"version"`
	Gateways []localClaudeGateway `json:"gateways"`
}

type localClaudeGateway struct {
	CenterURL         string `json:"center_url"`
	WorkspaceID       string `json:"workspace_id"`
	AgentID           string `json:"agent_id"`
	BaseURL           string `json:"base_url"`
	AuthTokenEnv      string `json:"auth_token_env"`
	AllowInsecureHTTP bool   `json:"allow_insecure_http,omitempty"`
}

var localGatewayEnvName = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,127}$`)

func localGatewayURL(raw string, originOnly, allowHTTP bool) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Hostname() != "" && u.User == nil && u.RawQuery == "" && u.Fragment == "" &&
		(u.Scheme == "https" || allowHTTP && u.Scheme == "http") &&
		(!originOnly || u.Path == "") && !strings.ContainsAny(raw, "\r\n\t ")
}

func readLocalClaudeGateways(path string) (localClaudeGateways, error) {
	var config localClaudeGateways
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return config, nil
	}
	if err != nil {
		return config, errors.New("cannot read local Claude gateway configuration")
	}
	if !info.Mode().IsRegular() || info.Size() > maxLocalClaudeGatewayBytes || (runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0) {
		return config, errors.New("local Claude gateway configuration must be a private regular file (chmod 600), at most 64 KiB")
	}
	f, err := os.Open(path)
	if err != nil {
		return config, errors.New("cannot open local Claude gateway configuration")
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return config, errors.New("local Claude gateway configuration changed while opening")
	}
	decoder := json.NewDecoder(io.LimitReader(f, maxLocalClaudeGatewayBytes+1))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&config) != nil || decoder.Decode(new(any)) != io.EOF || config.Version != 1 || len(config.Gateways) > 64 {
		// Decoder errors can include input contents: do not put them in task
		// results or logs, even if a user accidentally pasted a secret here.
		return config, errors.New("invalid local Claude gateway configuration")
	}
	seen := map[string]bool{}
	for _, g := range config.Gateways {
		workspace, werr := uuid.Parse(g.WorkspaceID)
		agent, aerr := uuid.Parse(g.AgentID)
		if werr != nil || aerr != nil || workspace == uuid.Nil || agent == uuid.Nil ||
			!localGatewayURL(g.CenterURL, true, true) || !localGatewayURL(g.BaseURL, false, g.AllowInsecureHTTP) ||
			!localGatewayEnvName.MatchString(g.AuthTokenEnv) {
			return config, errors.New("invalid local Claude gateway scope, URL or environment reference")
		}
		key := g.CenterURL + "/" + g.WorkspaceID + "/" + g.AgentID
		if seen[key] {
			return config, errors.New("duplicate local Claude gateway scope")
		}
		seen[key] = true
	}
	return config, nil
}

// Apply only to the child environment after center-supplied custom_env. Never
// mutate task.Agent, persist resolved secrets, or include values in errors.
func applyLocalClaudeGateway(profile, centerURL, provider string, task Task, env map[string]string) error {
	if provider != "claude" {
		return nil
	}
	dir, err := cli.ProfileDir(profile)
	if err != nil {
		return errors.New("cannot locate local Claude gateway configuration")
	}
	config, err := readLocalClaudeGateways(filepath.Join(dir, localClaudeGatewayFile))
	if err != nil {
		return err
	}
	for _, g := range config.Gateways {
		if g.CenterURL != strings.TrimRight(centerURL, "/") || g.WorkspaceID != task.WorkspaceID || g.AgentID != task.AgentID {
			continue
		}
		// Read the daemon's environment, never the server-provided env map.
		token := os.Getenv(g.AuthTokenEnv)
		if strings.TrimSpace(token) == "" || strings.ContainsAny(token, "\r\n\x00") {
			return errors.New("local Claude gateway credential is unavailable; export its configured environment variable and restart this daemon profile")
		}
		env["ANTHROPIC_BASE_URL"] = g.BaseURL
		env["ANTHROPIC_AUTH_TOKEN"] = token
		// Empty/false child overrides suppress competing inherited credentials
		// and cloud-provider modes without changing the daemon's environment.
		for _, key := range []string{"ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_CUSTOM_HEADERS"} {
			env[key] = ""
		}
		for _, key := range []string{"CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY"} {
			env[key] = "0"
		}
		return nil
	}
	return nil
}
