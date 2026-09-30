package centerrecovery

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Restore targets an offline, empty database. pg_restore runs one transaction
// and never uses --clean: failure must not delete the destination's records.
func Restore(ctx context.Context, data []byte, directory, databaseURL, confirmedCenter, pgRestore string) error {
	manifest, err := Inspect(data)
	if err != nil {
		return err
	}
	if confirmedCenter == "" || confirmedCenter != manifest.CenterID {
		return errors.New("--confirm-center must match the snapshot center ID")
	}
	env, name, err := DatabaseEnvironment(databaseURL)
	if err != nil {
		return err
	}
	// A bare database name containing '=' or a URI prefix would be interpreted
	// by libpq as connection configuration rather than a name.
	if name == "" {
		return errors.New("destination database is required")
	}
	conn, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		return errors.New("could not connect to destination database")
	}
	defer conn.Close(ctx)
	var occupied bool
	err = conn.QueryRow(ctx, `SELECT EXISTS (
 SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
 WHERE n.nspname NOT IN ('pg_catalog','information_schema') AND n.nspname NOT LIKE 'pg_toast%' AND n.nspname NOT LIKE 'pg_temp%'
 UNION ALL SELECT 1 FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname NOT IN ('pg_catalog','information_schema')
 UNION ALL SELECT 1 FROM pg_type t JOIN pg_namespace n ON n.oid=t.typnamespace WHERE n.nspname NOT IN ('pg_catalog','information_schema')
 UNION ALL SELECT 1 FROM pg_largeobject_metadata
 UNION ALL SELECT 1 FROM pg_namespace WHERE nspname NOT IN ('public','pg_catalog','information_schema') AND nspname NOT LIKE 'pg_toast%' AND nspname NOT LIKE 'pg_temp%'
 )`).Scan(&occupied)
	if err != nil {
		return errors.New("could not verify destination is empty")
	}
	if occupied {
		return errors.New("destination database is not empty; recovery never merges or overwrites")
	}
	if pgRestore == "" {
		pgRestore = "pg_restore"
	}
	binary, err := exec.LookPath(pgRestore)
	if err != nil {
		return errors.New("pg_restore is required on PATH or via --pg-restore")
	}
	if err = Extract(data, directory); err != nil {
		return err
	}
	// Use a connection string containing only dbname, escaped according to libpq;
	// host/user/password/TLS remain in the child environment, never in argv.
	cmd := exec.CommandContext(ctx, binary, "--single-transaction", "--exit-on-error", "--no-owner", "--no-acl", "--dbname", connectionDBName(name), filepath.Join(directory, "database.dump"))
	cmd.Env, cmd.Stdout, cmd.Stderr = env, io.Discard, io.Discard
	if err = cmd.Run(); err != nil {
		return errors.New("pg_restore did not report success; inspect the destination before retrying. Extracted files remain for inspection")
	}
	return nil
}

func connectionDBName(name string) string {
	return "dbname='" + strings.NewReplacer("\\", "\\\\", "'", "\\'").Replace(name) + "'"
}
