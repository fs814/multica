package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestHandleTaskDaemonShutdownPreservesRetryAndCompletedResults(t *testing.T) {
	for _, status := range []string{"cancelled", "blocked", "error", "completed"} {
		t.Run(status, func(t *testing.T) {
			var failed atomic.Value
			var completes atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.HasSuffix(r.URL.Path, "/fail"):
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					failed.Store(body)
				case strings.HasSuffix(r.URL.Path, "/complete"):
					completes.Add(1)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"status":"running"}`))
			}))
			defer srv.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			d := &Daemon{
				client:             NewClient(srv.URL),
				logger:             slog.New(slog.NewTextHandler(io.Discard, nil)),
				runtimeIndex:       map[string]Runtime{"rt-1": {ID: "rt-1", Provider: "codex"}},
				cancelPollInterval: time.Hour,
			}
			d.runner = taskRunnerFunc(func(context.Context, Task, string, int, *slog.Logger) (TaskResult, error) {
				cancel()
				result := TaskResult{Status: status, Comment: "backend result", SessionID: "resume-session", WorkDir: "retained-workdir"}
				if status == "error" {
					return result, errors.New("backend stopped")
				}
				return result, nil
			})
			d.handleTask(ctx, Task{ID: "shutdown-task", RuntimeID: "rt-1"}, 0)
			if status == "completed" {
				if completes.Load() != 1 || failed.Load() != nil {
					t.Fatalf("completed result was lost during shutdown: completes=%d, failure=%v", completes.Load(), failed.Load())
				}
				return
			}
			body, _ := failed.Load().(map[string]any)
			if body["failure_reason"] != "runtime_offline" {
				t.Fatalf("failure=%v; daemon shutdown must stay retryable", body)
			}
			if body["session_id"] != "resume-session" || body["work_dir"] != "retained-workdir" {
				t.Fatalf("resume metadata lost: %v", body)
			}
			if body["error"] == "task cancelled by server" || completes.Load() != 0 {
				t.Fatalf("incorrect shutdown disposition: %v", body)
			}
		})
	}
}
