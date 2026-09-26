package worksync_test

import (
	"context"
	"fmt"
	"github.com/jackc/pgx/v5"
	ws "github.com/multica-ai/multica/server/internal/worksync"
	"testing"
	"time"
)

func TestCenterMultirowWriterAgainstSingleRowWriter(t *testing.T) {
	for _, enrolled := range []bool{false, true} {
		name := "disabled"
		if enrolled {
			name = "enrolled"
		}
		t.Run(name, func(t *testing.T) {
			r := setup(t)
			a, b := r.fx.Issue(t, "a"), r.fx.Issue(t, "b")
			if enrolled {
				enroll(t, r)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			tx1, err := r.c.Pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx1.Rollback(context.Background())
			tx2, err := r.c.Pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx2.Rollback(context.Background())
			if _, err = tx1.Exec(ctx, `UPDATE issue SET title='first' WHERE id=$1`, a); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			pid := tx2.Conn().PgConn().PID()
			go func() {
				_, e := tx2.Exec(ctx, `UPDATE issue SET title='single' WHERE id=$1`, b)
				if e == nil {
					e = tx2.Commit(ctx)
				} else {
					_ = tx2.Rollback(ctx)
				}
				done <- e
			}()
			var secondErr error
			completed := false
			// Before the fix B waits in capture while holding b. With deferred
			// capture it can commit, since A has not entered its journal phase.
			for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
				select {
				case secondErr = <-done:
					completed = true
				default:
				}
				if completed {
					break
				}
				var waiting bool
				if err = r.c.Pool.QueryRow(ctx, `SELECT wait_event_type IS NOT DISTINCT FROM 'Lock' FROM pg_stat_activity WHERE pid=$1`, pid).Scan(&waiting); err != nil {
					t.Fatal(err)
				}
				if waiting {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			_, firstErr := tx1.Exec(ctx, `UPDATE issue SET title='multirow' WHERE id=$1`, b)
			if firstErr == nil {
				firstErr = tx1.Commit(ctx)
			} else {
				_ = tx1.Rollback(ctx)
			}
			if !completed {
				secondErr = <-done
			}
			if firstErr != nil || secondErr != nil {
				t.Fatalf("writers failed: multirow=%v single=%v", firstErr, secondErr)
			}
			if enrolled {
				changes := pull(t, r, 2, false)
				if len(changes.Records) != 3 {
					t.Fatalf("missing writes: %+v", changes)
				}
				for i, rec := range changes.Records {
					if rec.Version != int64(i+3) {
						t.Fatalf("non-contiguous journal: %+v", changes)
					}
				}
				if changes.Records[0].ID != b || string(changes.Records[0].Fields["title"]) != `"single"` || changes.Records[1].ID != a || changes.Records[2].ID != b || string(changes.Records[2].Fields["title"]) != `"multirow"` {
					t.Fatalf("wrong commit order: %+v", changes)
				}
			}
		})
	}
}

// A single-entity Push finishes its business write before flushing capture;
// an online transaction must still be able to continue to that same row.
func TestCenterMultirowWriterAgainstPush(t *testing.T) {
	r := setup(t)
	a, b := r.fx.Issue(t, "a"), r.fx.Issue(t, "b")
	enroll(t, r)
	rep, cfg := replica(t, r, "a")
	syncReplica(t, r, rep)
	op := queue(t, rep, "issue", b, "title", "pushed")
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	tx, err := r.c.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, `UPDATE issue SET title='first' WHERE id=$1`, a); err != nil {
		t.Fatal(err)
	}
	pidCh := make(chan uint32, 1)
	authorize := r.c.Authorize
	r.c.Authorize = func(ctx context.Context, tx pgx.Tx, p ws.Principal, s ws.Scope, action string, op *ws.Operation) error {
		if action == "write" {
			pidCh <- tx.Conn().PgConn().PID()
		}
		return authorize(ctx, tx, p, s, action, op)
	}
	done := make(chan error, 1)
	go func() {
		receipt, e := r.c.Push(ctx, cfg.Principal, r.scope, op)
		if e == nil && (receipt.Status != "applied" || receipt.Record.Version != 3) {
			e = fmt.Errorf("unexpected receipt: %+v", receipt)
		}
		done <- e
	}()
	pid := <-pidCh
	completed := false
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		select {
		case err = <-done:
			completed = true
		default:
		}
		if completed {
			break
		}
		var waiting bool
		if err = r.c.Pool.QueryRow(ctx, `SELECT wait_event_type IS NOT DISTINCT FROM 'Lock' FROM pg_stat_activity WHERE pid=$1`, pid).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	_, writeErr := tx.Exec(ctx, `UPDATE issue SET title='online' WHERE id=$1`, b)
	if writeErr == nil {
		writeErr = tx.Commit(ctx)
	} else {
		_ = tx.Rollback(ctx)
	}
	if !completed {
		err = <-done
	}
	if err != nil || writeErr != nil {
		t.Fatalf("push=%v online=%v", err, writeErr)
	}
	changes := pull(t, r, 2, false)
	if len(changes.Records) != 3 || changes.Records[0].ID != b || changes.Records[1].ID != a || changes.Records[2].ID != b || changes.Cursor != 5 {
		t.Fatalf("missing/order of changes: %+v", changes)
	}
}

func TestCenterCrossScopeCommitLockOrder(t *testing.T) {
	r, other := setup(t), setup(t)
	a, b := r.fx.Issue(t, "a"), r.fx.Issue(t, "b")
	c, d := other.fx.Issue(t, "c"), other.fx.Issue(t, "d")
	enroll(t, r)
	enroll(t, other)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	tx1, err := r.c.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx1.Rollback(context.Background())
	tx2, err := r.c.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx2.Rollback(context.Background())
	// Opposite workspace order, disjoint business rows. Only the journal
	// scopes overlap, so commit must sort them regardless of event order.
	for _, write := range []struct {
		tx pgx.Tx
		id string
	}{{tx1, a}, {tx2, c}, {tx1, d}, {tx2, b}} {
		if _, err = write.tx.Exec(ctx, `UPDATE issue SET title='changed' WHERE id=$1`, write.id); err != nil {
			t.Fatal(err)
		}
	}
	done := make(chan error, 1)
	go func() { done <- tx1.Commit(ctx) }()
	err2 := tx2.Commit(ctx)
	if err1 := <-done; err1 != nil || err2 != nil {
		t.Fatalf("commits: %v / %v", err1, err2)
	}
	for _, rig := range []rig{r, other} {
		changes := pull(t, rig, 2, false)
		if len(changes.Records) != 2 || changes.Cursor != 4 {
			t.Fatalf("missing scope changes: %+v", changes)
		}
	}
}

func TestCenterEnrollCannotRecreateDeletedWorkspace(t *testing.T) {
	r := setup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	tx, err := r.c.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, `SELECT id FROM workspace WHERE id=$1 FOR UPDATE`, r.scope.Workspace); err != nil {
		t.Fatal(err)
	}
	pidCh := make(chan uint32, 1)
	authorize := r.c.Authorize
	r.c.Authorize = func(ctx context.Context, tx pgx.Tx, p ws.Principal, s ws.Scope, action string, op *ws.Operation) error {
		err := authorize(ctx, tx, p, s, action, op)
		pidCh <- tx.Conn().PgConn().PID()
		return err
	}
	done := make(chan error, 1)
	go func() { done <- r.c.Enroll(ctx, r.principal, r.scope) }()
	pid := <-pidCh
	waiting := false
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		if err = r.c.Pool.QueryRow(ctx, `SELECT wait_event_type IS NOT DISTINCT FROM 'Lock' FROM pg_stat_activity WHERE pid=$1`, pid).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !waiting {
		t.Fatal("enroll did not wait on workspace fence")
	}
	if _, err = tx.Exec(ctx, `DELETE FROM member WHERE workspace_id=$1`, r.scope.Workspace); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `DELETE FROM workspace WHERE id=$1`, r.scope.Workspace); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-done; err == nil {
		t.Fatal("enrollment recreated deleted workspace scope")
	}
	if n := r.fx.Count(t, `SELECT count(*) FROM work_sync_scope WHERE workspace_id=$1`, r.scope.Workspace); n != 0 {
		t.Fatalf("orphan scopes: %d", n)
	}
}
