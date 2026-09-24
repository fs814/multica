package handler

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/multica-ai/multica/server/internal/testutil"
)

var workspaceMemoryTables = []string{"project_memory_binding", "project_memory_scope", "project_memory_task", "project_memory_request"}

type memoryDeleteFailStarter struct {
	txStarter
	check func(pgx.Tx)
}

func (s memoryDeleteFailStarter) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := s.txStarter.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return &memoryDeleteFailTx{Tx: tx, check: s.check}, nil
}

type memoryDeleteFailTx struct {
	pgx.Tx
	check func(pgx.Tx)
}

func (tx *memoryDeleteFailTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if strings.HasPrefix(sql, "-- name: DeleteWorkspace :exec") {
		tx.check(tx.Tx)
		return pgconn.CommandTag{}, fmt.Errorf("injected failure after memory cleanup")
	}
	return tx.Tx.Exec(ctx, sql, args...)
}

func TestDeleteWorkspaceProjectMemory(t *testing.T) {
	if testPool == nil {
		t.Skip("database not available")
	}
	for _, rollback := range []bool{false, true} {
		t.Run(fmt.Sprintf("rollback=%v", rollback), func(t *testing.T) {
			seed := func(slug string) string {
				ws := dbfx.Insert(t, "workspace", testutil.Cols{"name": slug, "slug": slug})
				dbfx.Exec(t, `INSERT INTO member(workspace_id,user_id,role) VALUES($1,$2,'owner')`, ws, testUserID)
				dbfx.Exec(t, `INSERT INTO project_memory_binding(workspace_id,project_id) VALUES($1,gen_random_uuid())`, ws)
				dbfx.Exec(t, `INSERT INTO project_memory_scope(workspace_id,scope_kind,scope_id) VALUES($1,'issue',gen_random_uuid())`, ws)
				dbfx.Exec(t, `INSERT INTO project_memory_task(workspace_id,task_id,context) VALUES($1,gen_random_uuid(),'{}')`, ws)
				dbfx.Exec(t, `INSERT INTO project_memory_request(id,workspace_id,project_id,owner_daemon_id,request) VALUES(gen_random_uuid(),$1,gen_random_uuid(),gen_random_uuid(),'{}')`, ws)
				t.Cleanup(func() {
					for _, table := range workspaceMemoryTables {
						_, _ = testPool.Exec(context.Background(), "DELETE FROM "+table+" WHERE workspace_id=$1", ws)
					}
				})
				return ws
			}
			victim := seed(fmt.Sprintf("memory-delete-victim-%v", rollback))
			other := seed(fmt.Sprintf("memory-delete-other-%v", rollback))
			h := *testHandler
			injected := false
			if rollback {
				h.TxStarter = memoryDeleteFailStarter{txStarter: h.TxStarter, check: func(tx pgx.Tx) {
					injected = true
					for _, table := range workspaceMemoryTables {
						var count int
						if err := tx.QueryRow(context.Background(), "SELECT count(*) FROM "+table+" WHERE workspace_id=$1", victim).Scan(&count); err != nil {
							t.Fatal(err)
						}
						if count != 0 {
							t.Errorf("%s not cleaned before rollback: %d", table, count)
						}
					}
				}}
			}
			req := withURLParam(newRequest("DELETE", "/api/workspaces/"+victim, nil), "id", victim)
			wantStatus, wantRows := http.StatusNoContent, 0
			if rollback {
				wantStatus, wantRows = http.StatusInternalServerError, 1
			}
			testutil.Call(t, h.DeleteWorkspace, req).Want(wantStatus)
			if injected != rollback {
				t.Fatalf("failure injection reached=%v, want %v", injected, rollback)
			}
			for _, table := range append(append([]string{}, workspaceMemoryTables...), "member") {
				if got := dbfx.Count(t, "SELECT count(*) FROM "+table+" WHERE workspace_id=$1", victim); got != wantRows {
					t.Errorf("victim %s rows=%d, want %d", table, got, wantRows)
				}
				if got := dbfx.Count(t, "SELECT count(*) FROM "+table+" WHERE workspace_id=$1", other); got != 1 {
					t.Errorf("other workspace %s rows=%d, want 1", table, got)
				}
			}
			if got := dbfx.Count(t, "SELECT count(*) FROM workspace WHERE id=$1", victim); got != wantRows {
				t.Errorf("workspace rows=%d, want %d", got, wantRows)
			}
		})
	}
}
