package daemon

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// TestRemoveTaskTempDirSucceeds covers the ordinary case: nothing is holding
// the directory, so it goes away on the first attempt.
func TestRemoveTaskTempDirSucceeds(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "multica-task-plain")
	if err := os.MkdirAll(filepath.Join(dir, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "nested", "f.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := removeTaskTempDir(dir); err != nil {
		t.Fatalf("removeTaskTempDir: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("directory still present: %v", err)
	}
}

// TestRemoveTaskTempDirTreatsMissingAsSuccess ensures an already-absent
// directory is not reported as a cleanup failure. The previous code logged a
// warning for this, which made real leaks harder to spot in daemon logs.
func TestRemoveTaskTempDirTreatsMissingAsSuccess(t *testing.T) {
	if err := removeTaskTempDir(filepath.Join(t.TempDir(), "never-created")); err != nil {
		t.Fatalf("missing dir should be success, got %v", err)
	}
}

// TestRemoveTaskTempDirRetriesWhileHandleOpen is the regression test for the
// half-deleted temp dir that poisoned the MSYS /tmp mapping. On Windows an
// open handle blocks deletion; cleanup must retry rather than give up on the
// first ERROR_SHARING_VIOLATION.
func TestRemoveTaskTempDirRetriesWhileHandleOpen(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("POSIX unlinks files that are still open; the retry only matters on Windows")
	}

	dir := filepath.Join(t.TempDir(), "multica-task-held")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	held := filepath.Join(dir, "held.txt")
	f, err := os.Create(held)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("busy"); err != nil {
		t.Fatal(err)
	}

	// Release the handle shortly after cleanup starts, well inside the
	// retry budget, emulating a tool subprocess finishing its teardown.
	closed := make(chan struct{})
	go func() {
		time.Sleep(300 * time.Millisecond)
		_ = f.Close()
		close(closed)
	}()

	start := time.Now()
	if err := removeTaskTempDir(dir); err != nil {
		<-closed
		t.Fatalf("removeTaskTempDir gave up while a handle was open: %v", err)
	}
	<-closed

	if elapsed := time.Since(start); elapsed < 200*time.Millisecond {
		t.Fatalf("expected cleanup to wait for the open handle, returned after %s", elapsed)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("directory still present after cleanup: %v", err)
	}
}

// TestRemoveTaskTempDirGivesUpEventually confirms the retry loop is bounded,
// so a permanently-held directory cannot wedge task completion.
func TestRemoveTaskTempDirGivesUpEventually(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("requires Windows delete-while-open semantics")
	}

	dir := filepath.Join(t.TempDir(), "multica-task-stuck")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(dir, "pinned.txt"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	start := time.Now()
	err = removeTaskTempDir(dir)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected an error for a permanently held directory")
	}
	if elapsed > taskTempDirRemoveTimeout+2*time.Second {
		t.Fatalf("cleanup overran its budget: %s", elapsed)
	}
}
