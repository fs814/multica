package worksync_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/testutil"
	ws "github.com/multica-ai/multica/server/internal/worksync"
)

type recoveryHTTPFixture struct {
	source                                        rig
	target                                        *testutil.Fixture
	cfg                                           ws.RecoveryHTTPConfig
	plan                                          ws.RecoveryPlan
	bundles                                       []ws.RecoveryBundle
	digest, token, issue, project, agent, deleted string
	server                                        *httptest.Server
	baseURL                                       string
}

func setupRecoveryHTTP(t *testing.T) *recoveryHTTPFixture {
	t.Helper()
	targetURL := os.Getenv("WORK_SYNC_RECOVERY_TEST_DATABASE_URL")
	if targetURL == "" {
		t.Skip("separate isolated WORK_SYNC_RECOVERY_TEST_DATABASE_URL required")
	}
	r := setup(t)
	h := &recoveryHTTPFixture{source: r}
	h.project = r.fx.Project(t, "recover project")
	h.agent = r.fx.Agent(t, "recover agent", "")
	h.issue = r.fx.Issue(t, "recover issue", testutil.Cols{"project_id": h.project})
	h.deleted = r.fx.Issue(t, "deleted")
	enroll(t, r)
	reps, _, _ := recoveryCopies(t, r)
	queue(t, reps[0], "issue", h.issue, "title", "pending")
	op := queue(t, reps[1], "issue", h.deleted, "title", "deleted edit")
	r.fx.Exec(t, `DELETE FROM issue WHERE id=$1`, h.deleted)
	batch := pull(t, r, 0, true)
	if err := reps[1].Apply(batch); err != nil {
		t.Fatal(err)
	}
	for _, rec := range batch.Records {
		if rec.ID == h.deleted {
			if err := reps[1].Acknowledge(ws.Receipt{Operation: op.ID, Status: "conflict", Record: rec}); err != nil {
				t.Fatal(err)
			}
		}
	}
	h.bundles, h.plan = exportCopies(t, r, reps)
	report, err := ws.PlanRecovery(h.plan, h.bundles)
	if err != nil {
		t.Fatal(err)
	}
	h.digest = report.Digest
	pool, err := pgxpool.New(context.Background(), targetURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	h.target = testutil.New(pool, r.scope.Workspace, r.fx.UserID)
	h.target.User(t, "recovery owner", uuid.NewString()+"@example.test", testutil.Cols{"id": r.fx.UserID})
	h.target.Workspace(t, "recovery target", "recovery-"+uuid.NewString(), testutil.Cols{"id": r.scope.Workspace})
	h.target.Member(t, r.scope.Workspace, r.fx.UserID, "owner")
	h.cfg = ws.RecoveryHTTPConfig{Enabled: true, SourceURL: os.Getenv("DATABASE_URL"), SourceDeployment: uuid.NewString(), TargetDeployment: uuid.NewString()}
	for _, pair := range []struct {
		fx *testutil.Fixture
		id string
	}{{r.fx, h.cfg.SourceDeployment}, {h.target, h.cfg.TargetDeployment}} {
		pair.fx.InsertNoID(t, "work_sync_recovery_deployment", testutil.Cols{"deployment_id": pair.id}, "deployment_id=$1", pair.id)
	}
	secret := make([]byte, 32)
	if _, err = rand.Read(secret); err != nil {
		t.Fatal(err)
	}
	h.token = "wrc_" + hex.EncodeToString(secret)
	planJSON, _ := json.Marshal(h.plan)
	h.target.InsertNoID(t, "work_sync_recovery_authority", testutil.Cols{
		"token_hash": auth.HashToken(h.token), "workspace_id": h.plan.Target.Workspace, "owner_id": h.plan.Owner,
		"operator_id": "fixture-operator", "source_deployment_id": h.cfg.SourceDeployment, "target_deployment_id": h.cfg.TargetDeployment,
		"plan": planJSON, "approved_report_hash": h.digest, "expires_at": time.Now().Add(time.Hour),
	}, "token_hash=$1", auth.HashToken(h.token))
	t.Cleanup(func() {
		// Fixture-only teardown: the product exposes no way to remove a fence.
		for _, fx := range []*testutil.Fixture{r.fx, h.target} {
			fx.Exec(t, `DELETE FROM work_sync_recovery_fence WHERE workspace_id=$1`, r.scope.Workspace)
		}
		for _, table := range []string{"work_sync_recovery", "work_sync_change", "work_sync_receipt", "work_sync_scope", "issue", "project", "agent"} {
			h.target.Exec(t, "DELETE FROM "+table+" WHERE workspace_id=$1", r.scope.Workspace)
		}
	})
	h.restart(t)
	return h
}

func (h *recoveryHTTPFixture) restart(t *testing.T) {
	t.Helper()
	if h.server != nil {
		h.server.Close()
	}
	h.server = httptest.NewServer(ws.NewRecoveryHTTPHandler(h.target.Pool, h.cfg))
	server := h.server
	t.Cleanup(server.Close)
}

func (h *recoveryHTTPFixture) request(action string) ws.RecoveryRequest {
	input := ws.RecoveryRequest{Schema: ws.Schema, Plan: h.plan}
	if action == "stage" {
		input.Bundles = h.bundles
	} else {
		input.ApprovedDigest = h.digest
	}
	return input
}

func (h *recoveryHTTPFixture) call(t *testing.T, action, token string, input ws.RecoveryRequest, want int) ws.RecoveryResponse {
	t.Helper()
	data, _ := json.Marshal(input)
	base := h.server.URL
	if h.baseURL != "" {
		base = h.baseURL
	}
	req, _ := http.NewRequest(http.MethodPost, base+ws.RecoveryHTTPPrefix+action, bytes.NewReader(data))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != want {
		t.Fatalf("%s status=%d want=%d body=%s", action, resp.StatusCode, want, body)
	}
	var out ws.RecoveryResponse
	if want == 200 {
		if err = json.Unmarshal(body, &out); err != nil {
			t.Fatal(err)
		}
		if out.Schema != ws.Schema || out.Status != action {
			t.Fatalf("invalid response: %s", body)
		}
	}
	return out
}

func TestRecoveryPublicAuthorizationAndProtocol(t *testing.T) {
	h := setupRecoveryHTTP(t)
	input := h.request("stage")
	for _, token := range []string{"", "mdt_fixture", "user-token", strings.Repeat("x", 68)} {
		h.call(t, "stage", token, input, 401)
	}
	h.call(t, "stage", "wrc_"+strings.Repeat("0", 64), input, 403)
	for _, body := range []string{`{"schema":1,"unknown":true}`, `{}`, `{} {}`, `{"schema":"bad"}`} {
		req, _ := http.NewRequest("POST", h.server.URL+ws.RecoveryHTTPPrefix+"stage", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+h.token)
		resp, err := h.server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != 400 {
			t.Fatalf("malformed request status=%d", resp.StatusCode)
		}
	}
	changed := input
	changed.Plan.Target.Epoch = uuid.NewString()
	h.call(t, "stage", h.token, changed, 403)
	changed = input
	changed.Plan.Owner = uuid.NewString()
	h.call(t, "stage", h.token, changed, 403)
	changed = input
	changed.Plan.Target.Workspace = uuid.NewString()
	h.call(t, "stage", h.token, changed, 400)
	h.target.Exec(t, `UPDATE member SET role='admin' WHERE workspace_id=$1`, h.plan.Target.Workspace)
	h.call(t, "stage", h.token, input, 403)
	h.target.Exec(t, `UPDATE member SET role='owner' WHERE workspace_id=$1`, h.plan.Target.Workspace)
	h.target.Exec(t, `UPDATE work_sync_recovery_authority SET expires_at=now()-interval '1 second' WHERE workspace_id=$1`, h.plan.Target.Workspace)
	h.call(t, "stage", h.token, input, 403)
	h.target.Exec(t, `UPDATE work_sync_recovery_authority SET expires_at=now()+interval '1 hour' WHERE workspace_id=$1`, h.plan.Target.Workspace)
	h.target.Exec(t, `UPDATE work_sync_recovery_authority SET revoked_at=now() WHERE workspace_id=$1`, h.plan.Target.Workspace)
	h.call(t, "stage", h.token, input, 403)
	h.target.Exec(t, `UPDATE work_sync_recovery_authority SET revoked_at=NULL WHERE workspace_id=$1`, h.plan.Target.Workspace)
	h.cfg.TargetDeployment = uuid.NewString()
	h.restart(t)
	h.call(t, "stage", h.token, input, 403)
	if n := h.target.Count(t, `SELECT count(*) FROM work_sync_recovery WHERE workspace_id=$1`, h.plan.Target.Workspace); n != 0 {
		t.Fatal("denial persisted data")
	}

	handler := ws.NewRecoveryHTTPHandler(nil, ws.RecoveryHTTPConfig{})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("POST", ws.RecoveryHTTPPrefix+"stage", nil))
	if rec.Code != 404 {
		t.Fatal("default recovery route enabled")
	}
	h.cfg.Enabled = false
	h.restart(t)
	h.call(t, "stage", h.token, input, 404)
}

func TestRecoveryPublicFenceRestartRetryAndProjection(t *testing.T) {
	h := setupRecoveryHTTP(t)
	stage := h.call(t, "stage", h.token, h.request("stage"), 200)
	if stage.Report == nil || stage.Report.Digest != h.digest || stage.Report.Pending != 1 || stage.Report.Conflicts != 1 {
		t.Fatal("report lost retained intent")
	}
	h.call(t, "activate", h.token, h.request("activate"), 403)
	if n := h.target.Count(t, `SELECT count(*) FROM issue WHERE workspace_id=$1`, h.plan.Target.Workspace); n != 0 {
		t.Fatal("unfenced import")
	}
	// A writer already in flight prevents fencing; no partial proof is saved.
	ctx := context.Background()
	tx, err := h.source.c.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `UPDATE issue SET title=title WHERE id=$1`, h.issue); err != nil {
		t.Fatal(err)
	}
	h.call(t, "fence", h.token, h.request("fence"), 503)
	if n := h.source.fx.Count(t, `SELECT count(*) FROM work_sync_recovery_fence WHERE workspace_id=$1 AND proof IS NOT NULL`, h.plan.Source.Workspace); n != 0 {
		t.Fatal("partial fence")
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	// An old snapshot cannot bypass a fence committed after its snapshot.
	stale, err := h.source.c.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	defer stale.Rollback(ctx)
	if _, err = stale.Exec(ctx, `SELECT * FROM work_sync_recovery_fence WHERE workspace_id=$1`, h.plan.Source.Workspace); err != nil {
		t.Fatal(err)
	}
	h.call(t, "fence", h.token, h.request("fence"), 200)
	// A live, otherwise valid daemon credential cannot read or write the old
	// epoch after fencing (409 preserves its local recovery copy).
	oldToken := httpGrant(t, h.source, "old-node")
	oldSync := ws.NewHTTPHandler(h.source.c.Pool, true)
	for _, action := range []string{"handshake", "pull", "push"} {
		body, _ := json.Marshal(ws.Request{Schema: ws.Schema, Scope: h.plan.Source})
		req := httptest.NewRequest("POST", ws.HTTPPrefix+action, bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+oldToken)
		rec := httptest.NewRecorder()
		oldSync.ServeHTTP(rec, req)
		if rec.Code != 409 {
			t.Fatalf("fenced old %s status=%d", action, rec.Code)
		}
	}
	if _, err = stale.Exec(ctx, `UPDATE issue SET title='stale write' WHERE id=$1`, h.issue); err == nil {
		t.Fatal("stale snapshot bypassed fence")
	}
	_ = stale.Rollback(ctx)
	// Restart both the HTTP service and source DB connection used by every call.
	h.restart(t)
	h.call(t, "stage", h.token, h.request("stage"), 200)
	h.call(t, "fence", h.token, h.request("fence"), 200)
	for _, query := range []string{
		`UPDATE issue SET title='old center' WHERE workspace_id=$1`,
		`DELETE FROM project WHERE workspace_id=$1`,
		`UPDATE agent SET description='old center' WHERE workspace_id=$1`,
		`UPDATE issue SET workspace_id=gen_random_uuid() WHERE workspace_id=$1`,
		`INSERT INTO project(workspace_id,title) VALUES($1,'old create')`,
		`DELETE FROM work_sync_scope WHERE workspace_id=$1`,
		`UPDATE work_sync_scope SET epoch=gen_random_uuid() WHERE workspace_id=$1`,
	} {
		if _, err = h.source.c.Pool.Exec(ctx, query, h.plan.Source.Workspace); err == nil {
			t.Fatalf("old writer succeeded: %s", query)
		}
	}
	bad := h.request("activate")
	bad.ApprovedDigest = strings.Repeat("0", 64)
	h.call(t, "activate", h.token, bad, 403)
	h.target.Exec(t, `UPDATE work_sync_recovery_authority SET revoked_at=now() WHERE workspace_id=$1`, h.plan.Target.Workspace)
	h.call(t, "activate", h.token, h.request("activate"), 403)
	h.target.Exec(t, `UPDATE work_sync_recovery_authority SET revoked_at=NULL WHERE workspace_id=$1`, h.plan.Target.Workspace)
	// Contended target import exhausts finite retries, leaves only staging.
	blocker, err := h.target.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(ctx)
	if _, err = blocker.Exec(ctx, `LOCK TABLE project IN ROW EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	h.call(t, "activate", h.token, h.request("activate"), 503)
	if n := h.target.Count(t, `SELECT count(*) FROM work_sync_recovery WHERE id=$1 AND activated`, h.plan.ID); n != 0 {
		t.Fatal("failed import activated")
	}
	_ = blocker.Rollback(ctx)
	h.restart(t)
	h.call(t, "activate", h.token, h.request("activate"), 200)
	h.call(t, "activate", h.token, h.request("activate"), 200)
	if n := h.target.Count(t, `SELECT count(*) FROM work_sync_change WHERE workspace_id=$1`, h.plan.Target.Workspace); n != 4 {
		t.Fatalf("journal records=%d", n)
	}
	if n := h.target.Count(t, `SELECT count(*) FROM issue WHERE id=$1`, h.deleted); n != 0 {
		t.Fatal("deleted issue resurrected")
	}
	if n := h.target.Count(t, `SELECT count(*) FROM issue WHERE id=$1 AND title='recover issue'`, h.issue); n != 1 {
		t.Fatal("pending replayed or issue missing")
	}
	if n := h.target.Count(t, `SELECT count(*) FROM agent_task_queue WHERE issue_id=$1 OR agent_id=$2`, h.issue, h.agent); n != 0 {
		t.Fatal("recovery executed task")
	}
	h.target.Exec(t, `UPDATE work_sync_recovery_authority SET revoked_at=now() WHERE workspace_id=$1`, h.plan.Target.Workspace)
	h.call(t, "activate", h.token, h.request("activate"), 403)
}

func TestRecoveryPublicInterruptedImportAndFenceFailure(t *testing.T) {
	h := setupRecoveryHTTP(t)
	h.call(t, "fence", h.token, h.request("fence"), 400)
	h.call(t, "stage", h.token, h.request("stage"), 200)
	h.call(t, "fence", h.token, h.request("fence"), 200)
	// Configuration and guard failures cannot be converted into an assertion
	// that the old center is safely offline.
	original := h.cfg
	h.cfg.SourceDeployment = uuid.NewString()
	h.restart(t)
	h.call(t, "activate", h.token, h.request("activate"), 403)
	h.cfg = original
	h.cfg.SourceURL = "postgres://fixture@127.0.0.1:1/missing?sslmode=disable&connect_timeout=1"
	h.restart(t)
	h.call(t, "activate", h.token, h.request("activate"), 503)
	h.cfg = original
	h.restart(t)
	h.source.fx.Exec(t, `ALTER TABLE project DISABLE TRIGGER work_sync_recovery_guard`)
	t.Cleanup(func() { h.source.fx.Exec(t, `ALTER TABLE project ENABLE TRIGGER work_sync_recovery_guard`) })
	h.call(t, "activate", h.token, h.request("activate"), 403)
	h.source.fx.Exec(t, `ALTER TABLE project ENABLE TRIGGER work_sync_recovery_guard`)
	// Pause only this fixture's Issue insertion, after Projects and Agents were
	// inserted. Cancel the real HTTP request and verify the entire import rolls back.
	h.target.Exec(t, `CREATE FUNCTION recovery_test_pause() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN IF NEW.id='`+h.issue+`'::uuid THEN PERFORM pg_sleep(10); END IF; RETURN NEW; END $$`)
	h.target.Exec(t, `CREATE TRIGGER recovery_test_pause AFTER INSERT ON issue FOR EACH ROW EXECUTE FUNCTION recovery_test_pause()`)
	t.Cleanup(func() {
		h.target.Exec(t, `DROP TRIGGER IF EXISTS recovery_test_pause ON issue`)
		h.target.Exec(t, `DROP FUNCTION IF EXISTS recovery_test_pause()`)
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	body, _ := json.Marshal(h.request("activate"))
	req, _ := http.NewRequestWithContext(ctx, "POST", h.server.URL+ws.RecoveryHTTPPrefix+"activate", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+h.token)
	done := make(chan error, 1)
	go func() {
		resp, err := h.server.Client().Do(req)
		if resp != nil {
			_ = resp.Body.Close()
		}
		done <- err
	}()
	waitRecoveryCondition(t, func() bool {
		return h.target.Count(t, `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND wait_event='PgSleep'`) == 1
	})
	cancel()
	if err := <-done; err == nil {
		t.Fatal("interrupted request succeeded")
	}
	waitRecoveryCondition(t, func() bool {
		return h.target.Count(t, `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND wait_event='PgSleep'`) == 0
	})
	for _, table := range []string{"issue", "project", "agent", "work_sync_scope", "work_sync_change"} {
		if n := h.target.Count(t, "SELECT count(*) FROM "+table+" WHERE workspace_id=$1", h.plan.Target.Workspace); n != 0 {
			t.Fatalf("interrupted import left %s", table)
		}
	}
	if n := h.target.Count(t, `SELECT count(*) FROM work_sync_recovery WHERE id=$1 AND activated`, h.plan.ID); n != 0 {
		t.Fatal("interrupted import marked active")
	}
	h.target.Exec(t, `DROP TRIGGER recovery_test_pause ON issue`)
	h.target.Exec(t, `DROP FUNCTION recovery_test_pause()`)
	h.target.Exec(t, `UPDATE work_sync_recovery_authority SET expires_at=now()-interval '1 second' WHERE workspace_id=$1`, h.plan.Target.Workspace)
	h.restart(t)
	h.call(t, "activate", h.token, h.request("activate"), 403)
	h.target.Exec(t, `UPDATE work_sync_recovery_authority SET expires_at=now()+interval '1 hour' WHERE workspace_id=$1`, h.plan.Target.Workspace)
	h.call(t, "activate", h.token, h.request("activate"), 200)
	h.call(t, "activate", h.token, h.request("activate"), 200)
}

func waitRecoveryCondition(t *testing.T, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ready() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("recovery fixture condition timed out")
}
