package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/internal/daemon/execenv"
	"github.com/multica-ai/multica/server/internal/projectmemory"
)

// Only the copied fixture executable and private helper argument enter these modes.
func init() {
	if len(os.Args) > 1 && os.Args[1] == "__memory_fixture_prepare" {
		if err := execenv.RunPreparationHelper(os.Stdin, os.Stdout, slog.New(slog.NewTextHandler(os.Stderr, nil))); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	if strings.HasPrefix(filepath.Base(os.Args[0]), "memory-fake-provider") {
		if len(os.Args) > 1 && os.Args[1] == "--version" {
			fmt.Println("0.128.0")
			os.Exit(0)
		}
		memoryFakeProvider()
		os.Exit(0)
	}
}

func memoryFakeProvider() {
	report := map[string]any{"args": os.Args[1:], "project": os.Getenv("MULTICA_PROJECT_ID"), "hermes_home": os.Getenv("HERMES_HOME"), "codex_home": os.Getenv("CODEX_HOME"), "snapshot_path": os.Getenv("MULTICA_PROJECT_MEMORY_SNAPSHOT")}
	methods := []string{}
	encoder := json.NewEncoder(os.Stdout)
	save := func() {
		report["methods"] = methods
		raw, _ := json.Marshal(report)
		if path := os.Getenv("MEMORY_FIXTURE_REPORT"); path != "" {
			_ = os.WriteFile(path, raw, 0600)
		}
	}
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 4096), 8<<20)
	for scanner.Scan() {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if json.Unmarshal(scanner.Bytes(), &req) != nil {
			continue
		}
		methods = append(methods, req.Method)
		if len(req.ID) == 0 {
			continue
		}
		result := any(map[string]any{})
		switch req.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": 1, "agentCapabilities": map[string]any{"loadSession": true}, "authMethods": []any{}, "agentInfo": map[string]string{"name": "memory-fixture", "version": "1"}}
		case "session/new":
			result = map[string]string{"sessionId": "memory-fixture-session"}
		case "thread/start":
			result = map[string]any{"thread": map[string]string{"id": "memory-fixture-thread"}}
		case "command/exec":
			result = map[string]any{"exitCode": 0, "stdout": "", "stderr": ""}
		case "session/load", "thread/resume":
			report["unexpected_resume"] = true
		case "session/prompt", "turn/start":
			var snapshot projectmemory.Snapshot
			raw, err := os.ReadFile(os.Getenv("MULTICA_PROJECT_MEMORY_SNAPSHOT"))
			report["snapshot_readable"] = err == nil
			if err == nil {
				_ = json.Unmarshal(raw, &snapshot)
				report["snapshot_project"] = snapshot.ProjectID
				report["entry"] = snapshot.Files["README.md"]
			}
			if home := os.Getenv("HERMES_HOME"); home != "" && req.Method == "session/prompt" {
				memories := filepath.Join(home, "memories")
				prior, _ := os.ReadFile(filepath.Join(memories, "MEMORY.md"))
				report["native_prior"] = string(prior)
				if dir, err := filepath.EvalSymlinks(memories); err == nil {
					report["native_store"] = dir
					ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
					unlock, lockErr := execenv.LockProjectNativeMemory(ctx, dir)
					cancel()
					report["native_lock_busy"] = lockErr != nil
					if unlock != nil {
						unlock()
					}
				}
				_ = os.WriteFile(filepath.Join(memories, "MEMORY.md"), []byte(snapshot.ProjectID), 0600)
				dotenv, _ := os.ReadFile(filepath.Join(home, ".env"))
				report["derived_dotenv"] = string(dotenv)
				_ = filepath.WalkDir(filepath.Join(home, "skills"), func(path string, entry os.DirEntry, err error) error {
					if err == nil && !entry.IsDir() {
						raw, _ := os.ReadFile(path)
						if strings.Contains(string(raw), "Fixture skill only.") {
							report["bound_skill_visible"] = true
						}
					}
					return nil
				})
			}
			if home := os.Getenv("CODEX_HOME"); home != "" {
				cfg, _ := os.ReadFile(filepath.Join(home, "config.toml"))
				report["codex_config"] = string(cfg)
			}
			save()
			if req.Method == "session/prompt" {
				_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": "memory-fixture-session", "update": map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]string{"type": "text", "text": "fixture complete"}}}})
				result = map[string]string{"stopReason": "end_turn"}
			} else {
				_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{}})
				_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "method": "turn/started", "params": map[string]any{"threadId": "memory-fixture-thread", "turn": map[string]string{"id": "turn-fixture"}}})
				_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "method": "item/completed", "params": map[string]any{"threadId": "memory-fixture-thread", "item": map[string]string{"type": "agentMessage", "id": "message-fixture", "text": "fixture complete"}}})
				_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "method": "turn/completed", "params": map[string]any{"threadId": "memory-fixture-thread", "turn": map[string]string{"id": "turn-fixture", "status": "completed"}}})
				continue
			}
		}
		_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}
	save()
}

// The product-router test launches this bridge as a separate daemon process.
// No alternate runtime implementation is used: it executes runTask and its helper.
func TestProjectMemoryRunTaskBridge(t *testing.T) {
	path := os.Getenv("MEMORY_FIXTURE_BRIDGE")
	if path == "" {
		return
	}
	var spec struct {
		Task                                       Task
		Optin                                      bool
		NamedProfile                               bool
		URL, Token, Root, Provider, Binary, Result string
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}
	t.Setenv(cli.TaskConfigRootEnv, filepath.Join(spec.Root, "profile"))
	t.Setenv("CODEX_HOME", filepath.Join(spec.Root, "host-codex"))
	t.Setenv("HERMES_HOME", filepath.Join(spec.Root, "host-hermes"))
	t.Setenv("MULTICA_CODEX_MEMORY", "0")
	if spec.Optin {
		t.Setenv("MULTICA_CODEX_MEMORY", "1")
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	d := New(Config{WorkspacesRoot: filepath.Join(spec.Root, "tasks"), ServerBaseURL: spec.URL, DaemonID: spec.Task.ProjectMemoryBindingOwner(), AgentTimeout: 20 * time.Second, Agents: map[string]AgentEntry{spec.Provider: {Path: spec.Binary}}}, logger)
	d.client.SetToken(spec.Token)
	d.executionEnvironmentCommand = func() ([]string, error) { return []string{os.Args[0], "__memory_fixture_prepare"}, nil }
	d.runtimeIndex[spec.Task.RuntimeID] = Runtime{ID: spec.Task.RuntimeID, Provider: spec.Provider, ProfileID: "fixture-profile"}
	launch := profileLaunchSpec{path: spec.Binary, version: "0.128.0"}
	if spec.NamedProfile {
		launch.fixedArgs = []string{"--profile", "fixture"}
	}
	d.profileLaunchSpecs["fixture-profile"] = launch
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	result, runErr := d.runTask(ctx, spec.Task, spec.Provider, 0, logger)
	response := map[string]any{"result": result, "env_root": result.EnvRoot}
	if runErr != nil {
		response["error"] = runErr.Error()
	} else {
		environment := execenv.Environment{RootDir: result.EnvRoot, WorkDir: result.WorkDir, LocalDirectory: len(spec.Task.ProjectResources) > 0}
		if cleanupErr := environment.Cleanup(true); cleanupErr != nil {
			response["cleanup_error"] = cleanupErr.Error()
		}
		_, statErr := os.Stat(result.EnvRoot)
		response["env_removed"] = os.IsNotExist(statErr)
	}
	raw, _ = json.Marshal(response)
	if err = os.WriteFile(spec.Result, raw, 0600); err != nil {
		t.Fatal(err)
	}
}
func (t Task) ProjectMemoryBindingOwner() string {
	if t.ProjectMemoryBinding != nil {
		return t.ProjectMemoryBinding.OwnerDaemonID
	}
	return "00000000-0000-4000-8000-000000000001"
}
