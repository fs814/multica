package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	DefaultReconcileInterval         = 30 * time.Second
	DefaultReconcileStaleAfter       = 30 * time.Second
	DefaultReconcileBatchSize  int32 = 100
)

type Reconciler struct {
	Engine     *Engine
	Queries    *db.Queries
	Interval   time.Duration
	StaleAfter time.Duration
	BatchSize  int32
}

func NewReconciler(engine *Engine, queries *db.Queries) *Reconciler {
	return &Reconciler{Engine: engine, Queries: queries, Interval: DefaultReconcileInterval, StaleAfter: DefaultReconcileStaleAfter, BatchSize: DefaultReconcileBatchSize}
}

func (r *Reconciler) Run(ctx context.Context) {
	interval := r.Interval
	if interval <= 0 {
		interval = DefaultReconcileInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := r.Sweep(ctx); err != nil {
				slog.Error("workflow reconciler sweep failed", "error", err)
			}
		}
	}
}

func (r *Reconciler) Sweep(ctx context.Context) error {
	if r == nil || r.Engine == nil || r.Queries == nil {
		return nil
	}
	if r.Engine.Metrics != nil {
		if seconds, err := r.Queries.OldestStalledWorkflowRunSeconds(ctx); err == nil {
			r.Engine.Metrics.SetOldestStalledRun(seconds)
		}
	}
	stale := r.StaleAfter
	if stale < 0 {
		stale = 0
	}
	limit := r.BatchSize
	if limit <= 0 {
		limit = DefaultReconcileBatchSize
	}
	tasks, err := r.Queries.ListWorkflowTasksAwaitingStepProgress(ctx, db.ListWorkflowTasksAwaitingStepProgressParams{StaleSeconds: stale.Seconds(), LimitCount: limit})
	if err != nil {
		return fmt.Errorf("list terminal workflow tasks: %w", err)
	}
	for _, task := range tasks {
		err := r.Engine.RecordTaskTerminal(ctx, RecordTaskTerminalInput{TaskID: task.TaskID, TaskStatus: task.TaskStatus, Result: workflowTaskOutput(task.Result), FailureReason: task.FailureReason.String, ErrorDetail: task.Error.String})
		r.record(reconcileOutcome(err))
		if err != nil {
			slog.Warn("workflow reconciler could not replay terminal task", "error", err)
		}
	}
	runs, err := r.Queries.ListStaleActiveWorkflowRuns(ctx, db.ListStaleActiveWorkflowRunsParams{StaleSeconds: stale.Seconds(), LimitCount: limit})
	if err != nil {
		return fmt.Errorf("list stale workflow runs: %w", err)
	}
	for _, run := range runs {
		err := r.Engine.ReconcileRun(ctx, run.WorkspaceID, run.ID)
		r.record(reconcileOutcome(err))
		if err != nil {
			slog.Warn("workflow reconciler could not repair run", "error", err)
		}
	}
	linked, err := r.Queries.ListAutopilotWorkflowRunsAwaitingSync(ctx, limit)
	if err != nil {
		return fmt.Errorf("list terminal autopilot workflow runs: %w", err)
	}
	for _, item := range linked {
		if item.WorkflowStatus == string(RunCompleted) {
			_, err = r.Queries.UpdateAutopilotRunCompleted(ctx, db.UpdateAutopilotRunCompletedParams{ID: item.AutopilotRunID, Result: mustJSON(map[string]any{"workflow_run_id": uuidString(item.WorkflowRunID)})})
		} else {
			reason := item.FailureReason.String
			if reason == "" {
				reason = item.BlockedReason.String
			}
			if reason == "" {
				reason = ReasonInvariantViolation
			}
			_, err = r.Queries.UpdateAutopilotRunFailed(ctx, db.UpdateAutopilotRunFailedParams{ID: item.AutopilotRunID, FailureReason: textOrNull(reason)})
		}
		r.record(reconcileOutcome(err))
		if err != nil {
			slog.Warn("workflow reconciler could not project autopilot run", "error", err)
		}
	}
	return nil
}

func (r *Reconciler) record(outcome string) {
	if r.Engine.Metrics != nil {
		r.Engine.Metrics.RecordReconciliation(outcome)
	}
}
func reconcileOutcome(err error) string {
	if err == nil {
		return "repaired_or_healthy"
	}
	if IsIdempotencyConflict(err) {
		return "replay"
	}
	return "error"
}

func workflowTaskOutput(result []byte) string {
	if len(result) == 0 {
		return ""
	}
	var payload struct {
		Output string `json:"output"`
	}
	if json.Unmarshal(result, &payload) == nil {
		return payload.Output
	}
	return string(result)
}

// ReconcileRun replays the same engine helpers as live commands. It never
// directly invents a successful status; irreconcilable state is blocked.
func (e *Engine) ReconcileRun(ctx context.Context, workspaceID, runID pgtype.UUID) error {
	effects := &txEffects{}
	err := e.runInTx(ctx, effects, func(ctx context.Context, q *db.Queries) error {
		run, err := q.GetWorkflowRunForUpdate(ctx, db.GetWorkflowRunForUpdateParams{ID: runID, WorkspaceID: workspaceID})
		if err != nil {
			return err
		}
		if IsTerminalRunStatus(RunStatus(run.Status)) || run.Status == string(RunBlocked) {
			return nil
		}
		version, err := q.GetWorkflowTemplateVersion(ctx, db.GetWorkflowTemplateVersionParams{ID: run.TemplateVersionID, WorkspaceID: workspaceID})
		if err != nil {
			return err
		}
		def, err := ParseDefinition(version.Definition)
		if err != nil {
			return err
		}
		limits := e.limitsFor(run, def)
		started := run.CreatedAt.Time
		if run.StartedAt.Valid {
			started = run.StartedAt.Time
		}
		if limits.MaxDurationSeconds > 0 && e.now().Sub(started) > time.Duration(limits.MaxDurationSeconds)*time.Second {
			if _, err := q.FailWorkflowRun(ctx, db.FailWorkflowRunParams{ID: run.ID, WorkspaceID: workspaceID, FailureReason: ReasonDurationLimitExceeded, FailureDetail: textOrNull("workflow exceeded max_duration_seconds")}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			effects.runChanged(run)
			return e.recordEvent(ctx, q, eventSpec{WorkspaceID: workspaceID, RunID: run.ID, Type: EventRunFailed, IdempotencyKey: "reconcile:" + uuidString(run.ID) + ":duration", ActorType: "system", Payload: mustJSON(map[string]any{"reason": ReasonDurationLimitExceeded})})
		}
		active, err := q.ListActiveWorkflowStepInstances(ctx, db.ListActiveWorkflowStepInstancesParams{RunID: run.ID, WorkspaceID: workspaceID})
		if err != nil {
			return err
		}
		for _, step := range active {
			node, ok := def.NodeByKey(step.NodeKey)
			if !ok {
				return e.blockRun(ctx, q, run, step, ReasonInvariantViolation, "active step node is absent from pinned definition", "system", pgtype.UUID{})
			}
			if step.Status == string(StepReady) && !step.TaskID.Valid && node.Type == NodeTypeAgent && (!step.ActivationTimeoutAt.Valid || !step.ActivationTimeoutAt.Time.After(e.now())) {
				return e.redispatchReadyAgent(ctx, q, run, def, node, step, effects)
			}
			if (step.Status == string(StepQueued) || step.Status == string(StepRunning)) && node.Type == NodeTypeAgent && !step.TaskID.Valid {
				return e.blockRun(ctx, q, run, step, ReasonInvariantViolation, "active agent step has no task", "system", pgtype.UUID{})
			}
		}
		if len(active) > 0 {
			return nil
		}
		steps, err := q.ListWorkflowStepInstances(ctx, db.ListWorkflowStepInstancesParams{RunID: run.ID, WorkspaceID: workspaceID})
		if err != nil {
			return err
		}
		if len(steps) == 0 {
			entry, ok := def.NodeByKey(def.EntryNode)
			if !ok {
				detail := "entry node is absent from pinned definition"
				if _, err := q.FailWorkflowRun(ctx, db.FailWorkflowRunParams{ID: run.ID, WorkspaceID: workspaceID, FailureReason: ReasonInvariantViolation, FailureDetail: textOrNull(detail)}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
					return err
				}
				effects.runChanged(run)
				return e.recordEvent(ctx, q, eventSpec{WorkspaceID: workspaceID, RunID: run.ID, Type: EventRunFailed, IdempotencyKey: "reconcile:" + uuidString(run.ID) + ":missing-entry", ActorType: "system", Payload: mustJSON(map[string]any{"reason": ReasonInvariantViolation, "detail": detail})})
			}
			_, err := e.activateNode(ctx, q, activateInput{Run: run, Def: def, Node: entry, Attempt: 1, ActorType: "system", Effects: effects})
			return err
		}
		last := steps[len(steps)-1]
		node, ok := def.NodeByKey(last.NodeKey)
		if !ok {
			return e.blockRun(ctx, q, run, last, ReasonInvariantViolation, "terminal step node is absent from pinned definition", "system", pgtype.UUID{})
		}
		if last.ParentStepID.Valid {
			return e.progressJoinFromChild(ctx, q, run, def, last, node, effects, "system", pgtype.UUID{})
		}
		if last.Status == string(StepPassed) || last.Status == string(StepSkipped) {
			return e.advanceFromStep(ctx, q, run, def, last, node, effects, "system", pgtype.UUID{})
		}
		return e.applyFailurePolicy(ctx, q, run, def, node, last, last.FailureReason.String, last.FailureDetail.String, effects, "system", pgtype.UUID{})
	})
	if IsIdempotencyConflict(err) {
		return nil
	}
	if err != nil {
		return err
	}
	effects.flush(ctx, e.Notifier)
	return nil
}

func (e *Engine) redispatchReadyAgent(ctx context.Context, q *db.Queries, run db.WorkflowRun, def *Definition, node *Node, step db.WorkflowStepInstance, effects *txEffects) error {
	entry, _ := def.EntryInputNode()
	input := ParseRunInputFor(run.Input, entry)
	upstream, err := e.latestSubmissionForRun(ctx, q, run)
	if err != nil {
		return err
	}
	var stored struct {
		FanOut struct {
			Key   string `json:"expansion_key"`
			Value string `json:"value"`
		} `json:"fan_out"`
	}
	_ = json.Unmarshal(step.Input, &stored)
	_, err = e.dispatchAgentStep(ctx, q, activateInput{Run: run, Def: def, Node: node, Attempt: step.Attempt, ParentStepID: step.ParentStepID, ExpansionKey: stored.FanOut.Key, ExpansionValue: stored.FanOut.Value, ActorType: "system", Reconcile: true, Effects: effects}, step, agentBrief{RunInput: input, ImageAttachment: imageAttachmentFromRunContext(run.Context), Upstream: upstream})
	return err
}
