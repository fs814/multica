package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestHistoricalDeliveryColumnSet(t *testing.T) {
	for _, state := range []string{"valid", "required_extra", "nullable_extra", "defaulted_extra", "dropped_extra"} {
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
			const insert = `INSERT INTO comment_agent_delivery(comment_id,agent_id,status) VALUES ('00000000-0000-0000-0000-000000000001','00000000-0000-0000-0000-000000000002','pending')`
			switch state {
			case "required_extra":
				exec("ALTER TABLE comment_agent_delivery ADD COLUMN legacy_required text NOT NULL")
				_, err := pool.Exec(ctx, insert)
				var pgErr *pgconn.PgError
				if !errors.As(err, &pgErr) || pgErr.Code != "23502" || pgErr.ColumnName != "legacy_required" {
					t.Fatalf("expected application-shaped INSERT to fail on legacy_required with 23502: %v", err)
				}
				t.Logf("application-shaped INSERT rejects incompatible column: %v", err)
				exec(`INSERT INTO comment_agent_delivery(comment_id,agent_id,status,legacy_required) VALUES ('00000000-0000-0000-0000-000000000001','00000000-0000-0000-0000-000000000002','pending','preserve me')`)
			case "nullable_extra":
				exec("ALTER TABLE comment_agent_delivery ADD COLUMN legacy_optional text")
			case "defaulted_extra":
				exec("ALTER TABLE comment_agent_delivery ADD COLUMN legacy_default text NOT NULL DEFAULT 'legacy'")
			case "dropped_extra":
				exec("ALTER TABLE comment_agent_delivery ADD COLUMN removed_column text")
				exec("ALTER TABLE comment_agent_delivery DROP COLUMN removed_column")
			}
			if state != "required_extra" {
				exec(insert)
			}
			// Include pending DDL before 507 as well as the real 508 index: a
			// check inside the per-file skip path would already be too late.
			probe := filepath.Join(t.TempDir(), "506_delivery_drift_probe.up.sql")
			if err := os.WriteFile(probe, []byte("CREATE TABLE delivery_drift_probe (value text)"), 0600); err != nil {
				t.Fatal(err)
			}
			opts.Files = append([]string{probe}, realMigrationFiles(t, []string{deliveryMigrationVersion, "508_comment_agent_delivery_pending_index"}, "up")...)
			beforeLedger := historicalLedger(t, pool)
			beforeSchema := historicalSchema(t, pool, schema)
			type receiptState struct {
				data                     string
				table, constraint, index uint32
				valid, ready, live       bool
			}
			snapshot := func() receiptState {
				t.Helper()
				var s receiptState
				if err := pool.QueryRow(ctx, `SELECT (SELECT jsonb_agg(to_jsonb(d) ORDER BY comment_id,agent_id)::text FROM comment_agent_delivery d), c.conrelid,c.oid,c.conindid,i.indisvalid,i.indisready,i.indislive FROM pg_constraint c JOIN pg_index i ON i.indexrelid=c.conindid WHERE c.conrelid='comment_agent_delivery'::regclass AND c.contype='p'`).Scan(&s.data, &s.table, &s.constraint, &s.index, &s.valid, &s.ready, &s.live); err != nil {
					t.Fatal(err)
				}
				if !s.valid || !s.ready || !s.live {
					t.Fatal("receipt primary key is not valid/ready/live")
				}
				return s
			}
			before := snapshot()
			valid := state == "valid" || state == "dropped_extra"
			for attempt := 0; attempt < 2; attempt++ {
				err := runMigrations(ctx, pool, opts)
				if valid {
					if err != nil {
						t.Fatal(err)
					}
				} else {
					if err == nil || !strings.Contains(err.Error(), "507 receipt schema drift") {
						t.Fatalf("expected explicit 507 drift before pending DDL: %v", err)
					}
					if !reflect.DeepEqual(beforeLedger, historicalLedger(t, pool)) || !reflect.DeepEqual(beforeSchema, historicalSchema(t, pool, schema)) {
						t.Fatal("rejected migration changed ledger or schema")
					}
					t.Logf("attempt %d: %v; ledger and schema unchanged", attempt+1, err)
				}
				if after := snapshot(); after != before {
					t.Fatalf("migration changed receipts or table/PK identity: before=%+v after=%+v", before, after)
				}
			}
			if valid {
				exec(strings.Replace(insert, "00000000-0000-0000-0000-000000000001", "00000000-0000-0000-0000-000000000003", 1))
				assertIndexValidity(t, pool, schema, "idx_comment_agent_delivery_task_pending", true)
				if ledger := historicalLedger(t, pool); len(ledger) != 3 || !ledger[deliveryMigrationVersion].Equal(beforeLedger[deliveryMigrationVersion]) {
					t.Fatalf("expected two new migrations without replaying 507: %v", ledger)
				}
				t.Log("normal receipt INSERT and pending migrations succeeded; original receipt and table/PK identity preserved")
			} else {
				t.Log("both rejections preserved receipt data, table/PK OIDs and valid/ready/live primary key")
			}
		})
	}
}

func TestMigrationOwnerCancellationReleasesLock(t *testing.T) {
	pool, schema := newHistoricalPool(t)
	opts := historicalOpts(schema, realMigrationFiles(t, []string{deliveryMigrationVersion}, "up"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	opts.Hooks = map[string]preMigrationHook{deliveryMigrationVersion: func(context.Context, *pgxpool.Pool) error {
		cancel()
		return ctx.Err()
	}}
	if err := runMigrations(ctx, pool, opts); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation while holding migration lock: %v", err)
	}
	// A separate pool avoids accidentally reacquiring a leaked lock reentrantly.
	other, err := pgxpool.NewWithConfig(context.Background(), pool.Config())
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	bounded, stop := context.WithTimeout(context.Background(), 2*time.Second)
	defer stop()
	opts.Hooks = nil
	if err := runMigrations(bounded, other, opts); err != nil {
		t.Fatalf("cancelled owner retained its lock: %v", err)
	}
}
