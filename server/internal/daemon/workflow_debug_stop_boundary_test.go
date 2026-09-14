//go:build unix

package daemon

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/workflow"
	"github.com/multica-ai/multica/server/pkg/agent"
)

type debugPhysicalBackend struct {
	ready, release, exited chan struct{}
	status                 string
	child                  *exec.Cmd
}

func (b *debugPhysicalBackend) Execute(ctx context.Context, _ string, opts agent.ExecOptions) (*agent.Session, error) {
	if err := b.child.Start(); err != nil {
		return nil, err
	}
	messages := make(chan agent.Message, 1)
	result := make(chan agent.Result, 1)
	close(b.ready)
	go func() {
		<-b.release
		_ = b.child.Process.Kill()
		_ = b.child.Wait()
		close(b.exited)
		messages <- agent.Message{Type: agent.MessageText, Content: "final message after physical exit"}
		close(messages)
		result <- agent.Result{Status: b.status, ProcessStoppedAt: time.Now().UTC()}
		close(result)
	}()
	return &agent.Session{Messages: messages, Result: result}, nil
}
func TestDebugReceiptWaitsForPhysicalExitAndFinalDelivery(t *testing.T) {
	for _, status := range []string{"completed", "cancelled", "timeout"} {
		t.Run(status, func(t *testing.T) {
			b := &debugPhysicalBackend{ready: make(chan struct{}), release: make(chan struct{}), exited: make(chan struct{}), status: status, child: exec.Command("sleep", "30")}
			var receipts, messages atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/messages") {
					messages.Add(1)
				}
				if strings.HasSuffix(r.URL.Path, "/execution-receipts") {
					select {
					case <-b.exited:
					default:
						t.Error("receipt before process exit")
					}
					var receipt workflow.DebugExecutionReceipt
					_ = json.NewDecoder(r.Body).Decode(&receipt)
					if messages.Load() != 1 || receipt.FinalMessageSeq != 1 || receipt.ProcessStoppedAt.IsZero() {
						t.Error("receipt before final transcript")
					}
					receipts.Add(1)
				}
				_, _ = w.Write([]byte(`{"status":"accepted"}`))
			}))
			defer srv.Close()
			d := newTestDaemon(t)
			d.client = NewClient(srv.URL)
			d.client.configureDebugDelivery(t.TempDir())
			d.debugStopWaitBudget = time.Second
			task := Task{ID: uuid.NewString(), WorkflowExecutionID: uuid.NewString(), WorkflowExecutionMode: "draft_test"}
			if err := d.client.registerDebugTask(&task); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			if status == "timeout" {
				cancel()
				ctx, cancel = context.WithTimeout(context.Background(), 100*time.Millisecond)
			}
			defer cancel()
			done := make(chan struct{})
			var out agent.Result
			var err error
			go func() {
				defer close(done)
				out, _, err = d.executeAndDrain(ctx, b, "", agent.ExecOptions{}, slog.Default(), task.ID, "", new(atomic.Int32))
			}()
			<-b.ready
			// Even a successful terminal transport callback while the child is alive
			// is not allowed to manufacture a lifecycle receipt.
			if err := d.client.AckTaskCancelled(context.Background(), task.ID, TaskCancelAck{}); err != nil {
				t.Error(err)
			}
			if status == "cancelled" {
				cancel()
			}
			if status != "completed" {
				<-ctx.Done()
				if status == "timeout" && ctx.Err() != context.DeadlineExceeded {
					t.Error("timeout fixture did not expire")
				}
				select {
				case <-done:
					t.Error("runner returned while physical child was still blocked")
				case <-time.After(25 * time.Millisecond):
				}
			}
			if receipts.Load() != 0 {
				t.Error("terminal API inferred process exit")
			}
			close(b.release)
			<-done
			if err != nil || out.Status != status {
				t.Fatalf("lifecycle %+v %v", out, err)
			}
			if err := d.client.finishDebugTaskDelivery(context.Background(), task.ID); err != nil {
				t.Fatal(err)
			}
			if receipts.Load() != 1 {
				t.Fatal("receipt not delivered after confirmed stop")
			}
		})
	}
}
func TestDebugUnconfirmedAttemptCannotBeSealedByLaterSuccess(t *testing.T) {
	c := NewClient("http://controlled.invalid")
	c.configureDebugDelivery(t.TempDir())
	id := uuid.NewString()
	if err := c.registerDebugTask(&Task{ID: id, WorkflowExecutionID: uuid.NewString(), WorkflowExecutionMode: "draft_test"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.beginDebugExecution(id); err != nil {
		t.Fatal(err)
	}
	if err := c.endDebugExecution(id, time.Time{}, false, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := c.beginDebugExecution(id); err != nil {
		t.Fatal(err)
	}
	if err := c.endDebugExecution(id, time.Now(), true, 0); err != nil {
		t.Fatal(err)
	}
	if err := c.finishDebugTaskDelivery(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	s := c.debugState("/api/daemon/tasks/" + id + "/start")
	if !s.data.Unconfirmed || s.data.Receipt != nil {
		t.Fatal("later attempt erased missing proof")
	}
}
