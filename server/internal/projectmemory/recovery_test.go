package projectmemory

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestProjectMemoryRecoveryProcess(t *testing.T) {
	path := os.Getenv("MEMORY_RECOVERY_FIXTURE")
	if path == "" {
		return
	}
	var spec struct {
		Store   Store
		Work    Work
		Binding Binding
		Result  string
		Read    bool
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}
	var result any
	if spec.Read {
		snap, err := spec.Store.Read(spec.Binding)
		if err != nil {
			t.Fatal(err)
		}
		result = snap
	} else {
		result = Execute(spec.Store, spec.Work)
	}
	raw, err = json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(spec.Result, raw, 0600); err != nil {
		t.Fatal(err)
	}
	// Deliberate abrupt process exit after the durable file, before any callback.
	if !spec.Read {
		os.Exit(23)
	}
}
func TestCoordinatorProcessRecoveryMigrationRollback(t *testing.T) {
	c, fx, s, b, _, task := coordinatorFixture(t)
	ctx := context.Background()
	b, _ = completeOperation(t, c, s, b, task, Operation{Action: "init", ExpectedBindingRevision: 1, Source: "recovery fixture"})
	b, _ = completeOperation(t, c, s, b, task, Operation{Action: "write", Path: "README.md", Content: "accepted", Source: "recovery fixture", ExpectedRevision: b.ContentRevision, ExpectedBindingRevision: b.Revision})
	original := b
	process := func(work Work, selected Binding, read bool) []byte {
		t.Helper()
		root := t.TempDir()
		output := filepath.Join(root, "result.json")
		input := filepath.Join(root, "input.json")
		raw, _ := json.Marshal(map[string]any{"Store": s, "Work": work, "Binding": selected, "Result": output, "Read": read})
		if err := os.WriteFile(input, raw, 0600); err != nil {
			t.Fatal(err)
		}
		exe, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(exe, "-test.run=^TestProjectMemoryRecoveryProcess$")
		cmd.Env = append(os.Environ(), "MEMORY_RECOVERY_FIXTURE="+input)
		logs, err := cmd.CombinedOutput()
		if read && err != nil {
			t.Fatalf("read process %v %s", err, logs)
		}
		if !read {
			exit, ok := err.(*exec.ExitError)
			if !ok || exit.ExitCode() != 23 {
				t.Fatalf("expected injected crash: %v %s", err, logs)
			}
		}
		raw, err = os.ReadFile(output)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	w, err := c.Submit(ctx, b.WorkspaceID, b.ProjectID, "", Operation{Action: "write", Path: "README.md", Content: "unpublished", Source: "crash fixture", ExpectedRevision: b.ContentRevision, ExpectedBindingRevision: b.Revision}, nil)
	if err != nil {
		t.Fatal(err)
	}
	next, err := c.Next(ctx, b.WorkspaceID, b.OwnerDaemonID)
	if err != nil || next == nil {
		t.Fatal(err)
	}
	var staged Result
	if err = json.Unmarshal(process(*next, b, false), &staged); err != nil || staged.Error != "" {
		t.Fatalf("stage %v %+v", err, staged)
	}
	current, err := c.Resolve(ctx, b.WorkspaceID, b.ProjectID, "")
	if err != nil || current.Generation != b.Generation {
		t.Fatal("crashed candidate became visible")
	}
	fx.Exec(t, "UPDATE project_memory_request SET expires_at=now()-interval '1 second' WHERE id=$1", w.ID)
	if err = c.Complete(ctx, b.WorkspaceID, b.OwnerDaemonID, w.ID, staged); err != nil {
		t.Fatal(err)
	}
	receipt, err := c.Receipt(ctx, b.WorkspaceID, b.ProjectID, "", w.ID)
	if err != nil || receipt.Status != "failed" {
		t.Fatal("expired callback published")
	}
	// Explicit retry creates a new revision; the orphan remains unselected.
	b, _ = completeOperation(t, c, s, b, "", Operation{Action: "write", Path: "README.md", Content: "updated", Source: "retry fixture", ExpectedRevision: b.ContentRevision, ExpectedBindingRevision: b.Revision})
	var read Snapshot
	if err = json.Unmarshal(process(Work{}, b, true), &read); err != nil || read.Files["README.md"] != "updated" {
		t.Fatal("restart lost publication")
	}
	// Forward and reverse storage migration; source authority is a bound root,
	// not any similarly named disposable execution worktree.
	source := t.TempDir()
	worktree := t.TempDir()
	sentinel := filepath.Join(worktree, "sentinel")
	_ = os.WriteFile(sentinel, []byte("untouched"), 0600)
	b, _ = completeOperation(t, c, s, b, "", Operation{Action: "migrate", Destination: source, Source: "to source", ExpectedRevision: b.ContentRevision, ExpectedBindingRevision: b.Revision})
	sourceBinding := b
	b, _ = completeOperation(t, c, s, b, "", Operation{Action: "migrate", Source: "to managed", ExpectedRevision: b.ContentRevision, ExpectedBindingRevision: b.Revision})
	if b.Backend != "managed" || b.Revision != 3 {
		t.Fatalf("reverse migration %+v", b)
	}
	if _, err = s.Read(sourceBinding); err != nil {
		t.Fatal("source rollback evidence removed")
	}
	old, err := s.Read(original)
	if err != nil {
		t.Fatal(err)
	}
	b, _ = completeOperation(t, c, s, b, "", Operation{Action: "import", Files: old.Files, Source: "explicit rollback from original digest " + original.Digest, ExpectedRevision: b.ContentRevision, ExpectedBindingRevision: b.Revision})
	if err = json.Unmarshal(process(Work{}, b, true), &read); err != nil || read.Files["README.md"] != "accepted" {
		t.Fatal("rollback did not restore bytes")
	}
	if raw, err := os.ReadFile(sentinel); err != nil || string(raw) != "untouched" {
		t.Fatal("execution worktree modified")
	}
	// A missing selected generation is surfaced; no older generation is silently selected.
	ns, err := s.namespace(b)
	if err != nil {
		t.Fatal(err)
	}
	selected := filepath.Join(ns, "versions", b.Generation, "snapshot.json")
	if err = os.Rename(selected, selected+".backup"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Read(b); err == nil {
		t.Fatal("missing generation silently recovered")
	}
	if err = os.Rename(selected+".backup", selected); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Read(b); err != nil {
		t.Fatal("restore failed")
	}
	t.Log("abrupt process exit before callback, stale callback, explicit retry, fresh-process read, managed/source/managed migration, CAS content rollback and missing-file restore passed")
}

func TestCoordinatorTaskFinishPublicationRace(t *testing.T) {
	for _, finishFirst := range []bool{true, false} {
		name := "publication_first"
		if finishFirst {
			name = "finish_first"
		}
		t.Run(name, func(t *testing.T) {
			c, fx, s, b, _, task := coordinatorFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			b, _ = completeOperation(t, c, s, b, task, Operation{Action: "init", ExpectedBindingRevision: 1, Source: "race fixture"})
			w, err := c.Submit(ctx, b.WorkspaceID, b.ProjectID, task, Operation{Action: "write", Path: "README.md", Content: "race", Source: "race fixture", ExpectedRevision: b.ContentRevision, ExpectedBindingRevision: b.Revision}, nil)
			if err != nil {
				t.Fatal(err)
			}
			next, err := c.Next(ctx, b.WorkspaceID, b.OwnerDaemonID)
			if err != nil || next == nil {
				t.Fatal(err)
			}
			result := Execute(s, *next)
			if finishFirst {
				tx, err := fx.Pool.Begin(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback(context.Background())
				if _, err = tx.Exec(ctx, "UPDATE agent_task_queue SET status='completed' WHERE id=$1", task); err != nil {
					t.Fatal(err)
				}
				done := make(chan error, 1)
				go func() { done <- c.Complete(ctx, b.WorkspaceID, b.OwnerDaemonID, w.ID, result) }()
				select {
				case err := <-done:
					t.Fatalf("publication passed uncommitted finish %v", err)
				case <-time.After(80 * time.Millisecond):
				}
				if err = tx.Commit(ctx); err != nil {
					t.Fatal(err)
				}
				if err = <-done; err != nil {
					t.Fatal(err)
				}
				current, err := c.Resolve(ctx, b.WorkspaceID, b.ProjectID, "")
				if err != nil || current.ContentRevision != b.ContentRevision {
					t.Fatal("finished task published")
				}

			} else {
				// Pause real Complete after authorize has locked the task but before its
				// binding UPDATE can commit, using a fixture-only PostgreSQL trigger.
				fx.Exec(t, `CREATE FUNCTION pause_memory_publication() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_advisory_xact_lock(817031); RETURN NEW; END $$`)
				fx.Exec(t, `CREATE TRIGGER pause_memory_publication BEFORE UPDATE ON project_memory_binding FOR EACH ROW EXECUTE FUNCTION pause_memory_publication()`)
				gate, err := fx.Pool.Begin(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer gate.Rollback(context.Background())
				var pid int32
				if err = gate.QueryRow(ctx, "SELECT pg_backend_pid() FROM pg_advisory_xact_lock(817031)").Scan(&pid); err != nil {
					t.Fatal(err)
				}
				published := make(chan error, 1)
				go func() { published <- c.Complete(ctx, b.WorkspaceID, b.OwnerDaemonID, w.ID, result) }()
				deadline := time.Now().Add(3 * time.Second)
				for {
					var waiting bool
					err = fx.Pool.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)))", pid).Scan(&waiting)
					if err != nil {
						t.Fatal(err)
					}
					if waiting {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("publication did not reach gate")
					}
					time.Sleep(10 * time.Millisecond)
				}
				finished := make(chan error, 1)
				go func() {
					_, err := fx.Pool.Exec(ctx, "UPDATE agent_task_queue SET status='completed' WHERE id=$1", task)
					finished <- err
				}()
				select {
				case err := <-finished:
					t.Fatalf("finish escaped actual publication lock: %v", err)
				case <-time.After(80 * time.Millisecond):
				}
				if err = gate.Commit(ctx); err != nil {
					t.Fatal(err)
				}
				if err = <-published; err != nil {
					t.Fatal(err)
				}
				if err = <-finished; err != nil {
					t.Fatal(err)
				}
				current, err := c.Resolve(ctx, b.WorkspaceID, b.ProjectID, "")
				if err != nil || current.ContentRevision != b.ContentRevision+1 {
					t.Fatal("publication-first lost its committed revision")
				}

			}
		})
	}
}
