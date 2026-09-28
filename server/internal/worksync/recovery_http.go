package worksync

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/auth"
)

const RecoveryHTTPPrefix = "/api/recovery/work/"

// RecoveryHTTPConfig is operator configuration, never request input. SourceURL
// must point at the old writer database, not a replica or restored clone.
type RecoveryHTTPConfig struct {
	Enabled          bool
	TargetDeployment string
	SourceDeployment string
	SourceURL        string
}

type RecoveryRequest struct {
	Schema         int              `json:"schema"`
	Plan           RecoveryPlan     `json:"plan"`
	Bundles        []RecoveryBundle `json:"bundles,omitempty"`
	ApprovedDigest string           `json:"approved_digest,omitempty"`
}

type RecoveryResponse struct {
	Schema int             `json:"schema"`
	Report *RecoveryReport `json:"report,omitempty"`
	Status string          `json:"status"`
}

func validDeployment(id string) bool {
	u, err := uuid.Parse(id)
	return err == nil && u != uuid.Nil && u.String() == id
}

// NewRecoveryHTTPHandler has no ordinary user/daemon authentication fallback.
// Credentials and their approved plans are provisioned independently in the
// destination database. The endpoint cannot grant or expand recovery authority.
func NewRecoveryHTTPHandler(pool *pgxpool.Pool, cfg RecoveryHTTPConfig) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json")
		if !cfg.Enabled {
			http.NotFound(w, req)
			return
		}
		if !validDeployment(cfg.TargetDeployment) || !validDeployment(cfg.SourceDeployment) ||
			cfg.TargetDeployment == cfg.SourceDeployment || cfg.SourceURL == "" || pool == nil {
			syncHTTPError(w, ErrConfiguration)
			return
		}
		if req.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			http.Error(w, "method not allowed", 405)
			return
		}
		action := strings.TrimPrefix(req.URL.Path, RecoveryHTTPPrefix)
		if action != "stage" && action != "fence" && action != "activate" {
			http.NotFound(w, req)
			return
		}
		token := strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer ")
		if !strings.HasPrefix(req.Header.Get("Authorization"), "Bearer wrc_") || len(token) != 68 {
			syncHTTPError(w, ErrUnauthenticated)
			return
		}
		var input RecoveryRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, req.Body, MaxWireBytes))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF || input.Schema != Schema ||
			input.Plan.Source.Validate() != nil || input.Plan.Target.Validate() != nil ||
			!validDeployment(input.Plan.ID) || !validDeployment(input.Plan.Owner) ||
			input.Plan.Source.Workspace != input.Plan.Target.Workspace ||
			input.Plan.Source.Group != input.Plan.Target.Group || input.Plan.Source.Epoch == input.Plan.Target.Epoch ||
			(action != "stage" && (len(input.Bundles) != 0 || len(input.ApprovedDigest) != 64)) {
			syncHTTPError(w, ErrOperation)
			return
		}
		ctx, cancel := context.WithTimeout(req.Context(), 30*time.Second)
		defer cancel()
		hash := auth.HashToken(token)
		authorize := func(ctx context.Context, tx pgx.Tx, p Principal, plan RecoveryPlan, action string) error {
			current, err := recoveryPrincipal(ctx, tx, cfg, hash, plan, action, input.ApprovedDigest)
			if err != nil {
				return err
			}
			if current != p {
				return ErrDenied
			}
			return nil
		}
		tx, err := pool.Begin(ctx)
		if err != nil {
			syncHTTPError(w, err)
			return
		}
		p, err := recoveryPrincipal(ctx, tx, cfg, hash, input.Plan, action, input.ApprovedDigest)
		_ = tx.Rollback(ctx)
		if err != nil {
			syncHTTPError(w, err)
			return
		}
		fence := DatabaseFence{SourceURL: cfg.SourceURL, SourceDeployment: cfg.SourceDeployment, TargetDeployment: cfg.TargetDeployment}
		service := Recovery{Enabled: true, Pool: pool, Authorize: authorize,
			VerifyFence: func(ctx context.Context, plan RecoveryPlan) error {
				return fence.Verify(ctx, plan, input.ApprovedDigest)
			}}
		result := RecoveryResponse{Schema: Schema, Status: action}
		switch action {
		case "stage":
			var report RecoveryReport
			report, err = service.Stage(ctx, p, input.Plan, input.Bundles)
			result.Report = &report
		case "fence":
			// Check the staged report and current authority while holding the same
			// workspace/session locks as activation. A fence survives a lost reply
			// or destination rollback and can only be retried with this exact plan.
			var tx pgx.Tx
			tx, err = service.begin(ctx, p, input.Plan, action)
			if err == nil {
				defer tx.Rollback(ctx)
				err = lockRecoveryReport(ctx, tx, input.Plan, input.ApprovedDigest)
				if err == nil {
					err = fence.Close(ctx, input.Plan, input.ApprovedDigest)
				}
				if err == nil {
					err = tx.Commit(ctx)
				}
			}
		case "activate":
			err = service.Activate(ctx, p, input.Plan, input.ApprovedDigest)
		}
		if err != nil {
			syncHTTPError(w, err)
			return
		}
		data, err := json.Marshal(result)
		if err != nil || len(data) > MaxWireBytes {
			syncHTTPError(w, ErrLimit)
			return
		}
		_, _ = w.Write(data)
	})
}

var ErrConfiguration = errors.New("recovery configuration unavailable")

func recoveryPrincipal(ctx context.Context, tx pgx.Tx, cfg RecoveryHTTPConfig, hash string, plan RecoveryPlan, action, approved string) (Principal, error) {
	if err := checkDeployment(ctx, tx, cfg.TargetDeployment); err != nil {
		return Principal{}, err
	}
	data, _ := json.Marshal(plan)
	var owner, operator, email string
	// Do not lock a grant before the workspace: workspace deletion uses the
	// opposite order. Revocation applies to subsequent authorization checks;
	// an already-authorized in-flight attempt may finish.
	err := tx.QueryRow(ctx, `SELECT a.owner_id::text,a.operator_id,u.email
 FROM work_sync_recovery_authority a
 JOIN member m ON m.workspace_id=a.workspace_id AND m.user_id=a.owner_id AND m.role='owner'
 JOIN "user" u ON u.id=a.owner_id
 WHERE a.token_hash=$1 AND a.workspace_id=$2 AND a.owner_id=$3 AND a.plan=$4::jsonb
 AND a.source_deployment_id=$5 AND a.target_deployment_id=$6
 AND a.revoked_at IS NULL AND isfinite(a.expires_at) AND a.expires_at>clock_timestamp()
 AND ($7='stage' OR (a.approved_report_hash=$8 AND a.approved_report_hash<>''))
 AND btrim(a.operator_id)<>''`, hash, plan.Target.Workspace, plan.Owner, data, cfg.SourceDeployment, cfg.TargetDeployment, action, approved).Scan(&owner, &operator, &email)
	if errors.Is(err, pgx.ErrNoRows) {
		return Principal{}, ErrDenied
	}
	if err != nil {
		return Principal{}, err
	}
	if auth.IsTemporarilyDisabledUser(owner, email) {
		return Principal{}, ErrDenied
	}
	return Principal{Account: owner, Actor: owner, Node: operator}, nil
}

func checkDeployment(ctx context.Context, tx pgx.Tx, expected string) error {
	var ok bool
	err := tx.QueryRow(ctx, `SELECT count(*)=1 AND COALESCE(bool_and(deployment_id=$1),false) FROM work_sync_recovery_deployment`, expected).Scan(&ok)
	if err != nil {
		return err
	}
	if !ok {
		return ErrDenied
	}
	return nil
}

func lockRecoveryReport(ctx context.Context, tx pgx.Tx, plan RecoveryPlan, approved string) error {
	var owner string
	if err := tx.QueryRow(ctx, `SELECT m.user_id::text FROM workspace w JOIN member m ON m.workspace_id=w.id WHERE w.id=$1 AND m.user_id=$2 AND m.role='owner' FOR UPDATE OF w,m`, plan.Target.Workspace, plan.Owner).Scan(&owner); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrDenied
		}
		return err
	}
	var data []byte
	var hash string
	err := tx.QueryRow(ctx, `SELECT report,report_hash FROM work_sync_recovery WHERE id=$1 AND workspace_id=$2 FOR UPDATE`, plan.ID, plan.Target.Workspace).Scan(&data, &hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrOperation
	}
	if err != nil {
		return err
	}
	var report RecoveryReport
	if json.Unmarshal(data, &report) != nil || report.checksum() != hash || hash != approved || digest(report.Plan) != digest(plan) {
		return ErrOperation
	}
	return nil
}
