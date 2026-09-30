package worksync_test

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// This fixture exercises the full Center router with separate old/new databases.
// Replica exports still use the existing on-disk replica API; the older binary
// fixture independently covers real daemon replication.
func TestRecoveryPublicCenterBinary(t *testing.T) {
	binary := os.Getenv("WORK_SYNC_CENTER_BINARY")
	if binary == "" {
		t.Skip("explicitly built Center binary required")
	}
	h := setupRecoveryHTTP(t)
	h.server.Close()
	logs := os.Getenv("WORK_SYNC_BINARY_LOG_DIR")
	if logs == "" {
		logs = t.TempDir()
	}
	if err := os.MkdirAll(logs, 0700); err != nil {
		t.Fatal(err)
	}
	start := func(enabled bool) func() {
		t.Helper()
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := listener.Addr().(*net.TCPAddr).Port
		_ = listener.Close()
		h.baseURL = fmt.Sprintf("http://127.0.0.1:%d", port)
		out, err := os.Create(filepath.Join(logs, fmt.Sprintf("recovery-center-%d.log", port)))
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(binary)
		cmd.Dir = t.TempDir()
		cmd.Env = []string{
			"PATH=" + os.Getenv("PATH"), "TMPDIR=" + os.TempDir(),
			"DATABASE_URL=" + os.Getenv("WORK_SYNC_RECOVERY_TEST_DATABASE_URL"),
			fmt.Sprintf("PORT=%d", port), "APP_ENV=test", "DO_NOT_TRACK=1", "MAINTENANCE_PORT=0",
			"MULTICA_WORK_RECOVERY_TARGET_ID=" + h.cfg.TargetDeployment,
			"MULTICA_WORK_RECOVERY_SOURCE_ID=" + h.cfg.SourceDeployment,
			"MULTICA_WORK_RECOVERY_SOURCE_DATABASE_URL=" + h.cfg.SourceURL,
		}
		if enabled {
			cmd.Env = append(cmd.Env, "MULTICA_WORK_RECOVERY_ENABLED=1")
		}
		cmd.Stdout, cmd.Stderr = out, out
		if err = cmd.Start(); err != nil {
			_ = out.Close()
			t.Fatal(err)
		}
		_ = out.Close()
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		stopped := false
		stop := func() {
			if stopped {
				return
			}
			stopped = true
			_ = cmd.Process.Signal(syscall.SIGTERM)
			select {
			case <-done:
			case <-time.After(15 * time.Second):
				_ = cmd.Process.Kill()
				<-done
				t.Error("Center required forced termination")
			}
		}
		t.Cleanup(stop)
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			resp, err := http.Get(h.baseURL + "/health")
			if err == nil {
				_ = resp.Body.Close()
				if resp.StatusCode == 200 {
					t.Logf("Center ready pid=%d recovery_enabled=%v", cmd.Process.Pid, enabled)
					return stop
				}
			}
			select {
			case err := <-done:
				stopped = true
				t.Fatalf("Center exited: %v; inspect fixture logs", err)
			default:
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatal("Center readiness timeout")
		return stop
	}
	stop := start(false)
	h.call(t, "stage", h.token, h.request("stage"), 404)
	stop()
	stop = start(true)
	h.call(t, "stage", "mdt_fixture", h.request("stage"), 401)
	h.call(t, "stage", h.token, h.request("stage"), 200)
	h.call(t, "activate", h.token, h.request("activate"), 403)
	h.call(t, "fence", h.token, h.request("fence"), 200)
	stop()
	stop = start(true)
	h.call(t, "stage", h.token, h.request("stage"), 200)
	h.call(t, "activate", h.token, h.request("activate"), 200)
	h.call(t, "activate", h.token, h.request("activate"), 200)
	if n := h.target.Count(t, `SELECT count(*) FROM work_sync_change WHERE workspace_id=$1`, h.plan.Target.Workspace); n != 4 {
		t.Fatalf("restored records=%d", n)
	}
	if n := h.target.Count(t, `SELECT count(*) FROM agent_task_queue WHERE issue_id=$1 OR agent_id=$2`, h.issue, h.agent); n != 0 {
		t.Fatal("recovery dispatched work")
	}
	stop()
}
