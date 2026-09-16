package projectmemory

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/google/uuid"
)

func fixture(t *testing.T) (Store, Binding) {
	t.Helper()
	s := Store{ManagedRoot: filepath.Join(t.TempDir(), "durable"), DaemonID: uuid.NewString()}
	return s, Binding{WorkspaceID: uuid.NewString(), ProjectID: uuid.NewString(), OwnerDaemonID: s.DaemonID, Backend: "managed", Revision: 1}
}
func publish(b Binding, c Candidate) Binding {
	b.Generation = c.Generation
	b.Digest = c.Digest
	b.ContentRevision = c.ContentRevision
	b.State = "ready"
	return b
}
func stage(t *testing.T, s Store, b Binding, body string) Binding {
	t.Helper()
	c, err := s.Stage(b, map[string]string{"README.md": body}, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	return publish(b, c)
}
func TestNamespacesAndPersistence(t *testing.T) {
	for _, mode := range []string{"managed", "shared-source", "independent-source"} {
		t.Run(mode, func(t *testing.T) {
			s, a := fixture(t)
			b := a
			b.ProjectID = uuid.NewString()
			if mode != "managed" {
				a.Backend = "source"
				b.Backend = "source"
				a.SourceRoot = t.TempDir()
				b.SourceRoot = a.SourceRoot
				if mode == "independent-source" {
					b.SourceRoot = t.TempDir()
				}
			}
			a = stage(t, s, a, "only A")
			b = stage(t, s, b, "only B")
			// A new Store simulates a restarted daemon; task directories are unrelated.
			restarted := Store{ManagedRoot: s.ManagedRoot, DaemonID: s.DaemonID}
			ar, err := restarted.Read(a)
			if err != nil || ar.Files["README.md"] != "only A" {
				t.Fatalf("A: %v %v", ar, err)
			}
			br, err := restarted.Read(b)
			if err != nil || br.Files["README.md"] != "only B" {
				t.Fatalf("B: %v %v", br, err)
			}
			forged := a
			forged.ProjectID = b.ProjectID
			if _, err := s.Read(forged); err == nil {
				t.Fatal("cross-project generation accepted")
			}
			forged = a
			forged.WorkspaceID = uuid.NewString()
			if _, err := s.Read(forged); err == nil {
				t.Fatal("cross-workspace generation accepted")
			}
			forged = a
			forged.OwnerDaemonID = uuid.NewString()
			if _, err := s.Read(forged); err == nil {
				t.Fatal("foreign owner accepted")
			}
		})
	}
}
func TestUnpublishedCandidatesAndExternalEdits(t *testing.T) {
	s, b := fixture(t)
	b = stage(t, s, b, "published")
	c, err := s.Stage(b, map[string]string{"README.md": "uncommitted"}, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Read(b)
	if err != nil || got.Files["README.md"] != "published" {
		t.Fatal("unpublished write became visible")
	}
	next := publish(b, c)
	got, err = s.Read(next)
	if err != nil || got.Files["README.md"] != "uncommitted" {
		t.Fatal(err)
	}
	ns, _ := s.namespace(b)
	if err := os.WriteFile(filepath.Join(ns, "versions", b.Generation, "snapshot.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Read(b); err != ErrConflict {
		t.Fatalf("external modification: %v", err)
	}
}
func TestCandidateConcurrencyAndInvalidPaths(t *testing.T) {
	s, b := fixture(t)
	var wg sync.WaitGroup
	generations := make(chan string, 10)
	for i := 0; i < 10; i++ {
		wg.Go(func() {
			c, err := s.Stage(b, map[string]string{"README.md": "parallel"}, "fixture")
			if err != nil {
				t.Error(err)
				return
			}
			generations <- c.Generation
		})
	}
	wg.Wait()
	close(generations)
	seen := map[string]bool{}
	for gen := range generations {
		if seen[gen] {
			t.Fatal("candidate collision")
		}
		seen[gen] = true
	}
	for _, name := range []string{"../B", "/absolute", "C:/file", "a\\b", "a/../b", "a/./b", "NUL", "COM1.md", "a.", "a "} {
		if _, err := s.Stage(b, map[string]string{name: "attack"}, "fixture"); err == nil {
			t.Errorf("accepted %q", name)
		}
	}
	if _, err := s.Stage(b, map[string]string{"A.md": "a", "a.md": "b"}, "fixture"); err == nil {
		t.Fatal("case collision accepted")
	}
}
func TestRejectLinkedStore(t *testing.T) {
	s, b := fixture(t)
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Dir(s.ManagedRoot), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, s.ManagedRoot); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := s.Stage(b, map[string]string{"README.md": "attack"}, "fixture"); err == nil {
		t.Fatal("linked store accepted")
	}
}

func TestRejectJunctionAndForgedManifest(t *testing.T) {
	s, b := fixture(t)
	b = stage(t, s, b, "accepted")
	ns, _ := s.namespace(b)
	path := filepath.Join(ns, "versions", b.Generation, "snapshot.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var snap Snapshot
	if err = json.Unmarshal(raw, &snap); err != nil {
		t.Fatal(err)
	}
	snap.ProjectID = uuid.NewString()
	raw, _ = json.Marshal(snap)
	sum := sha256.Sum256(raw)
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	b.Digest = hex.EncodeToString(sum[:])
	if _, err = s.Read(b); err == nil {
		t.Fatal("forged manifest accepted with matching digest")
	}
	if runtime.GOOS != "windows" {
		return
	}
	s, b = fixture(t)
	outside := t.TempDir()
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", s.ManagedRoot, outside).CombinedOutput(); err != nil {
		t.Fatalf("create fixture junction: %v %s", err, out)
	}
	if _, err = s.Stage(b, map[string]string{"README.md": "attack"}, "fixture"); err == nil {
		t.Fatal("junction accepted")
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatal("junction wrote outside namespace")
	}
}

func TestSourceBindingIgnoresExecutionWorktree(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	worktree := filepath.Join(root, "execution")
	for _, args := range [][]string{{"init", source}, {"-C", source, "-c", "user.name=Memory Fixture", "-c", "user.email=fixture@invalid", "commit", "--allow-empty", "-m", "fixture"}, {"-C", source, "worktree", "add", "--detach", worktree}} {
		cmd := exec.Command("git", args...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("fixture git: %v %s", err, out)
		}
	}
	t.Chdir(worktree)
	s, b := fixture(t)
	b.Backend = "source"
	b.SourceRoot = source
	b = stage(t, s, b, "bound source only")
	if _, err := s.Read(b); err != nil {
		t.Fatal(err)
	}
	ns, err := s.namespace(b)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(ns, "versions", b.Generation, "snapshot.json")); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(worktree, "datas", "memory")); !os.IsNotExist(err) {
		t.Fatal("execution worktree became memory authority")
	}
	if snap, err := s.Read(b); err != nil || snap.Files["README.md"] != "bound source only" {
		t.Fatal("source memory unreadable from worktree")
	}
}

func TestSourcePublishesOnlySelectedGenerationToDatas(t *testing.T) {
	s, b := fixture(t)
	b.Backend = "source"
	b.SourceRoot = t.TempDir()
	first := stage(t, s, b, "accepted")
	orphan, err := s.Stage(b, map[string]string{"README.md": "unpublished"}, "rejected contender")
	if err != nil {
		t.Fatal(err)
	}
	final, _ := s.namespace(b)
	staged, _ := s.stagingNamespace(b)
	if _, err := os.Stat(final); !os.IsNotExist(err) {
		t.Fatal("candidate leaked into final data")
	}
	restarted := Store{ManagedRoot: s.ManagedRoot, DaemonID: s.DaemonID}
	got, err := restarted.Read(first)
	if err != nil || got.Files["README.md"] != "accepted" {
		t.Fatalf("promote: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(final, "versions", first.Generation, "files", "README.md"))
	if err != nil || string(raw) != "accepted" {
		t.Fatalf("reviewable export: %s %v", raw, err)
	}
	if _, err := os.Stat(filepath.Join(staged, "versions", first.Generation)); !os.IsNotExist(err) {
		t.Fatal("selected generation not moved")
	}
	if _, err := os.Stat(filepath.Join(staged, "versions", orphan.Generation, "snapshot.json")); err != nil {
		t.Fatal("orphan not retained in intermediate storage")
	}
	if _, err := os.Stat(filepath.Join(final, "versions", orphan.Generation)); !os.IsNotExist(err) {
		t.Fatal("orphan published")
	}
	if _, err := restarted.Read(first); err != nil {
		t.Fatal(err)
	}
}

func TestCorruptSelectedSourceCandidateIsNotPublished(t *testing.T) {
	s, b := fixture(t)
	b.Backend = "source"
	b.SourceRoot = t.TempDir()
	b = stage(t, s, b, "accepted")
	staged, _ := s.stagingNamespace(b)
	final, _ := s.namespace(b)
	if err := os.WriteFile(filepath.Join(staged, "versions", b.Generation, "snapshot.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Read(b); err != ErrConflict {
		t.Fatalf("corrupt candidate accepted: %v", err)
	}
	if _, err := os.Stat(final); !os.IsNotExist(err) {
		t.Fatal("corrupt candidate reached final data")
	}
}

func TestConcurrentSourcePromotion(t *testing.T) {
	s, b := fixture(t)
	b.Backend = "source"
	b.SourceRoot = t.TempDir()
	b = stage(t, s, b, "accepted")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Go(func() {
			if snap, err := s.Read(b); err != nil || snap.Files["README.md"] != "accepted" {
				t.Errorf("read: %v", err)
			}
		})
	}
	wg.Wait()
}
func TestRejectFileDirectoryCollision(t *testing.T) {
	s, b := fixture(t)
	if _, err := s.Stage(b, map[string]string{"folder": "file", "Folder/entry.md": "nested"}, "fixture"); err == nil {
		t.Fatal("file/directory collision accepted")
	}
}
