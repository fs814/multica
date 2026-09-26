package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

const deliveryMigrationVersion = "507_comment_agent_delivery"

// Historical SQL and ledger identities are immutable. Refuse ambiguous 507
// states instead of silently skipping a missing constraint or recreating a
// receipt table. This also detects the DDL-committed/ledger-failed window.
func validateHistoricalDelivery(ctx context.Context, conn *pgxpool.Conn, recorded bool) error {
	var exists bool
	if err := conn.QueryRow(ctx, `SELECT to_regclass(current_schema()||'.comment_agent_delivery') IS NOT NULL`).Scan(&exists); err != nil {
		return err
	}
	if exists != recorded {
		return fmt.Errorf("507 receipt table/ledger mismatch (table=%t ledger=%t); stop and reconcile from a verified backup; do not replay SQL or insert a ledger entry blindly", exists, recorded)
	}
	if !exists {
		return nil
	}
	var columns, pkOK, defaultsOK bool
	var check string
	err := conn.QueryRow(ctx, `SELECT
  (SELECT count(*)=10 FROM pg_attribute WHERE attrelid='comment_agent_delivery'::regclass AND attnum>0 AND NOT attisdropped)
  AND (SELECT count(a.attname)=10 AND bool_and(format_type(atttypid,atttypmod)=expected.typ AND attnotnull=expected.required)
   FROM (VALUES ('comment_id','uuid',true),('agent_id','uuid',true),('task_id','uuid',false),('runtime_id','uuid',false),
   ('status','text',true),('failure_reason','text',false),('created_at','timestamp with time zone',true),
   ('updated_at','timestamp with time zone',true),('claimed_at','timestamp with time zone',false),('delivered_at','timestamp with time zone',false)) expected(name,typ,required)
   LEFT JOIN pg_attribute a ON a.attrelid='comment_agent_delivery'::regclass AND a.attname=expected.name AND NOT a.attisdropped),
  EXISTS(SELECT 1 FROM pg_constraint c JOIN pg_index i ON i.indexrelid=c.conindid
   WHERE c.conrelid='comment_agent_delivery'::regclass AND c.contype='p'
   AND pg_get_constraintdef(c.oid)='PRIMARY KEY (comment_id, agent_id)'
   AND i.indisvalid AND i.indisready AND i.indislive),
  (SELECT count(*)=2 AND bool_and(pg_get_expr(d.adbin,d.adrelid)='now()') FROM pg_attribute a JOIN pg_attrdef d ON d.adrelid=a.attrelid AND d.adnum=a.attnum WHERE a.attrelid='comment_agent_delivery'::regclass AND a.attname IN ('created_at','updated_at')),
  COALESCE((SELECT pg_get_constraintdef(oid) FROM pg_constraint
   WHERE conrelid='comment_agent_delivery'::regclass AND conname='comment_agent_delivery_status_check' AND convalidated),'')`).Scan(&columns, &pkOK, &defaultsOK, &check)
	if err != nil {
		return fmt.Errorf("inspect historical 507: %w", err)
	}
	const wantCheck = "CHECK ((status = ANY (ARRAY['pending'::text, 'steering'::text, 'delivered'::text, 'follow_up'::text])))"
	if !columns || !pkOK || !defaultsOK || strings.TrimSpace(check) != wantCheck {
		return fmt.Errorf("507 receipt schema drift (columns=%t primary_key=%t defaults=%t status_check=%t); stop for explicit recovery, preserving receipts", columns, pkOK, defaultsOK, strings.TrimSpace(check) == wantCheck)
	}
	return nil
}
