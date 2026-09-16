package execenv

import (
	"context"
	"encoding/json"
	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/projectmemory"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func projectMemoryTask() TaskContextForEnv {
	c := &projectmemory.Context{WorkspaceID: uuid.NewString(), ProjectID: uuid.NewString(), ScopeKind: "issue", ScopeID: uuid.NewString(), Epoch: 1, BindingRevision: 1}
	return TaskContextForEnv{IssueID: c.ScopeID, AgentID: uuid.NewString(), ProjectID: c.ProjectID, ProjectMemory: c, ProjectMemorySnapshot: &projectmemory.Snapshot{SchemaVersion: 1, WorkspaceID: c.WorkspaceID, ProjectID: c.ProjectID, BindingRevision: 1, ContentRevision: 1, Files: map[string]string{"README.md": "only this project"}}}
}
func TestProjectMemoryPrivateSnapshotAndNoSkillHermes(t *testing.T) {
	source := t.TempDir()
	mustWrite(t, filepath.Join(source, "config.yaml"), "model: hermes-4\n")
	mustWrite(t, filepath.Join(source, "memories", "MEMORY.md"), "host secret")
	task := projectMemoryTask()
	persistent := t.TempDir()
	env, err := Prepare(PrepareParams{WorkspacesRoot: t.TempDir(), WorkspaceID: task.ProjectMemory.WorkspaceID, TaskID: uuid.NewString(), Provider: "hermes", HermesSourceHome: source, HermesMemoryStore: persistent, Task: task}, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	if env.HermesHome == "" {
		t.Fatal("project with no skills fell back to host")
	}
	if raw, err := os.ReadFile(filepath.Join(env.HermesHome, "memories", "MEMORY.md")); err == nil && strings.Contains(string(raw), "host secret") {
		t.Fatal("host memory imported")
	}
	raw, err := os.ReadFile(filepath.Join(env.RootDir, "project-memory", "snapshot.json"))
	if err != nil {
		t.Fatal(err)
	}
	var snapshot projectmemory.Snapshot
	if err = json.Unmarshal(raw, &snapshot); err != nil || snapshot.ProjectID != task.ProjectID {
		t.Fatal("wrong private snapshot")
	}
	if _, err = os.Stat(filepath.Join(env.WorkDir, "project-memory", "context.json")); err == nil {
		t.Fatal("identity written into shared cwd")
	}
	mustWrite(t, filepath.Join(env.HermesHome, "memories", "MEMORY.md"), "project knowledge")
	env.Cleanup(true)
	raw, err = os.ReadFile(filepath.Join(persistent, "MEMORY.md"))
	if err != nil || string(raw) != "project knowledge" {
		t.Fatal("cleanup removed durable memory")
	}
	if got := Reuse(ReuseParams{WorkDir: source, Provider: "hermes", Task: task}, testLogger()); got != nil {
		t.Fatal("unverified session reuse allowed")
	}
}
func TestProjectMemoryProviderAndSessionScope(t *testing.T) {
	a := projectMemoryTask()
	b := a
	c := *a.ProjectMemory
	b.ProjectMemory = &c
	b.ProjectMemory.ProjectID = uuid.NewString()
	if codexSessionStoreKey("", a) == codexSessionStoreKey("", b) {
		t.Fatal("Codex session shared between projects")
	}
	source := t.TempDir()
	if ProjectMemoryStorePath("", a.AgentID, source, a) == ProjectMemoryStorePath("", a.AgentID, source, b) {
		t.Fatal("Hermes memory shared between projects")
	}
	previous := codexSessionStoreKey("", a)
	a.ProjectMemory.Epoch += 2
	if previous == codexSessionStoreKey("", a) {
		t.Fatal("A-B-A epoch did not change session key")
	}
	t.Setenv(MulticaCodexMemoryEnv, "1")
	if err := checkProjectMemoryProvider("codex", a); err == nil {
		t.Fatal("unscoped Codex native memory allowed")
	}
	t.Setenv(MulticaCodexMemoryEnv, "0")
	if err := checkProjectMemoryProvider("codex", a); err != nil {
		t.Fatal(err)
	}
}

func TestProjectNativeMemoryLock(t *testing.T) {
	dir := t.TempDir()
	release, err := LockProjectNativeMemory(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if second, err := LockProjectNativeMemory(ctx, dir); err == nil {
		second()
		release()
		t.Fatal("concurrent native writer acquired lock")
	}
	release()
	third, err := LockProjectNativeMemory(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	third()
}
