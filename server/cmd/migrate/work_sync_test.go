package main

import (
	"context"
	"math/rand/v2"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestWorkSyncMigrationsRoundTripAndCapture(t *testing.T) {
	if os.Getenv("DATABASE_URL") == "" {
		t.Skip("requires an explicitly selected isolated DATABASE_URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	admin := openTestPool(t)
	schema := createScratchSchema(t, ctx, admin, "work_sync")
	pool := openTestPoolWithSearchPath(t, schema)
	for _, kind := range []string{"issue", "project", "agent"} {
		if _, err := pool.Exec(ctx, `CREATE TABLE `+kind+` (id uuid, workspace_id uuid, title text, name text, kind text DEFAULT 'user', description text, priority text DEFAULT 'none', custom_env jsonb)`); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `CREATE FUNCTION capture_issue_collaboration_wakeup() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NEW; END $$;
		CREATE TRIGGER capture_issue_collaboration_wakeup AFTER UPDATE ON issue FOR EACH ROW EXECUTE FUNCTION capture_issue_collaboration_wakeup()`); err != nil {
		t.Fatal(err)
	}
	versions := []string{"533_work_sync", "534_work_sync_scope_identity", "535_work_sync_change_position", "536_work_sync_operation_identity", "537_work_sync_node_sequence", "538_work_sync_capture", "539_work_sync_entity_history", "540_work_sync_wakeup_guard", "541_work_sync_commit_capture", "542_work_sync_grant", "543_work_sync_grant_identity", "544_work_sync_recovery", "545_work_sync_recovery_identity"}
	opts := runOptions{SchemaMigrationsTable: schema + ".schema_migrations", AdvisoryLockKey: int64(rand.Uint64()&0x7fffffffffffffff) | 1}
	apply := func(direction string, names []string) {
		t.Helper()
		opts.Direction, opts.Files, opts.Hooks = direction, realMigrationFiles(t, names, direction), hooksForDirection(direction)
		if err := runMigrations(ctx, pool, opts); err != nil {
			t.Fatal(err)
		}
	}
	apply("up", versions)
	apply("up", versions)
	// Model a crash after DDL committed but before its ledger insert.
	if _, err := pool.Exec(ctx, `DELETE FROM schema_migrations WHERE version IN ('533_work_sync','538_work_sync_capture','541_work_sync_commit_capture','542_work_sync_grant','543_work_sync_grant_identity','544_work_sync_recovery','545_work_sync_recovery_identity')`); err != nil {
		t.Fatal(err)
	}
	apply("up", versions)
	for _, index := range []string{"work_sync_scope_identity", "work_sync_change_position", "work_sync_operation_identity", "work_sync_node_sequence", "work_sync_entity_history", "idx_work_sync_grant_identity", "idx_work_sync_recovery_identity"} {
		assertIndexValidity(t, pool, schema, index, true)
	}
	workspace, other := uuid.NewString(), uuid.NewString()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	count := func(sql string, want int, args ...any) {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, sql, args...).Scan(&n); err != nil || n != want {
			t.Fatalf("count = %d, want %d: %v", n, want, err)
		}
	}
	exec(`INSERT INTO issue(id,workspace_id,title) VALUES ($1,$2,'disabled')`, uuid.NewString(), workspace)
	count(`SELECT count(*) FROM work_sync_change`, 0)
	for _, w := range []string{workspace, other} {
		exec(`INSERT INTO work_sync_scope(workspace_id,group_id,epoch) VALUES ($1,$2,$3)`, w, uuid.NewString(), uuid.NewString())
	}
	for _, kind := range []string{"issue", "project", "agent"} {
		entity := uuid.NewString()
		exec(`INSERT INTO `+kind+`(id,workspace_id,title,name,custom_env) VALUES ($1,$2,'initial','initial','{"secret":"test-only"}')`, entity, workspace)
		exec(`UPDATE `+kind+` SET custom_env='{"secret":"changed"}' WHERE id=$1`, entity)
		count(`SELECT count(*) FROM work_sync_change WHERE entity_id=$1`, 1, entity)
		exec(`UPDATE `+kind+` SET description='changed' WHERE id=$1`, entity)
		exec(`UPDATE `+kind+` SET workspace_id=$2 WHERE id=$1`, entity, other)
		count(`SELECT count(*) FROM work_sync_change WHERE entity_id=$1 AND workspace_id=$2 AND deleted`, 1, entity, workspace)
		exec(`DELETE FROM `+kind+` WHERE id=$1`, entity)
		count(`SELECT count(*) FROM work_sync_change WHERE entity_id=$1 AND workspace_id=$2 AND deleted`, 1, entity, other)
	}
	count(`SELECT count(*) FROM work_sync_change WHERE fields::text LIKE '%secret%'`, 0)
	entity := uuid.NewString()
	exec(`INSERT INTO agent(id,workspace_id,name,kind) VALUES ($1,$2,'system','system')`, entity, workspace)
	count(`SELECT count(*) FROM work_sync_change WHERE entity_id=$1`, 0, entity)
	exec(`UPDATE agent SET kind='user' WHERE id=$1`, entity)
	exec(`UPDATE agent SET kind='system' WHERE id=$1`, entity)
	count(`SELECT count(*) FROM work_sync_change WHERE entity_id=$1`, 2, entity)
	count(`SELECT count(*) FROM work_sync_change WHERE entity_id=$1 AND deleted`, 1, entity)
	// Events and the scope registry must obey savepoint rollback. In
	// particular, do not publish a queued change whose business write vanished.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	for _, stmt := range []string{
		`SAVEPOINT discarded`,
		`INSERT INTO issue(id,workspace_id,title) VALUES (gen_random_uuid(),'` + workspace + `','rolled back')`,
		`ROLLBACK TO SAVEPOINT discarded`,
	} {
		if _, err = tx.Exec(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	count(`SELECT count(*) FROM work_sync_change`, 17)
	count(`SELECT count(*) FROM pg_trigger WHERE tgname IN ('work_sync_issue','work_sync_project','work_sync_agent') AND tgdeferrable AND tginitdeferred AND tgrelid IN ('issue'::regclass,'project'::regclass,'agent'::regclass)`, 3)
	// Rolling back only capture/history leaves the durable journal intact.
	apply("down", []string{versions[8], versions[6], versions[5]})
	count(`SELECT count(*) FROM work_sync_change`, 17)
	apply("up", versions[5:])
	for i := len(versions) - 1; i >= 0; i-- {
		apply("down", versions[i:i+1])
	}
	apply("up", versions)
	count(`SELECT count(*) FROM work_sync_scope`, 0)
	count(`SELECT count(*) FROM work_sync_change`, 0)
}
