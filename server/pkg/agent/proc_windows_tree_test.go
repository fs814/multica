//go:build windows

package agent

import (
	"log/slog"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestStartOwnedProcessTreePropagatesStartFailure keeps the error contract of
// the cmd.Start() call that startOwnedProcessTree replaced: a child that cannot
// be created at all must surface as an error to the caller rather than being
// swallowed by the ownership machinery around it.
//
// The rest of this file's original coverage (orphan reaping, SIGKILL killing
// the whole tree, and truthful waitProcessGroupGone reporting) moved to
// proc_windows_test.go when upstream's Job Object implementation replaced the
// fork's: TestStartOwnedProcessTreeCapturesImmediateDescendants,
// TestCodexWindowsDescendantsDieWithTheOwnedProcessTree and
// TestWaitProcessGroupGoneWithoutOwnershipReportsUnconfirmed assert the same
// behaviour against the surviving API.
func TestStartOwnedProcessTreePropagatesStartFailure(t *testing.T) {
	cmd := exec.Command(filepath.Join(t.TempDir(), "does-not-exist.exe"))
	if err := startOwnedProcessTree(cmd, slog.Default()); err == nil {
		t.Fatal("expected an error starting a nonexistent executable")
	}
}
