//go:build unix

package daemon

import (
	"context"
	"encoding/json"
	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/workflow"
	"github.com/multica-ai/multica/server/pkg/agent"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

type reviewDelayedStopBackend struct {
	child                     *exec.Cmd
	started, release, stopped chan struct{}
}

func (b *reviewDelayedStopBackend) Execute(ctx context.Context, _ string, _ agent.ExecOptions) (*agent.Session, error) {
	if err := b.child.Start(); err != nil {
		return nil, err
	}
	messages := make(chan agent.Message)
	results := make(chan agent.Result, 1)
	close(b.started)
	go func() {
		<-ctx.Done()
		<-b.release // Hold asynchronous process cleanup at a deterministic barrier.
		_ = b.child.Process.Kill()
		_ = b.child.Wait()
		close(messages)
		results <- agent.Result{Status: "cancelled"}
		close(results)
		close(b.stopped)
	}()
	return &agent.Session{Messages: messages, Result: results}, nil
}

func TestReviewDebugCancelReceiptRequiresPhysicalStop(t *testing.T) {
	// A controlled child represents a backend whose asynchronous cancellation
	// cleanup has not finished. No paid agent, existing daemon or repo is used.
	child := exec.Command("sleep", "30")
	backend := &reviewDelayedStopBackend{child: child, started: make(chan struct{}), release: make(chan struct{}), stopped: make(chan struct{})}
	var release sync.Once
	defer func() { release.Do(func() { close(backend.release) }); <-backend.stopped }()
	premature := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/execution-receipts") {
			var receipt workflow.DebugExecutionReceipt
			if err := json.NewDecoder(r.Body).Decode(&receipt); err != nil {
				t.Error(err)
			}
			premature = receipt.ProcessStopped && child.Process.Signal(syscall.Signal(0)) == nil
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"accepted"}`))
	}))
	defer server.Close()
	d := newTestDaemon(t)
	d.debugStopWaitBudget = 50 * time.Millisecond
	d.client = NewClient(server.URL)
	d.client.configureDebugDelivery(t.TempDir())
	task := Task{ID: uuid.NewString(), WorkflowExecutionID: uuid.NewString(), WorkflowExecutionMode: "draft_test"}
	if err := d.client.registerDebugTask(&task); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { <-backend.started; cancel() }()
	result, _, err := d.executeAndDrain(ctx, backend, "controlled", agent.ExecOptions{}, slog.Default(), task.ID, "", new(atomic.Int32))
	if err != nil || result.Status != "cancelled" {
		t.Fatalf("cancel path: %+v %v", result, err)
	}
	// The handleTask cancel branch invokes this same production client method.
	if err = d.client.AckTaskCancelled(context.Background(), task.ID, TaskCancelAck{}); err != nil {
		t.Fatal(err)
	}
	if premature {
		t.Fatal("persisted process_stopped=true and sealed receipt while controlled child is alive and backend Result is pending")
	}
}
