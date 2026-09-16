package daemon

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/daemon/execenv"
	"github.com/multica-ai/multica/server/internal/projectmemory"
)

func TestProjectSessionGCReservationRaces(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CODEX_HOME", filepath.Join(root, "codex"))
	t.Setenv("MULTICA_TASK_CONFIG_ROOT", filepath.Join(root, "multica"))
	for _, provider := range []string{"codex", "hermes"} {
		for _, markFirst := range []bool{true, false} {
			name := provider + "/reserve_first"
			if markFirst {
				name = provider + "/mark_first"
			}
			t.Run(name, func(t *testing.T) {
				d := newCodexStoreGuardDaemon()
				task := execenv.TaskContextForEnv{AgentID: "fixture-agent", IssueID: "fixture-issue", ProjectMemory: &projectmemory.Context{WorkspaceID: "fixture-workspace", ProjectID: "fixture-project"}}
				store := execenv.CodexSessionStorePath("", task)
				if provider == "hermes" {
					store = execenv.HermesSessionStorePath("", task.AgentID, filepath.Join(root, "hermes"), task)
				}
				if err := os.MkdirAll(store, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(store, "transcript"), []byte("fixture"), 0600); err != nil {
					t.Fatal(err)
				}
				marked := make(chan struct{})
				visited := false
				reserve := func(path string) (func(), bool) {
					if path != store {
						return d.reserveStoreForDeletion(path)
					}
					visited = true
					if markFirst {
						d.markActiveStore(store)
						close(marked)
						return d.reserveStoreForDeletion(path)
					}
					commit, ok := d.reserveStoreForDeletion(path)
					if !ok {
						t.Fatal("reservation refused")
					}
					go func() { d.markActiveStore(store); close(marked) }()
					select {
					case <-marked:
						t.Fatal("task entered deleting store")
					case <-time.After(40 * time.Millisecond):
					}
					return commit, true
				}
				logger := newTestDaemon(t).logger
				if provider == "codex" {
					execenv.PruneCodexSessionStores("", time.Hour, time.Now().Add(48*time.Hour), reserve, logger)
				} else {
					execenv.PruneHermesSessionStores("", time.Hour, time.Now().Add(48*time.Hour), reserve, logger)
				}
				if !visited {
					t.Fatal("GC did not reserve conversation leaf")
				}
				select {
				case <-marked:
				case <-time.After(time.Second):
					t.Fatal("task remained blocked after GC")
				}
				_, err := os.Stat(store)
				if markFirst && err != nil {
					t.Fatal("active session deleted")
				}
				if !markFirst && !os.IsNotExist(err) {
					t.Fatal("stale session retained")
				}
				if _, ok := d.reserveStoreForDeletion(store); ok {
					t.Fatal("newly active task not protected")
				}
				d.unmarkActiveStore(store)
			})
		}
	}
}
