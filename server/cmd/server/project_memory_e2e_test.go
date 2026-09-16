package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/internal/daemon"
	"github.com/multica-ai/multica/server/internal/daemon/execenv"
	"github.com/multica-ai/multica/server/internal/projectmemory"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/pelletier/go-toml/v2"
)

func TestProjectMemoryProductClaimProviderCleanup(t *testing.T) {
	if os.Getenv("PROJECT_MEMORY_TEST_DATABASE_URL") == "" {
		t.Skip("set dedicated PROJECT_MEMORY_TEST_DATABASE_URL to run the product fixture")
	}
	if os.Getenv("DATABASE_URL") != os.Getenv("PROJECT_MEMORY_TEST_DATABASE_URL") {
		t.Fatal("dedicated product fixture database required")
	}
	root := t.TempDir()
	binary := filepath.Join(root, "memory-bridge.test.exe")
	build := exec.Command("go", "test", "-c", "-o", binary, "./internal/daemon")
	build.Dir = "../.."
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build daemon fixture: %v %s", err, output)
	}
	fake := filepath.Join(root, "memory-fake-provider.exe")
	data, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(fake, data, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(cli.TaskConfigRootEnv, filepath.Join(root, "profile"))
	t.Setenv("CODEX_HOME", filepath.Join(root, "host-codex"))
	t.Setenv("HERMES_HOME", filepath.Join(root, "host-hermes"))
	t.Setenv("MULTICA_CODEX_MEMORY", "0")
	host := filepath.Join(root, "host-hermes")
	if err = os.MkdirAll(filepath.Join(host, "memories"), 0700); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(host, "memories", "MEMORY.md"), []byte("unscoped host"), 0600)
	_ = os.WriteFile(filepath.Join(host, "config.yaml"), []byte("model: fixture\n"), 0600)
	_ = os.WriteFile(filepath.Join(host, ".env"), []byte("HERMES_HOME="+host+"\n"), 0600)
	named := filepath.Join(host, "profiles", "fixture")
	if err = os.MkdirAll(named, 0700); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(named, ".env"), []byte("HERMES_HOME="+host+"\nMEMORY_PROFILE_FIXTURE=selected\n"), 0600)
	_ = os.WriteFile(filepath.Join(named, "config.yaml"), []byte("model: fixture\n"), 0600)
	fx := testutil.New(testPool, testWorkspaceID, testUserID)
	owner := uuid.NewString()
	runtimeID := fx.Runtime(t, "memory e2e", testutil.Cols{"daemon_id": owner, "runtime_mode": "local", "provider": "hermes"})
	agentID := fx.Agent(t, "memory e2e", runtimeID, testutil.Cols{"runtime_mode": "local", "max_concurrent_tasks": 4})
	projectA := fx.Project(t, "memory A")
	projectB := fx.Project(t, "memory B")
	for _, project := range []string{projectA, projectB} {
		for _, table := range []string{"project_memory_request", "project_memory_binding"} {
			fx.Cleanup(t, "DELETE FROM "+table+" WHERE project_id=$1", project)
		}
	}
	fx.Cleanup(t, "DELETE FROM project_memory_task WHERE workspace_id=$1", testWorkspaceID)
	fx.Cleanup(t, "DELETE FROM project_memory_scope WHERE workspace_id=$1", testWorkspaceID)
	issue := fx.Issue(t, "memory e2e issue", testutil.Cols{"project_id": projectA})
	chat := fx.ChatSession(t, agentID, testutil.Cols{"project_id": projectA, "explicitly_created_at": time.Now()})
	skillID := fx.Insert(t, "skill", testutil.Cols{"workspace_id": testWorkspaceID, "name": "memory-fixture-" + uuid.NewString(), "content": "Fixture skill only.", "created_by": testUserID})
	fx.Exec(t, "INSERT INTO agent_skill(agent_id,skill_id,enabled) VALUES($1,$2,false)", agentID, skillID)
	fx.Cleanup(t, "DELETE FROM agent_skill WHERE agent_id=$1 AND skill_id=$2", agentID, skillID)
	client := cli.NewAPIClient(testServer.URL, testWorkspaceID, testToken)
	daemonClient := daemon.NewClient(testServer.URL)
	daemonClient.SetToken(testToken)
	profileRoot, _ := cli.ProfileDir("")
	store := projectmemory.Store{ManagedRoot: filepath.Join(profileRoot, "project-memory"), DaemonID: owner}
	call := func(method, path string, body any, want int) {
		req := testutil.WithHeaders(testutil.JSONRequest(method, path, body), "Authorization", "Bearer "+testToken, "X-Workspace-ID", testWorkspaceID)
		testutil.Call(t, testServer.Config.Handler.ServeHTTP, req).Want(want)
	}
	publish := func(project string, op projectmemory.Operation) {
		var b projectmemory.Binding
		if err := client.GetJSON(context.Background(), "/api/projects/"+project+"/memory", &b); err != nil {
			t.Fatal(err)
		}
		op.ExpectedRevision = b.ContentRevision
		op.ExpectedBindingRevision = b.Revision
		op.Source = "product fixture"
		var work projectmemory.Work
		if err := client.PostJSON(context.Background(), "/api/projects/"+project+"/memory/operations", op, &work); err != nil {
			t.Fatal(err)
		}
		// These are the exact owner routes under NewRouter/DaemonAuth.
		req := testutil.WithHeaders(testutil.JSONRequest("GET", "/api/daemon/runtimes/"+runtimeID+"/memory/next", nil), "Authorization", "Bearer "+testToken)
		var next struct {
			Work *projectmemory.Work `json:"work"`
		}
		testutil.Call(t, testServer.Config.Handler.ServeHTTP, req).Want(200).JSON(&next)
		if next.Work == nil || next.Work.ID != work.ID {
			t.Fatal("owner queue mismatch")
		}
		result := projectmemory.Execute(store, *next.Work)
		call("POST", "/api/daemon/runtimes/"+runtimeID+"/memory/"+work.ID+"/result", result, 200)
	}
	initialized := map[string]bool{}
	nativeSeen := map[string]bool{}
	priorSession, priorWorkdir := "", ""
	run := func(surface, project, provider string, skills bool, optin bool) {
		t.Helper()
		var selected any = project
		if project == "" {
			selected = nil
		}
		if surface == "issue" {
			call("PUT", "/api/issues/"+issue, map[string]any{"project_id": selected, "suppress_run": true}, 200)
		} else {
			var p any = project
			if project == "" {
				p = nil
			}
			call("PATCH", "/api/chat/sessions/"+chat, map[string]any{"project_id": p}, 200)
		}
		fx.Exec(t, "UPDATE agent_runtime SET provider=$2 WHERE id=$1", runtimeID, provider)
		reportPath := filepath.Join(root, uuid.NewString()+".provider.json")
		env := map[string]string{"MEMORY_FIXTURE_REPORT": reportPath, "HERMES_HOME": host}
		raw, _ := json.Marshal(env)
		fx.Exec(t, "UPDATE agent SET custom_env=$2 WHERE id=$1", agentID, raw)
		fx.Exec(t, "UPDATE agent_skill SET enabled=$3 WHERE agent_id=$1 AND skill_id=$2", agentID, skillID, skills)
		cols := testutil.Cols{"runtime_id": runtimeID}
		if surface == "issue" {
			cols["issue_id"] = issue
		} else {
			cols["chat_session_id"] = chat
		}
		taskID := fx.Task(t, agentID, cols)
		task, err := daemonClient.ClaimTask(context.Background(), runtimeID)
		if err != nil || task == nil {
			t.Fatalf("claim: %v %+v", err, task)
		}
		if task.ID != taskID {
			t.Fatalf("wrong claim: %s want %s", task.ID, taskID)
		}
		if task.PriorSessionID != "" || task.PriorWorkDir != "" {
			t.Fatal("claim retained old provider context")
		}
		if project != "" {
			if task.ProjectMemory == nil || task.ProjectMemory.ProjectID != project {
				t.Fatal("wrong claim memory scope")
			}
			if !initialized[project] {
				publish(project, projectmemory.Operation{Action: "init"})
				publish(project, projectmemory.Operation{Action: "write", Path: "README.md", Content: project})
				initialized[project] = true
			}
		} else if task.ProjectMemory != nil {
			t.Fatal("unset project retained memory")
		}
		if optin {
			t.Setenv("MULTICA_CODEX_MEMORY", "1")
		} else {
			t.Setenv("MULTICA_CODEX_MEMORY", "0")
		}
		resultPath := filepath.Join(root, uuid.NewString()+".result.json")
		inputPath := filepath.Join(root, uuid.NewString()+".input.json")
		payload, _ := json.Marshal(map[string]any{"Task": task, "Optin": optin, "NamedProfile": skills, "URL": testServer.URL, "Token": testToken, "Root": root, "Provider": provider, "Binary": fake, "Result": resultPath})
		_ = os.WriteFile(inputPath, payload, 0600)
		cmd := exec.Command(binary, "-test.run=^TestProjectMemoryRunTaskBridge$", "-test.v")
		cmd.Env = append(os.Environ(), "MEMORY_FIXTURE_BRIDGE="+inputPath)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("daemon bridge: %v %s", err, output)
		}
		raw, err = os.ReadFile(resultPath)
		if err != nil {
			t.Fatal(err)
		}
		var response struct {
			Result       daemon.TaskResult `json:"result"`
			Error        string            `json:"error"`
			EnvRemoved   bool              `json:"env_removed"`
			CleanupError string            `json:"cleanup_error"`
		}
		if err = json.Unmarshal(raw, &response); err != nil {
			t.Fatal(err)
		}
		if optin {
			if !strings.Contains(response.Error, "native memory") {
				t.Fatalf("opt-in not rejected: %s", raw)
			}
			if _, err = os.Stat(reportPath); !os.IsNotExist(err) {
				t.Fatal("provider launched despite unscoped opt-in")
			}
		} else {
			if response.Error != "" || response.Result.Status != "completed" || response.CleanupError != "" || !response.EnvRemoved {
				t.Fatalf("run/cleanup: %s", raw)
			}
			record, err := os.ReadFile(reportPath)
			if err != nil {
				t.Fatal(err)
			}
			var observed map[string]any
			if err = json.Unmarshal(record, &observed); err != nil {
				t.Fatal(err)
			}
			if project != "" {
				if observed["snapshot_project"] != project || observed["entry"] != project {
					t.Fatalf("wrong subprocess memory: %s", record)
				}
				if provider == "hermes" {
					if observed["native_lock_busy"] != true {
						t.Fatalf("native lock not held throughout child: %s", record)
					}
					if prior, ok := observed["native_prior"].(string); ok && prior != "" && prior != project {
						t.Fatalf("cross-project native memory %q", prior)
					}
					if nativeSeen[project] && observed["native_prior"] != project {
						t.Fatal("native memory did not persist across daemon processes")
					}
					nativeSeen[project] = true
					if skills && observed["bound_skill_visible"] != true {
						t.Fatalf("claimed skill not mounted: %s", record)
					}
					dotenv, _ := observed["derived_dotenv"].(string)
					home, _ := observed["hermes_home"].(string)
					if !strings.Contains(dotenv, home) || (skills && !strings.Contains(dotenv, "MEMORY_PROFILE_FIXTURE=selected")) {
						t.Fatalf("profile/dotenv isolation failed: %s", record)
					}
					args, _ := observed["args"].([]any)
					for _, arg := range args {
						if strings.Contains(arg.(string), "--profile") {
							t.Fatal("profile selector escaped overlay")
						}
					}
					storePath, _ := observed["native_store"].(string)
					lockCtx, cancel := context.WithTimeout(context.Background(), time.Second)
					unlock, lockErr := execenv.LockProjectNativeMemory(lockCtx, storePath)
					cancel()
					if lockErr != nil {
						t.Fatal("native lock not released after subprocess")
					}
					unlock()
					if observed["hermes_home"] == host {
						t.Fatal("Hermes host passthrough")
					}
				}
			} else if observed["snapshot_readable"] == true {
				t.Fatal("unset task read an old snapshot")
			}
			if provider == "codex" {
				config, _ := observed["codex_config"].(string)
				var cfg struct {
					Features struct{ Memories bool }
					Memories struct {
						GenerateMemories bool `toml:"generate_memories"`
						UseMemories      bool `toml:"use_memories"`
					}
				}
				if err := toml.Unmarshal([]byte(config), &cfg); err != nil || !strings.Contains(config, "multica-managed memory") || cfg.Features.Memories || cfg.Memories.GenerateMemories || cfg.Memories.UseMemories {
					t.Fatalf("Codex native memory not disabled: %s", record)
				}
			}
			if observed["unexpected_resume"] == true {
				t.Fatal("provider received a resume request")
			}
			priorSession, priorWorkdir = response.Result.SessionID, response.Result.WorkDir
			if project != "" {
				var b projectmemory.Binding
				if err = client.GetJSON(context.Background(), "/api/projects/"+project+"/memory", &b); err != nil {
					t.Fatal(err)
				}
				if snap, err := store.Read(b); err != nil || snap.Files["README.md"] != project {
					t.Fatal("cleanup lost canonical memory")
				}
			}
		}
		// Preserve realistic prior pointers; the next production claim must suppress them.
		fx.Exec(t, "UPDATE agent_task_queue SET status='completed',completed_at=now(),session_id=$2,work_dir=$3 WHERE id=$1", taskID, priorSession, priorWorkdir)
		t.Logf("surface=%s project=%s provider=%s skills=%v optin=%v: real claim/helper/provider/cleanup verified", surface, project, provider, skills, optin)
	}
	run("issue", projectA, "hermes", false, false)
	run("issue", projectB, "hermes", true, false)
	run("issue", projectA, "hermes", false, false)
	run("issue", "", "hermes", false, false)
	run("chat", projectA, "codex", false, false)
	run("chat", projectB, "codex", false, false)
	run("chat", projectA, "codex", false, true)
	run("chat", "", "codex", false, false)
}
