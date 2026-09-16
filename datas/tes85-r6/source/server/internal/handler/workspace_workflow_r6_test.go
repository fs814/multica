package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/workflow"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

var r6WorkflowTables = []string{"workflow_template", "workflow_template_version", "workflow_run", "workflow_step_instance", "workflow_submission", "workflow_acceptance", "workflow_event"}

func r6Workspace(t *testing.T) string {
	t.Helper()
	id := dbfx.Workspace(t, "Workflow teardown", "r6-"+uuid.NewString())
	dbfx.InsertNoID(t, "member", testutil.Cols{"workspace_id": id, "user_id": testUserID, "role": "owner"}, "workspace_id=$1", id)
	return id
}

func r6SeedWorkflowRows(t *testing.T, ws string) {
	t.Helper()
	tpl := dbfx.Insert(t, "workflow_template", testutil.Cols{"workspace_id": ws, "key": "r6", "name": "Teardown", "created_by_type": "member", "created_by_id": testUserID})
	ver := dbfx.Insert(t, "workflow_template_version", testutil.Cols{"workspace_id": ws, "template_id": tpl, "version": 1, "definition": testutil.Raw("'{}'::jsonb")})
	run := dbfx.Insert(t, "workflow_run", testutil.Cols{"workspace_id": ws, "template_id": tpl, "template_version_id": ver, "source": "manual", "idempotency_key": "r6"})
	step := dbfx.Insert(t, "workflow_step_instance", testutil.Cols{"workspace_id": ws, "run_id": run, "node_key": "analyze", "node_type": "agent", "trace_position": 1})
	dbfx.Insert(t, "workflow_submission", testutil.Cols{"workspace_id": ws, "run_id": run, "step_id": step, "verdict": "pass"})
	dbfx.Insert(t, "workflow_acceptance", testutil.Cols{"workspace_id": ws, "run_id": run, "step_id": step})
	dbfx.Insert(t, "workflow_event", testutil.Cols{"workspace_id": ws, "run_id": run, "event_type": "run.created", "idempotency_key": "r6", "actor_type": "member"})
}

func TestR6WorkspaceDeleteRemovesSevenWorkflowTablesAndPreservesNeighbor(t *testing.T) {
	target, neighbor := r6Workspace(t), r6Workspace(t)
	r6SeedWorkflowRows(t, target)
	r6SeedWorkflowRows(t, neighbor)
	testutil.Call(t, testHandler.DeleteWorkspace, withURLParam(newRequest("DELETE", "/api/workspaces/"+target, nil), "id", target)).Want(204)
	for _, table := range r6WorkflowTables {
		for ws, want := range map[string]int{target: 0, neighbor: 1} {
			if got := dbfx.Count(t, "SELECT count(*) FROM "+table+" WHERE workspace_id=$1", ws); got != want {
				t.Errorf("%s workspace %s: got %d want %d", table, ws, got, want)
			}
		}
	}
}

// Exercise the real handler against the exact lock held by DeleteWorkspace.
// Commit deletion while the creator is waiting: it must recheck existence,
// return 404, and leave no template/version behind after the sweep.
func TestR6TemplateCreateLosesToWorkspaceDeletion(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ws := r6Workspace(t)
	tx, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err := db.New(tx).LockWorkspaceForDelete(ctx, parseUUID(ws)); err != nil {
		t.Fatal(err)
	}
	req := newRequest("POST", "/api/workflow-templates?workspace_id="+ws, map[string]any{"key": "r6-create", "name": "Race", "definition": builtinBugFixDefinition(t)})
	req.Header.Set("X-Workspace-ID", ws)
	requestCtx, requestCancel := context.WithTimeout(req.Context(), 10*time.Second)
	defer requestCancel()
	req = req.WithContext(requestCtx)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { defer close(done); testHandler.CreateWorkflowTemplate(rec, req) }()
	if !waitForBlockedBackend(t, done) {
		t.Fatal("template create did not wait for workspace deletion")
	}
	if _, err := tx.Exec(ctx, "DELETE FROM member WHERE workspace_id=$1", ws); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, "DELETE FROM workspace WHERE id=$1", ws); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if rec.Code != http.StatusNotFound {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}
	for _, table := range []string{"workflow_template", "workflow_template_version"} {
		if n := dbfx.Count(t, "SELECT count(*) FROM "+table+" WHERE workspace_id=$1", ws); n != 0 {
			t.Fatalf("orphaned %s: %d", table, n)
		}
	}
}

// The workflow engine must wait at the workspace lock before it touches an
// existing issue/run. This also covers the terminal callback/reconciler path.
func TestR6EngineCommandsWaitForWorkspaceDeleteFence(t *testing.T) {
	detail, engine := r5ActiveRun(t)
	for _, command := range []string{"cancel", "reconcile", "start"} {
		t.Run(command, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			tx, err := testPool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(context.Background())
			if _, err := db.New(tx).LockWorkspaceForDelete(ctx, parseUUID(testWorkspaceID)); err != nil {
				t.Fatal(err)
			}
			done := make(chan struct{})
			outcome := make(chan error, 1)
			go func() {
				defer close(done)
				var err error
				switch command {
				case "cancel":
					_, err = engine.CancelRun(ctx, parseUUID(testWorkspaceID), parseUUID(detail.ID), parseUUID(testUserID))
				case "reconcile":
					err = engine.ReconcileRun(ctx, parseUUID(testWorkspaceID), parseUUID(detail.ID))
				case "start":
					_, err = engine.StartRun(ctx, workflow.StartRunInput{WorkspaceID: parseUUID(testWorkspaceID), TemplateID: parseUUID(detail.TemplateID), IdempotencyKey: "r6-start", Input: []byte(`{"title":"R6", "description":"Workspace fence"}`), AccountableUserID: parseUUID(testUserID), Source: "manual", ActorType: "member", ActorID: parseUUID(testUserID)})
				}
				outcome <- err
			}()
			if !waitForBlockedBackend(t, done) {
				t.Fatal("engine did not wait for workspace fence")
			}
			// Rollback stands in for a failed delete. The blocked writer may then proceed.
			if err := tx.Rollback(ctx); err != nil && err != pgx.ErrTxClosed {
				t.Fatal(err)
			}
			select {
			case <-done:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if err := <-outcome; err != nil {
				t.Fatal(fmt.Errorf("%s after rollback: %w", command, err))
			}
		})
	}
}

func TestR6WorkspaceDeleteFailureRollsBackWorkflowRows(t *testing.T) {
	target := r6Workspace(t)
	r6SeedWorkflowRows(t, target)
	old := testHandler.TxStarter
	testHandler.TxStarter = &r5FailCommitStarter{}
	defer func() { testHandler.TxStarter = old }()
	testutil.Call(t, testHandler.DeleteWorkspace, withURLParam(newRequest("DELETE", "/api/workspaces/"+target, nil), "id", target)).Want(500)
	for _, table := range r6WorkflowTables {
		if n := dbfx.Count(t, "SELECT count(*) FROM "+table+" WHERE workspace_id=$1", target); n != 1 {
			t.Fatalf("rollback lost %s: %d", table, n)
		}
	}
	if n := dbfx.Count(t, "SELECT count(*) FROM workspace WHERE id=$1", target); n != 1 {
		t.Fatal("workspace lost after rollback")
	}
}

func TestR6TemplateMutationsWaitForWorkspaceDeleteFence(t *testing.T) {
	cleanupWorkflowTemplates(t)
	for _, command := range []string{"publish", "save", "copy", "seed"} {
		t.Run(command, func(t *testing.T) {
			created := createWorkflowTemplateForTest(t, "r6-"+command)
			builtin := seededBugFixTemplate(t)
			var req *http.Request
			var handler http.HandlerFunc
			switch command {
			case "publish":
				handler = testHandler.PublishWorkflowTemplate
				req = withURLParam(newRequest("POST", "/api/workflow-templates/"+created.ID+"/publish", nil), "id", created.ID)
			case "save":
				handler = testHandler.UpdateWorkflowTemplate
				req = withURLParam(newRequest("PATCH", "/api/workflow-templates/"+created.ID, map[string]any{"revision": created.Revision, "name": "Changed"}), "id", created.ID)
			case "copy":
				handler = testHandler.DuplicateBuiltinWorkflowTemplate
				req = withURLParam(newRequest("POST", "/api/workflow-templates/"+builtin.ID+"/duplicate", nil), "id", builtin.ID)
			case "seed":
				handler = testHandler.ListWorkflowTemplates
				req = newRequest("GET", "/api/workflow-templates", nil)
			}
			ctx, cancel := context.WithTimeout(req.Context(), 10*time.Second)
			defer cancel()
			req = req.WithContext(ctx)
			tx, err := testPool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(context.Background())
			if _, err := db.New(tx).LockWorkspaceForDelete(ctx, parseUUID(testWorkspaceID)); err != nil {
				t.Fatal(err)
			}
			done := make(chan struct{})
			rec := httptest.NewRecorder()
			go func() { defer close(done); handler(rec, req) }()
			if !waitForBlockedBackend(t, done) {
				t.Fatal("template mutation did not wait for deletion")
			}
			if err := tx.Rollback(ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case <-done:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if rec.Code < 200 || rec.Code >= 300 {
				t.Fatalf("after rollback: %d %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestR6WorkspaceDeleteWaitsForWorkflowWriterAndSweepsItsCommit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ws := r6Workspace(t)
	tx, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	q := db.New(tx)
	if _, err := q.LockWorkspaceForWorkflowWrite(ctx, parseUUID(ws)); err != nil {
		t.Fatal(err)
	}
	if _, err := q.CreateWorkflowTemplate(ctx, db.CreateWorkflowTemplateParams{WorkspaceID: parseUUID(ws), Key: "r6-late", Name: "Late writer", CreatedByType: "member", CreatedByID: parseUUID(testUserID)}); err != nil {
		t.Fatal(err)
	}
	req := withURLParam(newRequest("DELETE", "/api/workspaces/"+ws, nil), "id", ws)
	requestCtx, requestCancel := context.WithTimeout(req.Context(), 10*time.Second)
	defer requestCancel()
	req = req.WithContext(requestCtx)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { defer close(done); testHandler.DeleteWorkspace(rec, req) }()
	if !waitForBlockedBackend(t, done) {
		t.Fatal("delete did not wait for uncommitted workflow writer")
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if rec.Code != 204 {
		t.Fatalf("delete after writer committed: %d %s", rec.Code, rec.Body.String())
	}
	if n := dbfx.Count(t, "SELECT count(*) FROM workflow_template WHERE workspace_id=$1", ws); n != 0 {
		t.Fatalf("late commit survived sweep: %d", n)
	}
}
