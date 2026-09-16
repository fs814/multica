package handler

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/projectmemory"
	"github.com/multica-ai/multica/server/internal/testutil"
)

func TestProjectMemoryOwnerAuthorizationMatrix(t *testing.T) {
	ctx := context.Background()
	owner := uuid.NewString()
	runtimeID := dbfx.Runtime(t, "memory owner", testutil.Cols{"daemon_id": owner})
	project := dbfx.Project(t, "memory owner authorization")
	for _, table := range []string{"project_memory_request", "project_memory_binding"} {
		dbfx.Cleanup(t, "DELETE FROM "+table+" WHERE project_id=$1", project)
	}
	c := testHandler.memoryCoordinator()
	b := projectmemory.Binding{WorkspaceID: testWorkspaceID, ProjectID: project, OwnerDaemonID: owner, Backend: "managed", Revision: 1, State: "pending"}
	work, err := c.Submit(ctx, testWorkspaceID, project, "", projectmemory.Operation{Action: "init", Source: "authorization fixture", ExpectedBindingRevision: 1}, &b)
	if err != nil {
		t.Fatal(err)
	}
	token := func(daemon, ws string, expiry time.Time) string {
		value := "mdt_" + uuid.NewString()
		dbfx.Insert(t, "daemon_token", testutil.Cols{"token_hash": auth.HashToken(value), "workspace_id": ws, "daemon_id": daemon, "expires_at": expiry})
		return value
	}
	good := token(owner, testWorkspaceID, time.Now().Add(time.Hour))
	foreign := token(uuid.NewString(), testWorkspaceID, time.Now().Add(time.Hour))
	wrongWS := token(owner, dbfx.Workspace(t, "memory other workspace", "memory-"+uuid.NewString()), time.Now().Add(time.Hour))
	expired := token(owner, testWorkspaceID, time.Now().Add(-time.Second))
	revoked := token(owner, testWorkspaceID, time.Now().Add(time.Hour))
	dbfx.Exec(t, "DELETE FROM daemon_token WHERE token_hash=$1", auth.HashToken(revoked))
	otherUser := dbfx.User(t, "other memory user", uuid.NewString()+"@fixture.invalid")
	dbfx.Member(t, testWorkspaceID, otherUser, "member")
	jwtFor := func(user string) string {
		value, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"sub": user, "exp": time.Now().Add(time.Hour).Unix()}).SignedString(auth.JWTSecret())
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	patFor := func(user string, revoked bool) string {
		value := "mul_" + uuid.NewString()
		dbfx.Insert(t, "personal_access_token", testutil.Cols{"user_id": user, "name": "memory fixture", "token_hash": auth.HashToken(value), "token_prefix": "mul_fixture", "revoked": revoked})
		return value
	}
	router := chi.NewRouter()
	router.Use(middleware.DaemonAuth(testHandler.Queries, nil, nil, nil))
	router.Get("/runtimes/{runtimeId}/memory/next", testHandler.NextProjectMemoryWork)
	router.Post("/runtimes/{runtimeId}/memory/{requestId}/result", testHandler.CompleteProjectMemoryWork)
	base := "/runtimes/" + runtimeID + "/memory"
	fake := projectmemory.Result{Candidate: &projectmemory.Candidate{Generation: uuid.NewString(), Digest: strings.Repeat("a", 64), ContentRevision: 1}}
	for _, tc := range []struct {
		name, token string
		code        int
	}{
		{"same_workspace_other_daemon", foreign, 403}, {"cross_workspace", wrongWS, 404}, {"expired", expired, 401}, {"revoked", revoked, 401},
		{"missing", "", 401}, {"invalid", "mdt_missing_fixture", 401}, {"other_member_jwt", jwtFor(otherUser), 403},
		{"other_member_pat", patFor(otherUser, false), 403}, {"revoked_owner_pat", patFor(testUserID, true), 401}, {"unbound_cloud_identity", "mcn_fixture", 401},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, method := range []string{"GET", "POST"} {
				path := base + "/next"
				var body any
				if method == "POST" {
					path = base + "/" + work.ID + "/result"
					body = fake
				}
				req := testutil.JSONRequest(method, path, body)
				if tc.token != "" {
					req.Header.Set("Authorization", "Bearer "+tc.token)
				}
				req.Header.Set("X-Daemon-ID", owner)
				req.Header.Set("X-User-ID", testUserID)
				testutil.Call(t, router.ServeHTTP, req).Want(tc.code)
			}
			var status string
			dbfx.QueryRow(t, "SELECT status FROM project_memory_request WHERE id=$1", work.ID).Scan(&status)
			current, err := c.Resolve(ctx, testWorkspaceID, project, "")
			if err != nil || status != "pending" || current.Generation != "" {
				t.Fatal("rejected owner request changed queue or binding")
			}
		})
	}
	request := testutil.WithHeaders(testutil.JSONRequest(http.MethodGet, base+"/next", nil), "Authorization", "Bearer "+good)
	var next struct {
		Work *projectmemory.Work `json:"work"`
	}
	testutil.Call(t, router.ServeHTTP, request).Want(200).JSON(&next)
	if next.Work == nil || next.Work.ID != work.ID {
		t.Fatal("owner did not receive work")
	}
	result := projectmemory.Execute(projectmemory.Store{ManagedRoot: filepath.Join(t.TempDir(), "persistent"), DaemonID: owner}, *next.Work)
	req := testutil.WithHeaders(testutil.JSONRequest("POST", base+"/"+work.ID+"/result", result), "Authorization", "Bearer "+good)
	testutil.Call(t, router.ServeHTTP, req).Want(200)
	current, err := c.Resolve(ctx, testWorkspaceID, project, "")
	if err != nil || current.ContentRevision != 1 {
		t.Fatal("owner publication failed")
	}
	for _, value := range []string{jwtFor(testUserID), patFor(testUserID, false)} {
		req := testutil.WithHeaders(testutil.JSONRequest("GET", base+"/next", nil), "Authorization", "Bearer "+value)
		testutil.Call(t, router.ServeHTTP, req).Want(200)
	}
}
