package worksync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Authorize must check the current actor, node registration, workspace and
// resource permissions using tx. Read/enroll require an explicitly authorized
// full projection: this foundation does not implement filtered replicas.
// No production adapter is installed until that grant model is approved.
type Authorize func(context.Context, pgx.Tx, Principal, Scope, string, *Operation) error

type Center struct {
	Enabled   bool
	Pool      *pgxpool.Pool
	Authorize Authorize
}

func id(s string) pgtype.UUID { var u pgtype.UUID; _ = u.Scan(s); return u }
func (c *Center) begin(ctx context.Context, p Principal, scope Scope, action string, op *Operation, read bool) (pgx.Tx, error) {
	if !c.Enabled {
		return nil, ErrDisabled
	}
	if scope.Validate() != nil {
		return nil, ErrScope
	}
	if c.Authorize == nil || p.Account == "" || p.Actor == "" || p.Node == "" {
		return nil, ErrDenied
	}
	opts := pgx.TxOptions{}
	if read {
		opts.IsoLevel = pgx.RepeatableRead
		opts.AccessMode = pgx.ReadOnly
	}
	tx, err := c.Pool.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	if err := c.Authorize(ctx, tx, p, scope, action, op); err != nil {
		_ = tx.Rollback(ctx)
		return nil, ErrDenied
	}
	return tx, nil
}

// Enroll explicitly establishes a baseline; it is never called at startup or
// by a migration. Table locks prevent writes from slipping between the baseline
// and journal installation. Existing deletions cannot be inferred at bootstrap.
func (c *Center) Enroll(ctx context.Context, p Principal, scope Scope) error {
	tx, err := c.begin(ctx, p, scope, "enroll", nil, false)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	// Match teardown's workspace -> business -> scope order and prevent a
	// delayed enrollment from resurrecting a scope after workspace deletion.
	var workspace string
	if err = tx.QueryRow(ctx, `SELECT id FROM workspace WHERE id=$1 FOR KEY SHARE`, scope.Workspace).Scan(&workspace); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `LOCK TABLE issue, project, agent IN SHARE ROW EXCLUSIVE MODE`); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO work_sync_scope(workspace_id,group_id,epoch) VALUES ($1,$2,$3)`, scope.Workspace, scope.Group, scope.Epoch); err != nil {
		return err
	}
	for _, kind := range []string{"project", "agent", "issue"} {
		_, err = tx.Exec(ctx, `SELECT work_sync_capture_row($1,to_jsonb(t),false) FROM `+kind+` t WHERE workspace_id=$2`, kind, scope.Workspace)
		if err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func scopeMatches(s db.WorkSyncScope, scope Scope) bool {
	return s.GroupID == id(scope.Group) && s.Epoch == id(scope.Epoch)
}
func record(row db.WorkSyncChange) (Record, error) {
	r := Record{Kind: row.Kind, ID: row.EntityID.String(), Version: row.Sequence, Deleted: row.Deleted}
	err := json.Unmarshal(row.Fields, &r.Fields)
	if err == nil {
		err = r.Validate()
	}
	return r, err
}

func (c *Center) Pull(ctx context.Context, p Principal, scope Scope, cursor int64, snapshot bool) (Batch, error) {
	tx, err := c.begin(ctx, p, scope, "read", nil, true)
	if err != nil {
		return Batch{}, err
	}
	defer tx.Rollback(ctx)
	q := db.New(tx)
	s, err := q.GetWorkSyncScope(ctx, id(scope.Workspace))
	if err != nil {
		return Batch{}, err
	}
	if !scopeMatches(s, scope) {
		return Batch{}, ErrScope
	}
	if cursor < 0 || cursor > s.Sequence || (snapshot && cursor != 0) {
		return Batch{}, ErrCursor
	}
	b := Batch{Schema: Schema, Scope: scope, From: cursor, Cursor: cursor, Snapshot: snapshot, Records: []Record{}}
	var rows []db.WorkSyncChange
	if snapshot {
		rows, err = q.ListWorkSyncSnapshot(ctx, db.ListWorkSyncSnapshotParams{WorkspaceID: id(scope.Workspace), Limit: MaxSnapshot + 1})
		if len(rows) > MaxSnapshot {
			return Batch{}, fmt.Errorf("snapshot exceeds foundation limit")
		}
		b.Cursor = s.Sequence
	} else {
		rows, err = q.ListWorkSyncChanges(ctx, db.ListWorkSyncChangesParams{WorkspaceID: id(scope.Workspace), Sequence: cursor, Limit: MaxBatch})
	}
	if err != nil {
		return Batch{}, err
	}
	for _, row := range rows {
		r, e := record(row)
		if e != nil {
			return Batch{}, e
		}
		b.Records = append(b.Records, r)
		if !snapshot {
			b.Cursor = r.Version
		}
	}
	b.Seal()
	if err = b.Validate(scope); err != nil {
		return Batch{}, err
	}
	return b, tx.Commit(ctx)
}

// Push merges only descriptive fields on existing entities. It deliberately
// does not invoke command handlers: assignment, workflow transitions, creation,
// deletion and execution remain online commands until domain policies exist.
func (c *Center) Push(ctx context.Context, p Principal, scope Scope, op Operation) (Receipt, error) {
	if !c.Enabled {
		return Receipt{}, ErrDisabled
	}
	if err := op.Validate(p, scope); err != nil {
		return Receipt{}, err
	}
	tx, err := c.begin(ctx, p, scope, "write", &op, false)
	if err != nil {
		return Receipt{}, err
	}
	defer tx.Rollback(ctx)
	// Acquire the only business row this transaction writes before its journal scope.
	if _, err = tx.Exec(ctx, `SELECT id FROM `+op.Kind+` WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, scope.Workspace, op.Entity); err != nil {
		return Receipt{}, err
	}
	q := db.New(tx)
	s, err := q.LockWorkSyncScope(ctx, id(scope.Workspace))
	if err != nil {
		return Receipt{}, err
	}
	if !scopeMatches(s, scope) {
		return Receipt{}, ErrScope
	}
	hash := digest(op)
	previous, err := q.GetWorkSyncReceipt(ctx, db.GetWorkSyncReceiptParams{WorkspaceID: id(scope.Workspace), OperationID: id(op.ID)})
	if err == nil {
		if previous.PayloadHash != hash || previous.ActorID != p.Actor || previous.NodeID != p.Node {
			return Receipt{}, ErrOperation
		}
		var receipt Receipt
		err = json.Unmarshal(previous.Receipt, &receipt)
		return receipt, err
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Receipt{}, err
	}
	last, err := q.GetWorkSyncNodeSequence(ctx, db.GetWorkSyncNodeSequenceParams{WorkspaceID: id(scope.Workspace), NodeID: op.Node, Incarnation: id(op.Incarnation)})
	if err != nil {
		return Receipt{}, err
	}
	if op.Sequence != last+1 {
		return Receipt{}, fmt.Errorf("%w: out of order", ErrOperation)
	}
	receipt := Receipt{Operation: op.ID, Status: "rejected"}
	localFields := map[string]AcceptedField{}
	if op.DependsOn != "" {
		dep, e := q.GetWorkSyncReceipt(ctx, db.GetWorkSyncReceiptParams{WorkspaceID: id(scope.Workspace), OperationID: id(op.DependsOn)})
		var r Receipt
		if e != nil || dep.NodeID != op.Node || dep.ActorID != op.Actor || dep.Incarnation != id(op.Incarnation) || dep.Sequence >= op.Sequence || json.Unmarshal(dep.Receipt, &r) != nil || r.Status != "applied" || r.Record.Key() != op.Kind+"/"+op.Entity {
			receipt.Reason = "dependency not applied"
		} else {
			for key, field := range r.LocalFields {
				localFields[key] = field
			}
		}
	}
	if receipt.Reason == "" {
		currentRow, e := q.GetWorkSyncCurrent(ctx, db.GetWorkSyncCurrentParams{WorkspaceID: id(scope.Workspace), Kind: op.Kind, EntityID: id(op.Entity)})
		if e != nil && !errors.Is(e, pgx.ErrNoRows) {
			return Receipt{}, e
		}
		if errors.Is(e, pgx.ErrNoRows) {
			receipt.Reason = "entity missing"
		} else {
			current, e := record(currentRow)
			if e != nil {
				return Receipt{}, e
			}
			receipt.Record = current
			baseRow, e := q.GetWorkSyncBase(ctx, db.GetWorkSyncBaseParams{WorkspaceID: id(scope.Workspace), Kind: op.Kind, EntityID: id(op.Entity), Sequence: op.Base})
			if e != nil && !errors.Is(e, pgx.ErrNoRows) {
				return Receipt{}, e
			}
			if errors.Is(e, pgx.ErrNoRows) {
				receipt.Reason = "base missing"
			} else if current.Deleted {
				receipt.Status = "conflict"
				receipt.Reason = "entity deleted"
			} else {
				base, e := record(baseRow)
				if e != nil {
					return Receipt{}, e
				}
				for key, field := range localFields {
					if field.Version > base.Version {
						base.Fields[key] = field.Value
					}
				}
				_, conflicts, e := Merge(base, current, op.Patch)
				if e != nil {
					return Receipt{}, e
				}
				if len(conflicts) > 0 {
					receipt.Status = "conflict"
					receipt.Conflicts = conflicts
				} else {
					changed := false
					for key, value := range op.Patch {
						if !equal(current.Fields[key], value) {
							changed = true
							break
						}
					}
					if changed {
						if err = applyPatch(ctx, tx, scope, op); err != nil {
							return Receipt{}, err
						}
					}
					// All business writes are finished. Flush deferred capture before
					// reading the resulting version and persisting the receipt.
					if _, err = tx.Exec(ctx, `SET CONSTRAINTS work_sync_issue, work_sync_project, work_sync_agent IMMEDIATE`); err != nil {
						return Receipt{}, err
					}
					row, e := q.GetWorkSyncCurrent(ctx, db.GetWorkSyncCurrentParams{WorkspaceID: id(scope.Workspace), Kind: op.Kind, EntityID: id(op.Entity)})
					if e != nil {
						return Receipt{}, e
					}
					receipt.Record, e = record(row)
					if e != nil {
						return Receipt{}, e
					}
					receipt.Status = "applied"
					for key, value := range op.Patch {
						localFields[key] = AcceptedField{Version: receipt.Record.Version, Value: value}
					}
					receipt.LocalFields = localFields
				}
			}
		}
	}
	body, err := json.Marshal(receipt)
	if err != nil {
		return Receipt{}, err
	}
	err = q.SaveWorkSyncReceipt(ctx, db.SaveWorkSyncReceiptParams{WorkspaceID: id(scope.Workspace), OperationID: id(op.ID), NodeID: op.Node, Incarnation: id(op.Incarnation), Sequence: op.Sequence, ActorID: op.Actor, PayloadHash: hash, Receipt: body})
	if err != nil {
		return Receipt{}, err
	}
	return receipt, tx.Commit(ctx)
}

func applyPatch(ctx context.Context, tx pgx.Tx, scope Scope, op Operation) error {
	if _, err := tx.Exec(ctx, `SELECT set_config('multica.work_sync_apply','on',true)`); err != nil {
		return err
	}
	keys := make([]string, 0, len(op.Patch))
	for key := range op.Patch {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	args := []any{scope.Workspace, op.Entity}
	sets := []string{}
	for _, key := range keys {
		// Validate has already constrained both identifiers and value types.
		var v *string
		if err := json.Unmarshal(op.Patch[key], &v); err != nil {
			return err
		}
		args = append(args, v)
		sets = append(sets, fmt.Sprintf("%s=$%d", pgx.Identifier{key}.Sanitize(), len(args)))
	}
	sets = append(sets, "updated_at=now()")
	if op.Kind == "issue" {
		sets = append(sets, "revision=revision+1")
	}
	_, err := tx.Exec(ctx, `UPDATE `+op.Kind+` SET `+strings.Join(sets, ",")+` WHERE workspace_id=$1 AND id=$2`, args...)
	return err
}
