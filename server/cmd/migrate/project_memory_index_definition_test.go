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

type concurrentIndexDefinitionCase struct{ version, index, table, columns string }

func TestProjectMemoryIndexRejectsMismatchedDefinition(t *testing.T) {
	cases := []concurrentIndexDefinitionCase{
		{"518_project_memory_binding_key", "project_memory_binding_key", "project_memory_binding", "workspace_id, project_id"},
		{"519_project_memory_scope_key", "project_memory_scope_key", "project_memory_scope", "workspace_id, scope_kind, scope_id"},
		{"520_project_memory_task_key", "project_memory_task_key", "project_memory_task", "task_id"},
		{"521_project_memory_request_key", "project_memory_request_key", "project_memory_request", "id"},
	}
	testConcurrentIndexDefinitions(t, cases, readIndexFixtureMigration(t, "517_project_memory"))
}

func testConcurrentIndexDefinitions(t *testing.T, cases []concurrentIndexDefinitionCase, ddl string) {
	t.Helper()
	base := openTestPool(t)
	for _, tc := range cases {
		sql := readIndexFixtureMigration(t, tc.version)
		unique := strings.Contains(sql, "CREATE UNIQUE INDEX")
		predicate := ""
		if i := strings.Index(sql, " WHERE "); i >= 0 {
			predicate = strings.TrimSuffix(strings.TrimSpace(sql[i:]), ";")
		}
		for _, variant := range []string{"nonunique", "columns", "descending", "partial", "expression", "nulls_not_distinct", "wrong_table", "wrong_schema", "predicate_removed"} {
			if variant == "nulls_not_distinct" && !unique {
				continue
			}
			if variant == "predicate_removed" && predicate == "" {
				continue
			}
			t.Run(tc.version+"/"+variant, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				schema := fmt.Sprintf("memory_shape_%d_%d", time.Now().UnixNano(), rand.Uint32())
				shadow := schema + "_shadow"
				for _, s := range []string{schema, shadow} {
					if _, err := base.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{s}.Sanitize()); err != nil {
						t.Fatal(err)
					}
					defer func(name string) {
						cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
						defer stop()
						if _, err := base.Exec(cleanup, "DROP SCHEMA "+pgx.Identifier{name}.Sanitize()+" CASCADE"); err != nil {
							t.Error(err)
						}
					}(s)
				}
				cfg := base.Config()
				cfg.ConnConfig.RuntimeParams["search_path"] = shadow + "," + schema
				pool, err := pgxpool.NewWithConfig(ctx, cfg)
				if err != nil {
					t.Fatal(err)
				}
				defer pool.Close()
				// Keep the target tables in the second search_path schema, while a same
				// named index in the first schema must not satisfy this migration.
				setup, err := pool.Acquire(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = setup.Exec(ctx, "SET search_path TO "+schema); err != nil {
					t.Fatal(err)
				}
				if _, err = setup.Exec(ctx, string(ddl)); err != nil {
					t.Fatal(err)
				}
				if _, err = setup.Exec(ctx, "SET search_path TO "+shadow+","+schema); err != nil {
					t.Fatal(err)
				}
				setup.Release()
				kind := "CREATE INDEX CONCURRENTLY "
				if unique {
					kind = "CREATE UNIQUE INDEX CONCURRENTLY "
				}
				prefix := kind + tc.index + " ON " + schema + "." + tc.table
				columns := tc.columns
				suffix := predicate
				switch variant {
				case "nonunique":
					otherKind := "CREATE UNIQUE INDEX CONCURRENTLY "
					if unique {
						otherKind = "CREATE INDEX CONCURRENTLY "
					}
					prefix = otherKind + tc.index + " ON " + schema + "." + tc.table
				case "columns":
					columns = "workspace_id, workspace_id"
				case "descending":
					columns = strings.ReplaceAll(tc.columns, ",", " DESC,") + " DESC"
				case "partial":
					suffix = " WHERE workspace_id IS NOT NULL"
				case "expression":
					columns = "(workspace_id::text)"
				case "nulls_not_distinct":
					suffix = " NULLS NOT DISTINCT" + predicate
				case "predicate_removed":
					suffix = ""
				case "wrong_table", "wrong_schema":
					otherSchema := schema
					if variant == "wrong_schema" {
						otherSchema = shadow
					}
					if _, err = pool.Exec(ctx, "CREATE TABLE "+otherSchema+".other_table (LIKE "+schema+"."+tc.table+")"); err != nil {
						t.Fatal(err)
					}
					prefix = kind + tc.index + " ON " + otherSchema + ".other_table"
				}
				if _, err = pool.Exec(ctx, prefix+" ("+columns+")"+suffix); err != nil {
					t.Fatal(err)
				}
				var original uint32
				if err = pool.QueryRow(ctx, "SELECT to_regclass($1)::oid", tc.index).Scan(&original); err != nil {
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
				err = runMigrations(ctx, pool, opts)
				if err == nil || !strings.Contains(err.Error(), "definition mismatch") {
					t.Fatalf("expected definition rejection, got %v", err)
				}
				var count int
				if err = pool.QueryRow(ctx, "SELECT count(*) FROM "+schema+".schema_migrations WHERE version=$1", tc.version).Scan(&count); err != nil || count != 0 {
					t.Fatalf("wrong ledger count %d: %v", count, err)
				}
				var after uint32
				if err = pool.QueryRow(ctx, "SELECT to_regclass($1)::oid", tc.index).Scan(&after); err != nil || after != original {
					t.Fatalf("mismatched valid index changed: %v", err)
				}
			})
		}
	}
}
