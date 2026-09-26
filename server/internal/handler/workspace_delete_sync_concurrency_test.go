package handler

import (
	"context"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/testutil"
)

func TestDeleteWorkspaceConcurrentWorkSyncCapture(t *testing.T) {
	if testPool == nil {
		t.Skip("database not available")
	}
	for _, journalOnly := range []bool{false, true} {
		t.Run(fmt.Sprintf("journal_only=%v", journalOnly), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			w := dbfx.Insert(t, "workspace", testutil.Cols{"name": "review sync deletion", "slug": "review-sync-" + uuid.NewString()})
			dbfx.Member(t, w, testUserID, "owner")
			project := dbfx.Insert(t, "project", testutil.Cols{"workspace_id": w, "title": "original"})
			dbfx.Exec(t, `INSERT INTO work_sync_scope(workspace_id,group_id,epoch) VALUES ($1,gen_random_uuid(),gen_random_uuid())`, w)
			t.Cleanup(func() {
				for _, table := range []string{"work_sync_scope", "work_sync_receipt", "work_sync_change"} {
					_, _ = testPool.Exec(context.Background(), "DELETE FROM "+table+" WHERE workspace_id=$1", w)
				}
			})
			writer, err := testPool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer writer.Rollback(context.Background())
			if journalOnly {
				// Exercise the scope wait and fresh cleanup snapshot independently of
				// the business-row barrier, as a final-phase sync writer would.
				_, err = writer.Exec(ctx, `SELECT work_sync_capture_row('project',to_jsonb(p),false) FROM project p WHERE id=$1`, project)
			} else {
				_, err = writer.Exec(ctx, `UPDATE project SET title='concurrent commit' WHERE id=$1`, project)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = writer.Exec(ctx, `INSERT INTO work_sync_receipt(workspace_id,operation_id,node_id,incarnation,sequence,actor_id,payload_hash,receipt) VALUES ($1,gen_random_uuid(),'review',gen_random_uuid(),1,$2,'test','{}')`, w, testUserID); err != nil {
				t.Fatal(err)
			}
			req := withURLParam(newRequest("DELETE", "/api/workspaces/"+w, nil), "id", w)
			response := httptest.NewRecorder()
			done := make(chan struct{})
			go func() { defer close(done); testHandler.DeleteWorkspace(response, req) }()
			waited := false
			for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); {
				if err = testPool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid() AND wait_event_type='Lock' AND (query LIKE '%CloseWorkspaceWorkSync%' OR query LIKE '%LockWorkspaceWorkSyncProjects%'))`).Scan(&waited); err != nil {
					t.Fatal(err)
				}
				if waited {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if !waited {
				_ = writer.Rollback(ctx)
				<-done
				t.Fatalf("delete did not reach sync cleanup: status=%d body=%s", response.Code, response.Body.String())
			}
			if err = writer.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			<-done
			if response.Code != 204 {
				t.Fatalf("delete status=%d body=%s", response.Code, response.Body.String())
			}
			for _, table := range []string{"workspace", "project", "work_sync_scope", "work_sync_change", "work_sync_receipt"} {
				column := "workspace_id"
				if table == "workspace" {
					column = "id"
				}
				n := dbfx.Count(t, "SELECT count(*) FROM "+table+" WHERE "+column+"=$1", w)
				t.Logf("after successful workspace deletion: %s=%d", table, n)
				if n != 0 {
					t.Errorf("workspace deletion retained %s: %d row(s)", table, n)
				}
			}
		})
	}
}
