package handler

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/multica-ai/multica/server/internal/workflow"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestReviewDebugAttachmentMetadataKeepsProxyContract(t *testing.T) {
	engine, fx := setupDebugHTTPMatrix(t)
	ctx := context.Background()
	agent := labelTestAgentWithCapabilities(t, "code_change")
	var metadata []byte
	fx.QueryRow(t, "SELECT metadata FROM agent_runtime WHERE id=(SELECT runtime_id FROM agent WHERE id=$1)", agent).Scan(&metadata)
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, "UPDATE agent_runtime SET metadata=$2 WHERE id=(SELECT runtime_id FROM agent WHERE id=$1)", agent, metadata)
	})
	fx.Exec(t, "UPDATE agent_runtime SET metadata=$2 WHERE id=(SELECT runtime_id FROM agent WHERE id=$1)", agent, []byte(`{"capabilities":["workflow_debug_stop_receipt_v1","workflow_debug_fixed_environment_v1"]}`))
	d := &workflow.Definition{SchemaVersion: 1, EntryNode: "work", Nodes: []workflow.Node{{Key: "work", Type: workflow.NodeTypeAgent, Routing: &workflow.Routing{Strategy: workflow.RoutingExplicit, AgentID: agent}, SubmissionSchema: "code_change", Next: []string{"end"}}, {Key: "end", Type: workflow.NodeTypeEnd}}}
	tpl, body := debugMatrixTemplate(t, d)
	raw, _ := json.Marshal(body)
	var in workflow.DraftTestRequest
	if err := json.Unmarshal(raw, &in); err != nil {
		t.Fatal(err)
	}
	ws, user := debugMatrixID(testWorkspaceID), debugMatrixID(testUserID)
	run, err := engine.StartDraftTest(ctx, ws, user, debugMatrixID(tpl.ID), in)
	if err != nil {
		t.Fatal(err)
	}
	tasks, err := engine.Queries.ListWorkflowDebugTasksForRun(ctx, db.ListWorkflowDebugTasksForRunParams{RunID: run.Run.ID, WorkspaceID: ws})
	if err != nil || len(tasks) != 1 {
		t.Fatalf("task fixture: %v %v", tasks, err)
	}
	id := seedAttachmentURL(t, "https://cdn.example.com/trial.txt", "trial.txt", "text/plain", 14)
	fx.Exec(t, "UPDATE attachment SET task_id=$2 WHERE id=$1", id, tasks[0].ID)
	store := &mockStorage{files: map[string][]byte{"trial.txt": []byte("trial artifact")}}
	type savedLinks struct {
		mode     string
		h        Handler
		response AttachmentResponse
	}
	var saved []savedLinks
	for _, mode := range []string{"cloudfront", "presign", "proxy"} {
		t.Run(mode+"/metadata", func(t *testing.T) {
			h := *testHandler
			h.Storage = store
			h.cfg.AttachmentDownloadMode = mode
			h.CFSigner = testCloudFrontSigner(t)
			w := httptest.NewRecorder()
			h.GetAttachmentByID(w, withURLParam(newRequest("GET", "/", nil), "id", id))
			var response AttachmentResponse
			if w.Code != 200 || !strings.Contains(w.Header().Get("Content-Type"), "application/json") || json.Unmarshal(w.Body.Bytes(), &response) != nil {
				t.Fatalf("metadata returned non-JSON: status=%d content-type=%s body=%q", w.Code, w.Header().Get("Content-Type"), w.Body.String())
			}
			for field, value := range map[string]string{"url": response.URL, "download_url": response.DownloadURL, "markdown_url": response.MarkdownURL, "attachment_download_url": response.AttachmentDownloadURL} {
				parsed, err := url.Parse(value)
				if err != nil || parsed.Host != "" || !strings.HasPrefix(parsed.Path, "/api/attachments/"+id+"/") || parsed.Query().Get("Signature") != "" || parsed.Query().Get("X-Amz-Signature") != "" {
					t.Fatalf("%s escaped tombstone-aware proxy: %q", field, value)
				}
			}
			saved = append(saved, savedLinks{mode, h, response})
		})
	}
	if len(saved) != 3 {
		t.Fatal("metadata matrix incomplete")
	}
	checkLinks := func(stage string, want int) {
		t.Helper()
		for _, s := range saved {
			t.Run(s.mode+"/"+stage, func(t *testing.T) {
				for field, value := range map[string]string{"url": s.response.URL, "download_url": s.response.DownloadURL, "markdown_url": s.response.MarkdownURL, "attachment_download_url": s.response.AttachmentDownloadURL, "content": "/api/attachments/" + id + "/content"} {
					w := httptest.NewRecorder()
					req := newRequest("GET", value, nil)
					if strings.Contains(value, "/signed-download") {
						req = httptest.NewRequest("GET", value, nil) // Native downloader: no headers or cookies.
					}
					req = withURLParam(req, "id", id)
					switch {
					case strings.Contains(value, "/signed-download"):
						s.h.DownloadAttachmentWithCapability(w, req)
					case strings.HasSuffix(value, "/content"):
						s.h.GetAttachmentContent(w, req)
					default:
						s.h.DownloadAttachment(w, req)
					}
					if w.Code != want {
						t.Errorf("%s status=%d want=%d body=%q", field, w.Code, want, w.Body.String())
					}
					if want == http.StatusOK && (w.Body.String() != "trial artifact" || w.Header().Get("Location") != "" || w.Header().Get("Cache-Control") != "no-store") {
						t.Errorf("%s did not proxy original body without caching", field)
					}
					if want == http.StatusOK && field == "attachment_download_url" && !strings.HasPrefix(w.Header().Get("Content-Disposition"), "attachment;") {
						t.Error("download intent lost")
					}
					if want != http.StatusOK && strings.Contains(w.Body.String(), "trial artifact") {
						t.Error("old link leaked purged payload")
					}
				}
			})
		}
	}
	checkLinks("before-purge", http.StatusOK)
	if _, err = engine.CancelRun(ctx, ws, run.Run.ID, user); err != nil {
		t.Fatal(err)
	}
	fx.Exec(t, "UPDATE workflow_run SET debug_purge_after=now()-interval '1 second' WHERE id=$1", run.Run.ID)
	engine.DeleteDebugObject = func(context.Context, db.WorkflowDebugCleanupObject) error {
		return errors.New("controlled object deletion failure")
	}
	if err = engine.PurgeDebugRun(ctx, ws, run.Run.ID); err != nil {
		t.Fatal(err)
	}
	purging, err := engine.Queries.GetWorkflowRun(ctx, db.GetWorkflowRunParams{ID: run.Run.ID, WorkspaceID: ws})
	if err != nil || !purging.DetailsPurgedAt.Valid || purging.PurgeCompletedAt.Valid {
		t.Fatalf("not at tombstone-before-object-delete boundary: %v", err)
	}
	checkLinks("tombstone-object-still-present", http.StatusNotFound)
	if _, ok := store.files["trial.txt"]; !ok {
		t.Fatal("negative link check ran only after object disappeared")
	}
	h := saved[0].h
	engine.DeleteDebugObject = h.DeleteDebugObject
	fx.Exec(t, "UPDATE workflow_debug_cleanup_object SET next_attempt_at=now() WHERE run_id=$1", run.Run.ID)
	if err = engine.PurgeDebugRun(ctx, ws, run.Run.ID); err != nil {
		t.Fatal(err)
	}
	purged, err := engine.Queries.GetWorkflowRun(ctx, db.GetWorkflowRunParams{ID: run.Run.ID, WorkspaceID: ws})
	if err != nil || !purged.PurgeCompletedAt.Valid {
		t.Fatalf("purge not completed: %v", err)
	}
	checkLinks("after-object-delete", http.StatusNotFound)
	if len(store.presignCalls) != 0 {
		t.Fatalf("trial minted storage signatures: %v", store.presignCalls)
	}
	if _, ok := store.files["trial.txt"]; ok {
		t.Fatal("cleanup did not remove object")
	}
}
