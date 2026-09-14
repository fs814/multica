package handler

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/testutil"
)

// Pause at transaction entry to reproduce a save landing after the caller's
// initial read. Legacy publish used to validate before this boundary.
type publishBarrier struct{ reached, release chan struct{} }

func (b *publishBarrier) Begin(ctx context.Context) (pgx.Tx, error) {
	close(b.reached)
	select {
	case <-b.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return testPool.Begin(ctx)
}

func TestWorkflowPublishRejectsInterleavedSave(t *testing.T) {
	for _, guarded := range []bool{true, false} {
		name := "legacy"
		if guarded {
			name = "guarded"
		}
		t.Run(name, func(t *testing.T) {
			cleanupWorkflowTemplates(t)
			created := createWorkflowTemplateForTest(t, "publish_barrier_"+name)
			barrier := &publishBarrier{make(chan struct{}), make(chan struct{})}
			h := *testHandler
			h.TxStarter = barrier
			var once sync.Once
			release := func() { once.Do(func() { close(barrier.release) }) }
			defer release()
			var body any
			want := http.StatusUnprocessableEntity
			if guarded {
				body = map[string]any{"revision": created.Revision, "draft_version_id": created.Versions[0].ID}
				want = http.StatusConflict
			}
			req := withURLParam(newRequest("POST", "/api/workflow-templates/"+created.ID+"/publish", body), "id", created.ID)
			done := make(chan struct{})
			go func() { defer close(done); testutil.Call(t, h.PublishWorkflowTemplate, req).Want(want) }()
			select {
			case <-barrier.reached:
			case <-time.After(5 * time.Second):
				t.Fatal("publish did not reach transaction")
			}
			incomplete := map[string]any{"schema_version": 2, "entry_node": "input", "nodes": []map[string]any{{"key": "input", "type": "input"}}}
			save := withURLParam(newRequest("PATCH", "/api/workflow-templates/"+created.ID, map[string]any{"revision": created.Revision, "definition": incomplete}), "id", created.ID)
			testutil.Call(t, testHandler.UpdateWorkflowTemplate, save).Want(http.StatusOK)
			release()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("publish did not finish")
			}
			var count int
			if err := testPool.QueryRow(context.Background(), "SELECT count(*) FROM workflow_template_version WHERE template_id=$1 AND status='published'", created.ID).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatal("published an unconfirmed/invalid draft")
			}
		})
	}
}

func TestWorkflowPublishPreconditions(t *testing.T) {
	cleanupWorkflowTemplates(t)
	created := createWorkflowTemplateForTest(t, "publish_preconditions")
	cases := []struct {
		name string
		body map[string]any
		want int
	}{
		{"partial", map[string]any{"revision": created.Revision}, http.StatusBadRequest},
		{"stale revision", map[string]any{"revision": created.Revision + 1, "draft_version_id": created.Versions[0].ID}, http.StatusConflict},
		{"wrong draft", map[string]any{"revision": created.Revision, "draft_version_id": created.ID}, http.StatusConflict},
		{"confirmed", map[string]any{"revision": created.Revision, "draft_version_id": created.Versions[0].ID}, http.StatusOK},
		{"already published", map[string]any{"revision": created.Revision, "draft_version_id": created.Versions[0].ID}, http.StatusConflict},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := withURLParam(newRequest("POST", "/api/workflow-templates/"+created.ID+"/publish", tc.body), "id", created.ID)
			testutil.Call(t, testHandler.PublishWorkflowTemplate, req).Want(tc.want)
		})
	}
}

func TestWorkflowPublishTwoWindows(t *testing.T) {
	cleanupWorkflowTemplates(t)
	created := createWorkflowTemplateForTest(t, "publish_two_windows")
	start := make(chan struct{})
	results := make(chan int, 2)
	for range 2 {
		go func() {
			<-start
			req := withURLParam(newRequest("POST", "/api/workflow-templates/"+created.ID+"/publish", map[string]any{
				"revision": created.Revision, "draft_version_id": created.Versions[0].ID,
			}), "id", created.ID)
			results <- testutil.Call(t, testHandler.PublishWorkflowTemplate, req).Code
		}()
	}
	close(start)
	codes := map[int]int{}
	for range 2 {
		select {
		case code := <-results:
			codes[code]++
		case <-time.After(5 * time.Second):
			t.Fatal("concurrent publish did not complete")
		}
	}
	if codes[http.StatusOK] != 1 || codes[http.StatusConflict] != 1 {
		t.Fatalf("expected one publication and one conflict, got %v", codes)
	}
}
