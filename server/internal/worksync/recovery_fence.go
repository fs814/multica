package worksync

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
)

// DatabaseFence permanently closes the old workspace's replicated business
// writes. It does not revoke external execution/network access: those remain
// deployment obligations before recovery. No un-fence endpoint exists.
type DatabaseFence struct {
	SourceURL        string
	SourceDeployment string
	TargetDeployment string
}

func (f DatabaseFence) proof(plan RecoveryPlan, approved string) []byte {
	data, _ := json.Marshal(struct {
		Plan   RecoveryPlan `json:"plan"`
		Report string       `json:"report"`
		Source string       `json:"source_deployment"`
		Target string       `json:"target_deployment"`
	}{plan, approved, f.SourceDeployment, f.TargetDeployment})
	return data
}

func (f DatabaseFence) open(ctx context.Context) (*pgx.Conn, pgx.Tx, error) {
	if !validDeployment(f.SourceDeployment) || !validDeployment(f.TargetDeployment) || f.SourceDeployment == f.TargetDeployment || f.SourceURL == "" {
		return nil, nil, ErrDenied
	}
	conn, err := pgx.Connect(ctx, f.SourceURL)
	if err != nil {
		return nil, nil, err
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		_ = conn.Close(ctx)
		return nil, nil, err
	}
	if err = checkDeployment(ctx, tx, f.SourceDeployment); err != nil {
		_ = tx.Rollback(ctx)
		_ = conn.Close(ctx)
		return nil, nil, err
	}
	return conn, tx, nil
}

// Close waits for no ordinary writer while holding partial barriers. Contention
// rolls back; the operator may retry the same immutable request.
func (f DatabaseFence) Close(ctx context.Context, plan RecoveryPlan, approved string) error {
	conn, tx, err := f.open(ctx)
	if err != nil {
		return err
	}
	defer conn.Close(context.Background())
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, `LOCK TABLE issue,project,agent,work_sync_scope IN SHARE ROW EXCLUSIVE MODE NOWAIT`); err != nil {
		return err
	}
	if err = f.checkGuards(ctx, tx); err != nil {
		return err
	}
	var group, epoch string
	err = tx.QueryRow(ctx, `SELECT group_id::text,epoch::text FROM work_sync_scope WHERE workspace_id=$1`, plan.Source.Workspace).Scan(&group, &epoch)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrScope
	}
	if err != nil {
		return err
	}
	if group != plan.Source.Group || epoch != plan.Source.Epoch {
		return ErrScope
	}
	proof := f.proof(plan, approved)
	tag, err := tx.Exec(ctx, `UPDATE work_sync_recovery_fence SET proof=$2::jsonb WHERE workspace_id=$1 AND (proof IS NULL OR proof=$2::jsonb)`, plan.Source.Workspace, proof)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrDenied
	}
	return tx.Commit(ctx)
}

func (f DatabaseFence) Verify(ctx context.Context, plan RecoveryPlan, approved string) error {
	conn, tx, err := f.open(ctx)
	if err != nil {
		return err
	}
	defer conn.Close(context.Background())
	defer tx.Rollback(context.Background())
	if err = f.checkGuards(ctx, tx); err != nil {
		return err
	}
	var ok bool
	err = tx.QueryRow(ctx, `SELECT proof=$2::jsonb FROM work_sync_recovery_fence WHERE workspace_id=$1 AND proof IS NOT NULL`, plan.Source.Workspace, f.proof(plan, approved)).Scan(&ok)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrDenied
	}
	if err != nil {
		return err
	}
	if !ok {
		return ErrDenied
	}
	return nil
}

func (f DatabaseFence) checkGuards(ctx context.Context, tx pgx.Tx) error {
	var n int
	err := tx.QueryRow(ctx, `SELECT count(*) FROM pg_trigger WHERE tgname='work_sync_recovery_guard'
 AND tgrelid IN ('issue'::regclass,'project'::regclass,'agent'::regclass,'work_sync_scope'::regclass)
 AND tgenabled IN ('O','A') AND tgtype=31 AND tgfoid='work_sync_recovery_write_guard()'::regprocedure`).Scan(&n)
	if err != nil {
		return err
	}
	if n != 4 {
		return ErrDenied
	}
	return nil
}
