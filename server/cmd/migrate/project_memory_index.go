package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Concurrent DDL and the ledger commit separately. An exact, usable index
// permits retrying just the ledger after a failed INSERT. Anything else fails
// closed; the ordinary pre-hook remains responsible for INVALID leftovers.
func createProjectMemoryIndexUnlessExact(index, table, columns string) migrationCondition {
	return createConcurrentIndexUnlessExact(index, table, true, "("+columns+")")
}

// definition is the canonical btree key/predicate tail from the shipped SQL.
// Comparing pg_get_indexdef also rejects changed ordering, predicates and options.
func createConcurrentIndexUnlessExact(index, table string, unique bool, definition string) migrationCondition {
	uniqueness := ""
	if unique {
		uniqueness = " UNIQUE"
	}
	return func(ctx context.Context, conn *pgxpool.Conn) (bool, string, error) {
		var exact bool
		err := conn.QueryRow(ctx, `
   SELECT COALESCE(
    idx.relkind = 'i' AND i.indisvalid AND i.indisready AND i.indislive
    AND i.indisunique = $4 AND i.indimmediate
    AND NOT i.indisprimary AND NOT i.indisexclusion AND NOT i.indnullsnotdistinct
    AND i.indexprs IS NULL
    AND i.indrelid = target.oid AND idx.relnamespace = target.relnamespace
    AND NOT EXISTS (SELECT 1 FROM pg_constraint c WHERE c.conindid = idx.oid)
    AND pg_get_indexdef(idx.oid) = format(
     'CREATE%s INDEX %I ON %I.%I USING btree %s',
     $5::text, $1::text, ns.nspname, target.relname, $3::text), FALSE)
   FROM pg_class idx
   LEFT JOIN pg_index i ON i.indexrelid = idx.oid
   LEFT JOIN pg_class target ON target.oid = to_regclass($2)
   LEFT JOIN pg_namespace ns ON ns.oid = target.relnamespace
   WHERE idx.oid = to_regclass($1)
  `, index, table, definition, unique, uniqueness).Scan(&exact)
		if errors.Is(err, pgx.ErrNoRows) {
			return true, "", nil
		}
		if err != nil {
			return false, "", fmt.Errorf("inspect concurrent index %s: %w", index, err)
		}
		if !exact {
			return false, "", fmt.Errorf("concurrent index %s definition mismatch or unusable", index)
		}
		return false, "exact valid index already exists; recovering ledger", nil
	}
}
