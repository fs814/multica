package worksync

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/auth"
)

const HTTPPrefix = "/api/daemon/sync/"
const MaxWireBytes = 32 << 20

// Request never accepts an account or actor from the wire. The daemon token,
// explicit grant and current runtime ownership resolve the principal together.
type Request struct {
	Schema    int        `json:"schema"`
	Scope     Scope      `json:"scope"`
	Cursor    int64      `json:"cursor,omitempty"`
	Snapshot  bool       `json:"snapshot,omitempty"`
	Operation *Operation `json:"operation,omitempty"`
}
type Handshake struct {
	Schema    int       `json:"schema"`
	Scope     Scope     `json:"scope"`
	Principal Principal `json:"principal"`
	MaxBatch  int       `json:"max_batch"`
}

// NewHTTPHandler is independently gated and intentionally bypasses the broader
// DaemonAuth fallback/caches: sync accepts only live, workspace-bound mdt tokens.
// Grants are provisioned explicitly by an operator, never by this endpoint.
func NewHTTPHandler(pool *pgxpool.Pool, enabled bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json")
		if !enabled {
			http.Error(w, "work replication disabled", http.StatusNotFound)
			return
		}
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			http.Error(w, "method not allowed", 405)
			return
		}
		action := strings.TrimPrefix(r.URL.Path, HTTPPrefix)
		if action != "handshake" && action != "pull" && action != "push" {
			http.NotFound(w, r)
			return
		}
		header := r.Header.Get("Authorization")
		token := strings.TrimPrefix(header, "Bearer ")
		if !strings.HasPrefix(header, "Bearer ") || !strings.HasPrefix(token, "mdt_") {
			http.Error(w, "daemon credential required", 401)
			return
		}
		var req Request
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, MaxWireBytes))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&req) != nil || decoder.Decode(new(any)) != io.EOF || req.Schema != Schema || req.Scope.Validate() != nil {
			http.Error(w, "invalid sync request", 400)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		hash := auth.HashToken(token)
		// Check independently before returning identity; repeat within each Center
		// transaction, including duplicate Push, so grants are never cached.
		tx, err := pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
		if err != nil {
			syncHTTPError(w, err)
			return
		}
		p, err := authorizedPrincipal(ctx, tx, hash, req.Scope)
		_ = tx.Rollback(ctx)
		if err != nil {
			syncHTTPError(w, err)
			return
		}
		center := Center{Enabled: true, Pool: pool, Authorize: func(ctx context.Context, tx pgx.Tx, principal Principal, scope Scope, action string, op *Operation) error {
			current, err := authorizedPrincipal(ctx, tx, hash, scope)
			if err != nil {
				return err
			}
			if current != principal || action == "enroll" {
				return ErrDenied
			}
			return nil
		}}
		var result any
		switch action {
		case "handshake":
			result = Handshake{Schema: Schema, Scope: req.Scope, Principal: p, MaxBatch: MaxBatch}
		case "pull":
			result, err = center.Pull(ctx, p, req.Scope, req.Cursor, req.Snapshot)
		case "push":
			if req.Operation == nil {
				err = ErrOperation
			} else {
				result, err = center.Push(ctx, p, req.Scope, *req.Operation)
			}
		}
		if err != nil {
			syncHTTPError(w, err)
			return
		}
		data, err := json.Marshal(result)
		if err != nil || len(data) > MaxWireBytes {
			http.Error(w, "sync response exceeds limit", http.StatusInsufficientStorage)
			return
		}
		_, _ = w.Write(data)
	})
}

// Only an explicitly granted owner/admin can receive this unfiltered
// projection. Regular members need a future ACL-filtered journal protocol.
// Runtime ownership is rechecked; a shared daemon ID with mixed owners fails
// closed rather than lending another user's credentials to the grant actor.
func authorizedPrincipal(ctx context.Context, tx pgx.Tx, hash string, scope Scope) (Principal, error) {
	var actor, node, email string
	var workspace string
	err := tx.QueryRow(ctx, `SELECT workspace_id::text, daemon_id FROM daemon_token WHERE token_hash=$1 AND expires_at>now()`, hash).Scan(&workspace, &node)
	if errors.Is(err, pgx.ErrNoRows) {
		return Principal{}, ErrUnauthenticated
	}
	if err != nil {
		return Principal{}, err
	}
	if workspace != scope.Workspace {
		return Principal{}, ErrDenied
	}
	var group, epoch string
	err = tx.QueryRow(ctx, `SELECT group_id::text, epoch::text FROM work_sync_scope WHERE workspace_id=$1`, scope.Workspace).Scan(&group, &epoch)
	if errors.Is(err, pgx.ErrNoRows) {
		return Principal{}, ErrScope
	}
	if err != nil {
		return Principal{}, err
	}
	if group != scope.Group || epoch != scope.Epoch {
		return Principal{}, ErrScope
	}
	err = tx.QueryRow(ctx, `SELECT g.actor_id::text, g.node_id, u.email
 FROM work_sync_grant g
 JOIN work_sync_scope s ON s.workspace_id=g.workspace_id AND s.group_id=g.group_id AND s.epoch=g.epoch
 JOIN daemon_token t ON t.workspace_id=g.workspace_id AND t.daemon_id=g.node_id
 JOIN member m ON m.workspace_id=g.workspace_id AND m.user_id=g.actor_id
 JOIN "user" u ON u.id=g.actor_id
 WHERE t.token_hash=$1 AND t.expires_at>now()
 AND g.workspace_id=$2 AND g.group_id=$3 AND g.epoch=$4
 AND g.revoked_at IS NULL AND g.expires_at>now() AND m.role IN ('owner','admin')
 AND EXISTS (SELECT 1 FROM agent_runtime r WHERE r.workspace_id=g.workspace_id AND r.daemon_id=g.node_id AND r.owner_id=g.actor_id)
 AND NOT EXISTS (SELECT 1 FROM agent_runtime r WHERE r.workspace_id=g.workspace_id AND r.daemon_id=g.node_id AND r.owner_id IS DISTINCT FROM g.actor_id)`, hash, scope.Workspace, scope.Group, scope.Epoch).Scan(&actor, &node, &email)
	if errors.Is(err, pgx.ErrNoRows) {
		return Principal{}, ErrDenied
	}
	if err != nil {
		return Principal{}, err
	}
	if auth.IsTemporarilyDisabledUser(actor, email) {
		return Principal{}, ErrDenied
	}
	return Principal{Account: actor, Actor: actor, Node: node}, nil
}

func syncHTTPError(w http.ResponseWriter, err error) {
	status := http.StatusServiceUnavailable
	switch {
	case errors.Is(err, ErrUnauthenticated):
		status = http.StatusUnauthorized
	case errors.Is(err, ErrDenied):
		status = http.StatusForbidden
	case errors.Is(err, ErrLimit):
		status = http.StatusInsufficientStorage
	case errors.Is(err, ErrDisabled):
		status = http.StatusNotFound
	case errors.Is(err, ErrScope), errors.Is(err, ErrCursor):
		status = http.StatusConflict
	case errors.Is(err, ErrOperation):
		status = http.StatusBadRequest
	}
	// Database/credential details never leave the server.
	http.Error(w, http.StatusText(status), status)
}
