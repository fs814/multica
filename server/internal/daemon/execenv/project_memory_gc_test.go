package execenv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReviewProjectSessionGCActiveStore(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CODEX_HOME", filepath.Join(root, "codex"))
	t.Setenv("MULTICA_TASK_CONFIG_ROOT", filepath.Join(root, "multica"))
	task := projectMemoryTask()
	for _, provider := range []string{"codex", "hermes"} {
		t.Run(provider, func(t *testing.T) {
			store := CodexSessionStorePath("", task)
			if provider == "hermes" {
				store = HermesSessionStorePath("", task.AgentID, filepath.Join(root, "hermes"), task)
			}
			if !strings.HasPrefix(store, root+string(os.PathSeparator)) {
				t.Fatal("unsafe fixture path")
			}
			mustWrite(t, filepath.Join(store, "fixture.txt"), "live transcript")
			var reserved string
			reserve := func(candidate string) (func(), bool) { reserved = candidate; return func() {}, candidate != store }
			now := time.Now().Add(30 * 24 * time.Hour)
			if provider == "codex" {
				PruneCodexSessionStores("", 14*24*time.Hour, now, reserve, testLogger())
			} else {
				PruneHermesSessionStores("", 14*24*time.Hour, now, reserve, testLogger())
			}
			if _, err := os.Stat(filepath.Join(store, "fixture.txt")); err != nil {
				t.Errorf("active project session deleted; marked active=%s; GC reserved=%s", store, reserved)
			}
		})
	}
}

func TestProjectSessionGCMixedNamespaces(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CODEX_HOME", filepath.Join(root, "codex"))
	t.Setenv("MULTICA_TASK_CONFIG_ROOT", filepath.Join(root, "multica"))
	for _, provider := range []string{"codex", "hermes"} {
		t.Run(provider, func(t *testing.T) {
			task := projectMemoryTask()
			active := CodexSessionStorePath("", task)
			if provider == "hermes" {
				active = HermesSessionStorePath("", task.AgentID, filepath.Join(root, "hermes"), task)
			}
			parent := filepath.Dir(filepath.Dir(active))
			stale := filepath.Join(filepath.Dir(active), "stale-child")
			legacy := filepath.Join(parent, "legacy-conversation")
			for _, dir := range []string{active, stale, legacy} {
				mustWrite(t, filepath.Join(dir, "transcript"), "fixture")
			}
			reserved := map[string]bool{}
			reserve := func(path string) (func(), bool) { reserved[path] = true; return func() {}, path != active }
			if provider == "codex" {
				PruneCodexSessionStores("", time.Hour, time.Now().Add(48*time.Hour), reserve, testLogger())
			} else {
				PruneHermesSessionStores("", time.Hour, time.Now().Add(48*time.Hour), reserve, testLogger())
			}
			if !reserved[active] || !reserved[stale] || !reserved[legacy] {
				t.Fatalf("wrong GC leaves: %v", reserved)
			}
			if _, err := os.Stat(active); err != nil {
				t.Fatal("live child lost")
			}
			for _, dir := range []string{stale, legacy} {
				if _, err := os.Stat(dir); !os.IsNotExist(err) {
					t.Fatalf("stale child retained: %s", dir)
				}
			}
		})
	}
}
