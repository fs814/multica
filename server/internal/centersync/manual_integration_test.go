package centersync

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/testutil"
	ws "github.com/multica-ai/multica/server/internal/worksync"
)

func TestManualAuthRejectsSpoofedHeaders(t *testing.T) {
	h, _ := fixture(t)
	protected := middleware.Auth(nil, nil, nil, nil)(h)
	for _, token := range []string{"", "invalid-session"} {
		r := testutil.JSONRequest("POST", Prefix+"/info", "{}")
		r.Header.Set("X-User-ID", h.config.Owner)
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		testutil.Call(t, protected.ServeHTTP, r).Want(http.StatusUnauthorized)
	}
}

// Run only with this checkout's managed, isolated DATABASE_URL. There is no
// fallback to a developer's default PostgreSQL instance.
func TestManualAuthenticatedRoundTrip(t *testing.T) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("managed DATABASE_URL required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	fx := testutil.New(pool, "", "")
	fx.UserID = fx.User(t, "sync source", uuid.NewString()+"@example.test")
	fx.WorkspaceID = fx.Workspace(t, "sync fixture", "manual-sync-"+uuid.NewString())
	fx.Member(t, fx.WorkspaceID, fx.UserID, "owner")
	issue := fx.Issue(t, "source title")
	destinationOwner := fx.User(t, "sync destination", uuid.NewString()+"@example.test")
	fx.Cleanup(t, `DELETE FROM work_sync_scope WHERE workspace_id=$1`, fx.WorkspaceID)
	fx.Cleanup(t, `DELETE FROM work_sync_receipt WHERE workspace_id=$1`, fx.WorkspaceID)
	fx.Cleanup(t, `DELETE FROM work_sync_change WHERE workspace_id=$1`, fx.WorkspaceID)
	newHandler := func(owner, origin string) *Handler {
		h, err := New(pool, Config{Owner: owner, Origin: origin, Root: filepath.Join(t.TempDir(), "private")})
		if err != nil {
			t.Fatal(err)
		}
		return h
	}
	source := newHandler(fx.UserID, "https://source.example.test")
	destination := newHandler(destinationOwner, "https://destination.example.test")
	call := func(h *Handler, actor, action string, input any, status int, output any) {
		t.Helper()
		token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"sub": actor, "exp": time.Now().Add(time.Hour).Unix()}).SignedString(auth.JWTSecret())
		if err != nil {
			t.Fatal(err)
		}
		r := testutil.JSONRequest("POST", Prefix+"/"+action, input)
		r.Header.Set("Authorization", "Bearer "+token)
		protected := middleware.Auth(nil, nil, nil, nil)(h)
		response := testutil.Call(t, protected.ServeHTTP, r).Want(status)
		if output != nil {
			response.JSON(output)
		}
	}
	var prepared struct {
		Scope     ws.Scope     `json:"scope"`
		Principal ws.Principal `json:"principal"`
	}
	call(source, fx.UserID, "prepare", request{Workspace: fx.WorkspaceID, Principal: ws.Principal{Node: destination.node}}, 200, &prepared)
	input := request{Source: source.config.Origin, Scope: prepared.Scope, Principal: prepared.Principal, Snapshot: true}
	var batch ws.Batch
	call(source, fx.UserID, "pull", input, 200, &batch)
	input.Batch = &batch
	var state ws.ReplicaState
	call(destination, destinationOwner, "apply", input, 200, &state)
	if !state.Initialized || len(state.Records) != 1 {
		t.Fatalf("missing replica: %+v", state)
	}
	input.Kind, input.Entity, input.Patch = "issue", issue, ws.Fields{"title": json.RawMessage(`"replica edit"`)}
	call(destination, destinationOwner, "edit", input, 200, &state)
	if len(state.Outbox) != 1 {
		t.Fatal("edit not queued")
	}
	input.Operation = &state.Outbox[0]
	var receipt ws.Receipt
	call(source, fx.UserID, "push", input, 200, &receipt)
	if receipt.Status != "applied" {
		t.Fatalf("push: %+v", receipt)
	}
	input.Receipt = &receipt
	call(destination, destinationOwner, "acknowledge", input, 200, &state)
	if len(state.Outbox) != 0 {
		t.Fatal("acknowledged edit stayed pending")
	}
	var title string
	fx.QueryRow(t, `SELECT title FROM issue WHERE id=$1 AND workspace_id=$2`, issue, fx.WorkspaceID).Scan(&title)
	if title != "replica edit" {
		t.Fatalf("source not updated: %s", title)
	}
	// Every retry must recheck current workspace ownership, even if the
	// operation already has a durable receipt.
	fx.Exec(t, `UPDATE member SET role='member' WHERE workspace_id=$1 AND user_id=$2`, fx.WorkspaceID, fx.UserID)
	call(source, fx.UserID, "push", input, 403, nil)
	call(destination, fx.UserID, "replica", input, 403, nil)
}
