package worksync_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/daemon"
	"github.com/multica-ai/multica/server/internal/testutil"
	ws "github.com/multica-ai/multica/server/internal/worksync"
)

func httpGrant(t *testing.T, r rig, node string) string {
	t.Helper()
	r.fx.Runtime(t, "sync fixture", testutil.Cols{"daemon_id": node})
	token := "mdt_fixture_" + uuid.NewString()
	r.fx.Insert(t, "daemon_token", testutil.Cols{"token_hash": auth.HashToken(token), "workspace_id": r.scope.Workspace, "daemon_id": node, "expires_at": testutil.Raw("now()+interval '1 hour'")})
	r.fx.Exec(t, `INSERT INTO work_sync_grant(workspace_id,group_id,epoch,actor_id,node_id,expires_at) VALUES($1,$2,$3,$4,$5,now()+interval '1 hour')`, r.scope.Workspace, r.scope.Group, r.scope.Epoch, r.fx.UserID, node)
	t.Cleanup(func() {
		r.fx.Exec(t, `DELETE FROM work_sync_grant WHERE workspace_id=$1 AND node_id=$2`, r.scope.Workspace, node)
	})
	return token
}
func httpReplica(t *testing.T, r rig, base, node, token string) (*daemon.WorkSyncClient, ws.ReplicaConfig) {
	t.Helper()
	cfg := ws.ReplicaConfig{Enabled: true, Root: t.TempDir(), Scope: r.scope, Principal: ws.Principal{Account: r.fx.UserID, Actor: r.fx.UserID, Node: node}}
	rep, err := ws.OpenReplica(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rep.Close() })
	transport, err := daemon.NewHTTPWorkSyncTransport(base, func() (string, error) { return token, nil })
	if err != nil {
		t.Fatal(err)
	}
	return &daemon.WorkSyncClient{Enabled: true, Replica: rep, Transport: transport}, cfg
}
func syncHTTP(t *testing.T, c *daemon.WorkSyncClient) {
	t.Helper()
	if err := c.SyncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
}
func stateHTTP(t *testing.T, c *daemon.WorkSyncClient) ws.ReplicaState {
	t.Helper()
	s, e := c.Replica.State()
	if e != nil {
		t.Fatal(e)
	}
	return s
}

func TestHTTPWorkSyncTwoDaemonsReconnectRestartAndReplay(t *testing.T) {
	r := setup(t)
	entities := []struct{ kind, id string }{
		{"issue", r.fx.Issue(t, "initial")},
		{"project", r.fx.Project(t, "initial")},
		{"agent", r.fx.Agent(t, "initial", "", testutil.Cols{"custom_env": testutil.Raw(`'{"secret":"never-export"}'::jsonb`)})},
	}
	enroll(t, r)
	tokenA, tokenB := httpGrant(t, r, "a"), httpGrant(t, r, "b")
	var offline, loseResponse atomic.Bool
	var outages atomic.Int32
	handler := ws.NewHTTPHandler(r.c.Pool, true)
	router := chi.NewRouter()
	router.Mount("/api/daemon/sync", http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		if offline.Load() {
			outages.Add(1)
			w.WriteHeader(503)
			return
		}
		if strings.HasSuffix(q.URL.Path, "/push") && loseResponse.CompareAndSwap(true, false) {
			// Commit the operation and lose its acknowledgement on the actual socket.
			out := httptest.NewRecorder()
			handler.ServeHTTP(out, q)
			if out.Code != 200 {
				t.Errorf("lost-response push status %d", out.Code)
			}
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				_ = conn.Close()
			}
			return
		}
		handler.ServeHTTP(w, q)
	}))
	server := httptest.NewServer(router)
	defer server.Close()
	a, cfgA := httpReplica(t, r, server.URL, "a", tokenA)
	b, _ := httpReplica(t, r, server.URL, "b", tokenB)
	syncHTTP(t, a)
	syncHTTP(t, b)
	if data, _ := json.Marshal(stateHTTP(t, a)); bytes.Contains(data, []byte("never-export")) {
		t.Fatal("secret exported")
	}
	wakeup := r.fx.Insert(t, "issue_wakeup", testutil.Cols{
		"id": uuid.NewString(), "workspace_id": r.scope.Workspace, "issue_id": entities[0].id, "agent_id": entities[2].id, "created_by": r.fx.UserID, "instruction": "never run",
		"kind": "event", "mode": "continuous", "event_types": []string{"issue.updated"},
	})
	r.fx.Cleanup(t, `DELETE FROM issue_wakeup_receipt WHERE wakeup_id=$1`, wakeup)
	tasksBefore := r.fx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE agent_id=$1`, entities[2].id)
	// All three entity types accept offline edits and preserve online edits on
	// other fields. Restart A before sending its durable queue.
	offline.Store(true)
	for _, e := range entities {
		queue(t, a.Replica, e.kind, e.id, "description", "offline A")
	}
	queue(t, b.Replica, "issue", entities[0].id, "priority", "high")
	if err := a.SyncOnce(context.Background()); !errors.Is(err, daemon.ErrSyncUnavailable) {
		t.Fatalf("offline: %v", err)
	}
	if err := a.Replica.Close(); err != nil {
		t.Fatal(err)
	}
	var err error
	a.Replica, err = ws.OpenReplica(cfgA)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Replica.Close()
	r.fx.Exec(t, `UPDATE issue SET title='online title' WHERE id=$1`, entities[0].id)
	r.fx.Exec(t, `UPDATE project SET title='online project' WHERE id=$1`, entities[1].id)
	r.fx.Exec(t, `UPDATE agent SET name='online agent' WHERE id=$1`, entities[2].id)
	offline.Store(false)
	loseResponse.Store(true)
	if err = a.SyncOnce(context.Background()); !errors.Is(err, daemon.ErrSyncUnavailable) {
		t.Fatalf("lost response: %v", err)
	}
	if len(stateHTTP(t, a).Outbox) != 3 {
		t.Fatal("lost response removed durable operation")
	}
	// Restart again with the acknowledged-on-Center operation still pending.
	_ = a.Replica.Close()
	a.Replica, err = ws.OpenReplica(cfgA)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Replica.Close()
	syncHTTP(t, a)
	syncHTTP(t, b)
	syncHTTP(t, a)
	as, bs := stateHTTP(t, a), stateHTTP(t, b)
	if !reflect.DeepEqual(as.Records, bs.Records) || len(as.Outbox) != 0 {
		t.Fatal("did not converge")
	}
	if n := r.fx.Count(t, `SELECT count(*) FROM work_sync_receipt WHERE workspace_id=$1`, r.scope.Workspace); n != 4 {
		t.Fatalf("duplicate receipts: %d", n)
	}
	if string(as.Records["issue/"+entities[0].id].Fields["title"]) != `"online title"` {
		t.Fatal("lost Center edit")
	}
	for _, e := range entities {
		if string(as.Records[e.kind+"/"+e.id].Fields["description"]) != `"offline A"` {
			t.Fatal("lost offline edit")
		}
	}
	if n := r.fx.Count(t, `SELECT count(*) FROM issue_wakeup_receipt WHERE wakeup_id=$1`, wakeup); n != 1 {
		t.Fatalf("sync/replay emitted wakeups: %d", n)
	}
	// Same-field collisions retain all three values and survive a checkpoint.
	for _, e := range entities {
		queue(t, a.Replica, e.kind, e.id, "description", "A collision")
		queue(t, b.Replica, e.kind, e.id, "description", "B collision")
	}
	syncHTTP(t, a)
	syncHTTP(t, b)
	syncHTTP(t, a)
	bs = stateHTTP(t, b)
	if len(bs.Review) != 3 {
		t.Fatalf("conflicts=%d", len(bs.Review))
	}
	for _, review := range bs.Review {
		if len(review.Receipt.Conflicts) != 1 || string(review.Receipt.Conflicts[0].Local) != `"B collision"` || string(review.Receipt.Conflicts[0].Base) != `"offline A"` {
			t.Fatal("missing conflict evidence")
		}
	}
	// Center deletion wins against pending edits for each entity kind.
	for _, e := range entities {
		queue(t, b.Replica, e.kind, e.id, "description", "edit after delete")
		r.fx.Exec(t, `DELETE FROM `+e.kind+` WHERE id=$1`, e.id)
	}
	// Both persistent loops reconnect without an event, drain and propagate
	// tombstones. Their lifetimes are joined before stores are closed.
	offline.Store(true)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 2)
	go func() { done <- a.Run(ctx, 10*time.Millisecond, nil) }()
	go func() { done <- b.Run(ctx, 10*time.Millisecond, nil) }()
	retryStart := outages.Load()
	retryDeadline := time.Now().Add(3 * time.Second)
	for outages.Load() < retryStart+2 && time.Now().Before(retryDeadline) {
		time.Sleep(5 * time.Millisecond)
	}
	offline.Store(false)
	deadline := time.Now().Add(5 * time.Second)
	converged := false
	for time.Now().Before(deadline) {
		as, bs = stateHTTP(t, a), stateHTTP(t, b)
		if len(bs.Outbox) == 0 && len(bs.Review) == 6 && reflect.DeepEqual(as.Records, bs.Records) {
			converged = true
			for _, v := range as.Records {
				converged = converged && v.Deleted
			}
			if converged {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	for i := 0; i < 2; i++ {
		if e := <-done; !errors.Is(e, context.Canceled) {
			t.Errorf("loop exit: %v", e)
		}
	}
	if !converged {
		t.Fatal("persistent loops did not converge after reconnect")
	}
	if n := r.fx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE agent_id=$1`, entities[2].id); n != tasksBefore {
		t.Fatalf("sync triggered task: %d", n)
	}
}

func TestHTTPWorkSyncAuthorizationRevocationAndIsolation(t *testing.T) {
	for _, revoke := range []string{"grant", "membership", "role", "token", "runtime", "expiry"} {
		t.Run(revoke, func(t *testing.T) {
			r := setup(t)
			issue := r.fx.Issue(t, "private projection")
			enroll(t, r)
			token := httpGrant(t, r, "a")
			server := httptest.NewServer(ws.NewHTTPHandler(r.c.Pool, true))
			defer server.Close()
			c, cfg := httpReplica(t, r, server.URL, "a", token)
			syncHTTP(t, c)
			op := queue(t, c.Replica, "issue", issue, "description", "quarantined intent")
			// Establish a duplicate receipt, while retaining local pending intent.
			if _, err := c.Transport.Push(context.Background(), cfg.Principal, cfg.Scope, op); err != nil {
				t.Fatal(err)
			}
			switch revoke {
			case "grant":
				r.fx.Exec(t, `UPDATE work_sync_grant SET revoked_at=now() WHERE workspace_id=$1`, r.scope.Workspace)
			case "membership":
				r.fx.Exec(t, `DELETE FROM member WHERE workspace_id=$1 AND user_id=$2`, r.scope.Workspace, r.fx.UserID)
			case "role":
				r.fx.Exec(t, `UPDATE member SET role='member' WHERE workspace_id=$1 AND user_id=$2`, r.scope.Workspace, r.fx.UserID)
			case "token":
				r.fx.Exec(t, `DELETE FROM daemon_token WHERE token_hash=$1`, auth.HashToken(token))
			case "runtime":
				r.fx.Exec(t, `UPDATE agent_runtime SET owner_id=NULL WHERE workspace_id=$1`, r.scope.Workspace)
			case "expiry":
				r.fx.Exec(t, `UPDATE work_sync_grant SET expires_at=now()-interval '1 second' WHERE workspace_id=$1`, r.scope.Workspace)
			}
			if revoke == "token" {
				if err := c.SyncOnce(context.Background()); !errors.Is(err, ws.ErrUnauthenticated) {
					t.Fatalf("invalid credential: %v", err)
				}
				if state := stateHTTP(t, c); len(state.Outbox) != 1 || len(state.Records) == 0 {
					t.Fatal("credential invalidation destroyed recovery copy")
				}
				return
			}
			if _, err := c.Transport.Push(context.Background(), cfg.Principal, cfg.Scope, op); !errors.Is(err, ws.ErrDenied) {
				t.Fatalf("duplicate bypassed revocation: %v", err)
			}
			if err := c.SyncOnce(context.Background()); !errors.Is(err, ws.ErrDenied) {
				t.Fatalf("revoke: %v", err)
			}
			if _, err := c.Replica.State(); !errors.Is(err, ws.ErrDenied) {
				t.Fatal("revoked projection still readable")
			}
			_ = c.Replica.Close()
			rep, err := ws.OpenReplica(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer rep.Close()
			if _, err = rep.State(); !errors.Is(err, ws.ErrDenied) {
				t.Fatal("restart reopened revoked data")
			}
			files, _ := filepath.Glob(filepath.Join(cfg.Root, "work-replicas", "v1", "*", "checkpoint.json"))
			data, err := os.ReadFile(files[0])
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(data, []byte("private projection")) || !bytes.Contains(data, []byte("quarantined intent")) {
				t.Fatal("purge/quarantine mismatch")
			}
		})
	}
}

func TestHTTPWorkSyncRejectsSpoofingAndMalformedWire(t *testing.T) {
	r := setup(t)
	r.fx.Issue(t, "secret workspace")
	enroll(t, r)
	token := httpGrant(t, r, "a")
	other := setup(t)
	other.fx.Issue(t, "other workspace")
	enroll(t, other)
	httpGrant(t, other, "a")
	server := httptest.NewServer(ws.NewHTTPHandler(r.c.Pool, true))
	defer server.Close()
	principal := ws.Principal{Account: r.fx.UserID, Actor: r.fx.UserID, Node: "a"}
	transport, err := daemon.NewHTTPWorkSyncTransport(server.URL, func() (string, error) { return token, nil })
	if err != nil {
		t.Fatal(err)
	}
	if _, err = transport.Pull(context.Background(), principal, other.scope, 0, true); !errors.Is(err, ws.ErrDenied) {
		t.Fatalf("cross workspace: %v", err)
	}
	scope := r.scope
	scope.Epoch = uuid.NewString()
	if err = transport.Handshake(context.Background(), principal, scope); !errors.Is(err, ws.ErrScope) {
		t.Fatalf("wrong history: %v", err)
	}
	principal.Actor = uuid.NewString()
	if err = transport.Handshake(context.Background(), principal, r.scope); !errors.Is(err, ws.ErrScope) {
		t.Fatalf("identity mismatch: %v", err)
	}
	for _, credential := range []string{"", "mat_fixture", "mul_fixture", "mdt_unknown"} {
		body, _ := json.Marshal(ws.Request{Schema: ws.Schema, Scope: r.scope})
		req, _ := http.NewRequest("POST", server.URL+ws.HTTPPrefix+"handshake", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+credential)
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != 401 && response.StatusCode != 403 {
			t.Fatalf("credential status %d", response.StatusCode)
		}
	}
	for _, body := range []string{`{}`, `{"schema":999}`, `{"schema":1,"actor":"spoof"}`, `{} {}`} {
		req := httptest.NewRequest("POST", ws.HTTPPrefix+"pull", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		out := httptest.NewRecorder()
		ws.NewHTTPHandler(r.c.Pool, true).ServeHTTP(out, req)
		if out.Code != 400 {
			t.Fatalf("malformed response %d", out.Code)
		}
	}
	out := httptest.NewRecorder()
	ws.NewHTTPHandler(r.c.Pool, false).ServeHTTP(out, httptest.NewRequest("POST", ws.HTTPPrefix+"pull", nil))
	if out.Code != 404 {
		t.Fatal("default enabled")
	}
}

func TestHTTPWorkSyncHistoryLossPreservesReplica(t *testing.T) {
	r := setup(t)
	r.fx.Issue(t, "recovery copy")
	enroll(t, r)
	token := httpGrant(t, r, "a")
	server := httptest.NewServer(ws.NewHTTPHandler(r.c.Pool, true))
	defer server.Close()
	client, _ := httpReplica(t, r, server.URL, "a", token)
	syncHTTP(t, client)
	before := stateHTTP(t, client)
	r.fx.Exec(t, `UPDATE work_sync_scope SET epoch=gen_random_uuid() WHERE workspace_id=$1`, r.scope.Workspace)
	if err := client.SyncOnce(context.Background()); !errors.Is(err, ws.ErrScope) {
		t.Fatalf("changed history: %v", err)
	}
	if after := stateHTTP(t, client); !reflect.DeepEqual(before, after) {
		t.Fatal("epoch change discarded checkpoint")
	}
	r.fx.Exec(t, `DELETE FROM work_sync_scope WHERE workspace_id=$1`, r.scope.Workspace)
	if err := client.SyncOnce(context.Background()); !errors.Is(err, ws.ErrScope) {
		t.Fatalf("missing history: %v", err)
	}
	if after := stateHTTP(t, client); !reflect.DeepEqual(before, after) {
		t.Fatal("missing history discarded checkpoint")
	}
}
