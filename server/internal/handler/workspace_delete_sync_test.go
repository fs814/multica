package handler

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/testutil"
)

func TestDeleteWorkspaceWorkSync(t *testing.T) {
	if testPool == nil {
		t.Skip("database not available")
	}
	tables := []string{"work_sync_scope", "work_sync_change", "work_sync_receipt", "work_sync_grant"}
	for _, rollback := range []bool{false, true} {
		t.Run(fmt.Sprintf("rollback=%v", rollback), func(t *testing.T) {
			seed := func() string {
				w := dbfx.Insert(t, "workspace", testutil.Cols{"name": "sync deletion", "slug": "sync-delete-" + uuid.NewString()})
				dbfx.Member(t, w, testUserID, "owner")
				dbfx.Exec(t, `INSERT INTO work_sync_scope(workspace_id,group_id,epoch) VALUES ($1,gen_random_uuid(),gen_random_uuid())`, w)
				dbfx.Issue(t, "captured", testutil.Cols{"workspace_id": w})
				dbfx.Exec(t, `INSERT INTO work_sync_receipt(workspace_id,operation_id,node_id,incarnation,sequence,actor_id,payload_hash,receipt) VALUES ($1,gen_random_uuid(),'node',gen_random_uuid(),1,$2,'test','{}')`, w, testUserID)
				dbfx.Exec(t, `INSERT INTO work_sync_grant(workspace_id,group_id,epoch,actor_id,node_id,expires_at) SELECT workspace_id,group_id,epoch,$2,'node',now()+interval '1 hour' FROM work_sync_scope WHERE workspace_id=$1`, w, testUserID)
				t.Cleanup(func() {
					for _, table := range tables {
						_, _ = testPool.Exec(context.Background(), "DELETE FROM "+table+" WHERE workspace_id=$1", w)
					}
				})
				return w
			}
			victim, other := seed(), seed()
			h := *testHandler
			if rollback {
				h.TxStarter = memoryDeleteFailStarter{txStarter: h.TxStarter, check: func(tx pgx.Tx) {
					for _, table := range tables {
						var n int
						if err := tx.QueryRow(context.Background(), "SELECT count(*) FROM "+table+" WHERE workspace_id=$1", victim).Scan(&n); err != nil || n != 0 {
							t.Errorf("%s not cleaned: %d %v", table, n, err)
						}
					}
				}}
			}
			status, want := http.StatusNoContent, 0
			if rollback {
				status, want = http.StatusInternalServerError, 1
			}
			req := withURLParam(newRequest("DELETE", "/api/workspaces/"+victim, nil), "id", victim)
			testutil.Call(t, h.DeleteWorkspace, req).Want(status)
			for _, table := range tables {
				if n := dbfx.Count(t, "SELECT count(*) FROM "+table+" WHERE workspace_id=$1", victim); n != want {
					t.Errorf("victim %s = %d, want %d", table, n, want)
				}
				if n := dbfx.Count(t, "SELECT count(*) FROM "+table+" WHERE workspace_id=$1", other); n != 1 {
					t.Errorf("foreign workspace %s = %d, want 1", table, n)
				}
			}
		})
	}
}
