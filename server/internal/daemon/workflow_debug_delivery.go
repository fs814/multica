package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/workflow"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type debugDeliveryRecord struct {
	Path         string          `json:"path"`
	Body         json.RawMessage `json:"body,omitempty"`
	Hash         string          `json:"hash"`
	Acknowledged bool            `json:"acknowledged"`
	Disposition  string          `json:"disposition,omitempty"`
}
type debugDeliveryJournal struct {
	Attempts          int                             `json:"attempts"`
	Unconfirmed       bool                            `json:"unconfirmed"`
	StoppedAt         time.Time                       `json:"stopped_at"`
	LifecycleComplete bool                            `json:"lifecycle_complete"`
	TerminalKind      string                          `json:"terminal_kind,omitempty"`
	TaskID            string                          `json:"task_id"`
	ExecutionID       string                          `json:"execution_id"`
	Records           []debugDeliveryRecord           `json:"records,omitempty"`
	Receipt           *workflow.DebugExecutionReceipt `json:"receipt,omitempty"`
	FinalSeq          int32                           `json:"final_seq"`
	Sealed            bool                            `json:"sealed"`
}
type debugDeliveryState struct {
	mu   sync.Mutex
	file string
	data debugDeliveryJournal
}
type debugDeliveryContextKey struct{}

// persistDebugDelivery fsyncs both the replacement file and its directory. A
// terminal callback is never sent before its retry record is durable. No auth
// token is stored here; replay uses the daemon's current authenticated client.
func persistDebugDelivery(s *debugDeliveryState) error {
	raw, err := json.Marshal(s.data)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(s.file), ".delivery-")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(raw)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(name, s.file); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(s.file))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
func (c *Client) configureDebugDelivery(root string) {
	if root == "" {
		return
	}
	sum := sha256.Sum256([]byte(c.baseURL))
	c.debugRoot = filepath.Join(root, ".workflow-delivery", hex.EncodeToString(sum[:16]))
	c.debugIncarnation = uuid.NewString()
	c.debugDeliveries = make(map[string]*debugDeliveryState)
}
func (c *Client) loadDebugDelivery() error {
	c.debugMu.Lock()
	defer c.debugMu.Unlock()
	if c.debugRoot == "" {
		return fmt.Errorf("draft trial delivery storage is not configured")
	}
	if err := os.MkdirAll(c.debugRoot, 0700); err != nil {
		return err
	}
	files, err := filepath.Glob(filepath.Join(c.debugRoot, "*.json"))
	if err != nil {
		return err
	}
	for _, file := range files {
		taskID := strings.TrimSuffix(filepath.Base(file), ".json")
		if _, err := uuid.Parse(taskID); err != nil {
			return err
		}
		if c.debugDeliveries[taskID] != nil {
			continue
		}
		raw, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		var data debugDeliveryJournal
		if err = json.Unmarshal(raw, &data); err != nil {
			return err
		}
		if data.TaskID != taskID {
			return fmt.Errorf("draft trial journal identity mismatch")
		}
		if _, err = uuid.Parse(data.ExecutionID); err != nil {
			return err
		}
		for _, record := range data.Records {
			if !strings.HasPrefix(record.Path, "/api/daemon/tasks/"+taskID+"/") {
				return fmt.Errorf("draft trial journal route mismatch")
			}
		}
		// Journals written before lifecycle proofs must not replay inferred receipts.
		if !data.LifecycleComplete {
			data.Receipt = nil
			data.Sealed = false
		}
		c.debugDeliveries[taskID] = &debugDeliveryState{file: file, data: data}
	}
	return nil
}
func (c *Client) registerDebugTask(task *Task) error {
	if task == nil || task.WorkflowExecutionMode != "draft_test" {
		return nil
	}
	if _, err := uuid.Parse(task.WorkflowExecutionID); err != nil {
		return err
	}
	if _, err := uuid.Parse(task.ID); err != nil {
		return err
	}
	if err := c.loadDebugDelivery(); err != nil {
		return err
	}
	c.debugMu.Lock()
	defer c.debugMu.Unlock()
	if old := c.debugDeliveries[task.ID]; old != nil {
		if old.data.ExecutionID != task.WorkflowExecutionID {
			return fmt.Errorf("draft trial claim changed")
		}
		return nil
	}
	state := &debugDeliveryState{file: filepath.Join(c.debugRoot, task.ID+".json"), data: debugDeliveryJournal{TaskID: task.ID, ExecutionID: task.WorkflowExecutionID}}
	if err := persistDebugDelivery(state); err != nil {
		return err
	}
	c.debugDeliveries[task.ID] = state
	return nil
}
func (c *Client) debugState(path string) *debugDeliveryState {
	if c == nil {
		return nil
	}
	parts := strings.Split(path, "/")
	if len(parts) < 6 || parts[1] != "api" || parts[2] != "daemon" || parts[3] != "tasks" {
		return nil
	}
	c.debugMu.Lock()
	defer c.debugMu.Unlock()
	return c.debugDeliveries[parts[4]]
}
func (c *Client) deliverDebugRequest(ctx context.Context, httpClient *http.Client, path string, body any, response any, stats *TransferStats) (bool, error) {
	if ctx.Value(debugDeliveryContextKey{}) != nil {
		return false, nil
	}
	state := c.debugState(path)
	if state == nil {
		return false, nil
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.data.Sealed {
		if strings.HasSuffix(path, "/start") {
			return true, fmt.Errorf("draft trial execution is closed")
		}
		return true, nil
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return true, err
	}
	sum := sha256.Sum256(append([]byte(path), raw...))
	hash := hex.EncodeToString(sum[:])
	found := false
	for _, record := range state.data.Records {
		if record.Hash == hash {
			found = true
			break
		}
	}
	if !found {
		state.data.Records = append(state.data.Records, debugDeliveryRecord{Path: path, Body: raw, Hash: hash})
		if strings.HasSuffix(path, "/messages") {
			var payload struct {
				Messages []TaskMessageData `json:"messages"`
			}
			if err = json.Unmarshal(raw, &payload); err != nil {
				return true, err
			}
			for _, message := range payload.Messages {
				if int32(message.Seq) > state.data.FinalSeq {
					state.data.FinalSeq = int32(message.Seq)
				}
			}
		}
		kind := ""
		if strings.HasSuffix(path, "/complete") {
			kind = "complete"
		}
		if strings.HasSuffix(path, "/fail") {
			kind = "fail"
		}
		if strings.HasSuffix(path, "/cancel-ack") {
			kind = "cancel_ack"
		}
		if kind != "" {
			state.data.TerminalKind = kind
		}

		if err = persistDebugDelivery(state); err != nil {
			return true, err
		}
	}
	if err = c.flushDebugDelivery(ctx, httpClient, state); err != nil {
		return true, err
	}
	if strings.HasSuffix(path, "/start") {
		for _, record := range state.data.Records {
			if record.Hash == hash && record.Disposition != "accepted" {
				return true, fmt.Errorf("draft trial start was not accepted")
			}
		}
	}
	return true, nil
}
func (c *Client) flushDebugDelivery(ctx context.Context, httpClient *http.Client, state *debugDeliveryState) error {
	ctx = context.WithValue(ctx, debugDeliveryContextKey{}, state.data.ExecutionID)
	for i := range state.data.Records {
		record := &state.data.Records[i]
		if record.Acknowledged {
			continue
		}
		var result struct {
			Status string `json:"status"`
		}
		var response any
		if strings.HasSuffix(record.Path, "/start") {
			response = &result
		}
		if err := c.postJSONViaObserved(ctx, httpClient, record.Path, record.Body, response, nil); err != nil {
			return err
		}
		record.Disposition = result.Status
		record.Acknowledged = true
		record.Body = nil
		if err := persistDebugDelivery(state); err != nil {
			return err
		}
	}
	if state.data.Receipt != nil && !state.data.Sealed {
		state.data.Receipt.FinalMessageSeq = state.data.FinalSeq
		if err := persistDebugDelivery(state); err != nil {
			return err
		}
		if err := c.postJSONViaObserved(ctx, httpClient, "/api/daemon/tasks/"+state.data.TaskID+"/execution-receipts", state.data.Receipt, nil, nil); err != nil {
			return err
		}
		state.data.Sealed = true
		state.data.Records = nil
		return persistDebugDelivery(state)
	}
	return nil
}
func (c *Client) ReplayDebugDeliveries(ctx context.Context) error {
	if c.debugRoot == "" {
		return nil
	}
	if err := c.loadDebugDelivery(); err != nil {
		return err
	}
	c.debugMu.Lock()
	states := make([]*debugDeliveryState, 0, len(c.debugDeliveries))
	for _, s := range c.debugDeliveries {
		states = append(states, s)
	}
	c.debugMu.Unlock()
	var first error
	for _, state := range states {
		state.mu.Lock()
		if !state.data.Sealed {
			if err := c.flushDebugDelivery(ctx, c.client, state); err != nil && first == nil {
				first = err
			}
		}
		state.mu.Unlock()
	}
	return first
}
func (c *Client) claimDebugTask(ctx context.Context, runtimeID string) (*Task, error) {
	if c.debugRoot == "" {
		return nil, nil
	}
	if err := c.loadDebugDelivery(); err != nil {
		return nil, err
	}
	var response struct {
		Task *Task `json:"task"`
	}
	err := c.postJSON(ctx, "/api/daemon/runtimes/"+runtimeID+"/workflow-test-tasks/claim", map[string]string{"daemon_incarnation_id": c.debugIncarnation}, &response)
	if err != nil {
		if request, ok := err.(*requestError); ok && (request.StatusCode == 404 || request.StatusCode == 503) {
			return nil, nil
		}
		return nil, err
	}
	if err = c.registerDebugTask(response.Task); err != nil {
		return nil, err
	}
	return response.Task, nil
}

// beginDebugExecution and endDebugExecution are lifecycle boundaries, never
// transport callbacks. Every attempt must be proved stopped before a receipt.
func (c *Client) beginDebugExecution(taskID string) (bool, error) {
	s := c.debugState("/api/daemon/tasks/" + taskID + "/start")
	if s == nil {
		return false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data.Attempts > 0 && s.data.StoppedAt.IsZero() {
		s.data.Unconfirmed = true
	}
	s.data.Attempts++
	s.data.StoppedAt = time.Time{}
	s.data.LifecycleComplete = false
	return true, persistDebugDelivery(s)
}
func (c *Client) endDebugExecution(taskID string, stopped time.Time, drained bool, finalSeq int32) error {
	s := c.debugState("/api/daemon/tasks/" + taskID + "/start")
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if stopped.IsZero() || !drained || s.data.FinalSeq != finalSeq {
		s.data.Unconfirmed = true
	} else {
		s.data.StoppedAt = stopped
	}
	return persistDebugDelivery(s)
}

// Called only after the task runner, usage, finalization and terminal reporting
// have finished. Queue acknowledgments alone can never attest physical exit.
func (c *Client) finishDebugTaskDelivery(ctx context.Context, taskID string) error {
	s := c.debugState("/api/daemon/tasks/" + taskID + "/start")
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data.Sealed || s.data.Unconfirmed {
		return nil
	}
	if s.data.Attempts == 0 {
		s.data.StoppedAt = time.Now().UTC()
	} // lifecycle ended before launching any backend
	if s.data.StoppedAt.IsZero() || s.data.TerminalKind == "" {
		return nil
	}
	s.data.LifecycleComplete = true
	if s.data.Receipt == nil {
		s.data.Receipt = &workflow.DebugExecutionReceipt{SchemaVersion: "1", ReceiptID: uuid.NewString(), Kind: s.data.TerminalKind, ProcessStopped: true, DeliveryDrained: true, ProcessStoppedAt: s.data.StoppedAt}
	}
	if err := persistDebugDelivery(s); err != nil {
		return err
	}
	return c.flushDebugDelivery(ctx, c.client, s)
}
