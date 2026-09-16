package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Node executors and the remaining engine commands.
//
// The through-line here is the plan's central invariant: "Agent Task completion
// is not business acceptance; a Run completes only through End" (plan section 4).
// completeAtEnd is the ONLY place CompleteWorkflowRun is called, and it is
// reachable only by traversing an edge into an End node. No amount of Agent
// success elsewhere can complete a Run.

// requestAcceptance parks a Step for human review. This is the seam that makes
// "the Agent said it was done" different from "a human agreed it was done".
func (e *Engine) requestAcceptance(ctx context.Context, q *db.Queries, in activateInput, step db.WorkflowStepInstance) (db.WorkflowStepInstance, error) {
	run := in.Run

	// Assemble what the reviewer needs to decide: the criteria from the graph
	// plus the upstream submissions that produced the work (plan section 8:
	// criteria, artifacts, verdicts, validation evidence, warnings, cost,
	// duration).
	submissions, err := q.ListWorkflowSubmissionsForRun(ctx, db.ListWorkflowSubmissionsForRunParams{
		RunID:       run.ID,
		WorkspaceID: run.WorkspaceID,
	})
	if err != nil {
		return db.WorkflowStepInstance{}, fmt.Errorf("list submissions for acceptance: %w", err)
	}
	evidence := make([]map[string]any, 0, len(submissions))
	for _, s := range submissions {
		item := map[string]any{
			"verdict":   s.Verdict,
			"rationale": s.Rationale,
		}
		if len(s.Artifact) > 0 {
			var artifact any
			if err := json.Unmarshal(s.Artifact, &artifact); err == nil {
				item["artifact"] = artifact
			}
		}
		evidence = append(evidence, item)
	}

	acceptanceCtx := map[string]any{
		"can_reject_without_rework": in.Def.SchemaVersion == GraphSchemaVersion,
		"criteria":                  in.Node.AcceptanceCriteria,
		"evidence":                  evidence,
		"targets":                   in.Node.ReworkTargets,
	}

	if _, err := q.CreateWorkflowAcceptance(ctx, db.CreateWorkflowAcceptanceParams{
		WorkspaceID: run.WorkspaceID,
		RunID:       run.ID,
		StepID:      step.ID,
		Context:     mustJSON(acceptanceCtx),
	}); err != nil {
		if isUniqueViolation(err) {
			// A pending acceptance already exists for this step: a replayed
			// activation, not a new review.
			return db.WorkflowStepInstance{}, newEngineError(ErrCodeIdempotencyConflict,
				"this step already has a pending acceptance")
		}
		return db.WorkflowStepInstance{}, fmt.Errorf("create acceptance: %w", err)
	}

	waiting, err := q.MarkWorkflowStepWaitingAcceptance(ctx, db.MarkWorkflowStepWaitingAcceptanceParams{
		ID:          step.ID,
		WorkspaceID: run.WorkspaceID,
	})
	if err != nil {
		return db.WorkflowStepInstance{}, fmt.Errorf("mark step waiting acceptance: %w", err)
	}

	if _, err := q.MarkWorkflowRunWaitingAcceptance(ctx, db.MarkWorkflowRunWaitingAcceptanceParams{
		ID:          run.ID,
		WorkspaceID: run.WorkspaceID,
	}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return db.WorkflowStepInstance{}, fmt.Errorf("mark run waiting acceptance: %w", err)
	}

	if err := e.recordEvent(ctx, q, eventSpec{
		WorkspaceID:    run.WorkspaceID,
		RunID:          run.ID,
		StepID:         step.ID,
		Type:           EventAcceptanceRequested,
		IdempotencyKey: stepEventKey(step, "acceptance-requested"),
		ActorType:      "system",
		Payload:        mustJSON(map[string]any{"node_key": in.Node.Key}),
	}); err != nil {
		return db.WorkflowStepInstance{}, err
	}
	return waiting, nil
}

// completeAtEnd is the only path to a completed Run.
func (e *Engine) completeAtEnd(ctx context.Context, q *db.Queries, in activateInput, step db.WorkflowStepInstance) (db.WorkflowStepInstance, error) {
	run := in.Run

	passed, err := q.MarkWorkflowStepPassed(ctx, db.MarkWorkflowStepPassedParams{
		ID:          step.ID,
		WorkspaceID: run.WorkspaceID,
	})
	if err != nil {
		return db.WorkflowStepInstance{}, fmt.Errorf("pass end step: %w", err)
	}

	completed, err := q.CompleteWorkflowRun(ctx, db.CompleteWorkflowRunParams{
		ID:          run.ID,
		WorkspaceID: run.WorkspaceID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// The `status = 'running'` guard did not match. Reaching End from a
			// non-running Run means the state machine was bypassed.
			return db.WorkflowStepInstance{}, newEngineError(ErrCodeInvalidTransition,
				"cannot complete a run that is not running")
		}
		return db.WorkflowStepInstance{}, fmt.Errorf("complete run: %w", err)
	}

	if err := e.recordEvent(ctx, q, eventSpec{
		WorkspaceID:    run.WorkspaceID,
		RunID:          run.ID,
		StepID:         step.ID,
		Type:           EventRunCompleted,
		IdempotencyKey: stepEventKey(step, "run-completed"),
		ActorType:      "system",
		Payload:        mustJSON(map[string]any{"end_node": in.Node.Key}),
	}); err != nil {
		return db.WorkflowStepInstance{}, err
	}
	if in.Effects != nil {
		in.Effects.runChanged(completed)
	}
	return passed, nil
}

// blockRun parks a Run with a classified reason. Blocked is first-class: the Run
// is preserved with an explanation and an action, rather than discarded.
func (e *Engine) blockRun(ctx context.Context, q *db.Queries, run db.WorkflowRun, step db.WorkflowStepInstance, reason, detail, actorType string, actorID pgtype.UUID) error {
	blocked, err := q.MarkWorkflowRunBlocked(ctx, db.MarkWorkflowRunBlockedParams{
		ID:            run.ID,
		WorkspaceID:   run.WorkspaceID,
		BlockedReason: reason,
		FailureDetail: textOrNull(detail),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Already terminal; nothing to block.
			return nil
		}
		return fmt.Errorf("mark run blocked: %w", err)
	}
	_ = blocked
	return e.recordEvent(ctx, q, eventSpec{
		WorkspaceID:    run.WorkspaceID,
		RunID:          run.ID,
		StepID:         step.ID,
		Type:           EventRunBlocked,
		IdempotencyKey: stepEventKey(step, "run-blocked"),
		ActorType:      defaultActorType(actorType),
		ActorID:        actorID,
		Payload:        mustJSON(map[string]any{"reason": reason, "detail": detail}),
	})
}

// ---------------------------------------------------------------------------
// SubmitResult / RecordTaskTerminal
// ---------------------------------------------------------------------------

// SubmitResultInput carries an Agent's output for a Step.
type SubmitResultInput struct {
	WorkspaceID pgtype.UUID
	StepID      pgtype.UUID
	// RawOutput is the Agent's verbatim output. The engine extracts the
	// delimited payload itself, so callers do not have to know the format.
	RawOutput string
	// Payload is the already-extracted structured submission (the preferred MCP
	// path). When set, RawOutput is retained only as evidence.
	Payload   []byte
	ActorType string
	ActorID   pgtype.UUID
}

// SubmitResult converts Agent output into a durable Submission and advances the
// Step.
//
// A submission that fails contract validation BLOCKS the step with
// submission_contract_invalid. It does not fail (the Agent may have done the
// work correctly and merely reported it wrongly) and it certainly does not pass.
// Both the raw output and the validation errors are persisted so a human can see
// exactly what was said and why it was refused.
func (e *Engine) SubmitResult(ctx context.Context, in SubmitResultInput) (db.WorkflowStepInstance, error) {
	var out db.WorkflowStepInstance
	effects := &txEffects{}

	err := e.runInTx(ctx, effects, func(ctx context.Context, q *db.Queries) error {
		step, run, err := e.lockStepAndRun(ctx, q, in.WorkspaceID, in.StepID)
		if err != nil {
			return err
		}
		if IsTerminalRunStatus(RunStatus(run.Status)) || IsTerminalStepStatus(StepStatus(step.Status)) {
			// A late submission for an already-decided attempt (e.g. the task was
			// cancelled and the Agent replied anyway) must not resurrect it.
			return newEngineError(ErrCodeInvalidTransition,
				fmt.Sprintf("step is already %s", step.Status))
		}

		def, err := ResolveRunDefinition(ctx, q, in.WorkspaceID, run)
		if err != nil {
			return err
		}
		node, ok := def.NodeByKey(step.NodeKey)
		if !ok {
			return newEngineError(ErrCodeInvariantViolation,
				fmt.Sprintf("step references node %q that is absent from its pinned version", step.NodeKey))
		}

		payload := in.Payload
		if len(payload) == 0 {
			if extracted, found := ExtractSubmission(in.RawOutput); found {
				payload = []byte(extracted)
			}
		}

		raw := TruncateRawResult(in.RawOutput)
		sub, problems, perr := ParseSubmission(payload, uuidString(step.ID))
		if perr != nil {
			// Record the failed submission as evidence, then block.
			if _, err := q.CreateWorkflowSubmission(ctx, db.CreateWorkflowSubmissionParams{
				WorkspaceID:      run.WorkspaceID,
				RunID:            run.ID,
				StepID:           step.ID,
				TaskID:           step.TaskID,
				SchemaVersion:    SchemaVersion,
				Verdict:          string(VerdictBlocked),
				Artifact:         []byte("{}"),
				Rationale:        "submission failed contract validation",
				RawResult:        textOrNull(raw),
				ValidationErrors: mustJSON(problems),
			}); err != nil {
				return fmt.Errorf("record invalid submission: %w", err)
			}
			blocked, err := q.MarkWorkflowStepBlocked(ctx, db.MarkWorkflowStepBlockedParams{
				ID:            step.ID,
				WorkspaceID:   run.WorkspaceID,
				FailureReason: ReasonSubmissionContractInvalid,
				FailureDetail: textOrNull(joinProblems(problems)),
			})
			if err != nil {
				return fmt.Errorf("block step on invalid submission: %w", err)
			}
			if def.SchemaVersion == GraphSchemaVersion {
				out = blocked
				return e.advanceGraphV2(ctx, q, run, def, effects, in.ActorType, in.ActorID)
			}
			if err := e.blockRun(ctx, q, run, step, ReasonSubmissionContractInvalid, joinProblems(problems), in.ActorType, in.ActorID); err != nil {
				return err
			}
			out = blocked
			effects.runChanged(run)
			return nil
		}

		var confidence pgtype.Float8
		if sub.Confidence != nil {
			confidence = pgtype.Float8{Float64: *sub.Confidence, Valid: true}
		}
		var rootCause pgtype.Text
		if sub.RootCause != nil {
			rootCause = textOrNull(*sub.RootCause)
		}
		if _, err := q.CreateWorkflowSubmission(ctx, db.CreateWorkflowSubmissionParams{
			WorkspaceID:   run.WorkspaceID,
			RunID:         run.ID,
			StepID:        step.ID,
			TaskID:        step.TaskID,
			SchemaVersion: int32(sub.SchemaVersion),
			Verdict:       string(sub.Verdict),
			Artifact:      mustJSON(sub.Artifact),
			Rationale:     sub.Rationale,
			Confidence:    confidence,
			RootCause:     rootCause,
			RawResult:     textOrNull(raw),
		}); err != nil {
			return fmt.Errorf("create submission: %w", err)
		}

		submitted, err := q.MarkWorkflowStepSubmitted(ctx, db.MarkWorkflowStepSubmittedParams{
			ID:          step.ID,
			WorkspaceID: run.WorkspaceID,
			Output:      mustJSON(sub.Artifact),
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return newEngineError(ErrCodeInvalidTransition,
					fmt.Sprintf("cannot submit from step status %s", step.Status))
			}
			return fmt.Errorf("mark step submitted: %w", err)
		}

		if err := e.recordEvent(ctx, q, eventSpec{
			WorkspaceID:    run.WorkspaceID,
			RunID:          run.ID,
			StepID:         step.ID,
			Type:           EventStepSubmitted,
			IdempotencyKey: stepEventKey(step, "submitted"),
			ActorType:      defaultActorType(in.ActorType),
			ActorID:        in.ActorID,
			Payload:        mustJSON(map[string]any{"verdict": string(sub.Verdict)}),
		}); err != nil {
			return err
		}

		advanced, err := e.resolveSubmittedStep(ctx, q, run, def, node, submitted, sub, effects, in.ActorType, in.ActorID)
		if err != nil {
			return err
		}
		out = advanced
		effects.runChanged(run)
		return nil
	})
	if err != nil {
		return db.WorkflowStepInstance{}, err
	}
	effects.flush(ctx, e.Notifier)
	return out, nil
}

// resolveSubmittedStep applies the verdict. A Condition successor receives every
// structured verdict; otherwise pass advances and fail/blocked applies policy.
func (e *Engine) resolveSubmittedStep(
	ctx context.Context,
	q *db.Queries,
	run db.WorkflowRun,
	def *Definition,
	node *Node,
	step db.WorkflowStepInstance,
	sub *Submission,
	effects *txEffects,
	actorType string,
	actorID pgtype.UUID,
) (db.WorkflowStepInstance, error) {
	if sub.Verdict == VerdictPass {
		passed, err := q.MarkWorkflowStepPassed(ctx, db.MarkWorkflowStepPassedParams{
			ID:          step.ID,
			WorkspaceID: run.WorkspaceID,
			Output:      mustJSON(sub.Artifact),
		})
		if err != nil {
			return db.WorkflowStepInstance{}, fmt.Errorf("pass step: %w", err)
		}
		if err := e.recordEvent(ctx, q, eventSpec{
			WorkspaceID:    run.WorkspaceID,
			RunID:          run.ID,
			StepID:         step.ID,
			Type:           EventStepPassed,
			IdempotencyKey: stepEventKey(step, "passed"),
			ActorType:      "system",
		}); err != nil {
			return db.WorkflowStepInstance{}, err
		}
		if err := e.advanceFromStep(ctx, q, run, def, passed, node, effects, actorType, actorID); err != nil {
			return db.WorkflowStepInstance{}, err
		}
		return passed, nil
	}

	// Non-pass: apply the node's declared failure policy.
	status, reason := VerdictToStepStatus(sub.Verdict)
	detail := sub.Rationale
	if sub.RootCause != nil && *sub.RootCause != "" {
		detail = *sub.RootCause
	}

	var terminal db.WorkflowStepInstance
	var err error
	if status == StepBlocked {
		terminal, err = q.MarkWorkflowStepBlocked(ctx, db.MarkWorkflowStepBlockedParams{
			ID:            step.ID,
			WorkspaceID:   run.WorkspaceID,
			FailureReason: reason,
			FailureDetail: textOrNull(detail),
		})
	} else {
		terminal, err = q.MarkWorkflowStepFailed(ctx, db.MarkWorkflowStepFailedParams{
			ID:            step.ID,
			WorkspaceID:   run.WorkspaceID,
			FailureReason: reason,
			FailureDetail: textOrNull(detail),
		})
	}
	if err != nil {
		return db.WorkflowStepInstance{}, fmt.Errorf("mark step %s: %w", status, err)
	}

	eventType := EventStepFailed
	if status == StepBlocked {
		eventType = EventStepBlocked
	}
	if err := e.recordEvent(ctx, q, eventSpec{
		WorkspaceID:    run.WorkspaceID,
		RunID:          run.ID,
		StepID:         step.ID,
		Type:           eventType,
		IdempotencyKey: stepEventKey(step, "terminal"),
		ActorType:      "system",
		Payload:        mustJSON(map[string]any{"reason": reason, "detail": detail}),
	}); err != nil {
		return db.WorkflowStepInstance{}, err
	}

	if def.SchemaVersion == GraphSchemaVersion {
		return terminal, e.advanceGraphV2(ctx, q, run, def, effects, actorType, actorID)
	}
	if terminal.ParentStepID.Valid {
		if err := e.progressJoinFromChild(ctx, q, run, def, terminal, node, effects, actorType, actorID); err != nil {
			return db.WorkflowStepInstance{}, err
		}
	} else if e.nextIsCondition(def, node) {
		if err := e.advanceToNext(ctx, q, run, def, node, effects, actorType, actorID); err != nil {
			return db.WorkflowStepInstance{}, err
		}
	} else if err := e.applyFailurePolicy(ctx, q, run, def, node, terminal, reason, detail, effects, actorType, actorID); err != nil {
		return db.WorkflowStepInstance{}, err
	}
	return terminal, nil
}

func (e *Engine) nextIsCondition(def *Definition, node *Node) bool {
	if len(node.Next) != 1 {
		return false
	}
	next, ok := def.NodeByKey(node.Next[0])
	return ok && next.Type == NodeTypeCondition
}

func (e *Engine) advanceFromStep(
	ctx context.Context,
	q *db.Queries,
	run db.WorkflowRun,
	def *Definition,
	step db.WorkflowStepInstance,
	node *Node,
	effects *txEffects,
	actorType string,
	actorID pgtype.UUID,
) error {
	if step.ParentStepID.Valid {
		return e.progressJoinFromChild(ctx, q, run, def, step, node, effects, actorType, actorID)
	}
	return e.advanceToNext(ctx, q, run, def, node, effects, actorType, actorID)
}

// applyFailurePolicy implements the node's on_failure: fail the Run, block it, or
// send work back to a bounded rework target.
func (e *Engine) applyFailurePolicy(
	ctx context.Context,
	q *db.Queries,
	run db.WorkflowRun,
	def *Definition,
	node *Node,
	step db.WorkflowStepInstance,
	reason, detail string,
	effects *txEffects,
	actorType string,
	actorID pgtype.UUID,
) error {
	if def.SchemaVersion == GraphSchemaVersion {
		return e.advanceGraphV2(ctx, q, run, def, effects, actorType, actorID)
	}
	switch node.EffectiveOnFailure() {
	case FailurePolicyFail:
		if _, err := q.FailWorkflowRun(ctx, db.FailWorkflowRunParams{
			ID:            run.ID,
			WorkspaceID:   run.WorkspaceID,
			FailureReason: reason,
			FailureDetail: textOrNull(detail),
		}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("fail run: %w", err)
		}
		return e.recordEvent(ctx, q, eventSpec{
			WorkspaceID:    run.WorkspaceID,
			RunID:          run.ID,
			StepID:         step.ID,
			Type:           EventRunFailed,
			IdempotencyKey: stepEventKey(step, "run-failed"),
			ActorType:      "system",
			Payload:        mustJSON(map[string]any{"reason": reason}),
		})

	case FailurePolicyRework:
		// Send work back to the first declared target. A richer choice (root-cause
		// driven) lands with U5; the bound is what matters now.
		if len(node.ReworkTargets) == 0 {
			return e.blockRun(ctx, q, run, step, reason, detail, actorType, actorID)
		}
		target := node.ReworkTargets[0]
		targetNode, ok := def.NodeByKey(target)
		if !ok {
			return newEngineError(ErrCodeInvariantViolation,
				fmt.Sprintf("rework target %q is absent from the pinned version", target))
		}
		nextAttempt, err := e.nextAttemptFor(ctx, q, run, target)
		if err != nil {
			return err
		}
		limits := e.limitsFor(run, def)
		exhausted, err := e.reworkRoundLimitReached(ctx, q, run, limits)
		if err != nil {
			return err
		}
		if exhausted {
			return e.blockRun(ctx, q, run, step, ReasonReworkLimitExceeded,
				fmt.Sprintf("run exhausted its %d rework rounds", limits.MaxReworkRounds),
				actorType, actorID)
		}
		if nextAttempt > int32(targetNode.EffectiveMaxAttempts(limits)) {
			// Rework budget spent: block rather than loop, so a human decides.
			return e.blockRun(ctx, q, run, step, ReasonReworkLimitExceeded,
				fmt.Sprintf("node %q exhausted its %d attempts", target, targetNode.EffectiveMaxAttempts(limits)),
				actorType, actorID)
		}
		_, err = e.activateNode(ctx, q, activateInput{
			Run:     run,
			Def:     def,
			Node:    targetNode,
			Attempt: nextAttempt,
			ReworkContext: map[string]any{
				"from_node": node.Key,
				"reason":    reason,
				"detail":    detail,
			},
			ActorType: actorType,
			ActorID:   actorID,
			Effects:   effects,
		})
		return err

	default: // FailurePolicyBlock
		return e.blockRun(ctx, q, run, step, reason, detail, actorType, actorID)
	}
}

// advanceToNext activates the successor of a passed node, or blocks the Run when
// the graph offers no legal move.
func (e *Engine) advanceToNext(
	ctx context.Context,
	q *db.Queries,
	run db.WorkflowRun,
	def *Definition,
	node *Node,
	effects *txEffects,
	actorType string,
	actorID pgtype.UUID,
) error {
	if def.SchemaVersion == GraphSchemaVersion {
		return e.advanceGraphV2(ctx, q, run, def, effects, actorType, actorID)
	}
	if len(node.Next) == 0 {
		// Only End legitimately has no successor, and End completes the Run in
		// completeAtEnd rather than arriving here.
		if node.Type == NodeTypeEnd {
			return nil
		}
		return newEngineError(ErrCodeInvariantViolation,
			fmt.Sprintf("node %q passed but has no successor", node.Key))
	}
	nextNode, ok := def.NodeByKey(node.Next[0])
	if !ok {
		return newEngineError(ErrCodeInvariantViolation,
			fmt.Sprintf("node %q points at %q which is absent from the pinned version", node.Key, node.Next[0]))
	}
	attempt, err := e.nextAttemptFor(ctx, q, run, nextNode.Key)
	if err != nil {
		return err
	}
	_, err = e.activateNode(ctx, q, activateInput{
		Run:       run,
		Def:       def,
		Node:      nextNode,
		Attempt:   attempt,
		ActorType: actorType,
		ActorID:   actorID,
		Effects:   effects,
	})
	return err
}

// nextAttemptFor returns the attempt number a new activation of node should use.
func (e *Engine) nextAttemptFor(ctx context.Context, q *db.Queries, run db.WorkflowRun, nodeKey string) (int32, error) {
	latest, err := q.GetLatestWorkflowStepAttempt(ctx, db.GetLatestWorkflowStepAttemptParams{
		RunID:   run.ID,
		NodeKey: nodeKey,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 1, nil
		}
		return 0, fmt.Errorf("get latest attempt for %q: %w", nodeKey, err)
	}
	return latest.Attempt + 1, nil
}

// reworkRoundLimitReached enforces the run-wide rewind budget. The caller holds
// the Run row lock (via lockStepAndRun), so counting the durable rework-root
// Steps and creating the next one in the same transaction is serialized across
// concurrent submissions and reviewers.
func (e *Engine) reworkRoundLimitReached(
	ctx context.Context,
	q *db.Queries,
	run db.WorkflowRun,
	limits Limits,
) (bool, error) {
	used, err := q.CountWorkflowReworkRounds(ctx, db.CountWorkflowReworkRoundsParams{
		RunID:       run.ID,
		WorkspaceID: run.WorkspaceID,
	})
	if err != nil {
		return false, fmt.Errorf("count rework rounds: %w", err)
	}
	return limits.MaxReworkRounds > 0 && used >= int64(limits.MaxReworkRounds), nil
}

// lockStepAndRun loads identity only, taking row
// locks in the fixed order Run-then-Step so concurrent commands on the same Run
// serialize rather than deadlock.
func (e *Engine) lockStepAndRun(ctx context.Context, q *db.Queries, workspaceID, stepID pgtype.UUID) (db.WorkflowStepInstance, db.WorkflowRun, error) {
	// Read the step unlocked first, only to learn its run id.
	probe, err := q.GetWorkflowStepInstance(ctx, db.GetWorkflowStepInstanceParams{
		ID:          stepID,
		WorkspaceID: workspaceID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return db.WorkflowStepInstance{}, db.WorkflowRun{}, newEngineError(ErrCodeNotFound, "step not found in this workspace")
		}
		return db.WorkflowStepInstance{}, db.WorkflowRun{}, fmt.Errorf("probe step: %w", err)
	}

	run, err := e.lockRun(ctx, q, db.GetWorkflowRunForUpdateParams{
		ID:          probe.RunID,
		WorkspaceID: workspaceID,
	})
	if err != nil {
		return db.WorkflowStepInstance{}, db.WorkflowRun{}, fmt.Errorf("lock run: %w", err)
	}

	step, err := q.GetWorkflowStepInstanceForUpdate(ctx, db.GetWorkflowStepInstanceForUpdateParams{
		ID:          stepID,
		WorkspaceID: workspaceID,
	})
	if err != nil {
		return db.WorkflowStepInstance{}, db.WorkflowRun{}, fmt.Errorf("lock step: %w", err)
	}

	return step, run, nil
}

// ---------------------------------------------------------------------------
// RecordTaskTerminal
// ---------------------------------------------------------------------------

// RecordTaskTerminalInput reports that an Agent Task reached a terminal state.
type RecordTaskTerminalInput struct {
	WorkspaceID pgtype.UUID
	TaskID      pgtype.UUID
	// TaskStatus is the task's terminal status: completed, failed, or cancelled.
	TaskStatus string
	// Result is the agent's output on completion.
	Result string
	// FailureReason is the taskfailure classification on failure.
	FailureReason string
	ErrorDetail   string
}

// OnAgentTaskTerminal is the flattened form of RecordTaskTerminal that
// TaskService's optional observer seam calls.
//
// It exists so the interface package service declares
// (service.WorkflowTaskTerminalObserver) can be satisfied without service naming
// this package's input struct. Package service already imports package workflow
// for the built-in templates, so a typed parameter would compile - but then the
// seam would carry a workflow type and "TaskService does not depend on the
// workflow engine" would be true only by accident. Keeping the signature to a
// UUID and strings makes it structural: the engine satisfies the interface, and
// nothing in package service names the engine.
//
// No workspace parameter: the workspace is derived from the Step row this looks up
// by task id, and that row is the authority. Taking one from the caller would
// either duplicate it or, if it disagreed, be wrong.
//
// A task with no owning Step returns nil. RecordTaskTerminal treats that as the
// common legacy case, which is what makes wiring this observer safe for every
// existing task path.
func (e *Engine) OnAgentTaskTerminal(ctx context.Context, taskID pgtype.UUID, taskStatus, result, failureReason, errorDetail string) error {
	return e.RecordTaskTerminal(ctx, RecordTaskTerminalInput{
		TaskID:        taskID,
		TaskStatus:    taskStatus,
		Result:        result,
		FailureReason: failureReason,
		ErrorDetail:   errorDetail,
	})
}

// RecordTaskTerminal is the hook TaskService calls (after its own commit) when a
// workflow-created task finishes.
//
// TaskService stays canonical for Task terminal state (plan section 7); this
// command only translates that into workflow state. A completed task with valid
// output becomes a Submission; a completed task with unparseable output blocks
// with submission_contract_invalid; a failed task goes through the node's failure
// policy carrying the taskfailure classification.
func (e *Engine) RecordTaskTerminal(ctx context.Context, in RecordTaskTerminalInput) error {
	step, err := e.Queries.GetWorkflowStepInstanceByTask(ctx, in.TaskID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Not a workflow task. Every legacy path lands here, so this must be
			// a cheap, silent no-op.
			return nil
		}
		return fmt.Errorf("find step for task: %w", err)
	}

	switch in.TaskStatus {
	case "completed":
		_, err := e.SubmitResult(ctx, SubmitResultInput{
			WorkspaceID: step.WorkspaceID,
			StepID:      step.ID,
			RawOutput:   in.Result,
			ActorType:   "agent",
			ActorID:     step.AgentID,
		})
		// A replayed terminal event is expected (at-least-once delivery); the
		// fence already rejected it, so do not escalate.
		if err != nil && (IsIdempotencyConflict(err) || IsInvalidTransition(err)) {
			return nil
		}
		return err

	case "failed", "cancelled":
		return e.recordTaskFailure(ctx, step, in)

	default:
		return nil
	}
}

func (e *Engine) recordTaskFailure(ctx context.Context, step db.WorkflowStepInstance, in RecordTaskTerminalInput) error {
	effects := &txEffects{}
	err := e.runInTx(ctx, effects, func(ctx context.Context, q *db.Queries) error {
		locked, run, err := e.lockStepAndRun(ctx, q, step.WorkspaceID, step.ID)
		if err != nil {
			return err
		}
		if IsTerminalRunStatus(RunStatus(run.Status)) || IsTerminalStepStatus(StepStatus(locked.Status)) {
			return nil // already consumed
		}

		def, err := ResolveRunDefinition(ctx, q, step.WorkspaceID, run)
		if err != nil {
			return err
		}
		node, ok := def.NodeByKey(locked.NodeKey)
		if !ok {
			return newEngineError(ErrCodeInvariantViolation,
				fmt.Sprintf("step references node %q absent from its pinned version", locked.NodeKey))
		}

		reason := in.FailureReason
		if reason == "" {
			reason = ReasonAgentTaskFailed
		}
		if in.TaskStatus == "cancelled" {
			reason = ReasonCancelled
		}

		failed, err := q.MarkWorkflowStepFailed(ctx, db.MarkWorkflowStepFailedParams{
			ID:            locked.ID,
			WorkspaceID:   run.WorkspaceID,
			FailureReason: reason,
			FailureDetail: textOrNull(in.ErrorDetail),
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			return fmt.Errorf("mark step failed: %w", err)
		}

		if err := e.recordEvent(ctx, q, eventSpec{
			WorkspaceID:    run.WorkspaceID,
			RunID:          run.ID,
			StepID:         locked.ID,
			Type:           EventStepFailed,
			IdempotencyKey: stepEventKey(locked, "task-terminal"),
			ActorType:      "system",
			Payload:        mustJSON(map[string]any{"reason": reason, "task_status": in.TaskStatus}),
		}); err != nil {
			return err
		}

		if failed.ParentStepID.Valid {
			if err := e.progressJoinFromChild(ctx, q, run, def, failed, node, effects, "system", pgtype.UUID{}); err != nil {
				return err
			}
		} else if err := e.applyFailurePolicy(ctx, q, run, def, node, failed, reason, in.ErrorDetail, effects, "system", pgtype.UUID{}); err != nil {
			return err
		}
		effects.runChanged(run)
		return nil
	})
	if err != nil {
		if IsIdempotencyConflict(err) {
			return nil
		}
		return err
	}
	effects.flush(ctx, e.Notifier)
	return nil
}

// ---------------------------------------------------------------------------
// DecideAcceptance
// ---------------------------------------------------------------------------

// DecideAcceptanceInput records a reviewer's decision.
type DecideAcceptanceInput struct {
	WorkspaceID  pgtype.UUID
	AcceptanceID pgtype.UUID
	// Accept true accepts; false rejects and requires Reason plus a permitted
	// ReworkTarget.
	Accept         bool
	Reason         string
	ReworkTarget   string
	ReviewerUserID pgtype.UUID
}

// DecideAcceptance applies a human accept/reject.
//
// Accepting advances past the acceptance node (typically into End, completing the
// Run). Rejecting creates a NEW attempt at a permitted target, preserving the
// prior attempt and its submissions, and injects the rejection reason so the
// Agent knows what to change.
func (e *Engine) DecideAcceptance(ctx context.Context, in DecideAcceptanceInput) (db.WorkflowAcceptance, error) {
	if !in.Accept && in.Reason == "" {
		return db.WorkflowAcceptance{}, newEngineError(ErrCodeAcceptanceConflict, "a rejection requires a reason")
	}

	var out db.WorkflowAcceptance
	effects := &txEffects{}
	deadlineExpired := false
	err := e.runInTx(ctx, effects, func(ctx context.Context, q *db.Queries) error {
		acceptance, err := q.GetWorkflowAcceptance(ctx, db.GetWorkflowAcceptanceParams{
			ID:          in.AcceptanceID,
			WorkspaceID: in.WorkspaceID,
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return newEngineError(ErrCodeNotFound, "acceptance not found in this workspace")
			}
			return fmt.Errorf("get acceptance: %w", err)
		}

		step, run, err := e.lockStepAndRun(ctx, q, in.WorkspaceID, acceptance.StepID)
		if err != nil {
			return err
		}

		if acceptance.Status != "pending" {
			return newEngineError(ErrCodeAcceptanceConflict,
				fmt.Sprintf("this acceptance was already %s", acceptance.Status))
		}

		if IsTerminalRunStatus(RunStatus(run.Status)) || IsTerminalStepStatus(StepStatus(step.Status)) {
			return newEngineError(ErrCodeInvalidTransition, "workflow is already terminal")
		}

		def, err := ResolveRunDefinition(ctx, q, in.WorkspaceID, run)
		if err != nil {
			return err
		}
		node, ok := def.NodeByKey(step.NodeKey)
		if !ok {
			return newEngineError(ErrCodeInvariantViolation, "acceptance step node is absent from the pinned version")
		}

		status := "accepted"
		if !in.Accept {
			status = "rejected"
			// The target must be one the graph permits: an arbitrary target would
			// let a reviewer reroute work in ways the author never validated.
			if in.ReworkTarget == "" && def.SchemaVersion != GraphSchemaVersion {
				return newEngineError(ErrCodeAcceptanceConflict, "a rejection requires a rework target")
			}
			if !node.AllowsReworkTo(in.ReworkTarget) && def.SchemaVersion != GraphSchemaVersion {
				return newEngineError(ErrCodeAcceptanceConflict,
					fmt.Sprintf("%q is not a permitted rework target for node %q", in.ReworkTarget, node.Key))
			}
		}

		decided, err := q.DecideWorkflowAcceptance(ctx, db.DecideWorkflowAcceptanceParams{
			ID:                  in.AcceptanceID,
			WorkspaceID:         in.WorkspaceID,
			Status:              status,
			ReviewerUserID:      in.ReviewerUserID,
			Reason:              textOrNull(in.Reason),
			ReworkTargetNodeKey: textOrNull(in.ReworkTarget),
			Context:             acceptance.Context,
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				// Lost the reviewer race: someone else decided first.
				return newEngineError(ErrCodeAcceptanceConflict, "another reviewer already decided this acceptance")
			}
			return fmt.Errorf("decide acceptance: %w", err)
		}
		out = decided

		if err := e.recordEvent(ctx, q, eventSpec{
			WorkspaceID:    run.WorkspaceID,
			RunID:          run.ID,
			StepID:         step.ID,
			Type:           EventAcceptanceDecided,
			IdempotencyKey: stepEventKey(step, "acceptance-"+status),
			ActorType:      "member",
			ActorID:        in.ReviewerUserID,
			Payload:        mustJSON(map[string]any{"status": status, "rework_target": in.ReworkTarget}),
		}); err != nil {
			return err
		}

		// The Run must be running again before any step transition below.
		if _, err := q.MarkWorkflowRunRunning(ctx, db.MarkWorkflowRunRunningParams{
			ID:          run.ID,
			WorkspaceID: run.WorkspaceID,
		}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("resume run: %w", err)
		}
		run.Status = string(RunRunning)

		if in.Accept {
			passed, err := q.MarkWorkflowStepPassed(ctx, db.MarkWorkflowStepPassedParams{
				ID:          step.ID,
				WorkspaceID: run.WorkspaceID,
			})
			if err != nil {
				return fmt.Errorf("pass acceptance step: %w", err)
			}
			if err := e.advanceFromStep(ctx, q, run, def, passed, node, effects, "member", in.ReviewerUserID); err != nil {
				return err
			}
			effects.runChanged(run)
			return nil
		}

		// Rejection: fail this attempt, then open a new attempt at the target.
		if _, err := q.MarkWorkflowStepFailed(ctx, db.MarkWorkflowStepFailedParams{
			ID:            step.ID,
			WorkspaceID:   run.WorkspaceID,
			FailureReason: ReasonAcceptanceRejected,
			FailureDetail: textOrNull(in.Reason),
		}); err != nil {
			return fmt.Errorf("fail rejected acceptance step: %w", err)
		}

		if def.SchemaVersion == GraphSchemaVersion {
			return e.advanceGraphV2(ctx, q, run, def, effects, "member", in.ReviewerUserID)
		}
		limits := e.limitsFor(run, def)
		exhausted, err := e.reworkRoundLimitReached(ctx, q, run, limits)
		if err != nil {
			return err
		}
		if exhausted {
			return e.blockRun(ctx, q, run, step, ReasonReworkLimitExceeded,
				fmt.Sprintf("run exhausted its %d rework rounds", limits.MaxReworkRounds),
				"member", in.ReviewerUserID)
		}

		targetNode, ok := def.NodeByKey(in.ReworkTarget)
		if !ok {
			return newEngineError(ErrCodeInvariantViolation, "rework target absent from the pinned version")
		}
		attempt, err := e.nextAttemptFor(ctx, q, run, in.ReworkTarget)
		if err != nil {
			return err
		}
		if attempt > int32(targetNode.EffectiveMaxAttempts(limits)) {
			return e.blockRun(ctx, q, run, step, ReasonReworkLimitExceeded,
				fmt.Sprintf("node %q exhausted its %d attempts", in.ReworkTarget, targetNode.EffectiveMaxAttempts(limits)),
				"member", in.ReviewerUserID)
		}
		if _, err := e.activateNode(ctx, q, activateInput{
			Run:     run,
			Def:     def,
			Node:    targetNode,
			Attempt: attempt,
			ReworkContext: map[string]any{
				"from_node":       node.Key,
				"reason":          in.Reason,
				"rejected_by":     uuidString(in.ReviewerUserID),
				"rejection_round": attempt,
			},
			ActorType: "member",
			ActorID:   in.ReviewerUserID,
			Effects:   effects,
		}); err != nil {
			return err
		}
		effects.runChanged(run)
		return nil
	})
	if err != nil {
		return db.WorkflowAcceptance{}, err
	}
	effects.flush(ctx, e.Notifier)
	if deadlineExpired {
		return out, newEngineError(ErrCodeInvalidTransition, "draft trial deadline exceeded")
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// CancelRun
// ---------------------------------------------------------------------------

// CancelRun cancels a Run and every non-terminal Step.
//
// Cascade is explicit application logic because the schema deliberately has no
// foreign keys or cascades (plan section 4). Agent Tasks are cancelled by the
// caller through TaskService, which remains canonical for task state.
func (e *Engine) CancelRun(ctx context.Context, workspaceID, runID pgtype.UUID, actorID pgtype.UUID) (db.WorkflowRun, error) {
	var out db.WorkflowRun
	effects := &txEffects{}
	err := e.runInTx(ctx, effects, func(ctx context.Context, q *db.Queries) error {
		var err error
		out, err = e.cancelRun(ctx, q, effects, workspaceID, runID, actorID)
		return err
	})
	if err != nil {
		return db.WorkflowRun{}, err
	}
	effects.flush(ctx, e.Notifier)
	return out, nil
}

// CancelRunInTx participates in the caller's transaction. The returned function
// must run only after that transaction commits; rollback discards all effects.
func (e *Engine) CancelRunInTx(ctx context.Context, tx pgx.Tx, workspaceID, runID, actorID pgtype.UUID) (db.WorkflowRun, func(context.Context), error) {
	effects := &txEffects{}
	txCtx := context.WithValue(ctx, txEffectsContextKey{}, effects)
	run, err := e.cancelRun(txCtx, e.Queries.WithTx(tx), effects, workspaceID, runID, actorID)
	return run, func(ctx context.Context) { effects.flush(ctx, e.Notifier) }, err
}

func (e *Engine) cancelRun(ctx context.Context, q *db.Queries, effects *txEffects, workspaceID, runID, actorID pgtype.UUID) (db.WorkflowRun, error) {
	var out db.WorkflowRun
	err := func() error {
		run, err := e.lockRun(ctx, q, db.GetWorkflowRunForUpdateParams{
			ID:          runID,
			WorkspaceID: workspaceID,
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return newEngineError(ErrCodeNotFound, "run not found in this workspace")
			}
			return fmt.Errorf("lock run: %w", err)
		}

		if IsTerminalRunStatus(RunStatus(run.Status)) {
			// Idempotent: cancelling an already-terminal Run is a no-op, not an
			// error, because a user clicking twice should not see a failure.
			out = run
			return nil
		}

		if _, err := q.CancelWorkflowStepInstancesForRun(ctx, db.CancelWorkflowStepInstancesForRunParams{
			RunID:       runID,
			WorkspaceID: workspaceID,
		}); err != nil {
			return fmt.Errorf("cancel steps: %w", err)
		}
		if _, err := q.CancelPendingWorkflowAcceptancesForRun(ctx, db.CancelPendingWorkflowAcceptancesForRunParams{
			RunID:       runID,
			WorkspaceID: workspaceID,
		}); err != nil {
			return fmt.Errorf("cancel acceptances: %w", err)
		}

		cancelled, err := q.CancelWorkflowRun(ctx, db.CancelWorkflowRunParams{
			ID:          runID,
			WorkspaceID: workspaceID,
		})
		if err != nil {
			return fmt.Errorf("cancel run: %w", err)
		}
		if err := e.recordEvent(ctx, q, eventSpec{
			WorkspaceID:    workspaceID,
			RunID:          runID,
			Type:           EventRunCancelled,
			IdempotencyKey: fmt.Sprintf("run:%s:cancelled", uuidString(runID)),
			ActorType:      "member",
			ActorID:        actorID,
		}); err != nil {
			return err
		}
		out = cancelled
		effects.runChanged(cancelled)
		return nil
	}()
	return out, err
}

func joinProblems(problems []string) string {
	out := ""
	for i, p := range problems {
		if i > 0 {
			out += "; "
		}
		out += p
	}
	return out
}
