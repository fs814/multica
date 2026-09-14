package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/workflow"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func setupDebugHTTPMatrix(t *testing.T) (*workflow.Engine, *testutil.Fixture) {
	t.Helper()
	cleanupWorkflowTemplates(t)
	cleanupWorkflowRuns(t)
	engine := withWorkflowEngineForTest(t)
	engine.DebugReady = true
	engine.ResolveDraftEnvironment = testHandler.ResolveDraftEnvironment
	engine.RevalidateDebugEnvironment = testHandler.RevalidateDebugEnvironment
	fx := testutil.New(testPool, testWorkspaceID, testUserID)
	for _, table := range []string{"workflow_execution_snapshot", "workflow_debug_quota", "workflow_debug_policy", "workflow_debug_task_execution", "workflow_debug_stop_request", "workflow_debug_cleanup_object", "workflow_debug_upload"} {
		fx.Cleanup(t, "DELETE FROM "+table+" WHERE workspace_id=$1", testWorkspaceID)
	}
	fx.InsertNoID(t, "workflow_debug_policy", testutil.Cols{"workspace_id": testWorkspaceID, "enabled": true}, "workspace_id=$1", testWorkspaceID)
	return engine, fx
}
func debugMatrixGraph() *workflow.Definition {
	return &workflow.Definition{SchemaVersion: 2, EntryNode: "gate", Nodes: []workflow.Node{{Key: "gate", Type: workflow.NodeTypeAcceptance, Next: []string{"end"}, NextIDs: []string{"ge"}, AcceptanceCriteria: []string{"controlled"}}, {Key: "end", Type: workflow.NodeTypeEnd}}}
}
func debugMatrixTemplate(t *testing.T, graph *workflow.Definition) (WorkflowTemplateDetailResponse, map[string]any) {
	t.Helper()
	var tpl WorkflowTemplateDetailResponse
	testutil.Call(t, testHandler.CreateWorkflowTemplate, newRequest("POST", "/", map[string]any{"key": "matrix-" + uuid.NewString(), "name": "Matrix", "definition": graph})).Want(201).JSON(&tpl)
	return tpl, map[string]any{"schema_version": "1", "expected_revision": tpl.Revision, "expected_debug_policy_revision": 1, "base_draft_version_id": tpl.Versions[0].ID, "definition": graph, "input": map[string]string{"title": "Trial", "description": "Controlled matrix"}, "idempotency_key": uuid.NewString(), "execution_acknowledged": true}
}
func debugMatrixID(value string) pgtype.UUID { var id pgtype.UUID; _ = id.Scan(value); return id }
func TestWorkflowDraftTrialHTTPPermissionsAndResources(t *testing.T) {
	engine, fx := setupDebugHTTPMatrix(t)
	member := fx.Insert(t, "user", testutil.Cols{"name": "Trial member", "email": uuid.NewString() + "@example.test"})
	fx.Insert(t, "member", testutil.Cols{"workspace_id": testWorkspaceID, "user_id": member, "role": "member"})
	admin := fx.Insert(t, "user", testutil.Cols{"name": "Trial admin", "email": uuid.NewString() + "@example.test"})
	fx.Insert(t, "member", testutil.Cols{"workspace_id": testWorkspaceID, "user_id": admin, "role": "admin"})
	outsider := fx.Insert(t, "user", testutil.Cols{"name": "Outsider", "email": uuid.NewString() + "@example.test"})
	foreign := fx.Insert(t, "workspace", testutil.Cols{"name": "Foreign", "slug": "foreign-" + uuid.NewString()})
	fx.Insert(t, "member", testutil.Cols{"workspace_id": foreign, "user_id": testUserID, "role": "owner"})
	project := fx.Insert(t, "project", testutil.Cols{"workspace_id": foreign, "title": "Foreign project"})
	image := fx.Insert(t, "attachment", testutil.Cols{"workspace_id": foreign, "uploader_type": "member", "uploader_id": testUserID, "filename": "foreign.png", "content_type": "image/png", "size_bytes": 10, "url": "https://controlled.invalid/foreign.png"})
	tpl, body := debugMatrixTemplate(t, debugMatrixGraph())
	req := func(method, id, user string, value any) *http.Request {
		r := withURLParam(newRequest(method, "/", value), "id", id)
		r.Header.Set("X-User-ID", user)
		return r
	}
	var result struct {
		Run WorkflowRunDetailResponse `json:"run"`
	}
	for _, tc := range []struct {
		user   string
		status int
	}{{outsider, 404}, {member, 403}, {admin, 201}, {testUserID, 201}} {
		body["idempotency_key"] = uuid.NewString()
		testutil.Call(t, testHandler.StartWorkflowTestRun, req("POST", tpl.ID, tc.user, body)).Want(tc.status)
	}
	// A new owner run is created after freeing the active quota used above.
	fx.Exec(t, "UPDATE workflow_run SET status='cancelled',completed_at=now() WHERE workspace_id=$1", testWorkspaceID)
	body["idempotency_key"] = uuid.NewString()
	testutil.Call(t, testHandler.StartWorkflowTestRun, req("POST", tpl.ID, testUserID, body)).Want(201).JSON(&result)
	id := result.Run.ID
	for _, fn := range []http.HandlerFunc{testHandler.GetWorkflowTestRun, testHandler.GetWorkflowTestRunDefinition} {
		testutil.Call(t, fn, req("GET", id, member, nil)).Want(200)
		testutil.Call(t, fn, req("GET", id, outsider, nil)).Want(404)
		r := req("GET", id, testUserID, nil)
		r.Header.Set("X-Workspace-ID", foreign)
		testutil.Call(t, fn, r).Want(404)
	}
	testutil.Call(t, testHandler.GetWorkflowTestSettings, req("GET", id, member, nil)).Want(403)
	testutil.Call(t, testHandler.UpdateWorkflowTestSettings, req("PATCH", id, member, map[string]any{})).Want(403)
	testutil.Call(t, testHandler.DecideWorkflowTestAcceptance, req("POST", id, member, map[string]bool{"accept": true})).Want(200)
	body["idempotency_key"] = uuid.NewString()
	testutil.Call(t, testHandler.StartWorkflowTestRun, req("POST", tpl.ID, testUserID, body)).Want(201).JSON(&result)
	engine.DebugReady = false
	testutil.Call(t, testHandler.CancelWorkflowTestRun, req("POST", result.Run.ID, member, nil)).Want(200)
	testutil.Call(t, testHandler.GetWorkflowTestRun, req("GET", result.Run.ID, member, nil)).Want(200)
	engine.DebugReady = true
	for _, field := range []string{"source", "callback", "execution_mode", "accountable_user_id"} {
		body[field] = "forged"
		testutil.Call(t, testHandler.StartWorkflowTestRun, req("POST", tpl.ID, testUserID, body)).Want(400)
		delete(body, field)
	}
	body["idempotency_key"] = uuid.NewString()
	body["project_id"] = project
	testutil.Call(t, testHandler.StartWorkflowTestRun, req("POST", tpl.ID, testUserID, body)).Want(404)
	delete(body, "project_id")
	r := req("POST", tpl.ID, testUserID, body)
	r.Header.Set("X-Workspace-ID", foreign)
	// Admission first requires the target workspace to opt in.
	fx.InsertNoID(t, "workflow_debug_policy", testutil.Cols{"workspace_id": foreign, "enabled": true}, "workspace_id=$1", foreign)
	fx.Cleanup(t, "DELETE FROM workflow_debug_quota WHERE workspace_id=$1", foreign)
	testutil.Call(t, testHandler.StartWorkflowTestRun, r).Want(404)
	d := &workflow.Definition{SchemaVersion: 1, EntryNode: "input", Nodes: []workflow.Node{{Key: "input", Type: workflow.NodeTypeInput, InputMode: workflow.InputModeImage, Next: []string{"end"}}, {Key: "end", Type: workflow.NodeTypeEnd}}}
	imageTpl, imageBody := debugMatrixTemplate(t, debugMatrixGraph())
	d.Nodes[0].ImageAttachmentID = image
	imageBody["definition"] = d
	imageBody["image_attachment_id"] = image
	testutil.Call(t, testHandler.StartWorkflowTestRun, req("POST", imageTpl.ID, testUserID, imageBody)).Want(422)
}

func TestWorkflowDraftTrialAttachmentLookupFailureRetainsProxy(t *testing.T) {
	tx, err := testPool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	h := *testHandler
	h.Queries = db.New(tx)
	h.Storage = &mockStorage{}
	a := db.Attachment{ID: debugMatrixID(uuid.NewString()), TaskID: debugMatrixID(uuid.NewString()), WorkspaceID: debugMatrixID(testWorkspaceID), Url: "https://cdn.example.com/private-trial.txt"}
	got := h.attachmentToResponse(a, attachmentURLModeSigned)
	if !strings.HasPrefix(got.URL, "/api/attachments/") || got.DownloadURL != got.URL || got.MarkdownURL != got.URL {
		t.Fatalf("failed lookup exposed object: %+v", got)
	}
	h.Queries = testHandler.Queries
	ordinary := h.attachmentToResponse(a, attachmentURLModeSigned)
	if ordinary.URL != a.Url || ordinary.MarkdownURL != a.Url {
		t.Fatal("ordinary missing trial row lost compatibility")
	}
}

type debugBlockingStorage struct {
	mockStorage
	entered, release chan struct{}
	fail             bool
}

func (s *debugBlockingStorage) Upload(ctx context.Context, key string, data []byte, contentType, filename string) (string, error) {
	close(s.entered)
	<-s.release
	if s.fail {
		return "", errors.New("controlled storage failure")
	}
	return s.mockStorage.Upload(ctx, key, data, contentType, filename)
}
func TestWorkflowDraftTrialUploadCancelFinalizeAndSharedCleanup(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprintf("storageFailure=%v", fail), func(t *testing.T) {
			engine, fx := setupDebugHTTPMatrix(t)
			agent := labelTestAgentWithCapabilities(t, "code_change")
			var priorMetadata []byte
			fx.QueryRow(t, "SELECT metadata FROM agent_runtime WHERE id=(SELECT runtime_id FROM agent WHERE id=$1)", agent).Scan(&priorMetadata)
			t.Cleanup(func() {
				_, _ = testPool.Exec(context.Background(), "UPDATE agent_runtime SET metadata=$2 WHERE id=(SELECT runtime_id FROM agent WHERE id=$1)", agent, priorMetadata)
			})
			fx.Exec(t, "UPDATE agent_runtime SET metadata=$2 WHERE id=(SELECT runtime_id FROM agent WHERE id=$1)", agent, []byte(`{"capabilities":["workflow_debug_stop_receipt_v1","workflow_debug_fixed_environment_v1"]}`))
			d := &workflow.Definition{SchemaVersion: 1, EntryNode: "work", Nodes: []workflow.Node{{Key: "work", Type: workflow.NodeTypeAgent, Routing: &workflow.Routing{Strategy: workflow.RoutingExplicit, AgentID: agent}, SubmissionSchema: "code_change", Next: []string{"end"}}, {Key: "end", Type: workflow.NodeTypeEnd}}}
			tpl, body := debugMatrixTemplate(t, d)
			raw, _ := json.Marshal(body)
			var in workflow.DraftTestRequest
			_ = json.Unmarshal(raw, &in)
			ws, user := debugMatrixID(testWorkspaceID), debugMatrixID(testUserID)
			ctx := context.Background()
			result, err := engine.StartDraftTest(ctx, ws, user, debugMatrixID(tpl.ID), in)
			if err != nil {
				t.Fatal(err)
			}
			tasks, err := engine.Queries.ListWorkflowDebugTasksForRun(ctx, db.ListWorkflowDebugTasksForRunParams{RunID: result.Run.ID, WorkspaceID: ws})
			if err != nil || len(tasks) != 1 {
				t.Fatalf("tasks %d %v status %s", len(tasks), err, result.Run.Status)
			}
			task := tasks[0]
			claim, err := engine.ClaimDebugTask(ctx, task.ID, task.RuntimeID, debugMatrixID(uuid.NewString()), func(context.Context, *db.Queries, db.AgentTaskQueue) error { return nil })
			if err != nil {
				t.Fatal(err)
			}
			store := &debugBlockingStorage{entered: make(chan struct{}), release: make(chan struct{}), fail: fail}
			h := *testHandler
			h.Storage = store
			engine.DeleteDebugObject = h.DeleteDebugObject
			var buf bytes.Buffer
			writer := multipart.NewWriter(&buf)
			part, _ := writer.CreateFormFile("file", "artifact.txt")
			_, _ = part.Write([]byte("controlled trial payload"))
			_ = writer.Close()
			req := httptest.NewRequest("POST", "/api/uploads", &buf)
			req.Header.Set("Content-Type", writer.FormDataContentType())
			req.Header.Set("X-Actor-Source", "task_token")
			req.Header.Set("X-Task-ID", uuidToString(task.ID))
			req.Header.Set("X-Agent-ID", agent)
			req.Header.Set("X-User-ID", testUserID)
			req.Header.Set("X-Workspace-ID", testWorkspaceID)
			req.Header.Set(workflow.DebugExecutionHeader, uuidToString(claim.ID))
			response := httptest.NewRecorder()
			done := make(chan struct{})
			go func() { defer close(done); h.UploadFile(response, req) }()
			select {
			case <-store.entered:
			case <-time.After(5 * time.Second):
				close(store.release)
				<-done
				t.Fatalf("upload not started: %d %s", response.Code, response.Body.String())
			}
			if _, err = engine.CancelRun(ctx, ws, result.Run.ID, user); err != nil {
				close(store.release)
				<-done
				t.Fatal(err)
			}
			fx.Exec(t, "UPDATE agent_task_queue SET status='cancelled',completed_at=now() WHERE id=$1", task.ID)
			fx.Exec(t, "UPDATE workflow_run SET debug_purge_after=now()-interval '1 second' WHERE id=$1", result.Run.ID)
			auth := func(context.Context, db.WorkflowDebugTaskExecution) error { return nil }
			receipt := workflow.DebugExecutionReceipt{SchemaVersion: "1", ReceiptID: uuid.NewString(), Kind: "cancel_ack", ProcessStopped: true, DeliveryDrained: true, ProcessStoppedAt: time.Now()}
			if err = engine.AcceptDebugExecutionReceipt(ctx, task.ID, claim.ID, auth, receipt); err == nil {
				t.Error("receipt accepted in-flight upload")
			}
			if err = engine.PurgeDebugRun(ctx, ws, result.Run.ID); err != nil {
				t.Error(err)
			}
			close(store.release)
			<-done
			want := 200
			if fail {
				want = 503
			}
			if response.Code != want {
				t.Fatalf("upload finalize %d: %s", response.Code, response.Body.String())
			}
			var state string
			var attID pgtype.UUID
			if err = testPool.QueryRow(ctx, "SELECT state,attachment_id FROM workflow_debug_upload WHERE claim_id=$1", claim.ID).Scan(&state, &attID); err != nil {
				t.Fatal(err)
			}
			expected := "finalized"
			if fail {
				expected = "aborted"
			}
			if state != expected {
				t.Fatalf("state %s", state)
			}
			// Shared business attachment is unlinked; exclusive upload is erased.
			issue := fx.Issue(t, "Shared artifact owner")
			shared := fx.Insert(t, "attachment", testutil.Cols{"workspace_id": ws, "task_id": task.ID, "issue_id": issue, "uploader_type": "member", "uploader_id": user, "filename": "shared.txt", "content_type": "text/plain", "size_bytes": 6, "url": store.ObjectURL("shared.txt")})
			_, _ = store.mockStorage.Upload(ctx, "shared.txt", []byte("shared"), "text/plain", "shared.txt")
			if err = engine.AcceptDebugExecutionReceipt(ctx, task.ID, claim.ID, auth, receipt); err != nil {
				t.Fatal(err)
			}
			if err = engine.PurgeDebugRun(ctx, ws, result.Run.ID); err != nil {
				t.Fatal(err)
			}
			var retainedTask pgtype.UUID
			if err = testPool.QueryRow(ctx, "SELECT task_id FROM attachment WHERE id=$1", shared).Scan(&retainedTask); err != nil || retainedTask.Valid {
				t.Fatal("shared attachment not detached", err)
			}
			store.mu.Lock()
			_, retained := store.files["shared.txt"]
			remaining := len(store.files)
			store.mu.Unlock()
			if !retained || remaining != 1 {
				t.Fatal("wrong object cleanup")
			}
			if _, err = engine.Queries.GetAttachment(ctx, db.GetAttachmentParams{ID: attID, WorkspaceID: ws}); !errors.Is(err, pgx.ErrNoRows) {
				t.Fatal("exclusive attachment retained", err)
			}
			late := httptest.NewRequest("POST", "/api/uploads", strings.NewReader("malformed body"))
			late.Header = req.Header.Clone()
			lateResponse := httptest.NewRecorder()
			h.UploadFile(lateResponse, late)
			if lateResponse.Code != 200 || !strings.Contains(lateResponse.Body.String(), "ignored_expired") {
				t.Fatalf("late upload %d %s", lateResponse.Code, lateResponse.Body.String())
			}
		})
	}
}

func TestWorkflowDraftTrialClaimRechecksRevokedAccess(t *testing.T) {
	for _, kind := range []string{"membership", "project", "resource", "agent"} {
		t.Run(kind, func(t *testing.T) {
			engine, fx := setupDebugHTTPMatrix(t)
			agent := labelTestAgentWithCapabilities(t, "code_change")
			var priorMetadata []byte
			fx.QueryRow(t, "SELECT metadata FROM agent_runtime WHERE id=(SELECT runtime_id FROM agent WHERE id=$1)", agent).Scan(&priorMetadata)
			t.Cleanup(func() {
				_, _ = testPool.Exec(context.Background(), "UPDATE agent_runtime SET metadata=$2 WHERE id=(SELECT runtime_id FROM agent WHERE id=$1)", agent, priorMetadata)
			})
			fx.Exec(t, "UPDATE agent_runtime SET metadata=$2 WHERE id=(SELECT runtime_id FROM agent WHERE id=$1)", agent, []byte(`{"capabilities":["workflow_debug_stop_receipt_v1","workflow_debug_fixed_environment_v1"]}`))
			project := fx.Insert(t, "project", testutil.Cols{"workspace_id": testWorkspaceID, "title": "Pinned project"})
			resource := fx.Insert(t, "project_resource", testutil.Cols{"project_id": project, "workspace_id": testWorkspaceID, "resource_type": "local_directory", "resource_ref": []byte(`{"path":"/controlled/trial","execution_mode":"worktree"}`)})
			d := &workflow.Definition{SchemaVersion: 1, EntryNode: "work", Nodes: []workflow.Node{{Key: "work", Type: workflow.NodeTypeAgent, Routing: &workflow.Routing{Strategy: workflow.RoutingExplicit, AgentID: agent}, SubmissionSchema: "code_change", Next: []string{"end"}}, {Key: "end", Type: workflow.NodeTypeEnd}}}
			tpl, body := debugMatrixTemplate(t, d)
			body["project_id"] = project
			raw, _ := json.Marshal(body)
			var in workflow.DraftTestRequest
			_ = json.Unmarshal(raw, &in)
			ws, user := debugMatrixID(testWorkspaceID), debugMatrixID(testUserID)
			ctx := context.Background()
			result, err := engine.StartDraftTest(ctx, ws, user, debugMatrixID(tpl.ID), in)
			if err != nil {
				t.Fatal(err)
			}
			tasks, err := engine.Queries.ListWorkflowDebugTasksForRun(ctx, db.ListWorkflowDebugTasksForRunParams{RunID: result.Run.ID, WorkspaceID: ws})
			if err != nil || len(tasks) != 1 {
				t.Fatalf("tasks %d %v", len(tasks), err)
			}
			task := tasks[0]
			snapshot, err := engine.Queries.GetWorkflowExecutionSnapshot(ctx, db.GetWorkflowExecutionSnapshotParams{ID: result.Run.ExecutionSnapshotID, WorkspaceID: ws})
			if err != nil {
				t.Fatal(err)
			}
			var pinned workflowDebugEnvironment
			_ = json.Unmarshal(snapshot.EnvironmentSnapshot, &pinned)
			if len(pinned.Resources) != 1 || !strings.Contains(string(pinned.Resources[0].ResourceRef), "worktree") {
				t.Fatal("mode not pinned")
			}
			switch kind {
			case "membership":
				fx.Exec(t, "DELETE FROM member WHERE workspace_id=$1 AND user_id=$2", ws, user)
				t.Cleanup(func() {
					_, _ = testPool.Exec(context.Background(), "INSERT INTO member(workspace_id,user_id,role) VALUES($1,$2,'owner')", ws, user)
				})
			case "project":
				fx.Exec(t, "DELETE FROM project WHERE id=$1", project)
			case "resource":
				fx.Exec(t, "DELETE FROM project_resource WHERE id=$1", resource)
			case "agent":
				fx.Exec(t, "UPDATE agent SET archived_at=now() WHERE id=$1", agent)
				t.Cleanup(func() {
					_, _ = testPool.Exec(context.Background(), "UPDATE agent SET archived_at=NULL WHERE id=$1", agent)
				})
			}
			grants := 0
			_, err = engine.ClaimDebugTask(ctx, task.ID, task.RuntimeID, debugMatrixID(uuid.NewString()), func(context.Context, *db.Queries, db.AgentTaskQueue) error { grants++; return nil })
			if err == nil || grants != 0 {
				t.Fatal("revoked access granted credentials")
			}
			claims, err := engine.Queries.ListWorkflowDebugExecutions(ctx, db.ListWorkflowDebugExecutionsParams{RunID: result.Run.ID, WorkspaceID: ws})
			if err != nil || len(claims) != 0 {
				t.Fatal("rejected claim persisted")
			}
		})
	}
}
