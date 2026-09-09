package service

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// Every fixture lives in a rolled-back transaction; no real workspace or
// runnable daemon receives test tasks.
func TestHTTPAssignmentIssueLifecycle(t *testing.T) {
	pool := newResolveOriginatorPool(t)
	for _, tc := range []struct {
		name, provider, initial, intervening, output, wantStart, wantEnd string
		reply, handoff, leader, cancel, reassign                         bool
	}{
		{name: "normal", provider: "knot-http", initial: "todo", output: "macOS result", wantStart: "in_progress", wantEnd: "in_review"},
		{name: "backlog", provider: "knot-http", initial: "backlog", output: "result", wantStart: "in_progress", wantEnd: "in_review"},
		{name: "local provider", provider: "codex", initial: "todo", output: "result", wantStart: "todo", wantEnd: "todo"},
		{name: "reply", provider: "knot-http", initial: "todo", output: "result", wantStart: "todo", wantEnd: "todo", reply: true},
		{name: "handoff", provider: "knot-http", initial: "todo", output: "result", wantStart: "todo", wantEnd: "todo", handoff: true},
		{name: "leader", provider: "knot-http", initial: "todo", output: "result", wantStart: "todo", wantEnd: "todo", leader: true},
		{name: "empty output", provider: "knot-http", initial: "todo", wantStart: "in_progress", wantEnd: "in_progress"},
		{name: "already done", provider: "knot-http", initial: "done", output: "result", wantStart: "done", wantEnd: "done"},
		{name: "human cancellation", provider: "knot-http", initial: "todo", intervening: "cancelled", output: "result", wantStart: "in_progress", wantEnd: "cancelled"},
		{name: "human completed", provider: "knot-http", initial: "todo", intervening: "done", output: "result", wantStart: "in_progress", wantEnd: "done"},
		{name: "human moved back", provider: "knot-http", initial: "todo", intervening: "todo", output: "result", wantStart: "in_progress", wantEnd: "todo"},
		{name: "task cancelled", provider: "knot-http", initial: "todo", output: "result", wantStart: "in_progress", wantEnd: "in_progress", cancel: true},
		{name: "reassigned", provider: "knot-http", initial: "todo", output: "result", wantStart: "in_progress", wantEnd: "in_progress", reassign: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			tx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(ctx)
			exec := func(sql string, args ...any) {
				t.Helper()
				if _, err := tx.Exec(ctx, sql, args...); err != nil {
					t.Fatal(err)
				}
			}
			var user, ws, runtimeID, agent, issue, taskID pgtype.UUID
			row := func(dest *pgtype.UUID, sql string, args ...any) {
				t.Helper()
				if err := tx.QueryRow(ctx, sql, args...).Scan(dest); err != nil {
					t.Fatal(err)
				}
			}
			row(&user, `INSERT INTO "user" (name,email) VALUES ('HTTP lifecycle test', gen_random_uuid()::text || '@multica.test') RETURNING id`)
			row(&ws, `INSERT INTO workspace (name,slug) VALUES ('HTTP lifecycle test',gen_random_uuid()::text) RETURNING id`)
			row(&runtimeID, `INSERT INTO agent_runtime (workspace_id,name,runtime_mode,provider,status,device_info,metadata,owner_id) VALUES ($1,'test','cloud',$2,'offline','','{}',$3) RETURNING id`, ws, tc.provider, user)
			row(&agent, `INSERT INTO agent (workspace_id,name,runtime_mode,runtime_config,runtime_id,owner_id) VALUES ($1,'test','cloud','{}',$2,$3) RETURNING id`, ws, runtimeID, user)
			row(&issue, `INSERT INTO issue (workspace_id,title,creator_type,creator_id,assignee_type,assignee_id,status,priority) VALUES ($1,'test','member',$2,'agent',$3,$4,'medium') RETURNING id`, ws, user, agent, tc.initial)
			var comment pgtype.UUID
			if tc.reply {
				row(&comment, `INSERT INTO comment (workspace_id,issue_id,author_type,author_id,content) VALUES ($3,$1,'member',$2,'reply') RETURNING id`, issue, user, ws)
			}
			handoff := ""
			if tc.handoff {
				handoff = "scoped handoff"
			}
			row(&taskID, `INSERT INTO agent_task_queue (agent_id,issue_id,runtime_id,status,trigger_comment_id,handoff_note,is_leader_task) VALUES ($1,$2,$3,'dispatched',$4,$5,$6) RETURNING id`, agent, issue, runtimeID, comment, handoff, tc.leader)
			bus := events.New()
			statusEvents := 0
			bus.Subscribe(protocol.EventIssueUpdated, func(events.Event) { statusEvents++ })
			svc := &TaskService{Queries: db.New(tx), TxStarter: tx, Bus: bus}
			checkStatus := func(want string) {
				t.Helper()
				got, err := svc.Queries.GetIssue(ctx, issue)
				if err != nil {
					t.Fatal(err)
				}
				if got.Status != want {
					t.Fatalf("issue status=%s, want %s", got.Status, want)
				}
			}
			if _, err := svc.StartTask(ctx, taskID); err != nil {
				t.Fatal(err)
			}
			checkStatus(tc.wantStart)
			if tc.intervening != "" {
				exec(`UPDATE issue SET status=$2 WHERE id=$1`, issue, tc.intervening)
			}
			if tc.cancel {
				exec(`UPDATE agent_task_queue SET status='cancelled' WHERE id=$1`, taskID)
			}
			if tc.reassign {
				exec(`UPDATE issue SET assignee_type='member',assignee_id=$2 WHERE id=$1`, issue, user)
			}
			result := []byte(`{"output":"` + tc.output + `"}`)
			for range 2 {
				if _, err := svc.CompleteTask(ctx, taskID, result, "", "", false, ""); err != nil {
					t.Fatal(err)
				}
				checkStatus(tc.wantEnd)
			}
			var count int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM agent_task_queue WHERE issue_id=$1`, issue).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 1 {
				t.Fatalf("lifecycle enqueued extra tasks: %d", count)
			}
			if tc.name == "normal" && statusEvents != 2 {
				t.Fatalf("status update events=%d, want 2", statusEvents)
			}
		})
	}
}
