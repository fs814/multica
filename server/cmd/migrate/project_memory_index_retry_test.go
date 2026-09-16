package main

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type concurrentIndexRetryCase struct{ version, index, table, seed string }

// Exercise shipped SQL and production hooks, including real backend cancellation.
func TestProjectMemoryConcurrentIndexRetry(t *testing.T) {
	const id = "'11111111-1111-4111-8111-111111111111'"
	cases := []concurrentIndexRetryCase{
		{"518_project_memory_binding_key", "project_memory_binding_key", "project_memory_binding", "INSERT INTO project_memory_binding (workspace_id,project_id) VALUES (" + id + "," + id + ")"},
		{"519_project_memory_scope_key", "project_memory_scope_key", "project_memory_scope", "INSERT INTO project_memory_scope (workspace_id,scope_kind,scope_id) VALUES (" + id + ",'project'," + id + ")"},
		{"520_project_memory_task_key", "project_memory_task_key", "project_memory_task", "INSERT INTO project_memory_task (task_id,workspace_id,context) VALUES (" + id + "," + id + ",'{}')"},
		{"521_project_memory_request_key", "project_memory_request_key", "project_memory_request", "INSERT INTO project_memory_request (id,workspace_id,project_id,owner_daemon_id,request) VALUES (" + id + "," + id + "," + id + "," + id + ",'{}')"},
	}
	testConcurrentIndexRetry(t, cases, readIndexFixtureMigration(t, "517_project_memory"))
}

func readIndexFixtureMigration(t *testing.T, version string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "migrations", version+".up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func testConcurrentIndexRetry(t *testing.T, cases []concurrentIndexRetryCase, ddl string) {
	t.Helper()
	base := openTestPool(t)
	for _, tc := range cases {
		for _, failure := range []string{"duplicate", "cancelled", "ledger"} {
			if failure == "duplicate" && !strings.Contains(readIndexFixtureMigration(t, tc.version), "CREATE UNIQUE INDEX") {
				continue
			}
			t.Run(tc.version+"/"+failure, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				schema := fmt.Sprintf("memory_retry_%d_%d", time.Now().UnixNano(), rand.Uint32())
				if _, err := base.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
					t.Fatal(err)
				}
				defer func() {
					cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
					defer stop()
					if _, err := base.Exec(cleanup, "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE"); err != nil {
						t.Error(err)
					}
				}()
				cfg := base.Config()
				cfg.ConnConfig.RuntimeParams["search_path"] = schema
				pool, err := pgxpool.NewWithConfig(ctx, cfg)
				if err != nil {
					t.Fatal(err)
				}
				defer pool.Close()
				if _, err = pool.Exec(ctx, string(ddl)); err != nil {
					t.Fatal(err)
				}
				if _, err = pool.Exec(ctx, tc.seed); err != nil {
					t.Fatal(err)
				}
				body, err := os.ReadFile(filepath.Join("..", "..", "migrations", tc.version+".up.sql"))
				if err != nil {
					t.Fatal(err)
				}
				file := filepath.Join(t.TempDir(), tc.version+".up.sql")
				if err = os.WriteFile(file, body, 0600); err != nil {
					t.Fatal(err)
				}
				opts := runOptions{Direction: "up", Files: []string{file}, SchemaMigrationsTable: schema + ".schema_migrations", AdvisoryLockKey: int64(rand.Uint64()&0x7fffffffffffffff) | 1, Hooks: preMigrationHooks, Conditions: upMigrationConditions}
				if failure == "duplicate" {
					if _, err = pool.Exec(ctx, tc.seed); err != nil {
						t.Fatal(err)
					}
					err = runMigrations(ctx, pool, opts)
				} else if failure == "ledger" {
					empty := opts
					empty.Files = nil
					if err = runMigrations(ctx, pool, empty); err != nil {
						t.Fatal(err)
					}
					if _, err = pool.Exec(ctx, "CREATE FUNCTION reject_ledger() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected ledger write failure'; END $$; CREATE TRIGGER reject_ledger BEFORE INSERT ON schema_migrations FOR EACH ROW EXECUTE FUNCTION reject_ledger()"); err != nil {
						t.Fatal(err)
					}
					err = runMigrations(ctx, pool, opts)
				} else {
					tx, txErr := pool.Begin(ctx)
					if txErr != nil {
						t.Fatal(txErr)
					}
					defer tx.Rollback(context.Background())
					if _, txErr = tx.Exec(ctx, "UPDATE "+tc.table+" SET workspace_id=workspace_id"); txErr != nil {
						t.Fatal(txErr)
					}
					result := make(chan error, 1)
					go func() { result <- runMigrations(ctx, pool, opts) }()
					ticker := time.NewTicker(20 * time.Millisecond)
					defer ticker.Stop()
					for {
						var pid int32
						e := pool.QueryRow(ctx, "SELECT pid FROM pg_stat_progress_create_index WHERE index_relid=to_regclass($1) AND phase='waiting for writers before build'", schema+"."+tc.index).Scan(&pid)
						if e == nil {
							var cancelled bool
							if e = pool.QueryRow(ctx, "SELECT pg_cancel_backend($1)", pid).Scan(&cancelled); e != nil || !cancelled {
								t.Fatalf("cancel build: %v, %v", cancelled, e)
							}
							break
						}
						if e != pgx.ErrNoRows {
							t.Fatal(e)
						}
						select {
						case early := <-result:
							t.Fatalf("build exited before cancellation: %v", early)
						case <-ctx.Done():
							t.Fatal("build never reached writer wait")
						case <-ticker.C:
						}
					}
					select {
					case err = <-result:
					case <-ctx.Done():
						t.Fatal("cancelled runner did not exit")
					}
					if txErr = tx.Rollback(ctx); txErr != nil {
						t.Fatal(txErr)
					}
				}
				if err == nil {
					t.Fatal("initial migration unexpectedly succeeded")
				}
				t.Logf("injected %s: %v", failure, err)
				code := map[string]string{"duplicate": "23505", "cancelled": "57014", "ledger": "P0001"}[failure]
				if !strings.Contains(err.Error(), code) {
					t.Fatalf("unexpected failure: %v", err)
				}
				assertIndexValidity(t, pool, schema, tc.index, failure == "ledger")
				var originalOID uint32
				if err = pool.QueryRow(ctx, "SELECT to_regclass($1)::oid", schema+"."+tc.index).Scan(&originalOID); err != nil {
					t.Fatal(err)
				}
				var count int
				ledger := pgx.Identifier{schema, "schema_migrations"}.Sanitize()
				if err = pool.QueryRow(ctx, "SELECT count(*) FROM "+ledger+" WHERE version=$1", tc.version).Scan(&count); err != nil || count != 0 {
					t.Fatalf("failed ledger count %d: %v", count, err)
				}
				if failure == "ledger" {
					if _, err = pool.Exec(ctx, "DROP TRIGGER reject_ledger ON schema_migrations"); err != nil {
						t.Fatal(err)
					}
				}
				if failure == "duplicate" {
					if _, err = pool.Exec(ctx, "DELETE FROM "+tc.table+" WHERE ctid=(SELECT ctid FROM "+tc.table+" LIMIT 1)"); err != nil {
						t.Fatal(err)
					}
				}
				if err = runMigrations(ctx, pool, opts); err != nil {
					t.Fatalf("retry: %v", err)
				}
				var recoveredOID uint32
				if err = pool.QueryRow(ctx, "SELECT to_regclass($1)::oid", schema+"."+tc.index).Scan(&recoveredOID); err != nil {
					t.Fatal(err)
				}
				if (failure == "ledger") != (originalOID == recoveredOID) {
					t.Fatalf("unexpected OID recovery: %d -> %d", originalOID, recoveredOID)
				}
				var valid, ready bool
				if err = pool.QueryRow(ctx, "SELECT indisvalid,indisready FROM pg_index WHERE indexrelid=to_regclass($1)", schema+"."+tc.index).Scan(&valid, &ready); err != nil || !valid || !ready {
					t.Fatalf("valid/ready=%v/%v: %v", valid, ready, err)
				}
				if err = pool.QueryRow(ctx, "SELECT count(*) FROM "+ledger+" WHERE version=$1", tc.version).Scan(&count); err != nil || count != 1 {
					t.Fatalf("retry ledger count %d: %v", count, err)
				}
				if err = runMigrations(ctx, pool, opts); err != nil {
					t.Fatalf("idempotent rerun: %v", err)
				}
			})
		}
	}
}
