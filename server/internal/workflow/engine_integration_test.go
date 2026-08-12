// Integration tests for the workflow engine. These run against a real Postgres
// because the invariants under test are enforced jointly by Go code and database
// constraints — the replay fences ARE unique indexes, and the transactional
// enqueue guarantee is only meaningful if a real transaction rolls back.
//
// Skipped automatically when no database is reachable, matching the pattern in
// cmd/server/integration_test.go.
package workflow

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func testDBURL() string {
	if v := os.Getenv("DATABASE_URL"); v != "" {
		return v
	}
	return "postgres://multica:multica@localhost:5432/multica?sslmode=disable"
}

// testEnv is one isolated fixture: its own workspace, user, runtime, and agent,
// so parallel runs and repeated runs never collide.
type testEnv struct {
	pool        *pgxpool.Pool
	q           *db.Queries
	engine      *Engine
	workspaceID pgtype.UUID
	userID      pgtype.UUID
	agentID     pgtype.UUID
	runtimeID   pgtype.UUID
	templateID  pgtype.UUID
	versionID   pgtype.UUID
}

func setupTestEnv(t *testing.T) *testEnv {
	t.Helper()
	ctx := context.Background()

	pool, err := pgxpool.New(ctx, testDBURL())
	if err != nil {
		t.Skipf("skipping: cannot connect to database: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("skipping: database not reachable: %v", err)
	}
	// Skip rather than fail when the workflow schema has not been migrated, so
	// this file does not break a checkout that has not run `migrate up`.
	var hasTable bool
	if err := pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_name='workflow_run')`).Scan(&hasTable); err != nil || !hasTable {
		pool.Close()
		t.Skip("skipping: workflow tables not migrated; run `make migrate-up`")
	}
	t.Cleanup(pool.Close)

	env := &testEnv{pool: pool, q: db.New(pool)}
	suffix := fmt.Sprintf("wf-%d", time.Now().UnixNano())

	if err := pool.QueryRow(ctx,
		`INSERT INTO "user" (name, email) VALUES ($1, $2) RETURNING id`,
		"Workflow Test", suffix+"@example.test").Scan(&env.userID); err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO workspace (name, slug) VALUES ($1, $2) RETURNING id`,
		"Workflow Test", suffix).Scan(&env.workspaceID); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO agent_runtime (workspace_id, name, runtime_mode, provider, status)
		 VALUES ($1, $2, 'local', 'claude', 'online') RETURNING id`,
		env.workspaceID, "rt-"+suffix).Scan(&env.runtimeID); err != nil {
		t.Fatalf("create runtime: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO agent (workspace_id, name, runtime_mode, runtime_id)
		 VALUES ($1, $2, 'local', $3) RETURNING id`,
		env.workspaceID, "agent-"+suffix, env.runtimeID).Scan(&env.agentID); err != nil {
		t.Fatalf("create agent: %v", err)
	}

	t.Cleanup(func() {
		bg := context.Background()
		// Explicit cleanup in dependency order: the workflow tables carry no
		// foreign keys, so nothing cascades for us.
		for _, stmt := range []string{
			`DELETE FROM workflow_event WHERE workspace_id = $1`,
			`DELETE FROM workflow_acceptance WHERE workspace_id = $1`,
			`DELETE FROM workflow_submission WHERE workspace_id = $1`,
			`DELETE FROM workflow_step_instance WHERE workspace_id = $1`,
			`DELETE FROM workflow_run WHERE workspace_id = $1`,
			`DELETE FROM workflow_template_version WHERE workspace_id = $1`,
			`DELETE FROM workflow_template WHERE workspace_id = $1`,
			`DELETE FROM agent_task_queue WHERE agent_id IN (SELECT id FROM agent WHERE workspace_id = $1)`,
			`DELETE FROM issue WHERE workspace_id = $1`,
			`DELETE FROM agent WHERE workspace_id = $1`,
			`DELETE FROM agent_runtime WHERE workspace_id = $1`,
			`DELETE FROM workspace WHERE id = $1`,
		} {
			if _, err := pool.Exec(bg, stmt, env.workspaceID); err != nil {
				t.Logf("cleanup %q: %v", stmt, err)
			}
		}
		if _, err := pool.Exec(bg, `DELETE FROM "user" WHERE id = $1`, env.userID); err != nil {
			t.Logf("cleanup user: %v", err)
		}
	})

	env.engine = &Engine{
		Queries:   env.q,
		TxStarter: pool,
		Router:    &fixedRouter{agentID: env.agentID, runtimeID: env.runtimeID},
		Schemas:   DefaultSchemaRegistry,
	}
	return env
}

// fixedRouter always returns the fixture agent, isolating engine behavior from
// routing policy (which U4 covers).
type fixedRouter struct {
	agentID   pgtype.UUID
	runtimeID pgtype.UUID
	fail      bool
}

func (r *fixedRouter) Route(ctx context.Context, q *db.Queries, req RouteRequest) (RouteResult, error) {
	if r.fail {
		return RouteResult{}, fmt.Errorf("no eligible agent for capability %q", req.Node.Key)
	}
	return RouteResult{AgentID: r.agentID, RuntimeID: r.runtimeID, Reason: "test:fixed"}, nil
}

// publishTemplate stores and publishes a definition, returning the pinned
// version. It validates first, so a malformed test graph fails loudly here
// rather than surfacing as a confusing engine error.
func (env *testEnv) publishTemplate(t *testing.T, def *Definition) {
	t.Helper()
	ctx := context.Background()

	if err := Validate(def, DefaultWorkspacePolicy, DefaultSchemaRegistry); err != nil {
		t.Fatalf("test definition is invalid: %v", err)
	}

	tpl, err := env.q.CreateWorkflowTemplate(ctx, db.CreateWorkflowTemplateParams{
		WorkspaceID:   env.workspaceID,
		Key:           fmt.Sprintf("tpl-%d", time.Now().UnixNano()),
		Name:          "Test Template",
		Description:   "",
		CreatedByType: "member",
		CreatedByID:   env.userID,
	})
	if err != nil {
		t.Fatalf("create template: %v", err)
	}
	env.templateID = tpl.ID

	raw, err := MarshalDefinition(def)
	if err != nil {
		t.Fatalf("marshal definition: %v", err)
	}
	version, err := env.q.CreateWorkflowTemplateVersion(ctx, db.CreateWorkflowTemplateVersionParams{
		WorkspaceID:   env.workspaceID,
		TemplateID:    tpl.ID,
		Definition:    raw,
		SchemaVersion: SchemaVersion,
	})
	if err != nil {
		t.Fatalf("create version: %v", err)
	}
	published, err := env.q.PublishWorkflowTemplateVersion(ctx, db.PublishWorkflowTemplateVersionParams{
		ID:              version.ID,
		WorkspaceID:     env.workspaceID,
		PublishedByType: "member",
		PublishedByID:   env.userID,
	})
	if err != nil {
		t.Fatalf("publish version: %v", err)
	}
	env.versionID = published.ID

	if _, err := env.q.SetWorkflowTemplateCurrentVersion(ctx, db.SetWorkflowTemplateCurrentVersionParams{
		ID:             tpl.ID,
		WorkspaceID:    env.workspaceID,
		CurrentVersion: published.Version,
	}); err != nil {
		t.Fatalf("set current version: %v", err)
	}
}

func (env *testEnv) startRun(t *testing.T, key string) db.WorkflowRun {
	t.Helper()
	res, err := env.engine.StartRun(context.Background(), StartRunInput{
		WorkspaceID:       env.workspaceID,
		TemplateID:        env.templateID,
		Source:            "manual",
		IdempotencyKey:    key,
		AccountableUserID: env.userID,
		ActorType:         "member",
		ActorID:           env.userID,
	})
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	return res.Run
}

func (env *testEnv) startRunForIssue(t *testing.T, key string) (db.WorkflowRun, db.Issue) {
	t.Helper()
	ctx := context.Background()
	var issueID pgtype.UUID
	if err := env.pool.QueryRow(ctx, `
		INSERT INTO issue (
			workspace_id, title, status, priority, creator_id, creator_type,
			number, position
		) VALUES ($1, $2, 'todo', 'none', $3, 'member', 1, 1)
		RETURNING id`, env.workspaceID, "Workflow parent", env.userID).Scan(&issueID); err != nil {
		t.Fatalf("create workflow issue: %v", err)
	}
	issue, err := env.q.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{
		ID: issueID, WorkspaceID: env.workspaceID,
	})
	if err != nil {
		t.Fatalf("load workflow issue: %v", err)
	}

	res, err := env.engine.StartRun(ctx, StartRunInput{
		WorkspaceID:       env.workspaceID,
		TemplateID:        env.templateID,
		IssueID:           issue.ID,
		Source:            "manual",
		IdempotencyKey:    key,
		AccountableUserID: env.userID,
		ActorType:         "member",
		ActorID:           env.userID,
	})
	if err != nil {
		t.Fatalf("StartRun with issue: %v", err)
	}
	return res.Run, issue
}

func (env *testEnv) reloadIssue(t *testing.T, issueID pgtype.UUID) db.Issue {
	t.Helper()
	issue, err := env.q.GetIssueInWorkspace(context.Background(), db.GetIssueInWorkspaceParams{
		ID: issueID, WorkspaceID: env.workspaceID,
	})
	if err != nil {
		t.Fatalf("reload workflow issue: %v", err)
	}
	return issue
}

func (env *testEnv) completeStepTask(t *testing.T, step db.WorkflowStepInstance) {
	t.Helper()
	if !step.TaskID.Valid {
		t.Fatalf("step %q has no task to complete", step.NodeKey)
	}
	if _, err := env.pool.Exec(context.Background(), `
		UPDATE agent_task_queue
		SET status = 'completed', completed_at = now()
		WHERE id = $1`, step.TaskID); err != nil {
		t.Fatalf("complete task for step %q: %v", step.NodeKey, err)
	}
	var status string
	var issueID pgtype.UUID
	if err := env.pool.QueryRow(context.Background(), `
		SELECT status, issue_id FROM agent_task_queue WHERE id = $1`, step.TaskID).Scan(&status, &issueID); err != nil {
		t.Fatalf("reload task for step %q: %v", step.NodeKey, err)
	}
	if status != "completed" {
		t.Fatalf("task for step %q remained status=%q issue_id=%v", step.NodeKey, status, issueID)
	}
}

func (env *testEnv) stepByNode(t *testing.T, runID pgtype.UUID, nodeKey string) db.WorkflowStepInstance {
	t.Helper()
	steps, err := env.q.ListWorkflowStepInstances(context.Background(), db.ListWorkflowStepInstancesParams{
		RunID:       runID,
		WorkspaceID: env.workspaceID,
	})
	if err != nil {
		t.Fatalf("list steps: %v", err)
	}
	// Highest attempt wins, so rework tests see the newest attempt.
	var found db.WorkflowStepInstance
	for _, s := range steps {
		if s.NodeKey == nodeKey && s.Attempt >= found.Attempt {
			found = s
		}
	}
	if !found.ID.Valid {
		t.Fatalf("no step found for node %q", nodeKey)
	}
	return found
}

func (env *testEnv) reloadRun(t *testing.T, runID pgtype.UUID) db.WorkflowRun {
	t.Helper()
	run, err := env.q.GetWorkflowRun(context.Background(), db.GetWorkflowRunParams{
		ID:          runID,
		WorkspaceID: env.workspaceID,
	})
	if err != nil {
		t.Fatalf("reload run: %v", err)
	}
	return run
}

func passPayload(summary string) string {
	return fmt.Sprintf(`%s
{"verdict":"pass","artifact":{"type":"code_change","summary":%q},"rationale":"done","confidence":0.9}
%s`, submissionOpen, summary, submissionClose)
}

// linearDefinition is a two-agent graph ending at End, with no acceptance gate.
func linearDefinition() *Definition {
	return &Definition{
		SchemaVersion: SchemaVersion,
		EntryNode:     "analyze",
		Nodes: []Node{
			{
				Key: "analyze", Type: NodeTypeAgent, Next: []string{"implement"},
				Routing:          &Routing{Strategy: RoutingCapability, Capability: "analysis"},
				SubmissionSchema: "analysis",
			},
			{
				Key: "implement", Type: NodeTypeAgent, Next: []string{"end"},
				Routing:          &Routing{Strategy: RoutingPreviousStep, FromNode: "analyze"},
				SubmissionSchema: "code_change",
			},
			{Key: "end", Type: NodeTypeEnd},
		},
	}
}

// TestStartRunActivatesEntryStepAndEnqueuesTaskAtomically proves the core
// transactional-enqueue guarantee: after StartRun commits, the first Step is
// queued AND its Agent Task exists, with the link written in both directions.
func TestStartRunActivatesEntryStepAndEnqueuesTaskAtomically(t *testing.T) {
	env := setupTestEnv(t)
	env.publishTemplate(t, linearDefinition())
	run := env.startRun(t, "run-atomic-1")

	if run.Status != string(RunRunning) {
		t.Errorf("run status = %q, want running", run.Status)
	}

	step := env.stepByNode(t, run.ID, "analyze")
	if step.Status != string(StepQueued) {
		t.Fatalf("entry step status = %q, want queued", step.Status)
	}
	if !step.TaskID.Valid {
		t.Fatal("entry step must have an Agent Task bound after StartRun commits")
	}
	if step.RoutingReason.String != "test:fixed" {
		t.Errorf("routing reason = %q, want the router's reason persisted", step.RoutingReason.String)
	}

	// The task must exist and point back at the step.
	var linkedStep pgtype.UUID
	var taskStatus string
	if err := env.pool.QueryRow(context.Background(),
		`SELECT workflow_step_instance_id, status FROM agent_task_queue WHERE id = $1`,
		step.TaskID).Scan(&linkedStep, &taskStatus); err != nil {
		t.Fatalf("load task: %v", err)
	}
	if uuidString(linkedStep) != uuidString(step.ID) {
		t.Errorf("task links to step %s, want %s", uuidString(linkedStep), uuidString(step.ID))
	}
	if taskStatus != "queued" {
		t.Errorf("task status = %q, want queued", taskStatus)
	}
}

// TestStartRunIsIdempotent: replaying the same intake event must return the
// original Run, never create a second one or a second Agent Task.
func TestStartRunIsIdempotent(t *testing.T) {
	env := setupTestEnv(t)
	env.publishTemplate(t, linearDefinition())
	ctx := context.Background()

	in := StartRunInput{
		WorkspaceID:       env.workspaceID,
		TemplateID:        env.templateID,
		Source:            "external",
		IdempotencyKey:    "duplicate-delivery",
		AccountableUserID: env.userID,
		ActorType:         "external",
	}
	first, err := env.engine.StartRun(ctx, in)
	if err != nil {
		t.Fatalf("first StartRun: %v", err)
	}
	if first.AlreadyExisted {
		t.Error("first StartRun should not report AlreadyExisted")
	}

	second, err := env.engine.StartRun(ctx, in)
	if err != nil {
		t.Fatalf("replayed StartRun must succeed, got: %v", err)
	}
	if !second.AlreadyExisted {
		t.Error("replayed StartRun must report AlreadyExisted")
	}
	if uuidString(second.Run.ID) != uuidString(first.Run.ID) {
		t.Errorf("replay created a different Run: %s vs %s", uuidString(second.Run.ID), uuidString(first.Run.ID))
	}

	// The decisive check: exactly one task, so the replay did not duplicate work.
	var runCount, taskCount int
	if err := env.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM workflow_run WHERE workspace_id = $1`, env.workspaceID).Scan(&runCount); err != nil {
		t.Fatalf("count runs: %v", err)
	}
	if err := env.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM agent_task_queue WHERE workflow_step_instance_id IS NOT NULL
		   AND workflow_step_instance_id IN (SELECT id FROM workflow_step_instance WHERE workspace_id = $1)`,
		env.workspaceID).Scan(&taskCount); err != nil {
		t.Fatalf("count tasks: %v", err)
	}
	if runCount != 1 {
		t.Errorf("run count = %d, want exactly 1 after a duplicate delivery", runCount)
	}
	if taskCount != 1 {
		t.Errorf("task count = %d, want exactly 1; a replay must never duplicate an Agent Task", taskCount)
	}
}

// TestRunReachesCompletionOnlyThroughEnd walks the whole happy path and asserts
// the Run completes exactly when the End node is reached — not when the last
// Agent Task finished.
func TestRunReachesCompletionOnlyThroughEnd(t *testing.T) {
	env := setupTestEnv(t)
	env.publishTemplate(t, linearDefinition())
	ctx := context.Background()
	run := env.startRun(t, "run-happy-path")

	// Analyze passes -> implement activates, Run still running.
	analyze := env.stepByNode(t, run.ID, "analyze")
	if _, err := env.engine.SubmitResult(ctx, SubmitResultInput{
		WorkspaceID: env.workspaceID,
		StepID:      analyze.ID,
		RawOutput:   passPayload("analysis complete"),
		ActorType:   "agent",
	}); err != nil {
		t.Fatalf("submit analyze: %v", err)
	}
	if got := env.reloadRun(t, run.ID).Status; got != string(RunRunning) {
		t.Fatalf("after the first Agent passed, run status = %q; a passing Task must NOT complete a Run", got)
	}
	implement := env.stepByNode(t, run.ID, "implement")
	if implement.Status != string(StepQueued) {
		t.Fatalf("implement step status = %q, want queued", implement.Status)
	}

	// Implement passes -> End runs -> Run completes.
	if _, err := env.engine.SubmitResult(ctx, SubmitResultInput{
		WorkspaceID: env.workspaceID,
		StepID:      implement.ID,
		RawOutput:   passPayload("code change ready"),
		ActorType:   "agent",
	}); err != nil {
		t.Fatalf("submit implement: %v", err)
	}

	final := env.reloadRun(t, run.ID)
	if final.Status != string(RunCompleted) {
		t.Fatalf("run status = %q, want completed after reaching End", final.Status)
	}
	if !final.CompletedAt.Valid {
		t.Error("a completed Run must have completed_at set")
	}
	end := env.stepByNode(t, run.ID, "end")
	if end.Status != string(StepPassed) {
		t.Errorf("end step status = %q, want passed", end.Status)
	}
}

// TestRunStatusProjectsOntoParentIssue is the regression for a Workflow whose
// child steps all completed while its parent Issue remained in todo. Run state
// is canonical, but every committed transition must update its Issue projection
// in the same transaction.
func TestRunStatusProjectsOntoParentIssue(t *testing.T) {
	env := setupTestEnv(t)
	env.publishTemplate(t, &Definition{
		SchemaVersion: SchemaVersion,
		EntryNode:     "work",
		Nodes: []Node{
			{
				Key: "work", Type: NodeTypeAgent, Next: []string{"end"},
				Routing:          &Routing{Strategy: RoutingCapability, Capability: "code"},
				SubmissionSchema: "code_change",
			},
			{Key: "end", Type: NodeTypeEnd},
		},
		Limits: Limits{MaxAttemptsPerNode: 3},
	})
	ctx := context.Background()
	run, issue := env.startRunForIssue(t, "run-parent-status-projection")

	if got := env.reloadIssue(t, issue.ID).Status; got != "in_progress" {
		t.Fatalf("issue status after run start = %q, want in_progress", got)
	}

	work := env.stepByNode(t, run.ID, "work")
	env.completeStepTask(t, work)
	if _, err := env.engine.SubmitResult(ctx, SubmitResultInput{
		WorkspaceID: env.workspaceID,
		StepID:      work.ID,
		RawOutput:   passPayload("work complete"),
		ActorType:   "agent",
	}); err != nil {
		t.Fatalf("submit work: %v", err)
	}

	if got := env.reloadRun(t, run.ID).Status; got != string(RunCompleted) {
		t.Fatalf("run status = %q, want completed", got)
	}
	if got := env.reloadIssue(t, issue.ID).Status; got != "done" {
		t.Fatalf("issue status after all workflow steps completed = %q, want done", got)
	}
}

// TestInvalidSubmissionBlocksAndNeverPasses is the anti-false-success guarantee
// end to end: prose that sounds like success must block the Step, record the raw
// output as evidence, and leave the Run recoverable.
func TestInvalidSubmissionBlocksAndNeverPasses(t *testing.T) {
	env := setupTestEnv(t)
	env.publishTemplate(t, linearDefinition())
	ctx := context.Background()
	run := env.startRun(t, "run-bad-submission")
	analyze := env.stepByNode(t, run.ID, "analyze")

	step, err := env.engine.SubmitResult(ctx, SubmitResultInput{
		WorkspaceID: env.workspaceID,
		StepID:      analyze.ID,
		RawOutput:   "Yes, all done! Everything works great, tests are green.",
		ActorType:   "agent",
	})
	if err != nil {
		t.Fatalf("SubmitResult should block, not error: %v", err)
	}
	if step.Status != string(StepBlocked) {
		t.Fatalf("step status = %q, want blocked; natural language must never imply pass", step.Status)
	}
	if step.FailureReason.String != ReasonSubmissionContractInvalid {
		t.Errorf("failure reason = %q, want %q", step.FailureReason.String, ReasonSubmissionContractInvalid)
	}

	blocked := env.reloadRun(t, run.ID)
	if blocked.Status != string(RunBlocked) {
		t.Errorf("run status = %q, want blocked", blocked.Status)
	}
	if blocked.BlockedReason.String != ReasonSubmissionContractInvalid {
		t.Errorf("run blocked_reason = %q, want %q", blocked.BlockedReason.String, ReasonSubmissionContractInvalid)
	}

	// The Agent's actual words must be retained as evidence, with the reason why
	// they were refused.
	var verdict, raw string
	var validationErrors []byte
	if err := env.pool.QueryRow(ctx,
		`SELECT verdict, raw_result, validation_errors FROM workflow_submission WHERE step_id = $1`,
		analyze.ID).Scan(&verdict, &raw, &validationErrors); err != nil {
		t.Fatalf("load submission: %v", err)
	}
	if verdict != string(VerdictBlocked) {
		t.Errorf("recorded verdict = %q, want blocked", verdict)
	}
	if raw == "" {
		t.Error("raw agent output must be retained for diagnosis")
	}
	if len(validationErrors) == 0 || string(validationErrors) == "null" {
		t.Error("validation errors must be recorded so a human can see why it was refused")
	}

	// No downstream step may have been created from a blocked step.
	steps, err := env.q.ListWorkflowStepInstances(ctx, db.ListWorkflowStepInstancesParams{
		RunID: run.ID, WorkspaceID: env.workspaceID,
	})
	if err != nil {
		t.Fatalf("list steps: %v", err)
	}
	for _, s := range steps {
		if s.NodeKey == "implement" || s.NodeKey == "end" {
			t.Errorf("a blocked step must not advance the graph, but %q was created", s.NodeKey)
		}
	}
}

// TestFailVerdictTriggersReworkWithContext proves rework creates a NEW attempt,
// preserves the old one, and injects the reason.
func TestFailVerdictTriggersReworkWithContext(t *testing.T) {
	env := setupTestEnv(t)
	def := &Definition{
		SchemaVersion: SchemaVersion,
		EntryNode:     "analyze",
		Nodes: []Node{
			{
				Key: "analyze", Type: NodeTypeAgent, Next: []string{"validate"},
				Routing:          &Routing{Strategy: RoutingCapability, Capability: "analysis"},
				SubmissionSchema: "analysis",
			},
			{
				Key: "validate", Type: NodeTypeAgent, Next: []string{"end"},
				Routing:          &Routing{Strategy: RoutingCapability, Capability: "test"},
				SubmissionSchema: "test_report",
				OnFailure:        FailurePolicyRework,
				ReworkTargets:    []string{"analyze"},
			},
			{Key: "end", Type: NodeTypeEnd},
		},
		Limits: Limits{MaxAttemptsPerNode: 3},
	}
	env.publishTemplate(t, def)
	ctx := context.Background()
	run := env.startRun(t, "run-rework")

	analyze1 := env.stepByNode(t, run.ID, "analyze")
	if _, err := env.engine.SubmitResult(ctx, SubmitResultInput{
		WorkspaceID: env.workspaceID, StepID: analyze1.ID,
		RawOutput: passPayload("first analysis"), ActorType: "agent",
	}); err != nil {
		t.Fatalf("submit analyze: %v", err)
	}

	validate := env.stepByNode(t, run.ID, "validate")
	failPayload := fmt.Sprintf(`%s
{"verdict":"fail","artifact":{"type":"test_report","summary":"2 tests failed"},"rationale":"pagination is still off by one","root_cause":"the fix addressed the wrong branch"}
%s`, submissionOpen, submissionClose)
	if _, err := env.engine.SubmitResult(ctx, SubmitResultInput{
		WorkspaceID: env.workspaceID, StepID: validate.ID,
		RawOutput: failPayload, ActorType: "agent",
	}); err != nil {
		t.Fatalf("submit validate fail: %v", err)
	}

	// The failed attempt is preserved.
	failedValidate, err := env.q.GetWorkflowStepInstance(ctx, db.GetWorkflowStepInstanceParams{
		ID: validate.ID, WorkspaceID: env.workspaceID,
	})
	if err != nil {
		t.Fatalf("reload validate: %v", err)
	}
	if failedValidate.Status != string(StepFailed) {
		t.Errorf("validate status = %q, want failed", failedValidate.Status)
	}

	// A new analyze attempt exists, and the original is untouched.
	analyze2 := env.stepByNode(t, run.ID, "analyze")
	if analyze2.Attempt != 2 {
		t.Fatalf("rework should create attempt 2, got attempt %d", analyze2.Attempt)
	}
	if uuidString(analyze2.ID) == uuidString(analyze1.ID) {
		t.Fatal("rework must create a new row, not mutate the prior attempt")
	}
	still, err := env.q.GetWorkflowStepInstance(ctx, db.GetWorkflowStepInstanceParams{
		ID: analyze1.ID, WorkspaceID: env.workspaceID,
	})
	if err != nil {
		t.Fatalf("reload analyze attempt 1: %v", err)
	}
	if still.Status != string(StepPassed) {
		t.Errorf("attempt 1 status = %q, want it preserved as passed", still.Status)
	}

	// The rejection reason must reach the new attempt's input.
	if len(analyze2.Input) == 0 {
		t.Fatal("rework attempt should carry input")
	}
	inputStr := string(analyze2.Input)
	if !contains(inputStr, "rework") || !contains(inputStr, "wrong branch") {
		t.Errorf("rework context must be injected into the new attempt input, got: %s", inputStr)
	}

	// The original submission history survives.
	subs, err := env.q.ListWorkflowSubmissionsForRun(ctx, db.ListWorkflowSubmissionsForRunParams{
		RunID: run.ID, WorkspaceID: env.workspaceID,
	})
	if err != nil {
		t.Fatalf("list submissions: %v", err)
	}
	if len(subs) != 2 {
		t.Errorf("expected both submissions preserved, got %d", len(subs))
	}
}

// TestAcceptanceGateSeparatesTaskCompletionFromBusinessDone is the plan's
// headline distinction: the Agent finished, yet the Run waits for a human.
func TestAcceptanceGateSeparatesTaskCompletionFromBusinessDone(t *testing.T) {
	env := setupTestEnv(t)
	env.publishTemplate(t, acceptanceDefinition())
	ctx := context.Background()
	run := env.startRun(t, "run-acceptance")

	implement := env.stepByNode(t, run.ID, "implement")
	if _, err := env.engine.SubmitResult(ctx, SubmitResultInput{
		WorkspaceID: env.workspaceID, StepID: implement.ID,
		RawOutput: passPayload("feature implemented"), ActorType: "agent",
	}); err != nil {
		t.Fatalf("submit implement: %v", err)
	}

	// Agent Task succeeded, but the Run is NOT done.
	waiting := env.reloadRun(t, run.ID)
	if waiting.Status != string(RunWaitingAcceptance) {
		t.Fatalf("run status = %q, want waiting_acceptance; Agent completion is not business acceptance", waiting.Status)
	}

	acceptanceStep := env.stepByNode(t, run.ID, "acceptance")
	pending, err := env.q.GetPendingWorkflowAcceptanceForStep(ctx, db.GetPendingWorkflowAcceptanceForStepParams{
		StepID: acceptanceStep.ID, WorkspaceID: env.workspaceID,
	})
	if err != nil {
		t.Fatalf("expected a pending acceptance: %v", err)
	}
	// The reviewer needs the evidence to decide.
	if len(pending.Context) == 0 || !contains(string(pending.Context), "criteria") {
		t.Errorf("acceptance context should carry criteria and evidence, got: %s", pending.Context)
	}

	// Accepting completes the Run through End.
	if _, err := env.engine.DecideAcceptance(ctx, DecideAcceptanceInput{
		WorkspaceID:    env.workspaceID,
		AcceptanceID:   pending.ID,
		Accept:         true,
		ReviewerUserID: env.userID,
	}); err != nil {
		t.Fatalf("accept: %v", err)
	}
	done := env.reloadRun(t, run.ID)
	if done.Status != string(RunCompleted) {
		t.Errorf("run status = %q, want completed after acceptance", done.Status)
	}
}

func acceptanceDefinition() *Definition {
	return &Definition{
		SchemaVersion: SchemaVersion,
		EntryNode:     "implement",
		Nodes: []Node{
			{
				Key: "implement", Type: NodeTypeAgent, Next: []string{"acceptance"},
				Routing:          &Routing{Strategy: RoutingCapability, Capability: "code"},
				SubmissionSchema: "code_change",
			},
			{
				Key: "acceptance", Type: NodeTypeAcceptance, Next: []string{"end"},
				AcceptanceCriteria: []string{"happy path verified", "edge case covered"},
				ReworkTargets:      []string{"implement"},
			},
			{Key: "end", Type: NodeTypeEnd},
		},
		Limits: Limits{MaxAttemptsPerNode: 3},
	}
}

// TestRejectionCreatesTargetedReworkAttempt: a human rejection must reopen a
// specific node with the reason attached.
func TestRejectionCreatesTargetedReworkAttempt(t *testing.T) {
	env := setupTestEnv(t)
	env.publishTemplate(t, acceptanceDefinition())
	ctx := context.Background()
	run := env.startRun(t, "run-rejection")

	implement1 := env.stepByNode(t, run.ID, "implement")
	if _, err := env.engine.SubmitResult(ctx, SubmitResultInput{
		WorkspaceID: env.workspaceID, StepID: implement1.ID,
		RawOutput: passPayload("first attempt"), ActorType: "agent",
	}); err != nil {
		t.Fatalf("submit: %v", err)
	}
	acceptanceStep := env.stepByNode(t, run.ID, "acceptance")
	pending, err := env.q.GetPendingWorkflowAcceptanceForStep(ctx, db.GetPendingWorkflowAcceptanceForStepParams{
		StepID: acceptanceStep.ID, WorkspaceID: env.workspaceID,
	})
	if err != nil {
		t.Fatalf("get pending acceptance: %v", err)
	}

	if _, err := env.engine.DecideAcceptance(ctx, DecideAcceptanceInput{
		WorkspaceID:    env.workspaceID,
		AcceptanceID:   pending.ID,
		Accept:         false,
		Reason:         "the edge case is still unhandled",
		ReworkTarget:   "implement",
		ReviewerUserID: env.userID,
	}); err != nil {
		t.Fatalf("reject: %v", err)
	}

	implement2 := env.stepByNode(t, run.ID, "implement")
	if implement2.Attempt != 2 {
		t.Fatalf("rejection should open attempt 2, got %d", implement2.Attempt)
	}
	if !contains(string(implement2.Input), "edge case is still unhandled") {
		t.Errorf("the rejection reason must reach the new attempt, got: %s", implement2.Input)
	}
	if implement2.Status != string(StepQueued) {
		t.Errorf("rework attempt status = %q, want queued with a fresh task", implement2.Status)
	}
	if !implement2.TaskID.Valid {
		t.Error("rework attempt must get its own Agent Task")
	}
	if uuidString(implement2.TaskID) == uuidString(implement1.TaskID) {
		t.Error("rework must create a NEW task, not reuse the old one")
	}
	// The Run resumed rather than staying parked.
	if got := env.reloadRun(t, run.ID).Status; got != string(RunRunning) {
		t.Errorf("run status = %q, want running after rework started", got)
	}
}

// TestSecondReviewerLosesTheRace: the pending-status guard is the acceptance
// conflict fence.
func TestSecondReviewerLosesTheRace(t *testing.T) {
	env := setupTestEnv(t)
	env.publishTemplate(t, acceptanceDefinition())
	ctx := context.Background()
	run := env.startRun(t, "run-reviewer-race")

	implement := env.stepByNode(t, run.ID, "implement")
	if _, err := env.engine.SubmitResult(ctx, SubmitResultInput{
		WorkspaceID: env.workspaceID, StepID: implement.ID,
		RawOutput: passPayload("done"), ActorType: "agent",
	}); err != nil {
		t.Fatalf("submit: %v", err)
	}
	acceptanceStep := env.stepByNode(t, run.ID, "acceptance")
	pending, err := env.q.GetPendingWorkflowAcceptanceForStep(ctx, db.GetPendingWorkflowAcceptanceForStepParams{
		StepID: acceptanceStep.ID, WorkspaceID: env.workspaceID,
	})
	if err != nil {
		t.Fatalf("get pending: %v", err)
	}

	if _, err := env.engine.DecideAcceptance(ctx, DecideAcceptanceInput{
		WorkspaceID: env.workspaceID, AcceptanceID: pending.ID,
		Accept: true, ReviewerUserID: env.userID,
	}); err != nil {
		t.Fatalf("first decision: %v", err)
	}

	_, err = env.engine.DecideAcceptance(ctx, DecideAcceptanceInput{
		WorkspaceID: env.workspaceID, AcceptanceID: pending.ID,
		Accept: false, Reason: "changed my mind", ReworkTarget: "implement",
		ReviewerUserID: env.userID,
	})
	if err == nil {
		t.Fatal("a second decision on the same acceptance must be rejected")
	}
	if ee, ok := err.(*EngineError); !ok || ee.Code != ErrCodeAcceptanceConflict {
		t.Errorf("expected an acceptance conflict, got %v", err)
	}
}

// TestRejectionToUnpermittedTargetIsRefused: a reviewer may not reroute work to
// a node the graph author never allowed.
func TestRejectionToUnpermittedTargetIsRefused(t *testing.T) {
	env := setupTestEnv(t)
	env.publishTemplate(t, acceptanceDefinition())
	ctx := context.Background()
	run := env.startRun(t, "run-bad-target")

	implement := env.stepByNode(t, run.ID, "implement")
	if _, err := env.engine.SubmitResult(ctx, SubmitResultInput{
		WorkspaceID: env.workspaceID, StepID: implement.ID,
		RawOutput: passPayload("done"), ActorType: "agent",
	}); err != nil {
		t.Fatalf("submit: %v", err)
	}
	acceptanceStep := env.stepByNode(t, run.ID, "acceptance")
	pending, err := env.q.GetPendingWorkflowAcceptanceForStep(ctx, db.GetPendingWorkflowAcceptanceForStepParams{
		StepID: acceptanceStep.ID, WorkspaceID: env.workspaceID,
	})
	if err != nil {
		t.Fatalf("get pending: %v", err)
	}

	if _, err := env.engine.DecideAcceptance(ctx, DecideAcceptanceInput{
		WorkspaceID: env.workspaceID, AcceptanceID: pending.ID,
		Accept: false, Reason: "nope", ReworkTarget: "end",
		ReviewerUserID: env.userID,
	}); err == nil {
		t.Fatal("rejecting to a non-permitted target must be refused")
	}

	// And a rejection with no reason is refused too.
	if _, err := env.engine.DecideAcceptance(ctx, DecideAcceptanceInput{
		WorkspaceID: env.workspaceID, AcceptanceID: pending.ID,
		Accept: false, ReworkTarget: "implement", ReviewerUserID: env.userID,
	}); err == nil {
		t.Fatal("rejecting without a reason must be refused")
	}
}

// TestRoutingFailureBlocksRunWithReason: no eligible Agent parks the Run with an
// explanation rather than crashing or failing silently.
func TestRoutingFailureBlocksRunWithReason(t *testing.T) {
	env := setupTestEnv(t)
	env.engine.Router = &fixedRouter{fail: true}
	env.publishTemplate(t, linearDefinition())

	res, err := env.engine.StartRun(context.Background(), StartRunInput{
		WorkspaceID:       env.workspaceID,
		TemplateID:        env.templateID,
		Source:            "manual",
		IdempotencyKey:    "run-no-agent",
		AccountableUserID: env.userID,
		ActorType:         "member",
		ActorID:           env.userID,
	})
	if err != nil {
		t.Fatalf("StartRun should block, not error: %v", err)
	}

	run := env.reloadRun(t, res.Run.ID)
	if run.Status != string(RunBlocked) {
		t.Fatalf("run status = %q, want blocked when no Agent is eligible", run.Status)
	}
	if run.BlockedReason.String != ReasonRoutingNoCandidate {
		t.Errorf("blocked_reason = %q, want %q", run.BlockedReason.String, ReasonRoutingNoCandidate)
	}
	step := env.stepByNode(t, run.ID, "analyze")
	if step.Status != string(StepBlocked) {
		t.Errorf("step status = %q, want blocked", step.Status)
	}
	if step.TaskID.Valid {
		t.Error("a step that failed routing must not have an Agent Task")
	}
}

// TestRecordTaskTerminalIgnoresNonWorkflowTasks: every legacy task path calls
// this hook, so a non-workflow task must be a silent no-op.
func TestRecordTaskTerminalIgnoresNonWorkflowTasks(t *testing.T) {
	env := setupTestEnv(t)
	ctx := context.Background()

	var taskID pgtype.UUID
	if err := env.pool.QueryRow(ctx,
		`INSERT INTO agent_task_queue (agent_id, runtime_id, status, priority)
		 VALUES ($1, $2, 'completed', 1) RETURNING id`,
		env.agentID, env.runtimeID).Scan(&taskID); err != nil {
		t.Fatalf("create legacy task: %v", err)
	}

	if err := env.engine.RecordTaskTerminal(ctx, RecordTaskTerminalInput{
		WorkspaceID: env.workspaceID,
		TaskID:      taskID,
		TaskStatus:  "completed",
		Result:      "some legacy output",
	}); err != nil {
		t.Errorf("a non-workflow task must be a silent no-op, got: %v", err)
	}
}

// TestRecordTaskTerminalRoutesFailureThroughPolicy proves the taskfailure
// classification is preserved rather than flattened to "unknown".
func TestRecordTaskTerminalRoutesFailureThroughPolicy(t *testing.T) {
	env := setupTestEnv(t)
	env.publishTemplate(t, linearDefinition())
	ctx := context.Background()
	run := env.startRun(t, "run-task-failed")
	step := env.stepByNode(t, run.ID, "analyze")

	if err := env.engine.RecordTaskTerminal(ctx, RecordTaskTerminalInput{
		WorkspaceID:   env.workspaceID,
		TaskID:        step.TaskID,
		TaskStatus:    "failed",
		FailureReason: "agent_error.provider_quota_limit",
		ErrorDetail:   "402 payment required",
	}); err != nil {
		t.Fatalf("RecordTaskTerminal: %v", err)
	}

	failed, err := env.q.GetWorkflowStepInstance(ctx, db.GetWorkflowStepInstanceParams{
		ID: step.ID, WorkspaceID: env.workspaceID,
	})
	if err != nil {
		t.Fatalf("reload step: %v", err)
	}
	if failed.Status != string(StepFailed) {
		t.Errorf("step status = %q, want failed", failed.Status)
	}
	// The classification must survive: it is what tells an operator to top up
	// billing rather than debug the agent.
	if failed.FailureReason.String != "agent_error.provider_quota_limit" {
		t.Errorf("failure reason = %q, want the taskfailure classification preserved", failed.FailureReason.String)
	}

	// linearDefinition's analyze node has no on_failure, so it defaults to block.
	blocked := env.reloadRun(t, run.ID)
	if blocked.Status != string(RunBlocked) {
		t.Errorf("run status = %q, want blocked (the default failure policy)", blocked.Status)
	}
}

// TestDuplicateTaskTerminalIsIgnored: terminal delivery is at-least-once, so a
// replay must not double-advance the graph.
func TestDuplicateTaskTerminalIsIgnored(t *testing.T) {
	env := setupTestEnv(t)
	env.publishTemplate(t, linearDefinition())
	ctx := context.Background()
	run := env.startRun(t, "run-dup-terminal")
	step := env.stepByNode(t, run.ID, "analyze")

	in := RecordTaskTerminalInput{
		WorkspaceID: env.workspaceID,
		TaskID:      step.TaskID,
		TaskStatus:  "completed",
		Result:      passPayload("analysis done"),
	}
	if err := env.engine.RecordTaskTerminal(ctx, in); err != nil {
		t.Fatalf("first terminal: %v", err)
	}
	if err := env.engine.RecordTaskTerminal(ctx, in); err != nil {
		t.Fatalf("replayed terminal must be a no-op, got: %v", err)
	}

	// Exactly one implement step, one submission per step.
	steps, err := env.q.ListWorkflowStepInstances(ctx, db.ListWorkflowStepInstancesParams{
		RunID: run.ID, WorkspaceID: env.workspaceID,
	})
	if err != nil {
		t.Fatalf("list steps: %v", err)
	}
	implementCount := 0
	for _, s := range steps {
		if s.NodeKey == "implement" {
			implementCount++
		}
	}
	if implementCount != 1 {
		t.Errorf("replay created %d implement steps, want exactly 1", implementCount)
	}

	var subCount int
	if err := env.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM workflow_submission WHERE step_id = $1`, step.ID).Scan(&subCount); err != nil {
		t.Fatalf("count submissions: %v", err)
	}
	if subCount != 1 {
		t.Errorf("replay recorded %d submissions, want exactly 1", subCount)
	}
}

// TestCancelRunCascadesToSteps: no DB cascades exist, so the engine must do it.
func TestCancelRunCascadesToSteps(t *testing.T) {
	env := setupTestEnv(t)
	env.publishTemplate(t, linearDefinition())
	ctx := context.Background()
	run := env.startRun(t, "run-cancel")

	cancelled, err := env.engine.CancelRun(ctx, env.workspaceID, run.ID, env.userID)
	if err != nil {
		t.Fatalf("CancelRun: %v", err)
	}
	if cancelled.Status != string(RunCancelled) {
		t.Errorf("run status = %q, want cancelled", cancelled.Status)
	}

	steps, err := env.q.ListWorkflowStepInstances(ctx, db.ListWorkflowStepInstancesParams{
		RunID: run.ID, WorkspaceID: env.workspaceID,
	})
	if err != nil {
		t.Fatalf("list steps: %v", err)
	}
	for _, s := range steps {
		if !IsTerminalStepStatus(StepStatus(s.Status)) {
			t.Errorf("step %q status = %q, want a terminal status after run cancellation", s.NodeKey, s.Status)
		}
	}

	// Cancelling twice is a no-op, not an error.
	if _, err := env.engine.CancelRun(ctx, env.workspaceID, run.ID, env.userID); err != nil {
		t.Errorf("cancelling an already-cancelled run must be idempotent, got: %v", err)
	}
}

// TestWorkspaceIsolation: a Run must be invisible and immutable from another
// workspace, even with the correct UUID.
func TestWorkspaceIsolation(t *testing.T) {
	env := setupTestEnv(t)
	env.publishTemplate(t, linearDefinition())
	ctx := context.Background()
	run := env.startRun(t, "run-isolation")

	var otherWS pgtype.UUID
	if err := env.pool.QueryRow(ctx,
		`INSERT INTO workspace (name, slug) VALUES ($1, $2) RETURNING id`,
		"Other", fmt.Sprintf("other-%d", time.Now().UnixNano())).Scan(&otherWS); err != nil {
		t.Fatalf("create other workspace: %v", err)
	}
	t.Cleanup(func() {
		_, _ = env.pool.Exec(context.Background(), `DELETE FROM workspace WHERE id = $1`, otherWS)
	})

	if _, err := env.q.GetWorkflowRun(ctx, db.GetWorkflowRunParams{
		ID: run.ID, WorkspaceID: otherWS,
	}); err == nil {
		t.Error("a run must not be readable from another workspace")
	} else if !errorsIsNoRows(err) {
		t.Errorf("expected no rows, got: %v", err)
	}

	if _, err := env.engine.CancelRun(ctx, otherWS, run.ID, env.userID); err == nil {
		t.Error("a run must not be cancellable from another workspace")
	}

	step := env.stepByNode(t, run.ID, "analyze")
	if _, err := env.engine.SubmitResult(ctx, SubmitResultInput{
		WorkspaceID: otherWS, StepID: step.ID,
		RawOutput: passPayload("cross-tenant write"), ActorType: "agent",
	}); err == nil {
		t.Error("a step must not be submittable from another workspace")
	}
}

// TestStartRunRequiresPublishedVersion: a draft graph is still mutable, so a Run
// must never pin one.
func TestStartRunRequiresPublishedVersion(t *testing.T) {
	env := setupTestEnv(t)
	ctx := context.Background()

	tpl, err := env.q.CreateWorkflowTemplate(ctx, db.CreateWorkflowTemplateParams{
		WorkspaceID: env.workspaceID, Key: fmt.Sprintf("draft-%d", time.Now().UnixNano()),
		Name: "Draft Only", CreatedByType: "member", CreatedByID: env.userID,
	})
	if err != nil {
		t.Fatalf("create template: %v", err)
	}
	raw, _ := MarshalDefinition(linearDefinition())
	version, err := env.q.CreateWorkflowTemplateVersion(ctx, db.CreateWorkflowTemplateVersionParams{
		WorkspaceID: env.workspaceID, TemplateID: tpl.ID,
		Definition: raw, SchemaVersion: SchemaVersion,
	})
	if err != nil {
		t.Fatalf("create version: %v", err)
	}

	if _, err := env.engine.StartRun(ctx, StartRunInput{
		WorkspaceID: env.workspaceID, TemplateID: tpl.ID,
		TemplateVersionID: version.ID,
		Source:            "manual", IdempotencyKey: "run-draft",
		AccountableUserID: env.userID,
	}); err == nil {
		t.Fatal("starting a Run on a draft version must be refused")
	}

	// And with no version pinned, an unpublished template has nothing to run.
	if _, err := env.engine.StartRun(ctx, StartRunInput{
		WorkspaceID: env.workspaceID, TemplateID: tpl.ID,
		Source: "manual", IdempotencyKey: "run-draft-2",
		AccountableUserID: env.userID,
	}); err == nil {
		t.Fatal("starting a Run on a template with no published version must be refused")
	}
}

// TestPublishedDefinitionIsImmutable guards the invariant that in-flight Runs
// cannot have their graph changed under them.
func TestPublishedDefinitionIsImmutable(t *testing.T) {
	env := setupTestEnv(t)
	env.publishTemplate(t, linearDefinition())
	ctx := context.Background()

	altered, _ := MarshalDefinition(acceptanceDefinition())
	_, err := env.q.UpdateWorkflowTemplateVersionDefinition(ctx, db.UpdateWorkflowTemplateVersionDefinitionParams{
		ID: env.versionID, WorkspaceID: env.workspaceID, Definition: altered,
	})
	if err == nil {
		t.Fatal("a published version's definition must not be updatable")
	}
	if !errorsIsNoRows(err) {
		t.Errorf("expected the status guard to match no rows, got: %v", err)
	}
}

// TestEventLogFormsRunTrace: the Run -> Step -> Task trace is a stated success
// criterion (plan section 1).
func TestEventLogFormsRunTrace(t *testing.T) {
	env := setupTestEnv(t)
	env.publishTemplate(t, linearDefinition())
	ctx := context.Background()
	run := env.startRun(t, "run-trace")

	analyze := env.stepByNode(t, run.ID, "analyze")
	if _, err := env.engine.SubmitResult(ctx, SubmitResultInput{
		WorkspaceID: env.workspaceID, StepID: analyze.ID,
		RawOutput: passPayload("done"), ActorType: "agent",
	}); err != nil {
		t.Fatalf("submit: %v", err)
	}

	events, err := env.q.ListWorkflowEventsForRun(ctx, db.ListWorkflowEventsForRunParams{
		RunID: run.ID, WorkspaceID: env.workspaceID,
	})
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	seen := map[string]bool{}
	for _, e := range events {
		seen[e.EventType] = true
	}
	for _, want := range []string{EventRunStarted, EventStepActivated, EventStepQueued, EventStepSubmitted, EventStepPassed} {
		if !seen[want] {
			t.Errorf("expected event %q in the run trace, got %v", want, keys(seen))
		}
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

func errorsIsNoRows(err error) bool {
	return err == pgx.ErrNoRows || (err != nil && contains(err.Error(), "no rows"))
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
