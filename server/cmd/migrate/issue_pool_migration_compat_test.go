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
				upgrade = issuePoolMigrationFiles(t, 1, 494)
			case "d_drive_1_283":
				bootstrap = issuePoolMigrationFiles(t, 1, 283)
				upgrade = append(issuePoolMigrationFiles(t, 449, 458), issuePoolMigrationFiles(t, 470, 494)...)
			case "legacy_449_469":
				bootstrap = append(issuePoolMigrationFiles(t, 1, 283), issuePoolMigrationFiles(t, 449, 458)...)
				upgrade = issuePoolMigrationFiles(t, 470, 494)
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
			var modeOK, mappingOK, outboxPK, activeIndexOK, inboxDedupeOK, oldIndexesGone, legacyCompatOK bool
			if err := pool.QueryRow(ctx, `SELECT
				EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='issue_pool_policy' AND column_name='workflow_input_mapping'),
				EXISTS(SELECT 1 FROM pg_constraint WHERE conrelid='issue_pool_notification_outbox'::regclass AND contype='p'),
				to_regclass(current_schema()||'.idx_issue_pool_item_active_issue_v3') IS NOT NULL,
				to_regclass(current_schema()||'.idx_inbox_issue_pool_notification_dedupe') IS NOT NULL,
				to_regclass(current_schema()||'.issue_pool_item_active_issue_v2_key') IS NULL
					AND to_regclass(current_schema()||'.idx_issue_pool_item_active_issue_v2') IS NULL
					AND to_regclass(current_schema()||'.issue_pool_item_reconcile_index') IS NULL
					AND to_regclass(current_schema()||'.issue_pool_cycle_autopilot_run_key') IS NULL
					AND to_regclass(current_schema()||'.idx_issue_pool_outbox_id') IS NULL,
				to_regclass(current_schema()||'.issue_pool_notification') IS NOT NULL
					AND EXISTS(SELECT 1 FROM pg_constraint WHERE conrelid='issue_pool_notification'::regclass AND contype='p')
					AND to_regclass(current_schema()||'.issue_pool_notification_dedup_key') IS NOT NULL`).
				Scan(&mappingOK, &outboxPK, &activeIndexOK, &inboxDedupeOK, &oldIndexesGone, &legacyCompatOK); err != nil {
				t.Fatalf("inspect upgraded schema: %v", err)
			}
			if err := pool.QueryRow(ctx, `SELECT EXISTS(
				SELECT 1 FROM pg_constraint
				WHERE conrelid='autopilot'::regclass AND conname='autopilot_execution_mode_check'
				  AND pg_get_constraintdef(oid) LIKE '%issue_pool%'
			)`).Scan(&modeOK); err != nil {
				t.Fatalf("inspect execution mode constraint: %v", err)
			}
			if !modeOK || !mappingOK || !outboxPK || !activeIndexOK || !inboxDedupeOK || !oldIndexesGone || !legacyCompatOK {
				t.Fatalf("upgrade invariants mode=%v mapping=%v outbox_pk=%v active_index=%v inbox_dedupe=%v old_indexes_gone=%v legacy_compat=%v",
					modeOK, mappingOK, outboxPK, activeIndexOK, inboxDedupeOK, oldIndexesGone, legacyCompatOK)
			}
			if path == "legacy_449_469" {
				var deferred, audited, completed, migratedNotices, retainedNotices int
				if err := pool.QueryRow(ctx, `SELECT
					count(*) FILTER (WHERE status='deferred' AND failure_code='legacy_direct_task')::int,
					count(*) FILTER (WHERE failure_detail ? 'legacy_execution')::int,
					count(*) FILTER (WHERE status='completed' AND failure_code IS NULL)::int
					FROM issue_pool_item`).Scan(&deferred, &audited, &completed); err != nil {
					t.Fatal(err)
				}
				if err := pool.QueryRow(ctx, `SELECT count(*)::int FROM issue_pool_notification_outbox
					WHERE delivered_at IS NOT NULL AND payload->>'migrated_from'='issue_pool_notification'`).Scan(&migratedNotices); err != nil {
					t.Fatal(err)
				}
				if err := pool.QueryRow(ctx, `SELECT count(*)::int FROM issue_pool_notification`).Scan(&retainedNotices); err != nil {
					t.Fatal(err)
				}
				if deferred != 3 || audited != 3 || completed != 1 || migratedNotices != 1 || retainedNotices != 1 {
					t.Fatalf("legacy mapping deferred=%d audited=%d completed=%d migrated_notices=%d retained_notices=%d, want 3/3/1/1/1",
						deferred, audited, completed, migratedNotices, retainedNotices)
				}
			}
		})
	}
}

func installLegacyIssuePool469Fixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	statements := []string{
		`ALTER TABLE autopilot DROP CONSTRAINT autopilot_execution_mode_check`,
		`ALTER TABLE autopilot ADD CONSTRAINT autopilot_execution_mode_check CHECK (execution_mode IN ('create_issue','run_only','issue_pool'))`,
		`ALTER TABLE issue_pool_cycle ADD COLUMN autopilot_run_id UUID, ADD COLUMN approved_count INTEGER NOT NULL DEFAULT 0 CHECK (approved_count>=0), ADD COLUMN rejected_count INTEGER NOT NULL DEFAULT 0 CHECK (rejected_count>=0), ADD COLUMN dispatched_count INTEGER NOT NULL DEFAULT 0 CHECK (dispatched_count>=0), ADD COLUMN awaiting_acceptance_count INTEGER NOT NULL DEFAULT 0 CHECK (awaiting_acceptance_count>=0), ADD COLUMN completed_count INTEGER NOT NULL DEFAULT 0 CHECK (completed_count>=0), ADD COLUMN failed_count INTEGER NOT NULL DEFAULT 0 CHECK (failed_count>=0), ADD COLUMN deferred_count INTEGER NOT NULL DEFAULT 0 CHECK (deferred_count>=0), ADD COLUMN completed_at TIMESTAMPTZ`,
		`ALTER TABLE issue_pool_cycle DROP CONSTRAINT issue_pool_cycle_status_check`,
		`ALTER TABLE issue_pool_cycle ADD CONSTRAINT issue_pool_cycle_status_check CHECK (status IN ('scanning','awaiting_review','running','completed','partial','failed'))`,
		`ALTER TABLE issue_pool_item DROP CONSTRAINT issue_pool_item_status_check`,
		`ALTER TABLE issue_pool_item ADD CONSTRAINT issue_pool_item_status_check CHECK (status IN ('claimed','approved','rejected','queued','running','awaiting_acceptance','completed','failed','blocked','cancelled','deferred'))`,
		`ALTER TABLE issue_pool_item ADD COLUMN task_id UUID, ADD COLUMN failure_reason TEXT, ADD COLUMN dispatch_started_at TIMESTAMPTZ, ADD COLUMN awaiting_acceptance_at TIMESTAMPTZ, ADD COLUMN completed_at TIMESTAMPTZ`,
		`CREATE UNIQUE INDEX CONCURRENTLY issue_pool_cycle_autopilot_run_key ON issue_pool_cycle (autopilot_run_id) WHERE autopilot_run_id IS NOT NULL`,
		`CREATE UNIQUE INDEX CONCURRENTLY issue_pool_item_task_key ON issue_pool_item (task_id) WHERE task_id IS NOT NULL`,
		`CREATE TABLE issue_pool_notification (id UUID NOT NULL DEFAULT gen_random_uuid(),cycle_id UUID NOT NULL,autopilot_id UUID NOT NULL,workspace_id UUID NOT NULL,recipient_id UUID NOT NULL,kind TEXT NOT NULL CHECK (kind IN ('review_requested','cycle_terminal')),inbox_item_id UUID NOT NULL,created_at TIMESTAMPTZ NOT NULL DEFAULT now())`,
		`CREATE UNIQUE INDEX CONCURRENTLY issue_pool_notification_id_key ON issue_pool_notification (id)`,
		`CREATE UNIQUE INDEX CONCURRENTLY issue_pool_notification_dedup_key ON issue_pool_notification (cycle_id,recipient_id,kind)`,
		`CREATE UNIQUE INDEX CONCURRENTLY issue_pool_item_active_issue_v2_key ON issue_pool_item (issue_id) WHERE status IN ('claimed','approved','queued','running','awaiting_acceptance')`,
		`DROP INDEX CONCURRENTLY IF EXISTS issue_pool_item_active_issue_key`,
		`ALTER TABLE issue_pool_notification ADD PRIMARY KEY USING INDEX issue_pool_notification_id_key`,
		`CREATE INDEX CONCURRENTLY issue_pool_item_reconcile_index ON issue_pool_item (status,updated_at) WHERE status IN ('approved','queued','running','awaiting_acceptance')`,
		`ALTER TABLE issue_pool_item DROP CONSTRAINT issue_pool_item_check1`,
		`ALTER TABLE issue_pool_item ADD CONSTRAINT issue_pool_item_review_check CHECK (status IN ('claimed','deferred') OR (reviewer_id IS NOT NULL AND reviewed_at IS NOT NULL))`,
	}
	for i, statement := range statements {
		if _, err := pool.Exec(ctx, statement); err != nil {
			t.Fatalf("install legacy 469 schema statement %d: %v", i, err)
		}
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
	_, err := pool.Exec(ctx, `
		WITH u AS (INSERT INTO "user"(name,email) VALUES('Legacy owner','legacy-owner@example.test') RETURNING id),
		w AS (INSERT INTO workspace(name,slug) VALUES('Legacy workspace','legacy-workspace') RETURNING id),
		m AS (INSERT INTO member(workspace_id,user_id,role) SELECT w.id,u.id,'owner' FROM w,u RETURNING user_id,workspace_id),
		a AS (INSERT INTO agent(workspace_id,name,runtime_mode,owner_id) SELECT workspace_id,'Legacy agent','local',user_id FROM m RETURNING id,workspace_id),
		ap AS (INSERT INTO autopilot(workspace_id,title,assignee_id,status,execution_mode,created_by_type,created_by_id)
			SELECT a.workspace_id,'Legacy pool',a.id,'active','issue_pool','member',m.user_id FROM a,m RETURNING id,workspace_id,created_by_id),
		p AS (INSERT INTO issue_pool_policy(autopilot_id,workspace_id,created_by_id) SELECT id,workspace_id,created_by_id FROM ap RETURNING id,autopilot_id,workspace_id,created_by_id),
		c AS (INSERT INTO issue_pool_cycle(policy_id,autopilot_id,workspace_id,idempotency_key,status,policy_snapshot,created_by_id,claimed_count)
			SELECT p.id,p.autopilot_id,p.workspace_id,'legacy-cycle','running','{}',p.created_by_id,4 FROM p RETURNING id,policy_id,autopilot_id,workspace_id,created_by_id),
		i1 AS (INSERT INTO issue_pool_item(cycle_id,policy_id,autopilot_id,workspace_id,issue_id,status,score,score_breakdown,selection_reasons,issue_snapshot,reviewer_id,reviewed_at,task_id)
			SELECT c.id,c.policy_id,c.autopilot_id,c.workspace_id,gen_random_uuid(),'queued',1,'{}','[]','{}',c.created_by_id,now(),gen_random_uuid() FROM c RETURNING cycle_id),
		i2 AS (INSERT INTO issue_pool_item(cycle_id,policy_id,autopilot_id,workspace_id,issue_id,status,score,score_breakdown,selection_reasons,issue_snapshot,reviewer_id,reviewed_at,task_id)
			SELECT c.id,c.policy_id,c.autopilot_id,c.workspace_id,gen_random_uuid(),'running',2,'{}','[]','{}',c.created_by_id,now(),gen_random_uuid() FROM c RETURNING cycle_id),
		i3 AS (INSERT INTO issue_pool_item(cycle_id,policy_id,autopilot_id,workspace_id,issue_id,status,score,score_breakdown,selection_reasons,issue_snapshot,reviewer_id,reviewed_at,task_id,awaiting_acceptance_at)
			SELECT c.id,c.policy_id,c.autopilot_id,c.workspace_id,gen_random_uuid(),'awaiting_acceptance',3,'{}','[]','{}',c.created_by_id,now(),gen_random_uuid(),now() FROM c RETURNING cycle_id),
		i4 AS (INSERT INTO issue_pool_item(cycle_id,policy_id,autopilot_id,workspace_id,issue_id,status,score,score_breakdown,selection_reasons,issue_snapshot,reviewer_id,reviewed_at,completed_at)
			SELECT c.id,c.policy_id,c.autopilot_id,c.workspace_id,gen_random_uuid(),'completed',4,'{}','[]','{}',c.created_by_id,now(),now() FROM c RETURNING cycle_id)
		INSERT INTO issue_pool_notification(cycle_id,autopilot_id,workspace_id,recipient_id,kind,inbox_item_id)
		SELECT c.id,c.autopilot_id,c.workspace_id,c.created_by_id,'review_requested',gen_random_uuid() FROM c
	`)
	if err != nil {
		t.Fatalf("seed mixed non-empty legacy fixture: %v", err)
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
