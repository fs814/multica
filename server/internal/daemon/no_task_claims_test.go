package daemon

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestNoTaskClaimsAllTransports(t *testing.T) {
	for _, disabled := range []bool{true, false} {
		name := "default"
		if disabled {
			name = "disabled"
		}
		t.Run(name, func(t *testing.T) {
			var httpClaims, wsClaims atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				httpClaims.Add(1)
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, `{"tasks":[],"task":null}`)
			}))
			defer server.Close()
			d := New(Config{ServerBaseURL: server.URL, WorkspacesRoot: t.TempDir(), NoTaskClaims: disabled}, slog.New(slog.NewTextHandler(io.Discard, nil)))
			ctx := context.Background()
			if _, err := d.client.ClaimTask(ctx, "runtime"); err != nil {
				t.Fatal(err)
			}
			if _, err := d.client.ClaimTasks(ctx, "daemon", []string{"runtime"}, 1); err != nil {
				t.Fatal(err)
			}
			if _, err := d.client.claimTasksLegacy(ctx, []string{"runtime"}, 1); err != nil {
				t.Fatal(err)
			}
			if _, err := d.client.claimDebugTask(ctx, "runtime"); err != nil {
				t.Fatal(err)
			}
			if _, err := d.ClaimTasksWSFirst(ctx, "daemon", []string{"runtime"}, 1); err != nil {
				t.Fatal(err)
			}
			generation := d.wsRPC.attach(func(frame []byte) (*wsOutbound, error) {
				wsClaims.Add(1)
				var message protocol.Message
				if err := json.Unmarshal(frame, &message); err != nil {
					t.Error(err)
				}
				var request protocol.RPCRequestPayload
				if err := json.Unmarshal(message.Payload, &request); err != nil {
					t.Error(err)
				}
				go d.wsRPC.deliver(protocol.RPCResponsePayload{RequestID: request.RequestID, Status: 200, Body: json.RawMessage(`{"tasks":[]}`)})
				return &wsOutbound{data: frame}, nil
			})
			d.wsRPC.markRPCV1Supported(generation)
			if _, err := d.ClaimTasksWSFirst(ctx, "daemon", []string{"runtime"}, 1); err != nil {
				t.Fatal(err)
			}
			if disabled {
				if httpClaims.Load() != 0 || wsClaims.Load() != 0 {
					t.Fatalf("claims escaped: http=%d ws=%d", httpClaims.Load(), wsClaims.Load())
				}
			} else if httpClaims.Load() < 5 || wsClaims.Load() != 1 {
				t.Fatalf("default transports not exercised: http=%d ws=%d", httpClaims.Load(), wsClaims.Load())
			}
		})
	}
}

func TestNoTaskClaimsPollLoopIgnoresWakeups(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); http.Error(w, "should not claim", 500) }))
	defer server.Close()
	d := New(Config{ServerBaseURL: server.URL, WorkspacesRoot: t.TempDir(), NoTaskClaims: true, PollInterval: time.Millisecond}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	wakeups := make(chan taskWakeup, 256)
	for i := 0; i < cap(wakeups); i++ {
		wakeups <- taskWakeup{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := d.pollLoop(ctx, wakeups); err != context.DeadlineExceeded {
		t.Fatalf("poll exit: %v", err)
	}
	if requests.Load() != 0 || d.activeTasks.Load() != 0 {
		t.Fatal("disabled poller made a request or started a task")
	}
}
