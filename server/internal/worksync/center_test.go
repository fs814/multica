package worksync_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/daemon"
	"github.com/multica-ai/multica/server/internal/testutil"
	ws "github.com/multica-ai/multica/server/internal/worksync"
)

type rig struct {
	c         *ws.Center
	scope     ws.Scope
	principal ws.Principal
	fx        *testutil.Fixture
}

func setup(t *testing.T) rig {
	t.Helper()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL required; never fall back to a shared database")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err = pool.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	fx := testutil.New(pool, "", "")
	fx.UserID = fx.User(t, "sync fixture", uuid.NewString()+"@example.test")
	fx.WorkspaceID = fx.Workspace(t, "sync fixture", "sync-"+uuid.NewString())
	fx.Member(t, fx.WorkspaceID, fx.UserID, "owner")
	scope := ws.Scope{Workspace: fx.WorkspaceID, Group: uuid.NewString(), Epoch: uuid.NewString()}
	p := ws.Principal{Account: "isolated-account", Actor: fx.UserID, Node: "center"}
	center := &ws.Center{Enabled: true, Pool: pool, Authorize: func(ctx context.Context, tx pgx.Tx, principal ws.Principal, requested ws.Scope, action string, op *ws.Operation) error {
		if requested != scope || principal.Account != p.Account || principal.Actor != p.Actor || (principal.Node != "center" && principal.Node != "a" && principal.Node != "b") {
			return ws.ErrDenied
		}
		var allowed bool
		err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM member WHERE workspace_id=$1 AND user_id=$2 AND role='owner')`, scope.Workspace, principal.Actor).Scan(&allowed)
		if err != nil || !allowed {
			return ws.ErrDenied
		}
		return nil
	}}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM work_sync_scope WHERE workspace_id=$1`, scope.Workspace)
		_, _ = pool.Exec(ctx, `DELETE FROM work_sync_receipt WHERE workspace_id=$1`, scope.Workspace)
		_, _ = pool.Exec(ctx, `DELETE FROM work_sync_change WHERE workspace_id=$1`, scope.Workspace)
	})
	return rig{center, scope, p, fx}
}

func f(key string, value any) ws.Fields { b, _ := json.Marshal(value); return ws.Fields{key: b} }
func enroll(t *testing.T, r rig) {
	t.Helper()
	if err := r.c.Enroll(context.Background(), r.principal, r.scope); err != nil {
		t.Fatal(err)
	}
}
func pull(t *testing.T, r rig, cursor int64, snapshot bool) ws.Batch {
	t.Helper()
	b, e := r.c.Pull(context.Background(), r.principal, r.scope, cursor, snapshot)
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func replica(t *testing.T, r rig, node string) (*ws.Replica, ws.ReplicaConfig) {
	t.Helper()
	p := r.principal
	p.Node = node
	cfg := ws.ReplicaConfig{Enabled: true, Root: t.TempDir(), Scope: r.scope, Principal: p}
	rep, err := ws.OpenReplica(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rep.Close() })
	return rep, cfg
}
func syncReplica(t *testing.T, r rig, rep *ws.Replica) {
	t.Helper()
	c := daemon.WorkSyncClient{Enabled: true, Replica: rep, Transport: r.c}
	if err := c.SyncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
}
func queue(t *testing.T, rep *ws.Replica, kind, entity, key string, value any) ws.Operation {
	t.Helper()
	op, e := rep.Queue(kind, entity, f(key, value))
	if e != nil {
		t.Fatal(e)
	}
	return op
}

func TestCenterThreeNodesPersistenceMergeAndDeletion(t *testing.T) {
	r := setup(t)
	issue := r.fx.Issue(t, "original")
	project := r.fx.Insert(t, "project", testutil.Cols{"workspace_id": r.scope.Workspace, "title": "original"})
	agent := r.fx.Agent(t, "test agent", "", testutil.Cols{"custom_env": testutil.Raw(`'{"SECRET":"never-export"}'::jsonb`)})
	if n := r.fx.Count(t, `SELECT count(*) FROM work_sync_change WHERE workspace_id=$1`, r.scope.Workspace); n != 0 {
		t.Fatal("default enrolled real data")
	}
	enroll(t, r)
	a, cfg := replica(t, r, "a")
	b, _ := replica(t, r, "b")
	syncReplica(t, r, a)
	syncReplica(t, r, b)
	as, _ := a.State()
	if len(as.Records) != 3 {
		t.Fatalf("baseline count %d", len(as.Records))
	}
	encoded, _ := json.Marshal(as)
	if strings.Contains(string(encoded), "never-export") || strings.Contains(string(encoded), "custom_env") {
		t.Fatal("secret leaked")
	}
	queue(t, a, "issue", issue, "priority", "high")
	queue(t, a, "project", project, "description", "offline project")
	queue(t, a, "agent", agent, "description", "offline agent")
	queue(t, a, "issue", issue, "description", "first")
	queue(t, a, "issue", issue, "description", "second")
	_ = a.Close()
	var err error
	a, err = ws.OpenReplica(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	r.fx.Exec(t, `UPDATE issue SET title='center title', updated_at=now()+interval '1 day' WHERE id=$1`, issue)
	syncReplica(t, r, a)
	syncReplica(t, r, b)
	as, _ = a.State()
	bs, _ := b.State()
	if !reflect.DeepEqual(as.Records, bs.Records) || len(as.Outbox) != 0 || len(as.Review) != 0 {
		t.Fatalf("failed to converge: %+v %+v", as, bs)
	}
	got := as.Records["issue/"+issue]
	if string(got.Fields["title"]) != `"center title"` || string(got.Fields["priority"]) != `"high"` || string(got.Fields["description"]) != `"second"` {
		t.Fatalf("lost edits: %+v", got)
	}
	for _, entity := range []struct{ kind, id string }{{"issue", issue}, {"project", project}, {"agent", agent}} {
		queue(t, a, entity.kind, entity.id, "description", "a's competing description")
		queue(t, b, entity.kind, entity.id, "description", "b's competing description")
	}
	syncReplica(t, r, a)
	syncReplica(t, r, b)
	bs, _ = b.State()
	if len(bs.Review) != 3 {
		t.Fatalf("expected three preserved conflicts, got %d", len(bs.Review))
	}
	for _, review := range bs.Review {
		if review.Receipt.Status != "conflict" || len(review.Receipt.Conflicts) != 1 {
			t.Fatalf("bad conflict: %+v", review)
		}
	}
	queue(t, b, "issue", issue, "title", "do not revive")
	r.fx.Exec(t, `DELETE FROM issue WHERE id=$1`, issue)
	r.fx.Exec(t, `UPDATE agent SET archived_at=now() WHERE id=$1`, agent)
	syncReplica(t, r, b)
	syncReplica(t, r, a)
	bs, _ = b.State()
	as, _ = a.State()
	if !bs.Records["issue/"+issue].Deleted || bs.Records["agent/"+agent].Deleted || len(bs.Review) != 4 || bs.Review[3].Receipt.Reason != "entity deleted" {
		t.Fatalf("tombstone/archive contract: %+v", bs)
	}
	if !reflect.DeepEqual(as.Records, bs.Records) {
		t.Fatal("delete did not converge")
	}
	if n := r.fx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE agent_id=$1`, agent); n != 0 {
		t.Fatal("sync dispatched tasks")
	}
}

func TestCenterReplayOrderAuthorizationAndAtomicRollback(t *testing.T) {
	r := setup(t)
	entity := r.fx.Issue(t, "original")
	enroll(t, r)
	a, _ := replica(t, r, "a")
	syncReplica(t, r, a)
	op := queue(t, a, "issue", entity, "title", "changed")
	p := r.principal
	p.Node = "a"
	ctx := context.Background()
	receipt, err := r.c.Push(ctx, p, r.scope, op)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := r.c.Push(ctx, p, r.scope, op)
	if err != nil || !reflect.DeepEqual(receipt, replay) {
		t.Fatalf("retry after response loss: %+v %v", replay, err)
	}
	if n := r.fx.Count(t, `SELECT revision FROM issue WHERE id=$1`, entity); n != 2 {
		t.Fatalf("duplicate applied twice; revision %d", n)
	}
	mutated := op
	mutated.Patch = f("title", "different payload")
	if _, err = r.c.Push(ctx, p, r.scope, mutated); !errors.Is(err, ws.ErrOperation) {
		t.Fatalf("accepted reused op id: %v", err)
	}
	second := op
	second.ID = uuid.NewString()
	second.Sequence = 3
	if _, err = r.c.Push(ctx, p, r.scope, second); !errors.Is(err, ws.ErrOperation) {
		t.Fatalf("accepted out of order: %v", err)
	}
	second.Sequence = 2
	second.Base = 100000
	missing, err := r.c.Push(ctx, p, r.scope, second)
	if err != nil || missing.Reason != "base missing" {
		t.Fatalf("missing base: %+v %v", missing, err)
	}
	for _, changed := range []ws.Principal{{Account: "other", Actor: p.Actor, Node: p.Node}, {Account: p.Account, Actor: uuid.NewString(), Node: p.Node}, {Account: p.Account, Actor: p.Actor, Node: "unknown"}} {
		if _, err = r.c.Pull(ctx, changed, r.scope, 0, true); !errors.Is(err, ws.ErrDenied) {
			t.Fatalf("scope export allowed: %v", err)
		}
	}
	foreign := r.scope
	foreign.Workspace = uuid.NewString()
	if _, err = r.c.Pull(ctx, p, foreign, 0, true); !errors.Is(err, ws.ErrDenied) {
		t.Fatalf("cross workspace: %v", err)
	}
	r.fx.Exec(t, `DELETE FROM member WHERE workspace_id=$1 AND user_id=$2`, r.scope.Workspace, p.Actor)
	if _, err = r.c.Push(ctx, p, r.scope, op); !errors.Is(err, ws.ErrDenied) {
		t.Fatalf("duplicate bypassed revoked grant: %v", err)
	}
	r.fx.Member(t, r.scope.Workspace, p.Actor, "owner")
	before := pull(t, r, 0, true)
	tx, err := r.c.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE issue SET title='rolled back' WHERE id=$1`, entity); err != nil {
		t.Fatal(err)
	}
	_ = tx.Rollback(ctx)
	after := pull(t, r, 0, true)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("business rollback retained journal or cursor")
	}
	// Failure after a business patch but before its receipt must roll back both.
	_, err = r.c.Pool.Exec(ctx, `ALTER TABLE work_sync_receipt ADD CONSTRAINT work_sync_test_failure CHECK (node_id <> 'a') NOT VALID`)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = r.c.Pool.Exec(ctx, `ALTER TABLE work_sync_receipt DROP CONSTRAINT IF EXISTS work_sync_test_failure`)
	})
	second.ID = uuid.NewString()
	second.Sequence = 3
	second.Base = receipt.Record.Version
	second.Patch = f("title", "must roll back")
	if _, err = r.c.Push(ctx, p, r.scope, second); err == nil {
		t.Fatal("receipt failure accepted")
	}
	after = pull(t, r, 0, true)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("receipt failure left partial business write")
	}
}

func TestDependentEditDoesNotAdoptUnseenCenterFields(t *testing.T) {
	r := setup(t)
	entity := r.fx.Issue(t, "original")
	enroll(t, r)
	a, _ := replica(t, r, "a")
	syncReplica(t, r, a)
	queue(t, a, "issue", entity, "priority", "high")
	queue(t, a, "issue", entity, "title", "offline title")
	r.fx.Exec(t, `UPDATE issue SET title='unseen center title' WHERE id=$1`, entity)
	syncReplica(t, r, a)
	s, err := a.State()
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Review) != 1 || s.Review[0].Receipt.Status != "conflict" ||
		string(s.Records["issue/"+entity].Fields["title"]) != `"unseen center title"` ||
		string(s.Records["issue/"+entity].Fields["priority"]) != `"high"` {
		t.Fatalf("dependent edit overwrote unseen center changes: %+v", s)
	}
}

func TestSyncApplyDoesNotScheduleWakeups(t *testing.T) {
	r := setup(t)
	entity := r.fx.Issue(t, "original")
	agent := r.fx.Agent(t, "wakeup target", "")
	wakeup := r.fx.Insert(t, "issue_wakeup", testutil.Cols{
		"id": uuid.NewString(), "workspace_id": r.scope.Workspace, "issue_id": entity,
		"agent_id": agent, "created_by": r.principal.Actor, "instruction": "must not run",
		"kind": "event", "mode": "continuous", "event_types": []string{"issue.updated"},
	})
	r.fx.Cleanup(t, `DELETE FROM issue_wakeup_receipt WHERE wakeup_id=$1`, wakeup)
	enroll(t, r)
	a, _ := replica(t, r, "a")
	syncReplica(t, r, a)
	queue(t, a, "issue", entity, "title", "replicated")
	syncReplica(t, r, a)
	if n := r.fx.Count(t, `SELECT count(*) FROM issue_wakeup_receipt WHERE wakeup_id=$1`, wakeup); n != 0 {
		t.Fatal("replication created a wakeup receipt")
	}
	// The transaction-local suppression must not leak into ordinary writes.
	r.fx.Exec(t, `UPDATE issue SET title='online' WHERE id=$1`, entity)
	if n := r.fx.Count(t, `SELECT count(*) FROM issue_wakeup_receipt WHERE wakeup_id=$1`, wakeup); n != 1 {
		t.Fatal("ordinary online update lost its wakeup")
	}
}

func TestCenterCommitOrderingAndSnapshotBoundary(t *testing.T) {
	r := setup(t)
	a := r.fx.Issue(t, "a")
	b := r.fx.Issue(t, "b")
	enroll(t, r)
	base := pull(t, r, 0, true)
	ctx := context.Background()
	tx1, err := r.c.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx1.Rollback(ctx)
	_, err = tx1.Exec(ctx, `UPDATE issue SET title='first uncommitted' WHERE id=$1`, a)
	if err != nil {
		t.Fatal(err)
	}
	tx2, err := r.c.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx2.Rollback(ctx)
	_, err = tx2.Exec(ctx, `UPDATE issue SET title='second committed' WHERE id=$1`, b)
	if err != nil {
		t.Fatal(err)
	}
	if err = tx2.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	// The transaction that finishes first owns the first committed cursor,
	// even when an earlier transaction has already changed a business row.
	during := pull(t, r, base.Cursor, false)
	if during.Cursor != base.Cursor+1 || len(during.Records) != 1 || during.Records[0].ID != b {
		t.Fatal("wrong first committed cursor")
	}
	if err = tx1.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	late := pull(t, r, during.Cursor, false)
	if len(late.Records) != 1 || late.Records[0].ID != a {
		t.Fatalf("skipped late commit: %+v", late)
	}
	changes := pull(t, r, base.Cursor, false)
	if len(changes.Records) != 2 || changes.Records[0].Version != base.Cursor+1 || changes.Records[1].Version != base.Cursor+2 {
		t.Fatalf("lost late commit: %+v", changes)
	}
	rep, _ := replica(t, r, "a")
	if err = rep.Apply(base); err != nil {
		t.Fatal(err)
	}
	if err = rep.Apply(changes); err != nil {
		t.Fatal(err)
	}
	s, _ := rep.State()
	if s.Cursor != changes.Cursor || string(s.Records["issue/"+b].Fields["title"]) != `"second committed"` {
		t.Fatal("snapshot plus incremental did not converge")
	}
}
