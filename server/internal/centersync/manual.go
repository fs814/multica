// Package centersync exposes manual, owner-only bounded workspace replication.
// There are no peer credentials, outbound HTTP clients, timers or background jobs.
// The Desktop coordinates each request using a separate login on each center.
package centersync

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/mail"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/auth"
	ws "github.com/multica-ai/multica/server/internal/worksync"
)

const Prefix = "/api/center-sync"

type Config struct{ Owner, Root, Origin string }

// NewFromEnvironment does not start a service or transfer data. The operator
// explicitly selects the one local account allowed to invoke manual sync.
func NewFromEnvironment(pool *pgxpool.Pool) (*Handler, error) {
	if os.Getenv("MULTICA_CENTER_SYNC_ENABLED") != "1" {
		return nil, nil
	}
	owner, err := resolveOwner(os.Getenv("MULTICA_CENTER_SYNC_OWNER_ID"), os.Getenv("MULTICA_CENTER_SYNC_OWNER_EMAIL"), func(email string) ([]string, error) {
		if pool == nil {
			return nil, errors.New("database unavailable")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		rows, err := pool.Query(ctx, `SELECT id::text FROM "user" WHERE lower(email)=lower($1) LIMIT 2`, email)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var ids []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return nil, err
			}
			ids = append(ids, id)
		}
		return ids, rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return New(pool, Config{Owner: owner, Root: os.Getenv("MULTICA_CENTER_SYNC_DIR"), Origin: os.Getenv("MULTICA_CENTER_SYNC_ORIGIN")})
}

// Resolve once against this center's database, never from a client request or
// another server's account ID. An explicit ID must agree with the email.
func resolveOwner(id, email string, lookup func(string) ([]string, error)) (string, error) {
	if email == "" {
		return id, nil
	}
	parsed, err := mail.ParseAddress(email)
	if err != nil || parsed.Address != email || (id != "" && !validID(id)) {
		return "", errors.New("manual center sync requires a plain owner email and valid optional owner UUID")
	}
	ids, err := lookup(email)
	if err != nil {
		return "", errors.New("manual center sync owner lookup failed; check this center's database")
	}
	if len(ids) != 1 || !validID(ids[0]) {
		return "", errors.New("manual center sync owner email must match exactly one existing local account; sign in first, then restart the center")
	}
	if id != "" && id != ids[0] {
		return "", errors.New("manual center sync owner UUID does not match the configured email on this center")
	}
	return ids[0], nil
}

type Handler struct {
	pool            *pgxpool.Pool
	config          Config
	node            string
	MembershipCache *auth.MembershipCache
}

func New(pool *pgxpool.Pool, config Config) (*Handler, error) {
	if !validID(config.Owner) || config.Root == "" || !filepath.IsAbs(config.Root) || origin(config.Origin) != nil {
		return nil, errors.New("manual center sync requires owner UUID, absolute private state directory and HTTPS origin")
	}
	// The destination identity binds the outbox to this origin and local owner.
	sum := sha256.Sum256([]byte(config.Origin + "\n" + config.Owner))
	return &Handler{pool: pool, config: config, node: "manual-center-" + hex.EncodeToString(sum[:])}, nil
}

func validID(s string) bool {
	u, err := uuid.Parse(s)
	return err == nil && u != uuid.Nil && u.String() == s
}
func origin(value string) error {
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return ws.ErrScope
	}
	return nil
}

type request struct {
	Workspace string        `json:"workspace,omitempty"`
	Source    string        `json:"source,omitempty"`
	Scope     ws.Scope      `json:"scope"`
	Principal ws.Principal  `json:"principal"`
	Cursor    int64         `json:"cursor,omitempty"`
	Snapshot  bool          `json:"snapshot,omitempty"`
	Batch     *ws.Batch     `json:"batch,omitempty"`
	Operation *ws.Operation `json:"operation,omitempty"`
	Receipt   *ws.Receipt   `json:"receipt,omitempty"`
	Kind      string        `json:"kind,omitempty"`
	Entity    string        `json:"entity,omitempty"`
	Patch     ws.Fields     `json:"patch,omitempty"`
}

// ServeHTTP must be mounted behind the application's Auth + RequireHumanActor.
// X-User-ID is stamped by Auth, never accepted directly on a public route.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Header.Get("X-User-ID") != h.config.Owner || r.Header.Get("X-Actor-Source") != "" {
		http.Error(w, "Only the configured sync owner can sync this center", 403)
		return
	}
	if r.Method != "POST" {
		http.Error(w, "Manual sync requires POST", 405)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := h.owner(ctx); err != nil {
		respondError(w, err)
		return
	}
	if strings.HasPrefix(r.URL.Path, Prefix+"/merge-") {
		h.serveMerge(ctx, w, r)
		return
	}
	var input request
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, ws.MaxWireBytes))
	d.DisallowUnknownFields()
	if d.Decode(&input) != nil || d.Decode(new(any)) != io.EOF {
		respondError(w, ws.ErrOperation)
		return
	}
	var result any
	var err error
	switch strings.TrimPrefix(r.URL.Path, Prefix+"/") {
	case "info":
		result = map[string]any{"schema": ws.Schema, "mode": "manual", "owner": h.config.Owner, "origin": h.config.Origin, "node": h.node, "content_merge": mergeVersion, "attachment_warnings": 1, "attachment_chunks": 1}
	case "prepare":
		result, err = h.prepare(ctx, input)
	case "pull", "push":
		if input.Principal.Actor != h.config.Owner || input.Principal.Account != h.config.Owner || !validNode(input.Principal.Node) {
			err = ws.ErrDenied
			break
		}
		if strings.HasSuffix(r.URL.Path, "/pull") {
			result, err = h.center().Pull(ctx, input.Principal, input.Scope, input.Cursor, input.Snapshot)
		} else if input.Operation == nil {
			err = ws.ErrOperation
		} else {
			result, err = h.center().Push(ctx, input.Principal, input.Scope, *input.Operation)
		}
	case "replica", "apply", "acknowledge", "edit":
		result, err = h.replica(input, strings.TrimPrefix(r.URL.Path, Prefix+"/"))
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		respondError(w, err)
		return
	}
	data, err := json.Marshal(result)
	if err != nil || len(data) > ws.MaxWireBytes {
		respondError(w, ws.ErrLimit)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(data)
}

func validNode(s string) bool {
	if !strings.HasPrefix(s, "manual-center-") || len(s) != len("manual-center-")+64 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(s, "manual-center-"))
	return err == nil
}

func (h *Handler) owner(ctx context.Context) error {
	var email string
	err := h.pool.QueryRow(ctx, `SELECT email FROM "user" WHERE id=$1`, h.config.Owner).Scan(&email)
	if errors.Is(err, pgx.ErrNoRows) || auth.IsTemporarilyDisabledUser(h.config.Owner, email) {
		return ws.ErrDenied
	}
	return err
}

func (h *Handler) center() *ws.Center {
	return &ws.Center{Enabled: true, Pool: h.pool, Authorize: func(ctx context.Context, tx pgx.Tx, p ws.Principal, scope ws.Scope, action string, op *ws.Operation) error {
		if p.Actor != h.config.Owner || p.Account != h.config.Owner || !validNode(p.Node) {
			return ws.ErrDenied
		}
		var email string
		err := tx.QueryRow(ctx, `SELECT u.email FROM "user" u JOIN member m ON m.user_id=u.id WHERE u.id=$1 AND m.workspace_id=$2 AND m.role='owner'`, p.Actor, scope.Workspace).Scan(&email)
		if errors.Is(err, pgx.ErrNoRows) || auth.IsTemporarilyDisabledUser(p.Actor, email) {
			return ws.ErrDenied
		}
		if err != nil {
			return err
		}
		var fenced bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM work_sync_recovery_fence WHERE workspace_id=$1 AND proof IS NOT NULL)`, scope.Workspace).Scan(&fenced); err != nil {
			return err
		}
		if fenced {
			return ws.ErrScope
		}
		return nil
	}}
}

func (h *Handler) prepare(ctx context.Context, input request) (any, error) {
	if !validID(input.Workspace) || !validNode(input.Principal.Node) {
		return nil, ws.ErrScope
	}
	p := ws.Principal{Account: h.config.Owner, Actor: h.config.Owner, Node: input.Principal.Node}
	scope := ws.Scope{Workspace: input.Workspace}
	err := h.pool.QueryRow(ctx, `SELECT group_id::text,epoch::text FROM work_sync_scope WHERE workspace_id=$1`, input.Workspace).Scan(&scope.Group, &scope.Epoch)
	if errors.Is(err, pgx.ErrNoRows) {
		scope.Group = uuid.NewString()
		scope.Epoch = uuid.NewString()
		err = h.center().Enroll(ctx, p, scope)
	}
	if err != nil {
		return nil, err
	}
	// Check current ownership/history even for an already-enrolled workspace.
	if _, err = h.center().Pull(ctx, p, scope, 0, false); err != nil {
		return nil, err
	}
	return map[string]any{"scope": scope, "principal": p}, nil
}

func (h *Handler) replica(input request, action string) (any, error) {
	if origin(input.Source) != nil || input.Source == h.config.Origin || input.Scope.Validate() != nil || !validID(input.Principal.Actor) || input.Principal.Account != input.Principal.Actor || input.Principal.Node != h.node {
		return nil, ws.ErrScope
	}
	if err := os.MkdirAll(h.config.Root, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(h.config.Root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("sync directory must be owner-only")
	}
	namespace := sha256.Sum256([]byte(h.config.Owner + "\n" + input.Source))
	r, err := ws.OpenReplica(ws.ReplicaConfig{Enabled: true, Root: filepath.Join(h.config.Root, hex.EncodeToString(namespace[:])), Scope: input.Scope, Principal: input.Principal})
	if err != nil {
		return nil, err
	}
	defer r.Close()
	switch action {
	case "apply":
		if input.Batch == nil {
			return nil, ws.ErrOperation
		}
		err = r.Apply(*input.Batch)
	case "acknowledge":
		if input.Receipt == nil {
			return nil, ws.ErrOperation
		}
		err = r.Acknowledge(*input.Receipt)
	case "edit":
		_, err = r.Queue(input.Kind, input.Entity, input.Patch)
	}
	if err != nil {
		return nil, err
	}
	return r.State()
}

func respondError(w http.ResponseWriter, err error) {
	status, message := 503, "Manual sync failed; check server configuration"
	switch {
	case errors.Is(err, ws.ErrDenied):
		status, message = 403, "Sync owner or workspace owner permission required"
	case errors.Is(err, ws.ErrScope), errors.Is(err, ws.ErrCursor):
		status, message = 409, "Sync identity or history mismatch"
	case errors.Is(err, ws.ErrOperation):
		status, message = 400, "Invalid sync operation"
	case errors.Is(err, ws.ErrLimit):
		status, message = 507, "Workspace exceeds bounded sync capacity"
	}
	http.Error(w, message, status)
}
