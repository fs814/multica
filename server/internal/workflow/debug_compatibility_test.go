package workflow

import (
	"context"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDebugPolicyChangesDoNotRewriteExistingRun(t *testing.T) {
	env, in := setupDebugTest(t)
	ctx := context.Background()
	first, err := env.engine.StartDraftTest(ctx, env.workspaceID, env.userID, env.templateID, in)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = debugClaimFixture(t, env, first.Run)
	p := DefaultDebugPolicy()
	p.Enabled = true
	p.UserActiveRuns = 1
	p.WorkspaceActiveRuns = 1
	p.MaxDurationSeconds = 60
	p.RetentionSeconds = 86400
	updated, err := env.engine.UpdateDebugPolicy(ctx, env.workspaceID, env.userID, 1, p)
	if err != nil {
		t.Fatal(err)
	}
	same, err := env.engine.UpdateDebugPolicy(ctx, env.workspaceID, env.userID, updated.Revision, p)
	if err != nil || same.Revision != updated.Revision {
		t.Fatalf("same value revision: %v", err)
	}
	pinned := env.reloadRun(t, first.Run.ID)
	if pinned.DebugDeadlineAt != first.Run.DebugDeadlineAt || pinned.DebugRetentionSeconds != first.Run.DebugRetentionSeconds || pinned.DebugPolicyRevision != first.Run.DebugPolicyRevision {
		t.Fatal("policy rewrite changed an accepted run")
	}
	if _, err = env.engine.CancelRun(ctx, env.workspaceID, first.Run.ID, env.userID); err != nil {
		t.Fatal(err)
	}
	if err = env.engine.MaintainDebugRun(ctx, env.workspaceID, first.Run.ID); err != nil {
		t.Fatal(err)
	}
	in.ExpectedDebugPolicyRevision = updated.Revision
	in.IdempotencyKey = "second-after-policy-change"
	next, err := env.engine.StartDraftTest(ctx, env.workspaceID, env.userID, env.templateID, in)
	if err != nil {
		t.Fatal(err)
	}
	if next.Run.DebugRetentionSeconds.Int32 != 86400 || next.Run.DebugDeadlineAt.Time.Sub(next.Run.CreatedAt.Time).Seconds() != 60 {
		t.Fatal("new run did not pin new policy")
	}
	usage, err := env.q.GetWorkflowDebugUsage(ctx, db.GetWorkflowDebugUsageParams{WorkspaceID: env.workspaceID, UserID: env.userID})
	if err != nil || usage.UserActive != 1 || usage.UserHourly != 2 || usage.WaitingStop != 1 {
		t.Fatalf("logical quota confused with physical stop: %+v %v", usage, err)
	}
}
func TestDebugRowsFenceEveryDownMigration(t *testing.T) {
	env, in := setupDebugTest(t)
	ctx := context.Background()
	if _, err := env.engine.StartDraftTest(ctx, env.workspaceID, env.userID, env.templateID, in); err != nil {
		t.Fatal(err)
	}
	paths, err := filepath.Glob("../../migrations/5*.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, path := range paths {
		name := filepath.Base(path)
		if name < "501_" || name > "516_z" {
			continue
		}
		checked++
		t.Run(name, func(t *testing.T) {
			sql, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			tx, err := env.pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(ctx)
			_, err = tx.Exec(ctx, string(sql))
			if err == nil || !strings.Contains(err.Error(), "draft trial records exist") {
				t.Fatalf("rollback guard did not refuse before DDL: %v", err)
			}
		})
	}
	if checked != 16 {
		t.Fatalf("checked %d down migrations, want 16", checked)
	}
}
