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

// TestIssuePoolMigrationUpgradePaths runs the production migrator against the
// three supported deployment ledgers. Each path has a private schema and a
// real, non-empty schema_migrations table.
func TestIssuePoolMigrationUpgradePaths(t *testing.T) {
	for _, path := range []string{"fresh", "d_drive_1_283", "legacy_449_469"} {
		t.Run(path, func(t *testing.T) {
			pool, schema := newIssuePoolMigrationPool(t)
			ctx := context.Background()
			opts := func(files []string) runOptions {
				return runOptions{
					Direction: "up", Files: files, SchemaMigrationsTable: schema + ".schema_migrations",
					AdvisoryLockKey: int64(rand.Uint64()&0x7fffffffffffffff) | 1, Hooks: preMigrationHooks,
				}
			}

			var bootstrap, upgrade []string
			switch path {
			case "fresh":
				upgrade = issuePoolMigrationFiles(t, 1, 490)
			case "d_drive_1_283":
				bootstrap = issuePoolMigrationFiles(t, 1, 283)
				upgrade = append(issuePoolMigrationFiles(t, 449, 458), issuePoolMigrationFiles(t, 470, 490)...)
			case "legacy_449_469":
				bootstrap = append(issuePoolMigrationFiles(t, 1, 283), issuePoolMigrationFiles(t, 449, 458)...)
				upgrade = issuePoolMigrationFiles(t, 470, 490)
			}
			if len(bootstrap) > 0 {
				if err := runMigrations(ctx, pool, opts(bootstrap)); err != nil {
					t.Fatalf("bootstrap real schema: %v", err)
				}
			}
			if path == "legacy_449_469" {
				installLegacyIssuePool469Fixture(t, ctx, pool)
			}
			if err := runMigrations(ctx, pool, opts(upgrade)); err != nil {
				t.Fatalf("formal upgrade: %v", err)
			}
			if err := runMigrations(ctx, pool, opts(upgrade)); err != nil {
				t.Fatalf("idempotent rerun: %v", err)
			}

			var ledgerCount int
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&ledgerCount); err != nil || ledgerCount == 0 {
				t.Fatalf("non-empty migration ledger: count=%d err=%v", ledgerCount, err)
			}
			var modeOK, mappingOK, outboxPK, activeIndexOK, inboxDedupeOK, oldIndexesGone bool
			if err := pool.QueryRow(ctx, `SELECT
				EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='issue_pool_policy' AND column_name='workflow_input_mapping'),
				EXISTS(SELECT 1 FROM pg_constraint WHERE conrelid='issue_pool_notification_outbox'::regclass AND contype='p'),
				to_regclass(current_schema()||'.idx_issue_pool_item_active_issue_v3') IS NOT NULL,
				to_regclass(current_schema()||'.idx_inbox_issue_pool_notification_dedupe') IS NOT NULL,
				to_regclass(current_schema()||'.issue_pool_item_active_issue_v2_key') IS NULL
					AND to_regclass(current_schema()||'.idx_issue_pool_item_active_issue_v2') IS NULL
					AND to_regclass(current_schema()||'.issue_pool_item_reconcile_index') IS NULL
					AND to_regclass(current_schema()||'.issue_pool_cycle_autopilot_run_key') IS NULL
					AND to_regclass(current_schema()||'.idx_issue_pool_outbox_id') IS NULL`).
				Scan(&mappingOK, &outboxPK, &activeIndexOK, &inboxDedupeOK, &oldIndexesGone); err != nil {
				t.Fatalf("inspect upgraded schema: %v", err)
			}
			if err := pool.QueryRow(ctx, `SELECT EXISTS(
				SELECT 1 FROM pg_constraint
				WHERE conrelid='autopilot'::regclass AND conname='autopilot_execution_mode_check'
				  AND pg_get_constraintdef(oid) LIKE '%issue_pool%'
			)`).Scan(&modeOK); err != nil {
				t.Fatalf("inspect execution mode constraint: %v", err)
			}
			var effectiveStatusOK, reviewInvariantOK bool
			if err := pool.QueryRow(ctx, `SELECT
				to_regprocedure('public.issue_effective_status(uuid,text)') IS NOT NULL,
				EXISTS(SELECT 1 FROM pg_constraint WHERE conrelid='issue_pool_item'::regclass
					AND conname='issue_pool_item_review_check'
					AND pg_get_constraintdef(oid) LIKE '%deferred%')`).Scan(&effectiveStatusOK, &reviewInvariantOK); err != nil {
				t.Fatalf("inspect recovery compatibility: %v", err)
			}
			if !modeOK || !mappingOK || !outboxPK || !activeIndexOK || !inboxDedupeOK || !oldIndexesGone || !effectiveStatusOK || !reviewInvariantOK {
				t.Fatalf("upgrade invariants mode=%v mapping=%v outbox_pk=%v active_index=%v inbox_dedupe=%v old_indexes_gone=%v effective_status=%v review_invariant=%v",
					modeOK, mappingOK, outboxPK, activeIndexOK, inboxDedupeOK, oldIndexesGone, effectiveStatusOK, reviewInvariantOK)
			}
			if path == "legacy_449_469" {
				var blocked, audited, migratedNotices int
				if err := pool.QueryRow(ctx, `SELECT
					count(*) FILTER (WHERE status='blocked' AND failure_code='legacy_execution_unmappable')::int,
					count(*) FILTER (WHERE failure_detail ? 'legacy_execution')::int
					FROM issue_pool_item`).Scan(&blocked, &audited); err != nil {
					t.Fatal(err)
				}
				if err := pool.QueryRow(ctx, `SELECT count(*)::int FROM issue_pool_notification_outbox
					WHERE delivered_at IS NOT NULL AND payload->>'migrated_from'='issue_pool_notification'`).Scan(&migratedNotices); err != nil {
					t.Fatal(err)
				}
				if blocked != 2 || audited != 2 || migratedNotices != 1 {
					t.Fatalf("legacy mapping blocked=%d audited=%d notices=%d, want 2/2/1", blocked, audited, migratedNotices)
				}
				var legacyTableGone bool
				if err := pool.QueryRow(ctx, `SELECT to_regclass(current_schema()||'.issue_pool_notification') IS NULL`).Scan(&legacyTableGone); err != nil || !legacyTableGone {
					t.Fatalf("legacy notification disposition gone=%v err=%v", legacyTableGone, err)
				}
			}
		})
	}
}

func TestIssuePoolOutboxPrimaryKeyRecoversInvalidConcurrentIndex(t *testing.T) {
	pool, schema := newIssuePoolMigrationPool(t)
	ctx := context.Background()
	opts := func(files []string) runOptions {
		return runOptions{
			Direction: "up", Files: files, SchemaMigrationsTable: schema + ".schema_migrations",
			AdvisoryLockKey: int64(rand.Uint64()&0x7fffffffffffffff) | 1, Hooks: preMigrationHooks,
		}
	}

	if err := runMigrations(ctx, pool, opts(issuePoolMigrationFiles(t, 1, 484))); err != nil {
		t.Fatalf("migrate through 484: %v", err)
	}
	// 473 normally keeps IDs unique while the replacement index is built. Drop
	// that safety index only in this fixture so duplicate rows can make the
	// concurrent 485 build fail and leave its same-named INVALID relation, the
	// exact crash artifact the pre-hook must recover.
	if _, err := pool.Exec(ctx, `
		DROP INDEX idx_issue_pool_outbox_id;
		WITH duplicate_id AS (SELECT gen_random_uuid() AS id)
		INSERT INTO issue_pool_notification_outbox (
			id,workspace_id,autopilot_id,cycle_id,recipient_id,event_type,payload
		)
		SELECT duplicate_id.id,gen_random_uuid(),gen_random_uuid(),gen_random_uuid(),
			gen_random_uuid(),'cycle_terminal','{}'::jsonb
		FROM duplicate_id CROSS JOIN generate_series(1,2)
	`); err != nil {
		t.Fatalf("seed duplicate outbox IDs: %v", err)
	}
	if err := runMigrations(ctx, pool, opts(issuePoolMigrationFiles(t, 485, 485))); err == nil {
		t.Fatal("migration 485 unexpectedly succeeded with duplicate IDs")
	}
	var invalid bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM pg_index i JOIN pg_class c ON c.oid=i.indexrelid
		WHERE c.relnamespace=current_schema()::regnamespace
		  AND c.relname='issue_pool_notification_outbox_pkey_candidate'
		  AND NOT i.indisvalid
	)`).Scan(&invalid); err != nil || !invalid {
		t.Fatalf("invalid candidate after interrupted build=%v err=%v", invalid, err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM issue_pool_notification_outbox a
		USING issue_pool_notification_outbox b WHERE a.id=b.id AND a.ctid>b.ctid`); err != nil {
		t.Fatalf("repair duplicate fixture row: %v", err)
	}
	if err := runMigrations(ctx, pool, opts(issuePoolMigrationFiles(t, 485, 488))); err != nil {
		t.Fatalf("rerun 485-488 after invalid index: %v", err)
	}

	var primaryKey, candidateGone bool
	if err := pool.QueryRow(ctx, `SELECT
		EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='issue_pool_notification_outbox'::regclass AND contype='p'),
		to_regclass(current_schema()||'.issue_pool_notification_outbox_pkey_candidate') IS NULL`).
		Scan(&primaryKey, &candidateGone); err != nil {
		t.Fatalf("inspect recovered outbox primary key: %v", err)
	}
	if !primaryKey || !candidateGone {
		t.Fatalf("recovered outbox primary_key=%v candidate_gone=%v", primaryKey, candidateGone)
	}
}

func installLegacyIssuePool469Fixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("testdata", "tes66-v2", "*.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	applied := 0
	for _, file := range files {
		var version int
		if _, err := fmt.Sscanf(filepath.Base(file), "%d_", &version); err != nil || version < 459 || version > 469 {
			continue
		}
		sql, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, string(sql)); err != nil {
			t.Fatalf("install original legacy migration %s: %v", file, err)
		}
		applied++
	}
	if applied != 11 {
		t.Fatalf("legacy fixture count = %d, want 11", applied)
	}
	for _, version := range []string{
		"459_issue_pool_execution_schema", "460_issue_pool_cycle_run_index", "461_issue_pool_item_task_index",
		"462_issue_pool_notification_schema", "463_issue_pool_notification_id_index", "464_issue_pool_notification_dedup_index",
		"465_issue_pool_item_active_issue_v2_index", "466_issue_pool_item_active_issue_old_drop",
		"467_issue_pool_notification_primary_key", "468_issue_pool_item_reconcile_index", "469_issue_pool_item_review_invariant",
	} {
		if _, err := pool.Exec(ctx, `INSERT INTO schema_migrations(version) VALUES($1)`, version); err != nil {
			t.Fatalf("seed legacy ledger %s: %v", version, err)
		}
	}
	_, err = pool.Exec(ctx, `
		WITH u AS (INSERT INTO "user"(name,email) VALUES('Legacy owner','legacy-owner@example.test') RETURNING id),
		w AS (INSERT INTO workspace(name,slug) VALUES('Legacy workspace','legacy-workspace') RETURNING id),
		m AS (INSERT INTO member(workspace_id,user_id,role) SELECT w.id,u.id,'owner' FROM w,u RETURNING user_id,workspace_id),
		a AS (INSERT INTO agent(workspace_id,name,runtime_mode,owner_id) SELECT workspace_id,'Legacy agent','local',user_id FROM m RETURNING id,workspace_id),
		ap AS (INSERT INTO autopilot(workspace_id,title,assignee_id,status,execution_mode,created_by_type,created_by_id)
			SELECT a.workspace_id,'Legacy pool',a.id,'active','issue_pool','member',m.user_id FROM a,m RETURNING id,workspace_id,created_by_id),
		p AS (INSERT INTO issue_pool_policy(autopilot_id,workspace_id,created_by_id) SELECT id,workspace_id,created_by_id FROM ap RETURNING id,autopilot_id,workspace_id,created_by_id),
		c AS (INSERT INTO issue_pool_cycle(policy_id,autopilot_id,workspace_id,idempotency_key,status,policy_snapshot,created_by_id,claimed_count)
			SELECT p.id,p.autopilot_id,p.workspace_id,'legacy-cycle','running','{}',p.created_by_id,2 FROM p RETURNING id,policy_id,autopilot_id,workspace_id,created_by_id),
		i1 AS (INSERT INTO issue_pool_item(cycle_id,policy_id,autopilot_id,workspace_id,issue_id,status,score,score_breakdown,selection_reasons,issue_snapshot,reviewer_id,reviewed_at,task_id)
			SELECT c.id,c.policy_id,c.autopilot_id,c.workspace_id,gen_random_uuid(),'queued',1,'{}','[]','{}',c.created_by_id,now(),gen_random_uuid() FROM c RETURNING cycle_id),
		i2 AS (INSERT INTO issue_pool_item(cycle_id,policy_id,autopilot_id,workspace_id,issue_id,status,score,score_breakdown,selection_reasons,issue_snapshot,reviewer_id,reviewed_at,task_id)
			SELECT c.id,c.policy_id,c.autopilot_id,c.workspace_id,gen_random_uuid(),'awaiting_acceptance',2,'{}','[]','{}',c.created_by_id,now(),gen_random_uuid() FROM c RETURNING cycle_id)
		INSERT INTO issue_pool_notification(cycle_id,autopilot_id,workspace_id,recipient_id,kind,inbox_item_id)
		SELECT c.id,c.autopilot_id,c.workspace_id,c.created_by_id,'review_requested',gen_random_uuid() FROM c
	`)
	if err != nil {
		t.Fatalf("seed non-empty legacy ledger fixture: %v", err)
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

func TestIssuePoolInboxDedupeRecoversInvalidConcurrentIndex(t *testing.T) {
	pool, schema := newIssuePoolMigrationPool(t)
	ctx := context.Background()
	opts := func(files []string) runOptions {
		return runOptions{
			Direction: "up", Files: files, SchemaMigrationsTable: schema + ".schema_migrations",
			AdvisoryLockKey: int64(rand.Uint64()&0x7fffffffffffffff) | 1, Hooks: preMigrationHooks,
		}
	}

	if err := runMigrations(ctx, pool, opts(issuePoolMigrationFiles(t, 1, 478))); err != nil {
		t.Fatalf("migrate through 478: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		WITH u AS (
			INSERT INTO "user"(name,email) VALUES('Inbox recovery owner','inbox-recovery@example.test') RETURNING id
		), w AS (
			INSERT INTO workspace(name,slug) VALUES('Inbox recovery workspace','inbox-recovery') RETURNING id
		), m AS (
			INSERT INTO member(workspace_id,user_id,role) SELECT w.id,u.id,'owner' FROM w,u RETURNING workspace_id,user_id
		)
		INSERT INTO inbox_item(workspace_id,recipient_type,recipient_id,type,title,details)
		SELECT m.workspace_id,'member',m.user_id,'issue_pool','duplicate notification',
			jsonb_build_object('notification_id','interrupted-479')
		FROM m CROSS JOIN generate_series(1,2)
	`); err != nil {
		t.Fatalf("seed duplicate inbox notification IDs: %v", err)
	}
	if err := runMigrations(ctx, pool, opts(issuePoolMigrationFiles(t, 479, 479))); err == nil {
		t.Fatal("migration 479 unexpectedly succeeded with duplicate notification IDs")
	}
	var invalid bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM pg_index i JOIN pg_class c ON c.oid=i.indexrelid
		WHERE c.relnamespace=current_schema()::regnamespace
		  AND c.relname='idx_inbox_issue_pool_notification_dedupe'
		  AND NOT i.indisvalid
	)`).Scan(&invalid); err != nil || !invalid {
		t.Fatalf("same-name INVALID index exists=%v err=%v", invalid, err)
	}
	var failedLedger int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM schema_migrations
		WHERE version='479_inbox_issue_pool_notification_dedupe_index'`).Scan(&failedLedger); err != nil || failedLedger != 0 {
		t.Fatalf("failed 479 ledger rows=%d err=%v", failedLedger, err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM inbox_item a USING inbox_item b
		WHERE a.type='issue_pool' AND b.type='issue_pool'
		  AND a.details->>'notification_id'=b.details->>'notification_id' AND a.ctid>b.ctid`); err != nil {
		t.Fatalf("repair duplicate inbox fixture: %v", err)
	}

	upgrade := issuePoolMigrationFiles(t, 479, 490)
	if err := runMigrations(ctx, pool, opts(upgrade)); err != nil {
		t.Fatalf("formal 479+ recovery: %v", err)
	}
	var valid bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM pg_index i JOIN pg_class c ON c.oid=i.indexrelid
		WHERE c.relnamespace=current_schema()::regnamespace
		  AND c.relname='idx_inbox_issue_pool_notification_dedupe'
		  AND i.indisvalid AND i.indisready
	)`).Scan(&valid); err != nil || !valid {
		t.Fatalf("rebuilt index valid=%v err=%v", valid, err)
	}
	for _, file := range upgrade {
		version := strings.TrimSuffix(strings.TrimSuffix(filepath.Base(file), ".sql"), ".up")
		var rows int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM schema_migrations WHERE version=$1`, version).Scan(&rows); err != nil || rows != 1 {
			t.Fatalf("ledger version %s rows=%d err=%v", version, rows, err)
		}
	}
	var ledgerBefore int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&ledgerBefore); err != nil {
		t.Fatal(err)
	}
	if err := runMigrations(ctx, pool, opts(upgrade)); err != nil {
		t.Fatalf("idempotent 479+ rerun: %v", err)
	}
	var ledgerAfter int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&ledgerAfter); err != nil || ledgerAfter != ledgerBefore {
		t.Fatalf("idempotent ledger count before=%d after=%d err=%v", ledgerBefore, ledgerAfter, err)
	}
}
