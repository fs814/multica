//go:build windows

package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// buildOrphanSpawner compiles a helper that spawns a long-lived grandchild and
// records its pid, then exits immediately. This reproduces the shape that
// leaked: an agent CLI that exits while a tool subprocess it started keeps
// running (the real-world case was sh.exe spinning on a severed pipe at ~100%
// kernel time, holding the deleted per-task TEMP dir open).
func buildOrphanSpawner(t *testing.T) (exePath string, pidFile string) {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "spawner.go")
	exePath = filepath.Join(dir, "spawner.exe")
	pidFile = filepath.Join(dir, "grandchild.pid")

	const source = `package main

import (
	"fmt"
	"os"
	"os/exec"
	"time"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "grandchild" {
		time.Sleep(10 * time.Minute)
		return
	}
	child := exec.Command(os.Args[0], "grandchild")
	if err := child.Start(); err != nil {
		panic(err)
	}
	if err := os.WriteFile(os.Getenv("PID_FILE"), []byte(fmt.Sprint(child.Process.Pid)), 0o600); err != nil {
		panic(err)
	}
	// LEADER_PERSIST keeps the leader alive so a test can observe a tree
	// that is genuinely still populated. Otherwise exit immediately,
	// abandoning the grandchild — the orphan shape being regression-tested.
	if os.Getenv("LEADER_PERSIST") == "1" {
		time.Sleep(10 * time.Minute)
	}
}`
	if err := os.WriteFile(src, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	build := exec.Command("go", "build", "-o", exePath, src)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build spawner: %v: %s", err, out)
	}
	return exePath, pidFile
}

func readPidWhenReady(t *testing.T, pidFile string) int {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(pidFile)
		if err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("grandchild pid file never appeared")
	return 0
}

func pidAlive(pid int) bool {
	// tasklist is authoritative and, unlike OpenProcess, does not report a
	// still-open handle to an already-terminated process as alive.
	out, err := exec.Command("tasklist", "/FI", "PID eq "+strconv.Itoa(pid), "/NH").Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), strconv.Itoa(pid))
}

func waitPidGone(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !pidAlive(pid) {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return !pidAlive(pid)
}

// TestStartAgentProcessReapsOrphanedGrandchildren is the regression test for
// the leak: an agent that exits normally must not leave descendants behind.
// Before the Job Object, the grandchild here survived indefinitely.
func TestStartAgentProcessReapsOrphanedGrandchildren(t *testing.T) {
	exePath, pidFile := buildOrphanSpawner(t)

	cmd := exec.Command(exePath)
	cmd.Env = append(os.Environ(), "PID_FILE="+pidFile)
	hideAgentWindow(cmd)
	configureProcessGroup(cmd)

	if err := startAgentProcess(cmd); err != nil {
		t.Fatalf("startAgentProcess: %v", err)
	}
	leaderPid := cmd.Process.Pid
	grandchildPid := readPidWhenReady(t, pidFile)
	t.Cleanup(func() {
		if pidAlive(grandchildPid) {
			_ = exec.Command("taskkill", "/PID", strconv.Itoa(grandchildPid), "/F", "/T").Run()
		}
	})

	if grandchildPid == leaderPid {
		t.Fatal("grandchild pid must differ from leader pid")
	}

	// The leader exits on its own; Wait reaps it.
	if err := cmd.Wait(); err != nil {
		t.Fatalf("leader wait: %v", err)
	}

	if !waitPidGone(grandchildPid, 15*time.Second) {
		t.Fatalf("grandchild %d survived the leader — orphan leak regressed", grandchildPid)
	}
}

// TestSignalProcessGroupSIGKILLTerminatesWholeTree covers the cancellation
// path: a group SIGKILL must reach descendants, not just the leader.
func TestSignalProcessGroupSIGKILLTerminatesWholeTree(t *testing.T) {
	exePath, pidFile := buildOrphanSpawner(t)

	cmd := exec.Command(exePath)
	// Persist the leader so this exercises the cancellation path (an explicit
	// group SIGKILL against a live tree), not the exit-reaper path.
	cmd.Env = append(os.Environ(), "PID_FILE="+pidFile, "LEADER_PERSIST=1")
	hideAgentWindow(cmd)

	if err := startAgentProcess(cmd); err != nil {
		t.Fatalf("startAgentProcess: %v", err)
	}
	leaderPid := cmd.Process.Pid
	grandchildPid := readPidWhenReady(t, pidFile)
	t.Cleanup(func() {
		for _, pid := range []int{leaderPid, grandchildPid} {
			if pidAlive(pid) {
				_ = exec.Command("taskkill", "/PID", strconv.Itoa(pid), "/F", "/T").Run()
			}
		}
	})

	signalProcessGroup(cmd.Process, syscall.SIGKILL)

	if !waitPidGone(grandchildPid, 15*time.Second) {
		t.Fatalf("grandchild %d survived a group SIGKILL", grandchildPid)
	}
	if !waitPidGone(leaderPid, 15*time.Second) {
		t.Fatalf("leader %d survived a group SIGKILL", leaderPid)
	}
	_ = cmd.Wait()
}

// TestWaitProcessGroupGoneReportsTruthfully guards against the old
// hardcoded `return false`, which made positive cleanup unverifiable and is
// why codexInitializeRetrySupported had to stay disabled.
func TestWaitProcessGroupGoneReportsTruthfully(t *testing.T) {
	exePath, pidFile := buildOrphanSpawner(t)

	cmd := exec.Command(exePath)
	// Keep the leader alive so the tree is legitimately non-empty for the
	// first assertion; otherwise the reaper correctly empties it immediately.
	cmd.Env = append(os.Environ(), "PID_FILE="+pidFile, "LEADER_PERSIST=1")
	hideAgentWindow(cmd)

	if err := startAgentProcess(cmd); err != nil {
		t.Fatalf("startAgentProcess: %v", err)
	}
	grandchildPid := readPidWhenReady(t, pidFile)
	t.Cleanup(func() {
		if pidAlive(grandchildPid) {
			_ = exec.Command("taskkill", "/PID", strconv.Itoa(grandchildPid), "/F", "/T").Run()
		}
	})

	// Leader and grandchild are both alive, so the tree is NOT gone: a short
	// timeout must report false rather than claiming success.
	if waitProcessGroupGone(cmd.Process, 300*time.Millisecond) {
		t.Fatal("reported tree gone while a grandchild was still running")
	}

	signalProcessGroup(cmd.Process, syscall.SIGKILL)

	// After the tree kill it must report true — the assertion the old
	// unconditional false could never satisfy.
	if !waitProcessGroupGone(cmd.Process, 15*time.Second) {
		t.Fatal("tree was killed but waitProcessGroupGone still reported alive")
	}
	_ = cmd.Wait()
}

// TestStartAgentProcessPropagatesStartFailure keeps the error contract of the
// cmd.Start() call it replaced.
func TestStartAgentProcessPropagatesStartFailure(t *testing.T) {
	cmd := exec.Command(filepath.Join(t.TempDir(), "does-not-exist.exe"))
	if err := startAgentProcess(cmd); err == nil {
		t.Fatal("expected an error starting a nonexistent executable")
	}
}
