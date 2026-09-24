package handler

import (
	"context"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/projectmemory"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Read-only review regression. All data lives in an isolated fixture schema.
func TestReviewMemoryForeignDaemonHTTP(t *testing.T) {
	ctx := context.Background()
	dsn := os.Getenv("PROJECT_MEMORY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Fatal("dedicated database required")
	}
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "review_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE"); admin.Close() })
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	ws, owner, other, project := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	fx := testutil.New(pool, ws, uuid.NewString())
	fx.Exec(t, `CREATE TABLE issue(id uuid,workspace_id uuid,project_id uuid);
 CREATE TABLE chat_session(id uuid,workspace_id uuid,project_id uuid);
 CREATE TABLE agent_task_queue(id uuid,status text);
 CREATE TABLE agent_runtime(id uuid,workspace_id uuid,daemon_id text,name text DEFAULT '',runtime_mode text DEFAULT '',provider text DEFAULT '',status text DEFAULT '',device_info text DEFAULT '',metadata jsonb DEFAULT '{}',last_seen_at timestamptz,created_at timestamptz,updated_at timestamptz,owner_id uuid,legacy_daemon_id text,visibility text DEFAULT '',profile_id uuid,custom_name text);
 CREATE TABLE daemon_token(id uuid,token_hash text,workspace_id uuid,daemon_id text,expires_at timestamptz,created_at timestamptz);`)
	for _, prefix := range []string{"517_", "518_", "519_", "520_", "521_", "522_"} {
		names, err := filepath.Glob("../../migrations/" + prefix + "project_memory*.up.sql")
		if err != nil || len(names) != 1 {
			t.Fatalf("migration %s: matches=%d error=%v", prefix, len(names), err)
		}
		raw, err := os.ReadFile(names[0])
		if err != nil {
			t.Fatal(err)
		}
		fx.Exec(t, string(raw))
	}
	runtimeID := fx.Insert(t, "agent_runtime", testutil.Cols{"id": uuid.NewString(), "workspace_id": ws, "daemon_id": owner})
	token := "mdt_review_fixture"
	fx.Insert(t, "daemon_token", testutil.Cols{"id": uuid.NewString(), "token_hash": auth.HashToken(token), "workspace_id": ws, "daemon_id": other, "expires_at": time.Now().Add(time.Hour)})
	c := projectmemory.Coordinator{DB: pool}
	b := projectmemory.Binding{WorkspaceID: ws, ProjectID: project, OwnerDaemonID: owner, Backend: "managed", Revision: 1}
	w, err := c.Submit(ctx, ws, project, "", projectmemory.Operation{Action: "init", ExpectedBindingRevision: 1, Source: "fixture"}, &b)
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{Queries: db.New(pool), DB: pool, TxStarter: pool}
	t.Run("task_auth_and_project_isolation", func(t *testing.T) {
		fx.Exec(t, `CREATE TABLE project(id uuid,workspace_id uuid,title text DEFAULT '',description text,icon text,status text DEFAULT '',lead_type text,lead_id uuid,created_at timestamptz,updated_at timestamptz,priority text DEFAULT '',start_date date,due_date date);
  CREATE TABLE member(id uuid,workspace_id uuid,user_id uuid,role text,created_at timestamptz);
  CREATE TABLE task_token(id uuid,token_hash text,task_id uuid,agent_id uuid,workspace_id uuid,user_id uuid,expires_at timestamptz,created_at timestamptz);`)
		user, agent := uuid.NewString(), uuid.NewString()
		fx.Insert(t, "project", testutil.Cols{"id": project, "workspace_id": ws})
		bProject := fx.Insert(t, "project", testutil.Cols{"id": uuid.NewString(), "workspace_id": ws})
		fx.Insert(t, "member", testutil.Cols{"id": uuid.NewString(), "workspace_id": ws, "user_id": user, "role": "member"})
		issue := fx.Insert(t, "issue", testutil.Cols{"id": uuid.NewString(), "workspace_id": ws, "project_id": project})
		task := fx.Insert(t, "agent_task_queue", testutil.Cols{"id": uuid.NewString(), "status": "running"})
		_, _, err := c.Claim(ctx, task, projectmemory.Context{WorkspaceID: ws, ProjectID: project, ScopeKind: "issue", ScopeID: issue}, b)
		if err != nil {
			t.Fatal(err)
		}
		taskToken := "mat_review_fixture"
		fx.Insert(t, "task_token", testutil.Cols{"id": uuid.NewString(), "token_hash": auth.HashToken(taskToken), "task_id": task, "agent_id": agent, "workspace_id": ws, "user_id": user, "expires_at": time.Now().Add(time.Hour)})
		bb := b
		bb.ProjectID = bProject
		if _, err = c.Submit(ctx, ws, bProject, "", projectmemory.Operation{Action: "init", ExpectedBindingRevision: 1, Source: "fixture"}, &bb); err != nil {
			t.Fatal(err)
		}
		routes := chi.NewRouter()
		routes.Use(middleware.Auth(h.Queries, nil, nil, nil))
		routes.Use(middleware.RequireWorkspaceMember(h.Queries))
		routes.Get("/api/projects/{id}/memory", h.ResolveProjectMemory)
		routes.Post("/api/projects/{id}/memory/operations", h.SubmitProjectMemory)
		get := func(p string, want int) {
			req := testutil.WithHeaders(testutil.JSONRequest("GET", "/api/projects/"+p+"/memory", nil), "Authorization", "Bearer "+taskToken, "X-Actor-Source", "member", "X-Workspace-ID", uuid.NewString(), "X-Task-ID", uuid.NewString())
			testutil.Call(t, routes.ServeHTTP, req).Want(want)
		}
		get(project, 200)
		get(bProject, 409)
		req := testutil.WithHeaders(testutil.JSONRequest("POST", "/api/projects/"+bProject+"/memory/operations", projectmemory.Operation{Action: "write", Path: "README.md", Content: "cross project", ExpectedBindingRevision: 1, Source: "fixture"}), "Authorization", "Bearer "+taskToken)
		testutil.Call(t, routes.ServeHTTP, req).Want(409)
		fx.Exec(t, "UPDATE issue SET project_id=$1 WHERE id=$2", bProject, issue)
		fx.Exec(t, "UPDATE issue SET project_id=$1 WHERE id=$2", project, issue)
		get(project, 409)
		t.Log("actual Auth/RequireWorkspaceMember: forged headers stripped; current project allowed; other existing project read/write and A-B-A stale context rejected")
	})
	router := chi.NewRouter()
	router.Use(middleware.DaemonAuth(h.Queries, nil, nil, nil))
	router.Get("/api/daemon/runtimes/{runtimeId}/memory/next", h.NextProjectMemoryWork)
	router.Post("/api/daemon/runtimes/{runtimeId}/memory/{requestId}/result", h.CompleteProjectMemoryWork)
	url := "/api/daemon/runtimes/" + runtimeID + "/memory/next"
	req := testutil.WithHeaders(testutil.JSONRequest("GET", url, nil), "Authorization", "Bearer "+token)
	res := testutil.Call(t, router.ServeHTTP, req)
	if res.Code == 200 {
		var got struct {
			Work *projectmemory.Work `json:"work"`
		}
		res.JSON(&got)
		if got.Work == nil || got.Work.ID != w.ID {
			t.Fatalf("unexpected body %s", res.Text())
		}
		t.Errorf("foreign daemon token claimed owner work: HTTP %d, authenticated=%s owner=%s", res.Code, other, owner)
		fake := projectmemory.Result{Candidate: &projectmemory.Candidate{Generation: uuid.NewString(), Digest: strings.Repeat("a", 64), ContentRevision: 1}}
		req = testutil.WithHeaders(testutil.JSONRequest("POST", "/api/daemon/runtimes/"+runtimeID+"/memory/"+w.ID+"/result", fake), "Authorization", "Bearer "+token)
		completion := testutil.Call(t, router.ServeHTTP, req)
		current, e := c.Resolve(ctx, ws, project, "")
		if completion.Code == 200 && e == nil && current.State == "ready" {
			t.Errorf("foreign daemon published nonexistent generation: HTTP 200 revision=%d state=%s", current.ContentRevision, current.State)
		}
	} else if res.Code != 403 && res.Code != 404 {
		t.Fatalf("unexpected rejection %d %s", res.Code, res.Text())
	}
}
