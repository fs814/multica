package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/pkg/agent"
)

// LocalIssue deliberately has no Center identity. Only an explicit remote
// issue is sent to Center; local issues and their transcript stay in the
// Desktop-owned daemon profile, even after the daemon connects to Center.
type LocalIssue struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	Description string    `json:"description,omitempty"`
	Machine     string    `json:"machine"`
	Directory   string    `json:"directory"`
	Provider    string    `json:"provider"`
	Status      string    `json:"status"`
	Output      string    `json:"output,omitempty"`
	Error       string    `json:"error,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type createLocalIssueRequest struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Machine     string `json:"machine"`
	Directory   string `json:"directory"`
	Provider    string `json:"provider"`
}

func (d *Daemon) localIssuesDir() (string, error) {
	profileDir, err := cli.ProfileDir(d.cfg.Profile)
	if err != nil {
		return "", err
	}
	return filepath.Join(profileDir, "local-issues"), nil
}

func (d *Daemon) initLocalIssues() error {
	dir, err := d.localIssuesDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return err
	}
	d.localIssueToken = hex.EncodeToString(secret)
	// The bearer token is intentionally not exposed by /health or the renderer.
	// Electron main reads it from the selected profile for each request.
	tokenFile, err := os.CreateTemp(dir, ".api-token-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tokenFile.Name())
	if _, err := tokenFile.WriteString(d.localIssueToken); err != nil {
		tokenFile.Close()
		return err
	}
	if err := tokenFile.Close(); err != nil {
		return err
	}
	if err := os.Rename(tokenFile.Name(), filepath.Join(dir, "api-token")); err != nil {
		return err
	}
	// A daemon restart cannot resume an in-flight provider session. Make that
	// visible instead of leaving an issue in "running" forever.
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		issue, err := d.readLocalIssue(id)
		if err == nil && (issue.Status == "running" || issue.Status == "queued") {
			issue.Status = "interrupted"
			issue.Error = "Local daemon stopped before the issue finished"
			issue.UpdatedAt = time.Now().UTC()
			if err := d.writeLocalIssue(issue); err != nil {
				return err
			}
		}
	}
	return nil
}

func (d *Daemon) localIssuePath(id string) (string, error) {
	if len(id) != 32 {
		return "", errors.New("invalid local issue ID")
	}
	if _, err := hex.DecodeString(id); err != nil {
		return "", errors.New("invalid local issue ID")
	}
	dir, err := d.localIssuesDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, id+".json"), nil
}

func (d *Daemon) readLocalIssue(id string) (LocalIssue, error) {
	path, err := d.localIssuePath(id)
	if err != nil {
		return LocalIssue{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return LocalIssue{}, err
	}
	var issue LocalIssue
	if err := json.Unmarshal(data, &issue); err != nil {
		return LocalIssue{}, err
	}
	return issue, nil
}

func (d *Daemon) writeLocalIssue(issue LocalIssue) error {
	path, err := d.localIssuePath(issue.ID)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(issue, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".issue-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func (d *Daemon) authorizeLocalIssues(w http.ResponseWriter, r *http.Request) bool {
	if d.localIssueToken == "" || r.Header.Get("Authorization") != "Bearer "+d.localIssueToken {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return false
	}
	return true
}

func (d *Daemon) localIssuesHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !d.authorizeLocalIssues(w, r) {
			return
		}
		switch r.Method {
		case http.MethodGet:
			dir, err := d.localIssuesDir()
			if err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			issues := make([]LocalIssue, 0)
			for _, entry := range entries {
				if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
					continue
				}
				issue, err := d.readLocalIssue(strings.TrimSuffix(entry.Name(), ".json"))
				if err == nil {
					issues = append(issues, issue)
				}
			}
			sort.Slice(issues, func(i, j int) bool { return issues[i].CreatedAt.After(issues[j].CreatedAt) })
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(issues)
		case http.MethodPost:
			if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
				http.Error(w, "JSON required", http.StatusUnsupportedMediaType)
				return
			}
			var req createLocalIssueRequest
			decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&req); err != nil {
				http.Error(w, "invalid issue: "+err.Error(), 400)
				return
			}
			req.Title = strings.TrimSpace(req.Title)
			if req.Title == "" || len(req.Title) > 300 || len(req.Description) > 60000 {
				http.Error(w, "title or description length invalid", 400)
				return
			}
			if req.Machine != "" && req.Machine != "local" {
				http.Error(w, "remote issues must be created through Center", 400)
				return
			}
			directory, err := filepath.Abs(req.Directory)
			if req.Directory == "" || err != nil {
				http.Error(w, "a local directory is required", 400)
				return
			}
			info, err := os.Stat(directory)
			if err != nil || !info.IsDir() {
				http.Error(w, "local directory does not exist", 400)
				return
			}
			provider := req.Provider
			agents := d.agents()
			if provider == "" {
				if _, ok := agents["codex"]; ok {
					provider = "codex"
				} else {
					names := make([]string, 0, len(agents))
					for name := range agents {
						names = append(names, name)
					}
					sort.Strings(names)
					if len(names) > 0 {
						provider = names[0]
					}
				}
			}
			if _, ok := agents[provider]; !ok {
				http.Error(w, "selected local agent is not installed", 400)
				return
			}
			idBytes := make([]byte, 16)
			if _, err := rand.Read(idBytes); err != nil {
				http.Error(w, "cannot create issue ID", 500)
				return
			}
			now := time.Now().UTC()
			issue := LocalIssue{ID: hex.EncodeToString(idBytes), Title: req.Title, Description: req.Description,
				Machine: "local", Directory: directory, Provider: provider, Status: "queued", CreatedAt: now, UpdatedAt: now}
			d.localIssuesMu.Lock()
			err = d.writeLocalIssue(issue)
			d.localIssuesMu.Unlock()
			if err != nil {
				http.Error(w, "cannot save issue: "+err.Error(), 500)
				return
			}
			go d.runLocalIssue(issue)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			json.NewEncoder(w).Encode(issue)
		default:
			w.Header().Set("Allow", "GET, POST")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

func (d *Daemon) localIssueHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !d.authorizeLocalIssues(w, r) {
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/local/issues/")
		issue, err := d.readLocalIssue(id)
		if err != nil {
			http.Error(w, "issue not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(issue)
	}
}

// localCapabilitiesHandler uses the same discovery code as Center's
// runtime-local-skill request. It returns inventory only: MCP command lines,
// URLs, headers, and environment variables never leave this machine.
func (d *Daemon) localCapabilitiesHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !d.authorizeLocalIssues(w, r) {
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		provider := r.URL.Query().Get("provider")
		if provider == "" {
			agents := d.agents()
			if _, ok := agents["codex"]; ok {
				provider = "codex"
			} else {
				names := make([]string, 0, len(agents))
				for name := range agents {
					names = append(names, name)
				}
				sort.Strings(names)
				if len(names) > 0 {
					provider = names[0]
				}
			}
		}
		if _, ok := d.agents()[provider]; !ok {
			http.Error(w, "selected local agent is not installed", http.StatusBadRequest)
			return
		}
		skills, skillsSupported, err := listRuntimeLocalSkills(provider)
		if err != nil {
			http.Error(w, "local skill discovery failed: "+err.Error(), 500)
			return
		}
		mcpServers, mcpSupported, err := listRuntimeLocalMcpServers(provider)
		if err != nil {
			http.Error(w, "local MCP discovery failed: "+err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"provider": provider, "skills": skills, "skills_supported": skillsSupported,
			"mcp_servers": mcpServers, "mcp_supported": mcpSupported,
		})
	}
}

// localIssueAgentEnv isolates Multica CLI identity without relocating HOME,
// CODEX_HOME, or XDG paths. That preserves the provider's native skills and
// MCP config while preventing a local issue from inheriting the daemon owner's
// Center credential through ~/.multica/config.json.
func (d *Daemon) localIssueAgentEnv(id string) (map[string]string, error) {
	dir, err := d.localIssuesDir()
	if err != nil {
		return nil, err
	}
	root := filepath.Join(dir, id, "cli-config")
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	return map[string]string{cli.TaskConfigRootEnv: root}, nil
}

func (d *Daemon) updateLocalIssue(issue *LocalIssue) {
	issue.UpdatedAt = time.Now().UTC()
	d.localIssuesMu.Lock()
	defer d.localIssuesMu.Unlock()
	if err := d.writeLocalIssue(*issue); err != nil {
		d.logger.Error("save local issue", "id", issue.ID, "error", err)
	}
}

func (d *Daemon) runLocalIssue(issue LocalIssue) {
	d.activeTasks.Add(1)
	defer d.activeTasks.Add(-1)
	issue.Status = "running"
	d.updateLocalIssue(&issue)
	ctx := d.rootCtx
	if ctx == nil {
		ctx = context.Background()
	}
	entry := d.agents()[issue.Provider]
	agentEnv, err := d.localIssueAgentEnv(issue.ID)
	if err != nil {
		issue.Status = "failed"
		issue.Error = fmt.Sprintf("prepare local agent environment: %v", err)
		d.updateLocalIssue(&issue)
		return
	}
	resolved, version, err := d.resolveAgentEntryForLaunch(ctx, issue.Provider, entry)
	if err == nil {
		var backend agent.Backend
		backend, err = agent.ResolveBackend(issue.Provider, agent.Config{
			ExecutablePath: resolved.Path, CLIVersion: version, Env: agentEnv, Logger: d.logger,
			TaskID: issue.ID, RuntimeID: "local-" + issue.Provider, DaemonVersion: d.cfg.CLIVersion,
			BuiltinRuntime: true,
		})
		if err == nil {
			prompt := issue.Title
			if issue.Description != "" {
				prompt += "\n\n" + issue.Description
			}
			var session *agent.Session
			session, err = backend.Execute(ctx, prompt, agent.ExecOptions{Cwd: issue.Directory, Model: entry.Model, Timeout: d.cfg.AgentTimeout})
			if err == nil {
				var transcript strings.Builder
				d.runningTasks.Add(1)
				for message := range session.Messages {
					if (message.Type == agent.MessageText || message.Type == agent.MessageError) && transcript.Len() < 200000 {
						transcript.WriteString(message.Content)
						transcript.WriteByte('\n')
					}
				}
				result, ok := <-session.Result
				d.runningTasks.Add(-1)
				if !ok {
					err = errors.New("agent ended without a result")
				} else {
					issue.Status = result.Status
					issue.Output = result.Output
					if issue.Output == "" {
						issue.Output = transcript.String()
					}
					issue.Error = result.Error
				}
			}
		}
	}
	if err != nil {
		issue.Status = "failed"
		issue.Error = fmt.Sprintf("local agent failed: %v", err)
	}
	if issue.Status == "" {
		issue.Status = "failed"
		issue.Error = "agent returned no status"
	}
	d.updateLocalIssue(&issue)
}
