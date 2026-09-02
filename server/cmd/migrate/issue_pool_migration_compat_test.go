package main

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestIssuePoolMigrationUpgradePaths runs the real 449-480 SQL through the
// three deployment shapes called out by TES-66. Each subtest has a private
// schema and ledger, so it never rewrites the developer or CI application
// schema.
func TestIssuePoolMigrationUpgradePaths(t *testing.T) {
	for _, path := range []string{"fresh_issue_pool", "d_drive_baseline", "applied_449_469"} {
		t.Run(path, func(t *testing.T) {
			pool, schema := newIssuePoolMigrationPool(t)
			ctx := context.Background()
			if _, err := pool.Exec(ctx, `CREATE TABLE autopilot (
				id UUID PRIMARY KEY DEFAULT gen_random_uuid(), execution_mode TEXT NOT NULL DEFAULT 'create_issue',
				CONSTRAINT autopilot_execution_mode_check CHECK (execution_mode IN ('create_issue','run_only')))`); err != nil {
				t.Fatalf("create baseline autopilot: %v", err)
			}
			if path == "d_drive_baseline" {
				if _, err := pool.Exec(ctx, `INSERT INTO autopilot(execution_mode) VALUES ('run_only')`); err != nil {
					t.Fatalf("seed D baseline row: %v", err)
				}
			}
			legacy := issuePoolMigrationFiles(t, 449, 469)
			execution := issuePoolMigrationFiles(t, 470, 480)
			opts := func(files []string) runOptions {
				return runOptions{
					Direction: "up", Files: files, SchemaMigrationsTable: schema + ".schema_migrations",
					AdvisoryLockKey: int64(rand.Uint64()&0x7fffffffffffffff) | 1, Hooks: preMigrationHooks,
				}
			}
			if path == "applied_449_469" {
				if err := runMigrations(ctx, pool, opts(legacy)); err != nil {
					t.Fatalf("legacy migrate: %v", err)
				}
				if _, err := pool.Exec(ctx, `WITH ap AS (
					INSERT INTO autopilot(execution_mode) VALUES ('issue_pool') RETURNING id
				), policy AS (
					INSERT INTO issue_pool_policy(autopilot_id,workspace_id,created_by_id)
					SELECT id,gen_random_uuid(),gen_random_uuid() FROM ap RETURNING *
				), cycle AS (
					INSERT INTO issue_pool_cycle(policy_id,autopilot_id,workspace_id,idempotency_key,status,policy_snapshot,created_by_id)
					SELECT id,autopilot_id,workspace_id,'legacy-direct','running','{}'::jsonb,created_by_id FROM policy RETURNING *
				)
				INSERT INTO issue_pool_item(cycle_id,policy_id,autopilot_id,workspace_id,issue_id,status,score,
					score_breakdown,selection_reasons,issue_snapshot,reviewer_id,reviewed_at,task_id)
				SELECT id,policy_id,autopilot_id,workspace_id,gen_random_uuid(),'running',1,'{}'::jsonb,'[]'::jsonb,
					'{}'::jsonb,created_by_id,now(),gen_random_uuid() FROM cycle`); err != nil {
					t.Fatalf("seed applied 449-469 direct-task row: %v", err)
				}
			} else {
				execution = append(legacy, execution...)
			}
			if err := runMigrations(ctx, pool, opts(execution)); err != nil {
				t.Fatalf("execution migrate: %v", err)
			}
			if err := runMigrations(ctx, pool, opts(execution)); err != nil {
				t.Fatalf("idempotent rerun: %v", err)
			}

			var modeOK, mappingOK, outboxOK, activeIndexOK bool
			if err := pool.QueryRow(ctx, `SELECT
				EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='issue_pool_policy' AND column_name='workflow_input_mapping'),
				to_regclass(current_schema()||'.issue_pool_notification_outbox') IS NOT NULL,
				to_regclass(current_schema()||'.idx_issue_pool_item_active_issue_v3') IS NOT NULL`).Scan(&mappingOK, &outboxOK, &activeIndexOK); err != nil {
				t.Fatalf("inspect upgraded schema: %v", err)
			}
			if _, err := pool.Exec(ctx, `INSERT INTO autopilot(execution_mode) VALUES ('issue_pool')`); err == nil {
				modeOK = true
			}
			if !modeOK || !mappingOK || !outboxOK || !activeIndexOK {
				t.Fatalf("upgrade invariants mode=%v mapping=%v outbox=%v active_index=%v", modeOK, mappingOK, outboxOK, activeIndexOK)
			}
			if path == "applied_449_469" {
				var status, failureCode string
				var workflowRunID *string
				if err := pool.QueryRow(ctx, `SELECT status,failure_code,workflow_run_id::text
					FROM issue_pool_item WHERE task_id IS NOT NULL`).Scan(&status, &failureCode, &workflowRunID); err != nil {
					t.Fatalf("inspect legacy direct-task conversion: %v", err)
				}
				if status != "deferred" || failureCode != "legacy_direct_task" || workflowRunID != nil {
					t.Fatalf("legacy direct-task row = status %q code %q workflow_run %v", status, failureCode, workflowRunID)
				}
			}
		})
	}
}

func issuePoolMigrationFiles(t *testing.T, first, last int) []string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read migrations: %v", err)
	}
	var files []string
	for _, entry := range entries {
		var version int
		if _, err := fmt.Sscanf(entry.Name(), "%d_", &version); err == nil && version >= first && version <= last && filepath.Ext(entry.Name()) == ".sql" && filepath.Ext(strings.TrimSuffix(entry.Name(), ".sql")) == ".up" {
			files = append(files, filepath.Join(dir, entry.Name()))
		}
	}
	sort.Strings(files)
	return files
}

func newIssuePoolMigrationPool(t *testing.T) (*pgxpool.Pool, string) {
	t.Helper()
	base := openTestPool(t)
	schema := fmt.Sprintf("issue_pool_upgrade_%d_%d", time.Now().UnixNano(), rand.Uint32())
	ident := pgx.Identifier{schema}.Sanitize()
	if _, err := base.Exec(context.Background(), "CREATE SCHEMA "+ident); err != nil {
		t.Fatal(err)
	}
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://multica:multica@localhost:5432/multica?sslmode=disable"
	}
	cfg, err := pgxpool.ParseConfig(dbURL)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := base.Exec(ctx, "DROP SCHEMA IF EXISTS "+ident+" CASCADE"); err != nil {
			t.Logf("cleanup schema: %v", err)
		}
	})
	return pool, schema
}
