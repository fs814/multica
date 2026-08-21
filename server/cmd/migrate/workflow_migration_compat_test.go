package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5"
)

// TestWorkflowDuplicatePrefixCompatibility proves the deployment constraint
// behind the 232-253 compatibility allow-list. Migration identity is the full
// stem, not the numeric display prefix, so two files that share a prefix remain
// independently replay-safe.
func TestWorkflowDuplicatePrefixCompatibility(t *testing.T) {
	t.Run("fresh database applies both full stems exactly once", func(t *testing.T) {
		f := newWorkflowCompatFixture(t)
		if err := runMigrations(context.Background(), f.pool, f.opts()); err != nil {
			t.Fatalf("fresh migrate: %v", err)
		}
		if got := f.appliedVersions(t); fmt.Sprint(got) != fmt.Sprint(f.versions) {
			t.Fatalf("versions = %v, want %v", got, f.versions)
		}
		for _, table := range f.tableNames {
			if !f.tableExists(t, table) {
				t.Fatalf("fresh migration did not create %s", table)
			}
		}
		// A second run exercises the same no-op path an already-upgraded server
		// takes; the fixture SQL is deliberately non-idempotent, so any replay
		// would fail with relation already exists.
		if err := runMigrations(context.Background(), f.pool, f.opts()); err != nil {
			t.Fatalf("fresh rerun replayed a full stem: %v", err)
		}
	})

	t.Run("already executed database skips both full stems", func(t *testing.T) {
		f := newWorkflowCompatFixture(t)
		ctx := context.Background()
		quotedTable := pgx.Identifier{f.schema, "schema_migrations"}.Sanitize()
		if _, err := f.pool.Exec(ctx, fmt.Sprintf(`CREATE TABLE %s (version TEXT PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL DEFAULT now())`, quotedTable)); err != nil {
			t.Fatalf("create ledger: %v", err)
		}
		for i, version := range f.versions {
			quotedData := pgx.Identifier{f.schema, f.tableNames[i]}.Sanitize()
			if _, err := f.pool.Exec(ctx, fmt.Sprintf(`CREATE TABLE %s (id BIGSERIAL PRIMARY KEY)`, quotedData)); err != nil {
				t.Fatalf("seed already-applied schema: %v", err)
			}
			if _, err := f.pool.Exec(ctx, fmt.Sprintf(`INSERT INTO %s (version) VALUES ($1)`, quotedTable), version); err != nil {
				t.Fatalf("seed migration ledger: %v", err)
			}
		}
		if err := runMigrations(ctx, f.pool, f.opts()); err != nil {
			t.Fatalf("already-applied migrate replayed SQL: %v", err)
		}
		if got := f.appliedVersions(t); fmt.Sprint(got) != fmt.Sprint(f.versions) {
			t.Fatalf("versions changed: got %v, want %v", got, f.versions)
		}
	})
}

func newWorkflowCompatFixture(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t)
	dir := t.TempDir()
	f.files = nil
	f.versions = []string{"232_existing_feature", "232_workflow_template"}
	f.tableNames = []string{"compat_existing", "compat_workflow"}
	for i, version := range f.versions {
		body := fmt.Sprintf("CREATE TABLE %s.%s (id BIGSERIAL PRIMARY KEY);\n",
			pgx.Identifier{f.schema}.Sanitize(), pgx.Identifier{f.tableNames[i]}.Sanitize())
		path := filepath.Join(dir, version+".up.sql")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("write compatibility migration: %v", err)
		}
		f.files = append(f.files, path)
	}
	return f
}
