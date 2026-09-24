package projectmemory

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/testutil"
)

func coordinatorFixture(t *testing.T) (Coordinator, *testutil.Fixture, Store, Binding, string, string) {
	t.Helper()
	dsn := os.Getenv("PROJECT_MEMORY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("PROJECT_MEMORY_TEST_DATABASE_URL must point to a dedicated fixture database")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "memory_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE"); admin.Close() })
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	// Minimal source tables isolate these tests from all production fixtures.
	_, err = pool.Exec(ctx, `CREATE TABLE issue(id uuid,workspace_id uuid,project_id uuid);
 CREATE TABLE chat_session(id uuid,workspace_id uuid,project_id uuid);
 CREATE TABLE agent_task_queue(id uuid,status text);`)
	if err != nil {
		t.Fatal(err)
	}
	for _, prefix := range []string{"517_", "518_", "519_", "520_", "521_", "522_"} {
		names, err := filepath.Glob("../../migrations/" + prefix + "project_memory*.up.sql")
		if err != nil || len(names) != 1 {
			t.Fatalf("migration %s: %v", prefix, err)
		}
		raw, err := os.ReadFile(names[0])
		if err != nil {
			t.Fatal(err)
		}
		if _, err = pool.Exec(ctx, string(raw)); err != nil {
			t.Fatalf("%s: %v", names[0], err)
		}
	}
	store, b := fixture(t)
	fx := testutil.New(pool, b.WorkspaceID, uuid.NewString())
	issue := fx.Insert(t, "issue", testutil.Cols{"id": uuid.NewString(), "workspace_id": b.WorkspaceID, "project_id": b.ProjectID})
	task := fx.Insert(t, "agent_task_queue", testutil.Cols{"id": uuid.NewString(), "status": "running"})
	c := Coordinator{DB: pool}
	frozen, _, err := c.Claim(ctx, task, Context{WorkspaceID: b.WorkspaceID, ProjectID: b.ProjectID, ScopeKind: "issue", ScopeID: issue}, b)
	if err != nil || frozen.Epoch != 1 {
		t.Fatalf("claim: %+v %v", frozen, err)
	}
	return c, fx, store, b, issue, task
}
func completeOperation(t *testing.T, c Coordinator, s Store, b Binding, task string, op Operation) (Binding, Receipt) {
	t.Helper()
	ctx := context.Background()
	w, err := c.Submit(ctx, b.WorkspaceID, b.ProjectID, task, op, nil)
	if err != nil {
		t.Fatal(err)
	}
	next, err := c.Next(ctx, b.WorkspaceID, b.OwnerDaemonID)
	if err != nil || next == nil {
		t.Fatalf("next: %v", err)
	}
	if next.ID != w.ID {
		t.Fatal("unexpected work")
	}
	if err = c.Complete(ctx, b.WorkspaceID, b.OwnerDaemonID, w.ID, Execute(s, *next)); err != nil {
		t.Fatal(err)
	}
	receipt, err := c.Receipt(ctx, b.WorkspaceID, b.ProjectID, task, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	b, err = c.Resolve(ctx, b.WorkspaceID, b.ProjectID, task)
	if err != nil {
		t.Fatal(err)
	}
	return b, receipt
}
func TestCoordinatorPublicationAndIsolation(t *testing.T) {
	c, fx, s, b, issue, task := coordinatorFixture(t)
	ctx := context.Background()
	b, r := completeOperation(t, c, s, b, task, Operation{Action: "init", ExpectedBindingRevision: 1, Source: "fixture"})
	if r.Status != "done" || b.ContentRevision != 1 {
		t.Fatalf("init: %+v %+v", b, r)
	}
	// Two candidates from the same content revision: exactly one publishes.
	works := make([]Work, 2)
	results := make([]Result, 2)
	for i := range works {
		w, err := c.Submit(ctx, b.WorkspaceID, b.ProjectID, task, Operation{Action: "write", Path: "README.md", Content: uuid.NewString(), Source: "fixture", ExpectedRevision: 1, ExpectedBindingRevision: 1}, nil)
		if err != nil {
			t.Fatal(err)
		}
		works[i] = w
		claimed, err := c.Next(ctx, b.WorkspaceID, b.OwnerDaemonID)
		if err != nil || claimed == nil {
			t.Fatal(err)
		}
		results[i] = Execute(s, *claimed)
	}
	var wg sync.WaitGroup
	for i := range works {
		wg.Go(func() {
			if err := c.Complete(ctx, b.WorkspaceID, b.OwnerDaemonID, works[i].ID, results[i]); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	count := 0
	for _, w := range works {
		r, err := c.Receipt(ctx, b.WorkspaceID, b.ProjectID, task, w.ID)
		if err != nil {
			t.Fatal(err)
		}
		if r.Status == "done" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("%d publications", count)
	}
	current, err := c.Resolve(ctx, b.WorkspaceID, b.ProjectID, task)
	if err != nil || current.ContentRevision != 2 {
		t.Fatalf("revision: %+v %v", current, err)
	}
	if _, err = c.Resolve(ctx, b.WorkspaceID, uuid.NewString(), task); err == nil {
		t.Fatal("cross-project read accepted")
	}
	if _, err = c.Resolve(ctx, uuid.NewString(), b.ProjectID, task); err == nil {
		t.Fatal("cross-workspace read accepted")
	}
	// Stage a valid write, then switch A -> B -> A before publication.
	w, err := c.Submit(ctx, b.WorkspaceID, b.ProjectID, task, Operation{Action: "write", Path: "README.md", Content: "late", Source: "fixture", ExpectedRevision: 2, ExpectedBindingRevision: 1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := c.Next(ctx, b.WorkspaceID, b.OwnerDaemonID)
	if err != nil {
		t.Fatal(err)
	}
	result := Execute(s, *claimed)
	fx.Exec(t, "UPDATE issue SET project_id=$1 WHERE id=$2", uuid.NewString(), issue)
	fx.Exec(t, "UPDATE issue SET project_id=$1 WHERE id=$2", b.ProjectID, issue)
	if err = c.Complete(ctx, b.WorkspaceID, b.OwnerDaemonID, w.ID, result); err != nil {
		t.Fatal(err)
	}
	if _, err = c.Resolve(ctx, b.WorkspaceID, b.ProjectID, task); err == nil {
		t.Fatal("old context survived A-B-A")
	}
	current, err = c.Resolve(ctx, b.WorkspaceID, b.ProjectID, "")
	if err != nil || current.ContentRevision != 2 {
		t.Fatal("late write published")
	}
	// Unset also revokes a fresh chat context.
	chat := fx.Insert(t, "chat_session", testutil.Cols{"id": uuid.NewString(), "workspace_id": b.WorkspaceID, "project_id": b.ProjectID})
	chatTask := fx.Insert(t, "agent_task_queue", testutil.Cols{"id": uuid.NewString(), "status": "running"})
	if _, _, err = c.Claim(ctx, chatTask, Context{WorkspaceID: b.WorkspaceID, ProjectID: b.ProjectID, ScopeKind: "chat_session", ScopeID: chat}, b); err != nil {
		t.Fatal(err)
	}
	fx.Exec(t, "UPDATE chat_session SET project_id=NULL WHERE id=$1", chat)
	if _, err = c.Resolve(ctx, b.WorkspaceID, b.ProjectID, chatTask); err == nil {
		t.Fatal("unset chat retained access")
	}
}
func TestCoordinatorMigrationOwnerAndExpiry(t *testing.T) {
	c, fx, s, b, _, task := coordinatorFixture(t)
	ctx := context.Background()
	b, _ = completeOperation(t, c, s, b, task, Operation{Action: "init", ExpectedBindingRevision: 1, Source: "fixture"})
	old := b
	b, r := completeOperation(t, c, s, b, "", Operation{Action: "migrate", ExpectedRevision: 1, ExpectedBindingRevision: 1, Destination: t.TempDir(), Source: "fixture migration"})
	if r.Status != "done" || b.Backend != "source" || b.Revision != 2 {
		t.Fatalf("migration: %+v %+v", b, r)
	}
	if _, err := s.Read(old); err != nil {
		t.Fatalf("rollback source lost: %v", err)
	}
	if _, err := s.Read(b); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Resolve(ctx, b.WorkspaceID, b.ProjectID, task); err == nil {
		t.Fatal("binding migration retained old task scope")
	}
	w, err := c.Submit(ctx, b.WorkspaceID, b.ProjectID, "", Operation{Action: "read", ExpectedRevision: b.ContentRevision, ExpectedBindingRevision: 2}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if next, err := c.Next(ctx, b.WorkspaceID, uuid.NewString()); err != nil || next != nil {
		t.Fatal("foreign owner claimed work")
	}
	fx.Exec(t, "UPDATE project_memory_request SET expires_at=now()-interval '1 second' WHERE id=$1", w.ID)
	r, err = c.Receipt(ctx, b.WorkspaceID, b.ProjectID, "", w.ID)
	if err != nil || r.Status != "unavailable" {
		t.Fatalf("offline: %+v %v", r, err)
	}
	if next, err := c.Next(ctx, b.WorkspaceID, b.OwnerDaemonID); err != nil || next != nil {
		t.Fatal("expired request claimed")
	}
	// Persisted pointer is independent of coordinator object lifetime.
	restarted := Coordinator{DB: fx.Pool}
	got, err := restarted.Resolve(ctx, b.WorkspaceID, b.ProjectID, "")
	if err != nil || got.Generation != b.Generation {
		t.Fatal("binding lost across restart")
	}
	raw, _ := json.Marshal(got)
	if len(raw) == 0 {
		t.Fatal("missing binding")
	}
}

func TestCoordinatorAtomicImportAndTaskScope(t *testing.T) {
	c, fx, s, b, _, task := coordinatorFixture(t)
	ctx := context.Background()
	b, _ = completeOperation(t, c, s, b, task, Operation{Action: "init", ExpectedBindingRevision: 1, Source: "fixture"})
	files := map[string]string{"README.md": "[evidence](evidence/source.json)", "evidence/source.json": "{\"gap\":\"historical text unavailable\"}", "issues.csv": "id,status\nfixture,open\n"}
	b, r := completeOperation(t, c, s, b, task, Operation{Action: "import", Files: files, Source: "fixture evidence", ExpectedBindingRevision: 1, ExpectedRevision: 1})
	if r.Status != "done" {
		t.Fatalf("import: %+v", r)
	}
	snap, err := s.Read(b)
	if err != nil || len(snap.Files) != 3 || snap.Files["README.md"] != files["README.md"] {
		t.Fatal("import altered evidence")
	}
	immutable := fx.Insert(t, "agent_task_queue", testutil.Cols{"id": uuid.NewString(), "status": "running"})
	if _, _, err = c.Claim(ctx, immutable, Context{WorkspaceID: b.WorkspaceID, ProjectID: b.ProjectID, ScopeKind: "task", ScopeID: immutable}, b); err != nil {
		t.Fatal(err)
	}
	if _, err = c.Resolve(ctx, b.WorkspaceID, b.ProjectID, immutable); err != nil {
		t.Fatal(err)
	}
	fx.Exec(t, "UPDATE agent_task_queue SET status='completed' WHERE id=$1", immutable)
	if _, err = c.Resolve(ctx, b.WorkspaceID, b.ProjectID, immutable); err == nil {
		t.Fatal("completed task retained memory access")
	}
}

// Directory-lock waits retain the claimed memory context before runTask reports running.
func TestCoordinatorWaitingTaskLifecycle(t *testing.T) {
	c, fx, s, b, issue, task := coordinatorFixture(t)
	ctx := context.Background()
	fx.Exec(t, "UPDATE agent_task_queue SET status='waiting_local_directory' WHERE id=$1", task)
	b, receipt := completeOperation(t, c, s, b, task, Operation{Action: "init", ExpectedBindingRevision: 1, Source: "waiting fixture"})
	if receipt.Status != "done" {
		t.Fatalf("waiting init: %+v", receipt)
	}
	for _, status := range []string{"waiting_local_directory", "dispatched", "running"} {
		fx.Exec(t, "UPDATE agent_task_queue SET status=$1 WHERE id=$2", status, task)
		if _, err := c.Resolve(ctx, b.WorkspaceID, b.ProjectID, task); err != nil {
			t.Fatalf("active %s: %v", status, err)
		}
	}
	for _, status := range []string{"queued", "completed", "failed", "cancelled"} {
		fx.Exec(t, "UPDATE agent_task_queue SET status=$1 WHERE id=$2", status, task)
		if _, err := c.Resolve(ctx, b.WorkspaceID, b.ProjectID, task); err == nil {
			t.Fatalf("inactive %s accepted", status)
		}
	}
	fx.Exec(t, "UPDATE agent_task_queue SET status='waiting_local_directory' WHERE id=$1", task)
	if _, err := c.Resolve(ctx, b.WorkspaceID, uuid.NewString(), task); err == nil {
		t.Fatal("cross-project wait accepted")
	}
	fx.Exec(t, "UPDATE issue SET project_id=$1 WHERE id=$2", uuid.NewString(), issue)
	if _, err := c.Resolve(ctx, b.WorkspaceID, b.ProjectID, task); err == nil {
		t.Fatal("changed project while waiting accepted")
	}
}
