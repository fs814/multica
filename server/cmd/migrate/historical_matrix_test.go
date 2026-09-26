package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5"
	"math/rand/v2"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/migrations"
)

func historicalOpts(schema string, files []string) runOptions {
	return runOptions{Direction: "up", Files: files, SchemaMigrationsTable: schema + ".schema_migrations", AdvisoryLockKey: int64(rand.Uint64()&0x7fffffffffffffff) | 1, Hooks: hooksForDirection("up"), Conditions: conditionsForDirection("up")}
}

func historicalLedger(t *testing.T, pool *pgxpool.Pool) map[string]time.Time {
	t.Helper()
	rows, err := pool.Query(context.Background(), `SELECT version,applied_at FROM schema_migrations ORDER BY version`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	result := map[string]time.Time{}
	for rows.Next() {
		var v string
		var stamp time.Time
		if err := rows.Scan(&v, &stamp); err != nil {
			t.Fatal(err)
		}
		result[v] = stamp
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}

func historicalSchema(t *testing.T, pool *pgxpool.Pool, schema string) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(), `SELECT definition FROM (
 SELECT 'column:'||c.relname||':'||a.attname||':'||format_type(a.atttypid,a.atttypmod)||':'||a.attnotnull::text||':'||coalesce(pg_get_expr(d.adbin,d.adrelid),'') AS definition
 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace JOIN pg_attribute a ON a.attrelid=c.oid
 LEFT JOIN pg_attrdef d ON d.adrelid=c.oid AND d.adnum=a.attnum
 WHERE n.nspname=current_schema() AND c.relkind IN ('r','p') AND a.attnum>0 AND NOT a.attisdropped
 UNION ALL SELECT 'constraint:'||c.relname||':'||k.conname||':'||pg_get_constraintdef(k.oid) FROM pg_constraint k JOIN pg_class c ON c.oid=k.conrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=current_schema()
 UNION ALL SELECT 'index:'||indexname||':'||indexdef FROM pg_indexes WHERE schemaname=current_schema()
 UNION ALL SELECT 'trigger:'||c.relname||':'||pg_get_triggerdef(t.oid) FROM pg_trigger t JOIN pg_class c ON c.oid=t.tgrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=current_schema() AND NOT t.tgisinternal
 UNION ALL SELECT 'function:'||p.proname||':'||pg_get_functiondef(p.oid) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname=current_schema()
 ) q ORDER BY definition`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var result []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		result = append(result, strings.ReplaceAll(s, schema, "FIXTURE_SCHEMA"))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}

// These cut points correspond to the complete migration sets of the named
// commits, verified against Git when the immutable manifest was introduced.
// They are not claims about arbitrary forks with the same maximum prefix.
func TestHistoricalMigrationMatrix(t *testing.T) {
	if os.Getenv("DATABASE_URL") == "" {
		t.Skip("requires isolated DATABASE_URL")
	}
	ctx := context.Background()
	var expected []string
	for _, path := range []struct {
		name string
		last int
	}{
		{"empty", 0}, {"18f60063b", 532}, {"19987e327", 540}, {"77c01418a", 541}, {"e641ee118", 541},
	} {
		t.Run(path.name, func(t *testing.T) {
			pool, schema := newHistoricalPool(t)
			var version string
			if err := pool.QueryRow(ctx, "SELECT version()").Scan(&version); err != nil {
				t.Fatal(err)
			}
			t.Log(version)
			apply := func(files []string) {
				t.Helper()
				if err := runMigrations(ctx, pool, historicalOpts(schema, files)); err != nil {
					t.Fatal(err)
				}
			}
			if path.last > 0 {
				apply(issuePoolMigrationFiles(t, 1, path.last))
			}
			// Seed real receipt-shaped data only after the real 507 DDL has run.
			var oid, tableOID uint32
			var receiptHash string
			if path.last > 0 {
				if _, err := pool.Exec(ctx, `INSERT INTO comment_agent_delivery(comment_id,agent_id,status) VALUES ('00000000-0000-0000-0000-000000000001','00000000-0000-0000-0000-000000000002','pending')`); err != nil {
					t.Fatal(err)
				}
				if err := pool.QueryRow(ctx, `SELECT conindid,conrelid FROM pg_constraint WHERE conrelid='comment_agent_delivery'::regclass AND contype='p'`).Scan(&oid, &tableOID); err != nil {
					t.Fatal(err)
				}
			}
			if path.last > 0 {
				if err := pool.QueryRow(ctx, `SELECT md5(string_agg(to_jsonb(d)::text, '' ORDER BY comment_id,agent_id)) FROM comment_agent_delivery d`).Scan(&receiptHash); err != nil {
					t.Fatal(err)
				}
			}
			var before map[string]time.Time
			if path.last > 0 {
				before = historicalLedger(t, pool)
			}
			files := issuePoolMigrationFiles(t, 1, 541)
			apply(files)
			after := historicalLedger(t, pool)
			if len(after) != len(files) {
				t.Fatalf("ledger=%d files=%d", len(after), len(files))
			}
			for _, file := range files {
				if _, ok := after[migrations.ExtractVersion(file)]; !ok {
					t.Fatalf("readiness version missing: %s", file)
				}
			}
			for v, stamp := range before {
				if !stamp.Equal(after[v]) {
					t.Fatalf("replayed %s", v)
				}
			}
			apply(files)
			if !reflect.DeepEqual(after, historicalLedger(t, pool)) {
				t.Fatal("rerun changed ledger")
			}
			if path.last > 0 {
				var retained bool
				if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_constraint WHERE conrelid='comment_agent_delivery'::regclass AND conindid=$1 AND conrelid=$2) AND (SELECT count(*)=1 AND md5(string_agg(to_jsonb(d)::text, '' ORDER BY comment_id,agent_id))=$3 FROM comment_agent_delivery d)`, oid, tableOID, receiptHash).Scan(&retained); err != nil || !retained {
					t.Fatalf("receipt/PK not retained: %v", err)
				}
			}
			var invalid int
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_index i JOIN pg_class c ON c.oid=i.indrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=current_schema() AND NOT (i.indisvalid AND i.indisready AND i.indislive)`).Scan(&invalid); err != nil || invalid != 0 {
				t.Fatalf("invalid/not ready/not live indexes=%d err=%v", invalid, err)
			}
			got := historicalSchema(t, pool, schema)
			if expected == nil {
				expected = got
			} else if !reflect.DeepEqual(expected, got) {
				t.Fatalf("normalized schema differs from fresh installation")
			}
			t.Logf("full ledger %d identities; schema %d definitions sha256=%x; all indexes valid/ready/live; second run unchanged", len(after), len(got), sha256.Sum256([]byte(strings.Join(got, "\n"))))
		})
	}
}

func TestHistoricalCollisionLedgerPrefixes(t *testing.T) {
	if os.Getenv("DATABASE_URL") == "" {
		t.Skip("requires isolated DATABASE_URL")
	}
	raw, err := os.ReadFile(filepath.Join("..", "..", "internal", "migrations", "testdata", "frozen-migrations.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Groups map[string][]string `json:"accepted_collision_changes"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest.Groups) != 53 {
		t.Fatal("expected all 53 debt groups")
	}
	pool, schema := newHistoricalPool(t)
	ctx := context.Background()
	seen := 0
	// Replay the actual dependency closure in production order. At every member
	// of each debt group test a partially-applied ledger, then the complete group.
	// This covers every lexically valid cut, including both cuts in 284's triple.
	files := issuePoolMigrationFiles(t, 1, 541)
	for i := 0; i < len(files); {
		prefix := strings.Split(filepath.Base(files[i]), "_")[0]
		j := i + 1
		for j < len(files) && strings.HasPrefix(filepath.Base(files[j]), prefix+"_") {
			j++
		}
		group := files[i:j]
		if _, debt := manifest.Groups[prefix]; debt {
			seen++
			t.Run(prefix, func(t *testing.T) {
				for k := range group {
					if err := runMigrations(ctx, pool, historicalOpts(schema, group[:k+1])); err != nil {
						t.Fatal(err)
					}
					before := historicalLedger(t, pool)
					if err := runMigrations(ctx, pool, historicalOpts(schema, group[:k+1])); err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(before, historicalLedger(t, pool)) {
						t.Fatal("partial rerun changed ledger")
					}
					for _, file := range group[:k+1] {
						if _, ok := before[migrations.ExtractVersion(file)]; !ok {
							t.Fatalf("missing full stem %s", file)
						}
					}
				}
			})
			if t.Failed() {
				return
			}
		} else if err := runMigrations(ctx, pool, historicalOpts(schema, group)); err != nil {
			t.Fatal(err)
		}
		i = j
	}
	if seen != 53 {
		t.Fatalf("exercised %d groups", seen)
	}
	t.Logf("all %d groups replayed with real SQL and dependency closure", seen)
}

func TestHistoricalDeliveryDriftAndRetry(t *testing.T) {
	if os.Getenv("DATABASE_URL") == "" {
		t.Skip("requires isolated DATABASE_URL")
	}
	for _, state := range []string{"valid", "missing_table", "missing_ledger", "wrong_pk", "missing_check", "wrong_type", "missing_default", "ddl_without_ledger"} {
		t.Run(state, func(t *testing.T) {
			pool, schema := newHistoricalPool(t)
			ctx := context.Background()
			opts := historicalOpts(schema, realMigrationFiles(t, []string{deliveryMigrationVersion}, "up"))
			if err := runMigrations(ctx, pool, opts); err != nil {
				t.Fatal(err)
			}
			exec := func(sql string) {
				t.Helper()
				if _, err := pool.Exec(ctx, sql); err != nil {
					t.Fatal(err)
				}
			}
			switch state {
			case "missing_table":
				exec("DROP TABLE comment_agent_delivery")
			case "missing_ledger", "ddl_without_ledger":
				exec("DELETE FROM schema_migrations")
			case "wrong_pk":
				exec("ALTER TABLE comment_agent_delivery DROP CONSTRAINT comment_agent_delivery_pkey")
			case "missing_check":
				exec("ALTER TABLE comment_agent_delivery DROP CONSTRAINT comment_agent_delivery_status_check")
			case "missing_default":
				exec("ALTER TABLE comment_agent_delivery ALTER COLUMN created_at DROP DEFAULT")
			case "wrong_type":
				exec("ALTER TABLE comment_agent_delivery ALTER COLUMN failure_reason TYPE varchar(50)")
			}
			before := historicalLedger(t, pool)
			err := runMigrations(ctx, pool, opts)
			if state == "valid" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), "507") {
				t.Fatalf("drift not rejected: %v", err)
			}
			if !reflect.DeepEqual(before, historicalLedger(t, pool)) {
				t.Fatal("failed validation mutated ledger")
			}
			if err != nil {
				t.Log(err)
			}
		})
	}
}

func TestHistoricalDeliveryConstraints(t *testing.T) {
	pool, schema := newHistoricalPool(t)
	ctx := context.Background()
	opts := historicalOpts(schema, realMigrationFiles(t, []string{deliveryMigrationVersion, "508_comment_agent_delivery_pending_index"}, "up"))
	if err := runMigrations(ctx, pool, opts); err != nil {
		t.Fatal(err)
	}
	insert := `INSERT INTO comment_agent_delivery(comment_id,agent_id,status) VALUES ('00000000-0000-0000-0000-000000000001','00000000-0000-0000-0000-000000000002','pending')`
	if _, err := pool.Exec(ctx, insert); err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{insert, strings.Replace(insert, "'pending'", "'invalid'", 1), strings.Replace(insert, "'00000000-0000-0000-0000-000000000001'", "NULL", 1)} {
		if _, err := pool.Exec(ctx, sql); err == nil {
			t.Fatalf("constraint accepted %s", sql)
		}
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM comment_agent_delivery WHERE status IN ('pending','steering')`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("pending query count=%d err=%v", count, err)
	}
	assertIndexValidity(t, pool, schema, "idx_comment_agent_delivery_task_pending", true)
}

// A fresh database (not only search_path) is needed: historical SQL deliberately
// owns public functions, and an existing public index can shadow a scratch
// schema's absent index. Use only the explicitly selected test server.
func newHistoricalPool(t *testing.T) (*pgxpool.Pool, string) {
	t.Helper()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("requires explicitly selected isolated DATABASE_URL")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("multica_migration_test_%d_%d", time.Now().UnixNano(), rand.Uint32())
	ident := pgx.Identifier{name}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+ident+" TEMPLATE template0"); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	var pool *pgxpool.Pool
	t.Cleanup(func() {
		if pool != nil {
			pool.Close()
		}
		defer admin.Close()
		if _, err := admin.Exec(context.Background(), "DROP DATABASE "+ident); err != nil {
			t.Errorf("drop isolated database: %v", err)
		}
	})
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.Database = name
	pool, err = pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return pool, "public"
}

func TestHistoricalDeliveryLedgerFailureStopsRetry(t *testing.T) {
	pool, schema := newHistoricalPool(t)
	ctx := context.Background()
	opts := historicalOpts(schema, nil)
	if err := runMigrations(ctx, pool, opts); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `CREATE FUNCTION fail_507_ledger() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected 507 ledger failure'; END $$;
 CREATE TRIGGER fail_507 BEFORE INSERT ON schema_migrations FOR EACH ROW EXECUTE FUNCTION fail_507_ledger()`); err != nil {
		t.Fatal(err)
	}
	opts.Files = realMigrationFiles(t, []string{deliveryMigrationVersion}, "up")
	err := runMigrations(ctx, pool, opts)
	if err == nil || !strings.Contains(err.Error(), "record migration") {
		t.Fatalf("expected DDL success followed by ledger failure: %v", err)
	}
	if _, err := pool.Exec(ctx, "DROP TRIGGER fail_507 ON schema_migrations"); err != nil {
		t.Fatal(err)
	}
	err = runMigrations(ctx, pool, opts)
	if err == nil || !strings.Contains(err.Error(), "table/ledger mismatch") {
		t.Fatalf("ambiguous retry not stopped: %v", err)
	}
	if len(historicalLedger(t, pool)) != 0 {
		t.Fatal("failed DDL was marked applied")
	}
	t.Log(err)
}

func TestRecordedConditionDoesNotReplayWhenAvailabilityChanges(t *testing.T) {
	pool, schema := newHistoricalPool(t)
	ctx := context.Background()
	// Use the actual extension-gated historical file; do not fake a table/ledger.
	opts := historicalOpts(schema, realMigrationFiles(t, []string{"446_issue_properties_bigm_index"}, "up"))
	opts.Conditions = map[string]migrationCondition{"446_issue_properties_bigm_index": skipMigration("extension unavailable")}
	if err := runMigrations(ctx, pool, opts); err != nil {
		t.Fatal(err)
	}
	before := historicalLedger(t, pool)
	called := false
	opts.Conditions = map[string]migrationCondition{"446_issue_properties_bigm_index": func(context.Context, *pgxpool.Conn) (bool, string, error) { called = true; return true, "", nil }}
	if err := runMigrations(ctx, pool, opts); err != nil {
		t.Fatal(err)
	}
	if called || !reflect.DeepEqual(before, historicalLedger(t, pool)) {
		t.Fatal("recorded conditional migration replayed")
	}
}

func TestHistoricalConcurrentRunners(t *testing.T) {
	pool, schema := newHistoricalPool(t)
	opts := historicalOpts(schema, issuePoolMigrationFiles(t, 1, 541))
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	results := make(chan error, 2)
	for range 2 {
		go func() { results <- runMigrations(ctx, pool, opts) }()
	}
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if len(historicalLedger(t, pool)) != len(opts.Files) {
		t.Fatal("concurrent runners lost ledger entries")
	}
}

func TestMigrationLockCancellationThenRetry(t *testing.T) {
	pool, schema := newHistoricalPool(t)
	ctx := context.Background()
	opts := historicalOpts(schema, realMigrationFiles(t, []string{deliveryMigrationVersion}, "up"))
	holder, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Release()
	if _, err := holder.Exec(ctx, "SELECT pg_advisory_lock($1)", opts.AdvisoryLockKey); err != nil {
		t.Fatal(err)
	}
	timeout, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	err = runMigrations(timeout, pool, opts)
	cancel()
	if err == nil || !strings.Contains(err.Error(), "advisory lock") {
		t.Fatalf("lock cancellation not reported: %v", err)
	}
	if _, err := holder.Exec(ctx, "SELECT pg_advisory_unlock($1)", opts.AdvisoryLockKey); err != nil {
		t.Fatal(err)
	}
	retry, cancelRetry := context.WithTimeout(ctx, 10*time.Second)
	defer cancelRetry()
	if err := runMigrations(retry, pool, opts); err != nil {
		t.Fatal(err)
	}
}
