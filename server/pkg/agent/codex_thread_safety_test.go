package agent

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
)

func TestCodexThreadPublicationConcurrentWithNotifications(t *testing.T) {
	c, _, _ := newTestCodexClient(t)
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < 10000; i++ {
			c.setThreadID(fmt.Sprintf("thread-%d", i))
		}
	}()
	close(start)
	for i := 0; i < 10000; i++ {
		c.isNotificationFromOtherThread(map[string]any{"threadId": "another-thread"})
	}
	wg.Wait()
	c.setThreadID("main")
	if c.isNotificationFromOtherThread(map[string]any{"threadId": "main"}) {
		t.Fatal("main thread discarded")
	}
	if !c.isNotificationFromOtherThread(map[string]any{"threadId": "other"}) {
		t.Fatal("other thread accepted")
	}
	if c.isNotificationFromOtherThread(nil) {
		t.Fatal("notification without a thread must remain compatible")
	}
}

func TestCodexOutputPanicFailsOnlyItsOwnSession(t *testing.T) {
	c, _, _ := newTestCodexClient(t)
	c.notificationProtocol = "raw"
	c.setThreadID("main")
	pending := &pendingRPC{ch: make(chan rpcResult, 1)}
	c.pending[1] = pending
	c.onMessage = func(Message) { panic("malformed notification callback") }
	c.readOutput(strings.NewReader(`{"jsonrpc":"2.0","method":"turn/started","params":{"threadId":"main","turn":{"id":"turn-1"}}}` + "\n"))
	select {
	case <-c.processDone:
	default:
		t.Fatal("turn wait was not released")
	}
	if err := c.getProcessErr(); !errors.Is(err, errCodexProcessExited) || !strings.Contains(err.Error(), "notification handler panicked") {
		t.Fatalf("missing transport failure: %v", err)
	}
	select {
	case result := <-pending.ch:
		if result.err == nil {
			t.Fatal("RPC did not fail")
		}
	default:
		t.Fatal("pending RPC was left waiting")
	}
	healthy, _, _ := newTestCodexClient(t)
	healthy.notificationProtocol = "raw"
	healthy.setThreadID("other")
	completed := false
	healthy.onTurnDone = func(bool) { completed = true }
	healthy.handleLine(`{"jsonrpc":"2.0","method":"turn/completed","params":{"threadId":"other","turn":{"id":"turn-2","status":"completed"}}}`)
	if !completed {
		t.Fatal("another session could not finish")
	}
}
