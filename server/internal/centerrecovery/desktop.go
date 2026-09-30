package centerrecovery

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/auth"
)

const DesktopPrefix = "/api/center/recovery/desktop"

type desktopAuthority struct {
	owner  string
	root   string
	lookup func(context.Context, string) (string, error)
}

type desktopImportAuthority struct {
	Owner       string    `json:"owner"`
	JobID       string    `json:"job_id"`
	SessionHash string    `json:"session_hash"`
	ExpiresAt   time.Time `json:"expires_at"`
}

// DesktopHandlers must be mounted behind Auth + RequireHumanActor. A workspace
// owner is not automatically a deployment recovery owner. The operator chooses
// the local account explicitly; no recovery bearer is minted or substituted.
func DesktopHandlers(pool *pgxpool.Pool, version string, restart func()) (http.HandlerFunc, http.HandlerFunc, http.HandlerFunc) {
	a := desktopAuthority{owner: os.Getenv("MULTICA_RECOVERY_OWNER_ID"), root: StateDir(), lookup: func(ctx context.Context, id string) (string, error) {
		var email string
		err := pool.QueryRow(ctx, `SELECT email FROM "user" WHERE id=$1`, id).Scan(&email)
		return email, err
	}}
	importData, status := managedHandlers(a.root, restart, a.authorize, a.authorizeStatus, a.bindStatus)
	return snapshotHandler(a.authorize, captureFromEnvironment(version)), importData, status
}

func (a desktopAuthority) session(r *http.Request) (string, bool) {
	owner, err := uuid.Parse(a.owner)
	if err != nil || owner == uuid.Nil || owner.String() != a.owner || r.Header.Get("X-User-ID") != a.owner || r.Header.Get("X-Actor-Source") != "" {
		return "", false
	}
	value := r.Header.Get("Authorization")
	if !strings.HasPrefix(value, "Bearer ") {
		return "", false
	}
	token := strings.TrimPrefix(value, "Bearer ")
	claims, err := auth.ParseSessionToken(token)
	if err != nil || claims["sub"] != a.owner {
		return "", false
	}
	return token, true
}

func (a desktopAuthority) authorize(w http.ResponseWriter, r *http.Request) bool {
	if _, ok := a.session(r); !ok {
		http.Error(w, "Sign in as the configured center recovery owner", 403)
		return false
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	email, err := a.lookup(ctx, a.owner)
	if err != nil || auth.IsTemporarilyDisabledUser(a.owner, email) {
		http.Error(w, "Center recovery owner access required", 403)
		return false
	}
	return true
}

func sessionHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func (a desktopAuthority) bindStatus(r *http.Request, jobID string) error {
	token, ok := a.session(r)
	if !ok || a.root == "" || !filepath.IsAbs(a.root) {
		return errors.New("invalid import authority")
	}
	return writePrivateJSON(filepath.Join(a.root, "desktop-import-authority.json"), desktopImportAuthority{
		Owner: a.owner, JobID: jobID, SessionHash: sessionHash(token), ExpiresAt: time.Now().Add(30 * time.Minute),
	})
}

// Restoring may replace the original account table. Permit only the initiating
// session to read this one job for a bounded time; this never grants export or
// import authority after the account has disappeared. No plaintext login is saved.
func (a desktopAuthority) authorizeStatus(w http.ResponseWriter, r *http.Request) bool {
	token, ok := a.session(r)
	var grant desktopImportAuthority
	if !ok || a.root == "" || readJSON(filepath.Join(a.root, "desktop-import-authority.json"), &grant) != nil || grant.Owner != a.owner || grant.JobID != r.URL.Query().Get("job_id") || time.Now().After(grant.ExpiresAt) || subtle.ConstantTimeCompare([]byte(grant.SessionHash), []byte(sessionHash(token))) != 1 {
		http.Error(w, "Import status requires its initiating owner session", 403)
		return false
	}
	var state ImportStatus
	if readJSON(filepath.Join(a.root, "status.json"), &state) != nil || state.JobID != grant.JobID {
		http.Error(w, "Import status unavailable", 404)
		return false
	}
	return true
}
