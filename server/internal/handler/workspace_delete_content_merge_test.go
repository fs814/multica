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

func TestDeleteWorkspaceContentMergeClearsContentAndKeepsTombstone(t *testing.T) {
	if testPool == nil {
		t.Skip("isolated database required")
	}
	for _, rollback := range []bool{false, true} {
		t.Run(fmt.Sprintf("rollback=%v", rollback), func(t *testing.T) {
			seed := func() string {
				w := dbfx.Workspace(t, "merge deletion", "merge-delete-"+uuid.NewString())
				dbfx.Member(t, w, testUserID, "owner")
				for _, key := range []string{"workspace:" + w, "issue:" + uuid.NewString()} {
					dbfx.InsertNoID(t, "center_content_merge", testutil.Cols{
						"workspace_id": w, "owner_id": testUserID, "peer_origin": "https://peer.example.test", "record_key": key,
						"baseline": testutil.Raw(`'{"title":"content to erase"}'::jsonb`),
					}, "workspace_id=$1 AND record_key=$2", w, key)
				}
				return w
			}
			victim, other := seed(), seed()
			checkSettled := func(tx pgx.Tx) {
				var count int
				if err := tx.QueryRow(context.Background(), `SELECT count(*) FROM center_content_merge WHERE workspace_id=$1 AND (baseline <> '{}'::jsonb OR record_key <> 'workspace:' || $1::uuid::text)`, victim).Scan(&count); err != nil || count != 0 {
					t.Errorf("content baselines not erased: %d %v", count, err)
				}
			}
			h := *testHandler
			status := http.StatusNoContent
			if rollback {
				status = http.StatusInternalServerError
				h.TxStarter = memoryDeleteFailStarter{txStarter: h.TxStarter, check: checkSettled}
			}
			req := withURLParam(newRequest("DELETE", "/api/workspaces/"+victim, nil), "id", victim)
			testutil.Call(t, h.DeleteWorkspace, req).Want(status)
			wantContent, wantTombstones := 0, 1
			if rollback {
				wantContent, wantTombstones = 2, 0
			}
			if got := dbfx.Count(t, `SELECT count(*) FROM center_content_merge WHERE workspace_id=$1 AND baseline <> '{}'::jsonb`, victim); got != wantContent {
				t.Fatalf("remaining content = %d, want %d", got, wantContent)
			}
			if got := dbfx.Count(t, `SELECT count(*) FROM center_content_merge WHERE workspace_id=$1 AND baseline = '{}'::jsonb`, victim); got != wantTombstones {
				t.Fatalf("tombstones = %d, want %d", got, wantTombstones)
			}
			if got := dbfx.Count(t, `SELECT count(*) FROM center_content_merge WHERE workspace_id=$1 AND baseline <> '{}'::jsonb`, other); got != 2 {
				t.Fatal("another workspace's baseline was changed")
			}
		})
	}
}
