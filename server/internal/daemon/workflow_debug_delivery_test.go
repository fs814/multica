package daemon

import (
	"context"
	"encoding/json"
	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/workflow"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestDebugIgnoredStartNeverAuthorizesProcessLaunch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ignored_terminal"}`))
	}))
	defer server.Close()
	client := NewClient(server.URL)
	client.configureDebugDelivery(t.TempDir())
	task := Task{ID: uuid.NewString(), WorkflowExecutionID: uuid.NewString(), WorkflowExecutionMode: "draft_test"}
	if err := client.registerDebugTask(&task); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := client.StartTask(context.Background(), task.ID); err == nil {
			t.Fatal("terminal start response allowed runner to launch")
		}
	}
}

func TestDebugDeliverySurvivesRestartAndLostReceiptResponse(t *testing.T) {
	var mu sync.Mutex
	available := false
	messageCount := 0
	receiptCalls := 0
	receiptID := ""
	claimID := uuid.NewString()
	taskID := uuid.NewString()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Header.Get(workflow.DebugExecutionHeader) != claimID {
			t.Error("claim header missing")
		}
		if !available {
			http.Error(w, "offline", 503)
			return
		}
		switch r.URL.Path {
		case "/api/daemon/tasks/" + taskID + "/messages":
			messageCount++
		case "/api/daemon/tasks/" + taskID + "/cancel-ack":
			if messageCount != 1 {
				t.Error("ack preceded log acknowledgement")
			}
		case "/api/daemon/tasks/" + taskID + "/execution-receipts":
			var receipt workflow.DebugExecutionReceipt
			if json.NewDecoder(r.Body).Decode(&receipt) != nil {
				t.Error("bad receipt")
			}
			if receipt.FinalMessageSeq != 1 || !receipt.ProcessStopped || !receipt.DeliveryDrained {
				t.Error("incomplete receipt")
			}
			receiptCalls++
			if receiptID == "" {
				receiptID = receipt.ReceiptID
			} else if receiptID != receipt.ReceiptID {
				t.Error("retry minted a new receipt")
			}
			if receiptCalls == 1 {
				http.Error(w, "response lost after commit", 503)
				return
			}
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		w.WriteHeader(200)
	}))
	defer server.Close()
	root := t.TempDir()
	client := NewClient(server.URL)
	client.configureDebugDelivery(root)
	if err := client.registerDebugTask(&Task{ID: taskID, WorkflowExecutionID: claimID, WorkflowExecutionMode: "draft_test"}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := client.ReportTaskMessages(ctx, taskID, []TaskMessageData{{Seq: 1, Type: "text", Content: "final controlled log", CreatedAt: time.Now()}}); err == nil {
		t.Fatal("offline delivery should remain pending")
	}
	// Direct post avoids the ordinary six-minute terminal retry schedule while
	// exercising the same journal insertion and HTTP transport used by empty ack.
	if err := client.postJSON(ctx, "/api/daemon/tasks/"+taskID+"/cancel-ack", map[string]any{}, nil); err == nil {
		t.Fatal("offline ack unexpectedly succeeded")
	}
	if _, err := client.beginDebugExecution(taskID); err != nil {
		t.Fatal(err)
	}
	if err := client.endDebugExecution(taskID, time.Now().UTC(), true, 1); err != nil {
		t.Fatal(err)
	}
	if err := client.finishDebugTaskDelivery(ctx, taskID); err == nil {
		t.Fatal("offline receipt unexpectedly succeeded")
	}
	mu.Lock()
	available = true
	mu.Unlock()
	resumed := NewClient(server.URL)
	resumed.configureDebugDelivery(root)
	if err := resumed.ReplayDebugDeliveries(ctx); err == nil {
		t.Fatal("lost receipt response should remain pending")
	}
	if err := resumed.ReplayDebugDeliveries(ctx); err != nil {
		t.Fatal(err)
	}
	if err := resumed.ReplayDebugDeliveries(ctx); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if receiptCalls != 2 || messageCount != 1 {
		t.Fatalf("duplicate delivery: receipts=%d messages=%d", receiptCalls, messageCount)
	}
	state := resumed.debugState("/api/daemon/tasks/" + taskID + "/messages")
	if state == nil || !state.data.Sealed || len(state.data.Records) != 0 {
		t.Fatal("completed journal retained payload")
	}
}
func TestDebugDeliveryDoesNotInventStopAfterCrash(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/api/daemon/tasks/11111111-1111-4111-8111-111111111111/messages" {
			t.Error("invented terminal report")
		}
		w.WriteHeader(200)
	}))
	defer server.Close()
	root := t.TempDir()
	client := NewClient(server.URL)
	client.configureDebugDelivery(root)
	task := &Task{ID: "11111111-1111-4111-8111-111111111111", WorkflowExecutionID: uuid.NewString(), WorkflowExecutionMode: "draft_test"}
	if err := client.registerDebugTask(task); err != nil {
		t.Fatal(err)
	}
	if err := client.ReportTaskMessages(context.Background(), task.ID, []TaskMessageData{{Seq: 1, Type: "text"}}); err != nil {
		t.Fatal(err)
	}
	resumed := NewClient(server.URL)
	resumed.configureDebugDelivery(root)
	if err := resumed.ReplayDebugDeliveries(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("replayed acknowledged log or fabricated receipt")
	}
}
