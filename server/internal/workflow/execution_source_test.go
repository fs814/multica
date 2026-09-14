package workflow

import (
	"context"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"testing"
	"time"
)

func TestResolveExecutionSourceRejectsBeforeReading(t *testing.T) {
	ws := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	for _, tc := range []struct {
		name string
		run  db.WorkflowRun
	}{
		{"cross workspace", db.WorkflowRun{}},
		{"purged", db.WorkflowRun{WorkspaceID: ws, ExecutionMode: ExecutionDraftTest, DetailsPurgedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true}}},
		{"unknown source", db.WorkflowRun{WorkspaceID: ws, ExecutionMode: "future"}},
		{"missing published identity", db.WorkflowRun{WorkspaceID: ws, ExecutionMode: ExecutionPublished}},
		{"missing snapshot identity", db.WorkflowRun{WorkspaceID: ws, ExecutionMode: ExecutionDraftTest}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// A nil Queries makes any read panic: these decisions must be metadata-only.
			if _, err := ResolveRunDefinition(context.Background(), nil, ws, tc.run); err == nil {
				t.Fatal("invalid source accepted")
			}
		})
	}
}

func TestTerminalSubmissionDoesNotReadMissingGraph(t *testing.T) {
	env := setupTestEnv(t)
	env.publishTemplate(t, linearDefinition())
	run := env.startRun(t, "terminal-missing-graph")
	steps, err := env.q.ListWorkflowStepInstances(context.Background(), db.ListWorkflowStepInstancesParams{RunID: run.ID, WorkspaceID: env.workspaceID})
	if err != nil || len(steps) != 1 {
		t.Fatalf("steps: %v %v", steps, err)
	}
	fx := testutil.New(env.pool, uuidString(env.workspaceID), uuidString(env.userID))
	fx.Exec(t, "UPDATE workflow_run SET status='cancelled',completed_at=now() WHERE id=$1", run.ID)
	fx.Exec(t, "UPDATE workflow_template_version SET definition='{}' WHERE id=$1", env.versionID)
	_, err = env.engine.SubmitResult(context.Background(), SubmitResultInput{WorkspaceID: env.workspaceID, StepID: steps[0].ID, RawOutput: "must not parse or store"})
	if !IsInvalidTransition(err) {
		t.Fatalf("terminal check must precede graph parse: %v", err)
	}
	if err = env.engine.RecordTaskTerminal(context.Background(), RecordTaskTerminalInput{TaskID: steps[0].TaskID, TaskStatus: "failed", ErrorDetail: "must not persist"}); err != nil {
		t.Fatal(err)
	}
	subs, err := env.q.ListWorkflowSubmissionsForRun(context.Background(), db.ListWorkflowSubmissionsForRunParams{RunID: run.ID, WorkspaceID: env.workspaceID})
	if err != nil || len(subs) != 0 {
		t.Fatalf("terminal report stored submission: %v %v", subs, err)
	}
}
