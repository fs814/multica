package centersync

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestRuntimeMergeTwoDatabases(t *testing.T) {
	aURL, bURL := os.Getenv("MULTICA_MERGE_TEST_SOURCE_URL"), os.Getenv("MULTICA_MERGE_TEST_PEER_URL")
	if aURL == "" || bURL == "" {
		t.Skip("two isolated managed merge databases required")
	}
	if aURL == bURL {
		t.Fatal("test needs separate databases")
	}
	ctx := context.Background()
	email := uuid.NewString() + "@example.test"
	type center struct {
		h  *Handler
		fx *testutil.Fixture
	}
	setup := func(address, origin string) center {
		pool, err := pgxpool.New(ctx, address)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(pool.Close)
		fx := testutil.New(pool, "", "")
		fx.UserID = fx.User(t, "owner", email)
		h, err := New(pool, Config{Owner: fx.UserID, Origin: origin, Root: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		return center{h, fx}
	}
	a, b := setup(aURL, "https://a.example.test"), setup(bURL, "https://b.example.test")
	a.fx.WorkspaceID = a.fx.Workspace(t, "runtime merge", "runtime-"+uuid.NewString())
	workspace := a.fx.WorkspaceID
	a.fx.Member(t, workspace, a.fx.UserID, "owner")
	daemon := uuid.NewString()
	runtime := a.fx.Runtime(t, "machine Codex", testutil.Cols{"daemon_id": daemon, "runtime_mode": "local", "provider": "codex", "metadata": testutil.Raw(`'{"secret":"source-only"}'::jsonb`)})
	agent := a.fx.Agent(t, "bound", runtime, testutil.Cols{"runtime_mode": "local"})
	profile := a.fx.Insert(t, "runtime_profile", testutil.Cols{"workspace_id": workspace, "display_name": "custom", "protocol_family": "codex", "command_name": "local-wrapper", "fixed_args": testutil.Raw(`'["source-only"]'::jsonb`), "created_by": a.fx.UserID})
	customRuntime := a.fx.Runtime(t, "custom", testutil.Cols{"daemon_id": daemon, "runtime_mode": "local", "provider": "codex", "profile_id": profile})
	customAgent := a.fx.Agent(t, "custom bound", customRuntime, testutil.Cols{"runtime_mode": "local"})
	for _, c := range []center{a, b} {
		c.fx.Cleanup(t, `DELETE FROM center_content_merge WHERE workspace_id=$1`, workspace)
	}
	for _, table := range contentTables {
		b.fx.Cleanup(t, "DELETE FROM "+quoted(table.name)+" t WHERE "+table.scope, workspace)
	}
	b.fx.Cleanup(t, `DELETE FROM member WHERE workspace_id=$1`, workspace)
	export := func(c, peer center) contentBundle {
		bundle, err := c.h.exportContent(ctx, mergeInput{Workspace: workspace, Peer: peer.h.config.Origin})
		if err != nil {
			t.Fatal(err)
		}
		return bundle
	}
	apply := func(c, peer center, bundle contentBundle) mergeResult {
		result, err := c.h.mergeContent(ctx, mergeInput{Workspace: workspace, Peer: peer.h.config.Origin, Bundle: &bundle})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	initial := export(a, b)
	if result := apply(b, a, initial); len(result.Conflicts) != 0 {
		t.Fatal(result.Conflicts)
	}
	var destRuntime, status, metadata string
	var private, noHeartbeat bool
	b.fx.QueryRow(t, `SELECT r.id::text,r.status,r.metadata::text,r.visibility='private',r.last_seen_at IS NULL FROM agent a JOIN agent_runtime r ON r.id=a.runtime_id WHERE a.id=$1`, agent).Scan(&destRuntime, &status, &metadata, &private, &noHeartbeat)
	if status != "offline" || strings.Contains(metadata, "source-only") || !private || !noHeartbeat {
		t.Fatal("runtime liveness or credentials crossed centers")
	}
	var disabled bool
	b.fx.QueryRow(t, `SELECT NOT enabled AND command_name='' AND fixed_args='[]'::jsonb FROM runtime_profile WHERE id=$1`, profile).Scan(&disabled)
	if !disabled {
		t.Fatal("custom profile executable configuration leaked")
	}
	var linked bool
	b.fx.QueryRow(t, `SELECT r.profile_id=$2 FROM agent a JOIN agent_runtime r ON r.id=a.runtime_id WHERE a.id=$1`, customAgent, profile).Scan(&linked)
	if !linked {
		t.Fatal("custom profile binding missing")
	}
	// The real registration query must adopt the imported row, not mint a
	// second runtime. This simulates an authenticated daemon, not a real CLI.
	pgID := func(id string) pgtype.UUID { return pgtype.UUID{Bytes: uuid.MustParse(id), Valid: true} }
	registered, err := db.New(b.h.pool).UpsertAgentRuntime(ctx, db.UpsertAgentRuntimeParams{WorkspaceID: pgID(workspace), DaemonID: pgtype.Text{String: daemon, Valid: true}, Name: "machine Codex", RuntimeMode: "local", Provider: "codex", Status: "online", Metadata: []byte(`{"local":"retained"}`), OwnerID: pgID(b.fx.UserID)})
	if err != nil {
		t.Fatal(err)
	}
	if uuid.UUID(registered.ID.Bytes).String() != destRuntime {
		t.Fatal("registration duplicated synced machine")
	}
	// A subsequent source rename must preserve the peer's online lease and
	// metadata. Reverse sync must reuse the original source's different UUID.
	a.fx.Exec(t, `UPDATE agent_runtime SET custom_name='My machine' WHERE id=$1`, runtime)
	if result := apply(b, a, export(a, b)); len(result.Conflicts) != 0 {
		t.Fatal(result.Conflicts)
	}
	b.fx.QueryRow(t, `SELECT status,metadata::text FROM agent_runtime WHERE id=$1`, destRuntime).Scan(&status, &metadata)
	if status != "online" || !strings.Contains(metadata, "retained") {
		t.Fatal("sync replaced local runtime state")
	}
	if result := apply(a, b, export(b, a)); len(result.Conflicts) != 0 {
		t.Fatal(result.Conflicts)
	}
	var count int
	a.fx.QueryRow(t, `SELECT count(*) FROM agent_runtime WHERE workspace_id=$1 AND daemon_id=$2 AND profile_id IS NULL`, workspace, daemon).Scan(&count)
	if count != 1 {
		t.Fatal("independent UUID mapping duplicated runtime")
	}
	a.fx.QueryRow(t, `SELECT runtime_id=$2 FROM agent WHERE id=$1`, agent, runtime).Scan(&linked)
	if !linked {
		t.Fatal("source binding or local primary key changed")
	}
	// Upgrade an agent imported by content-merge v1, with no runtime field in
	// its saved baseline. Other content and its baseline remain in place.
	b.fx.Exec(t, `UPDATE agent SET runtime_id=NULL WHERE id=$1`, agent)
	b.fx.Exec(t, `UPDATE center_content_merge SET baseline=baseline-'runtime_id' WHERE workspace_id=$1 AND record_key=$2`, workspace, "agent:"+agent)
	if result := apply(b, a, export(a, b)); len(result.Conflicts) != 0 {
		t.Fatal(result.Conflicts)
	}
	b.fx.QueryRow(t, `SELECT runtime_id=$2 FROM agent WHERE id=$1`, agent, destRuntime).Scan(&linked)
	if !linked {
		t.Fatal("previously imported agent stayed unbound")
	}
	// Conflicting executable identity holds the entire transaction; an agent
	// must not bind to the peer's different provider under the same profile ID.
	b.fx.Exec(t, `UPDATE agent_runtime SET provider='claude' WHERE workspace_id=$1 AND profile_id=$2`, workspace, profile)
	a.fx.Exec(t, `UPDATE agent SET name='pending rename' WHERE id=$1`, agent)
	result := apply(b, a, export(a, b))
	if len(result.Conflicts) == 0 || result.Updated != 0 {
		t.Fatal("runtime identity conflict was not held for review")
	}
	var name string
	b.fx.QueryRow(t, `SELECT name FROM agent WHERE id=$1`, agent).Scan(&name)
	if name != "bound" {
		t.Fatal("runtime conflict partially committed workspace changes")
	}
	for _, c := range []center{a, b} {
		c.fx.QueryRow(t, `SELECT count(*) FROM agent_task_queue q JOIN agent a ON a.id=q.agent_id WHERE a.workspace_id=$1`, workspace).Scan(&count)
		if count != 0 {
			t.Fatal("sync dispatched agent work")
		}
	}
}
