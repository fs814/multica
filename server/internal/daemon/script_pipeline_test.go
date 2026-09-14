package daemon

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/multica-ai/multica/server/internal/workflow"
	"github.com/multica-ai/multica/server/pkg/scriptpipeline"
)

func TestScriptPipelineReturnsValidSubmissionWithoutProvider(t *testing.T) {
	shell, extension := "bash", ".sh"
	if runtime.GOOS == "windows" {
		shell, extension = "pwsh", ".ps1"
	}
	if _, err := exec.LookPath(shell); err != nil {
		t.Skip("shell unavailable")
	}
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "pass", true: "fail"}[fail], func(t *testing.T) {
			dir := t.TempDir()
			body := "echo script-result\n"
			if runtime.GOOS == "windows" {
				body = "Write-Output 'script-result'\n"
			}
			if fail {
				body += "exit 7\n"
			}
			if err := os.WriteFile(filepath.Join(dir, "run"+extension), []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			var reports atomic.Int32
			var started atomic.Bool
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/start") {
					started.Store(true)
					w.WriteHeader(http.StatusNoContent)
					return
				}
				if !started.Load() {
					t.Error("script output arrived before the task was marked running")
				}
				if !strings.HasSuffix(r.URL.Path, "/messages") {
					t.Errorf("unexpected provider/network call: %s", r.URL.Path)
				}
				reports.Add(1)
				w.WriteHeader(http.StatusNoContent)
			}))
			defer srv.Close()
			d := &Daemon{client: NewClient(srv.URL)}
			result, err := d.runScriptPipelineTask(context.Background(), Task{ID: "task-test", WorkflowRunID: "run-test", WorkflowStepInstanceID: "step-test", WorkflowScriptPipeline: &scriptpipeline.Config{Directory: dir, Platform: "auto", Steps: []string{"run"}, TimeoutSeconds: 30}}, slog.New(slog.NewTextHandler(io.Discard, nil)))
			if err != nil {
				t.Fatal(err)
			}
			submission, problems, err := workflow.ParseSubmission([]byte(result.Comment), "step-test")
			if err != nil {
				t.Fatalf("submission rejected: %v %v", err, problems)
			}
			expected := workflow.VerdictPass
			if fail {
				expected = workflow.VerdictFail
			}
			if submission.Verdict != expected || result.Status != "completed" || !strings.Contains(submission.Artifact.Summary, "script-result") {
				t.Fatalf("invalid result: %+v", result)
			}
			if !started.Load() || reports.Load() < 2 || d.runningTasks.Load() != 0 {
				t.Fatalf("messages=%d running=%d", reports.Load(), d.runningTasks.Load())
			}
		})
	}
}
