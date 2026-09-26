package worksync_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/testutil"
	ws "github.com/multica-ai/multica/server/internal/worksync"
)

func recoveryCopies(t *testing.T, r rig) ([]*ws.Replica, []ws.RecoveryBundle, ws.RecoveryPlan) {
	t.Helper()
	reps := []*ws.Replica{}
	for _, node := range []string{"a", "b"} {
		rep, err := ws.OpenReplica(ws.ReplicaConfig{Enabled: true, Root: t.TempDir(), Scope: r.scope, Principal: ws.Principal{Account: r.fx.UserID, Actor: r.fx.UserID, Node: node}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = rep.Close() })
		if err = rep.Apply(pull(t, r, 0, true)); err != nil {
			t.Fatal(err)
		}
		reps = append(reps, rep)
	}
	bundles, p := exportCopies(t, r, reps)
	return reps, bundles, p
}
func exportCopies(t *testing.T, r rig, reps []*ws.Replica) ([]ws.RecoveryBundle, ws.RecoveryPlan) {
	t.Helper()
	p := ws.RecoveryPlan{ID: uuid.NewString(), Source: r.scope, Target: r.scope, Owner: r.fx.UserID, Sources: map[string]string{}}
	p.Target.Epoch = uuid.NewString()
	bundles := []ws.RecoveryBundle{}
	for _, rep := range reps {
		b, err := rep.ExportRecovery()
		if err != nil {
			t.Fatal(err)
		}
		bundles = append(bundles, b)
		p.Sources[b.State.Principal.Node] = b.Digest
		if b.State.Cursor > p.Boundary {
			p.Boundary = b.State.Cursor
		}
	}
	return bundles, p
}
func recoveryService(t *testing.T, r rig, p ws.RecoveryPlan) (*ws.Recovery, *atomic.Bool, *atomic.Bool) {
	t.Helper()
	allowed, fenced := new(atomic.Bool), new(atomic.Bool)
	allowed.Store(true)
	recovery := &ws.Recovery{Enabled: true, Pool: r.c.Pool, Authorize: func(ctx context.Context, tx pgx.Tx, actor ws.Principal, plan ws.RecoveryPlan, action string) error {
		// Independent fixture authority pins the full plan. Membership and daemon
		// credentials alone cannot issue this permission.
		if !allowed.Load() || actor != r.principal || !reflect.DeepEqual(plan, p) {
			return ws.ErrDenied
		}
		return nil
	}, VerifyFence: func(context.Context, ws.RecoveryPlan) error {
		if !fenced.Load() {
			return ws.ErrDenied
		}
		return nil
	}}
	t.Cleanup(func() { r.fx.Exec(t, `DELETE FROM work_sync_recovery WHERE workspace_id=$1`, r.scope.Workspace) })
	return recovery, allowed, fenced
}
func eraseSource(t *testing.T, r rig) {
	t.Helper()
	for _, table := range []string{"issue", "project", "agent", "work_sync_scope", "work_sync_change", "work_sync_receipt", "work_sync_grant"} {
		r.fx.Exec(t, `DELETE FROM `+table+` WHERE workspace_id=$1`, r.scope.Workspace)
	}
}
func TestRecoveryStagingAuthorizationFenceRetryAndProjection(t *testing.T) {
	r := setup(t)
	project := r.fx.Project(t, "recover project")
	agent := r.fx.Agent(t, "recover agent", "", testutil.Cols{"custom_env": testutil.Raw(`'{"secret":"excluded"}'::jsonb`)})
	issue := r.fx.Issue(t, "recover issue", testutil.Cols{"project_id": project, "assignee_type": "agent", "assignee_id": agent})
	deleted := r.fx.Issue(t, "deleted")
	enroll(t, r)
	reps, _, _ := recoveryCopies(t, r)
	queue(t, reps[0], "issue", issue, "title", "pending local")
	op := queue(t, reps[1], "issue", deleted, "title", "must not resurrect")
	r.fx.Exec(t, `DELETE FROM issue WHERE id=$1`, deleted)
	latest := pull(t, r, 0, true)
	if err := reps[1].Apply(latest); err != nil {
		t.Fatal(err)
	}
	var tomb ws.Record
	for _, rec := range latest.Records {
		if rec.ID == deleted {
			tomb = rec
		}
	}
	if err := reps[1].Acknowledge(ws.Receipt{Operation: op.ID, Status: "conflict", Reason: "entity deleted", Record: tomb}); err != nil {
		t.Fatal(err)
	}
	bundles, p := exportCopies(t, r, reps)
	recovery, allowed, fenced := recoveryService(t, r, p)
	ctx := context.Background()
	allowed.Store(false)
	if _, err := recovery.Stage(ctx, r.principal, p, bundles); !errors.Is(err, ws.ErrDenied) {
		t.Fatalf("unauthorized stage: %v", err)
	}
	allowed.Store(true)
	report, err := recovery.Stage(ctx, r.principal, p, bundles)
	if err != nil {
		t.Fatal(err)
	}
	if report.Pending != 1 || report.Conflicts != 1 {
		t.Fatalf("intent lost: %+v", report)
	}
	if err = recovery.Activate(ctx, r.principal, p, report.Digest); !errors.Is(err, ws.ErrDenied) {
		t.Fatalf("unfenced activation: %v", err)
	}
	fenced.Store(true)
	if err = recovery.Activate(ctx, r.principal, p, report.Digest); !errors.Is(err, ws.ErrScope) {
		t.Fatalf("nonempty target: %v", err)
	}
	eraseSource(t, r)
	if n := r.fx.Count(t, `SELECT count(*) FROM issue WHERE workspace_id=$1`, r.scope.Workspace); n != 0 {
		t.Fatal("stage published business data")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err = recovery.Activate(cancelled, r.principal, p, report.Digest); err == nil {
		t.Fatal("cancelled activation succeeded")
	}
	if err = recovery.Activate(ctx, r.principal, p, "unapproved"); !errors.Is(err, ws.ErrOperation) {
		t.Fatalf("bad approval: %v", err)
	}
	allowed.Store(false)
	if err = recovery.Activate(ctx, r.principal, p, report.Digest); !errors.Is(err, ws.ErrDenied) {
		t.Fatalf("revoked approval: %v", err)
	}
	allowed.Store(true)
	// Reconstruct the service to simulate a Center restart between staging and activation.
	recovery2 := *recovery
	again, err := recovery2.Stage(ctx, r.principal, p, bundles)
	if err != nil || again.Digest != report.Digest {
		t.Fatalf("stage retry: %v", err)
	}
	for i := 0; i < 2; i++ {
		if err = recovery2.Activate(ctx, r.principal, p, report.Digest); err != nil {
			t.Fatal(err)
		}
	}
	if n := r.fx.Count(t, `SELECT count(*) FROM work_sync_recovery WHERE id=$1 AND activated`, p.ID); n != 1 {
		t.Fatal("missing activation receipt")
	}
	if n := r.fx.Count(t, `SELECT count(*) FROM issue WHERE workspace_id=$1`, r.scope.Workspace); n != 1 {
		t.Fatal("duplicate or resurrected issue")
	}
	if n := r.fx.Count(t, `SELECT count(*) FROM agent WHERE id=$1 AND runtime_id IS NULL AND permission_mode='private' AND runtime_config='{}' AND custom_env='{}'`, agent); n != 1 {
		t.Fatal("restored execution configuration")
	}
	if n := r.fx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE agent_id=$1`, agent); n != 0 {
		t.Fatal("recovery dispatched task")
	}
	if n := r.fx.Count(t, `SELECT count(*) FROM issue_wakeup_receipt r JOIN issue_wakeup w ON w.id=r.wakeup_id WHERE w.workspace_id=$1`, r.scope.Workspace); n != 0 {
		t.Fatal("recovery emitted wakeup")
	}
	c := &ws.Center{Enabled: true, Pool: r.c.Pool, Authorize: func(context.Context, pgx.Tx, ws.Principal, ws.Scope, string, *ws.Operation) error { return nil }}
	if _, err = c.Pull(ctx, r.principal, p.Source, 0, true); !errors.Is(err, ws.ErrScope) {
		t.Fatalf("accepted old epoch: %v", err)
	}
	batch, err := c.Pull(ctx, r.principal, p.Target, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Records) != len(report.Records) {
		t.Fatal("lost recovered projection")
	}
	for _, rec := range batch.Records {
		for _, want := range report.Records {
			if rec.Key() == want.Key() {
				rec.Version = want.Version
				if !reflect.DeepEqual(rec, want) {
					t.Fatalf("projection changed: %+v %+v", rec, want)
				}
			}
		}
	}
	if err := reps[0].Apply(batch); !errors.Is(err, ws.ErrScope) {
		t.Fatal("old replica accepted new epoch")
	}
}

func TestRecoveryComplementaryAcknowledgementsAndValidation(t *testing.T) {
	r := setup(t)
	a := r.fx.Project(t, "a")
	b := r.fx.Project(t, "b")
	enroll(t, r)
	reps, bundles, p := recoveryCopies(t, r)
	// Two replicas each durably acknowledged a different post-snapshot write.
	// Neither individual copy alone has the complete recovery boundary.
	for i, id := range []string{a, b} {
		r.fx.Exec(t, `UPDATE project SET title=title||' new' WHERE id=$1`, id)
		batch := pull(t, r, 0, true)
		for _, rec := range batch.Records {
			if rec.ID == id {
				bundles[i].State.Acknowledged[rec.Key()] = rec
			}
		}
	}
	p.Boundary += 2
	reseal := func(bs []ws.RecoveryBundle, plan *ws.RecoveryPlan) {
		for i := range bs {
			bs[i].Seal()
			plan.Sources[bs[i].State.Principal.Node] = bs[i].Digest
		}
	}
	reseal(bundles, &p)
	if report, err := ws.PlanRecovery(p, bundles); err != nil || len(report.Records) != 2 {
		t.Fatalf("complementary copies: %v", err)
	}
	for _, tc := range []string{"checksum", "missing node", "duplicate node", "foreign history", "revoked", "partial", "fork", "gap", "unreviewed", "unknown field", "missing dependency", "invalid intent", "unapproved owner"} {
		t.Run(tc, func(t *testing.T) {
			data, _ := json.Marshal(bundles)
			var bs []ws.RecoveryBundle
			_ = json.Unmarshal(data, &bs)
			data, _ = json.Marshal(p)
			var plan ws.RecoveryPlan
			_ = json.Unmarshal(data, &plan)
			switch tc {
			case "checksum":
				bs[0].Digest = "bad"
			case "missing node":
				bs = bs[:1]
			case "duplicate node":
				bs[1] = bs[0]
			case "foreign history":
				bs[0].State.Scope.Epoch = uuid.NewString()
			case "revoked":
				bs[0].State.Revoked = true
			case "partial":
				delete(bs[0].State.Records, "project/"+a)
			case "fork":
				x := bs[0].State.Records["project/"+a]
				x.Fields["title"] = json.RawMessage(`"fork"`)
				bs[0].State.Records[x.Key()] = x
			case "gap":
				bs[0].State.Acknowledged = map[string]ws.Record{}
			case "unreviewed":
				plan.Boundary--
			case "unknown field":
				x := bs[0].State.Records["project/"+a]
				x.Fields["secret"] = json.RawMessage(`"x"`)
				bs[0].State.Records[x.Key()] = x
			case "missing dependency":
				x := ws.Record{Kind: "issue", ID: uuid.NewString(), Version: plan.Boundary + 1, Fields: ws.Fields{"project_id": json.RawMessage(`"` + uuid.NewString() + `"`), "parent_issue_id": json.RawMessage(`null`)}}
				bs[1].State.Acknowledged[x.Key()] = x
				plan.Boundary++
			case "invalid intent":
				bs[0].State.Outbox = []ws.Operation{{ID: uuid.NewString()}}
			case "unapproved owner":
				plan.Owner = uuid.NewString()
			}
			if tc != "checksum" && tc != "missing node" && tc != "duplicate node" {
				reseal(bs, &plan)
			}
			if _, err := ws.PlanRecovery(plan, bs); err == nil {
				t.Fatal("invalid recovery accepted")
			}
		})
	}
	if err := reps[0].Revoke(); err != nil {
		t.Fatal(err)
	}
	if _, err := reps[0].ExportRecovery(); !errors.Is(err, ws.ErrDenied) {
		t.Fatal("revoked replica exported")
	}
}

func TestRecoveryActivationRollsBackPartialImportAndRechecksDuplicateAuthority(t *testing.T) {
	r := setup(t)
	project := r.fx.Project(t, "first insertion")
	agent := r.fx.Agent(t, "second insertion", "")
	_ = project
	_ = agent
	r.fx.Issue(t, "last insertion", testutil.Cols{"assignee_type": "member", "assignee_id": uuid.NewString()})
	enroll(t, r)
	_, bundles, p := recoveryCopies(t, r)
	recovery, allowed, fenced := recoveryService(t, r, p)
	report, err := recovery.Stage(context.Background(), r.principal, p, bundles)
	if err != nil {
		t.Fatal(err)
	}
	eraseSource(t, r)
	fenced.Store(true)
	// The missing member is intentionally outside the backed-up projection.
	// Projects and agents insert first, then identity validation aborts issues.
	for i := 0; i < 2; i++ {
		if err = recovery.Activate(context.Background(), r.principal, p, report.Digest); !errors.Is(err, ws.ErrOperation) {
			t.Fatalf("missing identity: %v", err)
		}
	}
	for _, table := range []string{"issue", "project", "agent", "work_sync_scope", "work_sync_change"} {
		if n := r.fx.Count(t, `SELECT count(*) FROM `+table+` WHERE workspace_id=$1`, r.scope.Workspace); n != 0 {
			t.Fatalf("partial import survived in %s", table)
		}
	}
	if n := r.fx.Count(t, `SELECT count(*) FROM work_sync_recovery WHERE id=$1 AND NOT activated`, p.ID); n != 1 {
		t.Fatal("staging lost after rollback")
	}
	allowed.Store(false)
	if _, err = recovery.Stage(context.Background(), r.principal, p, bundles); !errors.Is(err, ws.ErrDenied) {
		t.Fatal("retry bypassed authority")
	}
	denied := ws.Recovery{Enabled: true, Pool: r.c.Pool}
	if _, err = denied.Stage(context.Background(), r.principal, p, bundles); !errors.Is(err, ws.ErrDenied) {
		t.Fatal("nil authority accepted")
	}
	denied.Enabled = false
	if _, err = denied.Stage(context.Background(), r.principal, p, bundles); !errors.Is(err, ws.ErrDisabled) {
		t.Fatal("default enabled")
	}
}

func TestRecoveryConcurrentStageActivateAndCanceledImport(t *testing.T) {
	r := setup(t)
	r.fx.Project(t, "concurrent recovery")
	enroll(t, r)
	_, bundles, p := recoveryCopies(t, r)
	recovery, _, fenced := recoveryService(t, r, p)
	report, err := recovery.Stage(context.Background(), r.principal, p, bundles)
	if err != nil {
		t.Fatal(err)
	}
	eraseSource(t, r)
	fenced.Store(true)
	// Block a real import on the business table, cancel it after it obtained
	// the workspace/session locks, then prove that the same session can resume.
	blocker, err := r.c.Pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(context.Background())
	if _, err = blocker.Exec(context.Background(), `LOCK TABLE project IN SHARE MODE`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- recovery.Activate(ctx, r.principal, p, report.Digest) }()
	deadline := time.Now().Add(3 * time.Second)
	blocked := false
	for time.Now().Before(deadline) {
		var n int
		err = r.c.Pool.QueryRow(context.Background(), `SELECT count(*) FROM pg_locks WHERE relation='project'::regclass AND NOT granted`).Scan(&n)
		if err != nil {
			t.Fatal(err)
		}
		if n > 0 {
			blocked = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if err = <-result; err == nil || !blocked {
		t.Fatalf("import did not block/cancel: blocked=%v err=%v", blocked, err)
	}
	if err = blocker.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Repeated staging and activation share one immutable session. All callers
	// must complete, and only one journal baseline can be installed.
	results := make(chan error, 12)
	for i := 0; i < 12; i++ {
		go func(stage bool) {
			if stage {
				_, e := recovery.Stage(context.Background(), r.principal, p, bundles)
				results <- e
			} else {
				results <- recovery.Activate(context.Background(), r.principal, p, report.Digest)
			}
		}(i%2 == 0)
	}
	for i := 0; i < 12; i++ {
		if err = <-results; err != nil {
			t.Fatal(err)
		}
	}
	if n := r.fx.Count(t, `SELECT count(*) FROM work_sync_change WHERE workspace_id=$1`, r.scope.Workspace); n != 1 {
		t.Fatalf("duplicate baseline: %d", n)
	}
}
