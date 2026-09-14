package scriptpipeline

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func scriptFixture(t *testing.T, fail string) *Config {
	t.Helper()
	shell := "bash"
	ext := ".sh"
	if runtime.GOOS == "windows" {
		shell = "pwsh"
		ext = ".ps1"
	}
	if _, err := exec.LookPath(shell); err != nil {
		t.Skip("test shell not installed")
	}
	dir := filepath.Join(t.TempDir(), "project with spaces")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, stage := range Stages {
		body := "echo " + stage + " >> trace.txt\necho " + stage + "\n"
		if runtime.GOOS == "windows" {
			body = "'" + stage + "' | Add-Content -LiteralPath trace.txt\nWrite-Output '" + stage + "'\n"
		}
		if stage == fail {
			body += "exit 7\n"
		}
		if err := os.WriteFile(filepath.Join(dir, stage+ext), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return &Config{Directory: dir, Platform: "auto", Steps: []string{"run", "clone", "build"}, TimeoutSeconds: 20}
}
func TestRunOrdersStepsAndStopsOnFailure(t *testing.T) {
	for _, fail := range []string{"", "build"} {
		t.Run(fail, func(t *testing.T) {
			c := scriptFixture(t, fail)
			r := Run(context.Background(), c, nil)
			if (r.Error != "") != (fail != "") {
				t.Fatalf("error=%q", r.Error)
			}
			want := 3
			if fail != "" {
				want = 2
			}
			if len(r.Steps) != want {
				t.Fatalf("steps=%+v", r.Steps)
			}
			for i, s := range r.Steps {
				if s.Name != Stages[i] {
					t.Fatal("wrong order")
				}
			}
			trace, err := os.ReadFile(filepath.Join(c.Directory, "trace.txt"))
			if err != nil {
				t.Fatal(err)
			}
			if fail != "" && strings.Contains(string(trace), "run") {
				t.Fatal("ran after build failure")
			}
			if fail != "" && r.Steps[1].ExitCode != 7 {
				t.Fatal("exit code lost")
			}
		})
	}
}
func TestRunOnlySelectedStepAndPreflightAllScripts(t *testing.T) {
	c := scriptFixture(t, "")
	c.Steps = []string{"run"}
	r := Run(context.Background(), c, nil)
	if r.Error != "" || len(r.Steps) != 1 || r.Steps[0].Name != "run" {
		t.Fatalf("result=%+v", r)
	}
	c = scriptFixture(t, "")
	c.Scripts = map[string]string{"build": "missing" + filepath.Ext(r.Steps[0].Path)}
	r = Run(context.Background(), c, nil)
	if r.Error == "" {
		t.Fatal("missing script accepted")
	}
	if _, err := os.Stat(filepath.Join(c.Directory, "trace.txt")); !os.IsNotExist(err) {
		t.Fatal("started clone before validating build")
	}
}
func TestExplicitPathAndAmbiguousDiscovery(t *testing.T) {
	c := scriptFixture(t, "")
	c.Steps = []string{"run"}
	ext := ".sh"
	if runtime.GOOS == "windows" {
		ext = ".ps1"
	}
	path := filepath.Join(c.Directory, "run_other"+ext)
	if err := os.WriteFile(path, []byte("echo test"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.Plan(runtime.GOOS); err == nil {
		t.Fatal("ambiguous scripts accepted")
	}
	c.Scripts = map[string]string{"run": path}
	_, plan, err := c.Plan(runtime.GOOS)
	if err != nil || len(plan) != 1 || plan[0].Path != path {
		t.Fatalf("plan=%v err=%v", plan, err)
	}
}
func TestWrongPlatformAndInvalidSelection(t *testing.T) {
	c := scriptFixture(t, "")
	c.Platform = "linux"
	if runtime.GOOS == "linux" {
		c.Platform = "windows"
	}
	if _, _, err := c.Plan(runtime.GOOS); err == nil {
		t.Fatal("wrong platform accepted")
	}
	c.Platform = "auto"
	c.Steps = []string{}
	if err := c.Validate(); err == nil {
		t.Fatal("empty steps accepted")
	}
	c.Steps = []string{"run", "run"}
	if err := c.Validate(); err == nil {
		t.Fatal("duplicate steps accepted")
	}
}
func TestDirectoryLockCancelledWaitDoesNotLeak(t *testing.T) {
	release, err := lockDirectory(context.Background(), "test-lock")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := lockDirectory(ctx, "test-lock"); err == nil {
		t.Fatal("lock did not wait")
	}
	release()
	directoryLocks.Lock()
	defer directoryLocks.Unlock()
	if len(directoryLocks.items) != 0 {
		t.Fatal("directory locks leaked")
	}
}
func TestRunCancellation(t *testing.T) {
	c := scriptFixture(t, "")
	c.Steps = []string{"run"}
	ext := ".sh"
	body := "sleep 30\n"
	if runtime.GOOS == "windows" {
		ext = ".ps1"
		body = "Start-Sleep -Seconds 30\n"
	}
	os.WriteFile(filepath.Join(c.Directory, "run"+ext), []byte(body), 0600)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	start := time.Now()
	r := Run(ctx, c, nil)
	if r.Error == "" || time.Since(start) > 8*time.Second {
		t.Fatalf("cancellation did not stop process: %+v", r)
	}
}
