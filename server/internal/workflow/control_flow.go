package workflow

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func (e *Engine) passControlStep(ctx context.Context, q *db.Queries, run db.WorkflowRun, step db.WorkflowStepInstance, output any) (db.WorkflowStepInstance, error) {
	passed, err := q.MarkWorkflowStepPassed(ctx, db.MarkWorkflowStepPassedParams{
		ID: step.ID, WorkspaceID: run.WorkspaceID, Output: mustJSON(output),
	})
	if err != nil {
		return db.WorkflowStepInstance{}, fmt.Errorf("pass %s step %q: %w", step.NodeType, step.NodeKey, err)
	}
	if err := e.recordEvent(ctx, q, eventSpec{
		WorkspaceID: run.WorkspaceID, RunID: run.ID, StepID: step.ID,
		Type: EventStepPassed, IdempotencyKey: stepEventKey(step, "passed"),
		ActorType: "system", Payload: mustJSON(output),
	}); err != nil {
		return db.WorkflowStepInstance{}, err
	}
	return passed, nil
}

func (e *Engine) executeCondition(ctx context.Context, q *db.Queries, in activateInput, step db.WorkflowStepInstance, upstream *upstreamSubmission) (db.WorkflowStepInstance, error) {
	verdict := ""
	if upstream != nil {
		verdict = upstream.Verdict
	}
	target := ""
	for _, branch := range in.Node.Branches {
		if branch.WhenVerdict == verdict {
			target = branch.Target
			break
		}
	}
	if target == "" {
		for _, branch := range in.Node.Branches {
			if branch.WhenVerdict == "" {
				target = branch.Target
				break
			}
		}
	}
	if target == "" {
		return e.blockControlStep(ctx, q, in, step, ReasonInvariantViolation,
			fmt.Sprintf("condition %q has no branch for verdict %q", in.Node.Key, verdict))
	}
	passed, err := e.passControlStep(ctx, q, in.Run, step, map[string]any{"verdict": verdict, "target": target})
	if err != nil {
		return db.WorkflowStepInstance{}, err
	}
	next, ok := in.Def.NodeByKey(target)
	if !ok {
		return db.WorkflowStepInstance{}, newEngineError(ErrCodeInvariantViolation, fmt.Sprintf("condition target %q is absent", target))
	}
	attempt, err := e.nextAttemptFor(ctx, q, in.Run, target)
	if err != nil {
		return db.WorkflowStepInstance{}, err
	}
	_, err = e.activateNode(ctx, q, activateInput{Run: in.Run, Def: in.Def, Node: next, Attempt: attempt, ActorType: in.ActorType, ActorID: in.ActorID, Effects: in.Effects})
	return passed, err
}

// Fan-out expands the latest upstream artifact's references. The Submission
// contract already makes references the durable list of work pointers. An
// ordinal plus content hash creates stable expansion keys even for duplicates.
// Expansion and task enqueue share the activation transaction: all children
// commit together or none do.
func (e *Engine) executeFanOut(ctx context.Context, q *db.Queries, in activateInput, step db.WorkflowStepInstance, upstream *upstreamSubmission) (db.WorkflowStepInstance, error) {
	var items []string
	if upstream != nil {
		items = upstream.Artifact.References
	}
	if len(items) == 0 {
		return e.blockControlStep(ctx, q, in, step, ReasonFanOutEmpty,
			"fan-out requires at least one upstream artifact reference")
	}
	limit := in.Node.FanOutMax
	if limit == 0 {
		limit = e.limitsFor(in.Run, in.Def).MaxFanOut
	}
	if limit > 0 && len(items) > limit {
		return e.blockControlStep(ctx, q, in, step, ReasonFanOutLimitExceeded,
			fmt.Sprintf("fan-out produced %d children, limit is %d", len(items), limit))
	}
	child, ok := in.Def.NodeByKey(in.Node.Next[0])
	if !ok {
		return db.WorkflowStepInstance{}, newEngineError(ErrCodeInvariantViolation, "fan-out child node is absent")
	}
	startAttempt, err := e.nextAttemptFor(ctx, q, in.Run, child.Key)
	if err != nil {
		return db.WorkflowStepInstance{}, err
	}
	for i, raw := range items {
		value := strings.TrimSpace(raw)
		sum := sha256.Sum256([]byte(value))
		key := fmt.Sprintf("%03d:%x", i+1, sum[:12])
		if _, err := e.activateNode(ctx, q, activateInput{
			Run: in.Run, Def: in.Def, Node: child, Attempt: startAttempt + int32(i),
			ParentStepID: step.ID, ExpansionKey: key, ExpansionValue: value,
			ActorType: in.ActorType, ActorID: in.ActorID, Effects: in.Effects,
		}); err != nil {
			return db.WorkflowStepInstance{}, err
		}
	}
	if e.Metrics != nil {
		e.Metrics.ObserveFanOut(len(items))
	}
	return e.passControlStep(ctx, q, in.Run, step, map[string]any{"children": len(items)})
}

func (e *Engine) blockControlStep(ctx context.Context, q *db.Queries, in activateInput, step db.WorkflowStepInstance, reason, detail string) (db.WorkflowStepInstance, error) {
	blocked, err := q.MarkWorkflowStepBlocked(ctx, db.MarkWorkflowStepBlockedParams{
		ID: step.ID, WorkspaceID: in.Run.WorkspaceID,
		FailureReason: reason, FailureDetail: textOrNull(detail),
	})
	if err != nil {
		return db.WorkflowStepInstance{}, err
	}
	if err := e.recordEvent(ctx, q, eventSpec{WorkspaceID: in.Run.WorkspaceID, RunID: in.Run.ID, StepID: step.ID, Type: EventStepBlocked, IdempotencyKey: stepEventKey(step, "blocked"), ActorType: "system", Payload: mustJSON(map[string]any{"reason": reason})}); err != nil {
		return db.WorkflowStepInstance{}, err
	}
	if err := e.blockRun(ctx, q, in.Run, blocked, reason, detail, in.ActorType, in.ActorID); err != nil {
		return db.WorkflowStepInstance{}, err
	}
	return blocked, nil
}

// progressJoinFromChild activates the AND Join only after every durable sibling
// is terminal. Run-row locking serializes sibling completions; the unique Step
// attempt fence makes the final activation replay-safe across restarts.
func (e *Engine) progressJoinFromChild(ctx context.Context, q *db.Queries, run db.WorkflowRun, def *Definition, childStep db.WorkflowStepInstance, childNode *Node, effects *txEffects, actorType string, actorID pgtype.UUID) error {
	if len(childNode.Next) != 1 {
		return newEngineError(ErrCodeInvariantViolation, fmt.Sprintf("fan-out child %q must point to one join", childNode.Key))
	}
	join, ok := def.NodeByKey(childNode.Next[0])
	if !ok || join.Type != NodeTypeJoin {
		return newEngineError(ErrCodeInvariantViolation, fmt.Sprintf("fan-out child %q does not point to a join", childNode.Key))
	}
	children, err := q.ListWorkflowStepChildren(ctx, db.ListWorkflowStepChildrenParams{
		ParentStepID: childStep.ParentStepID, WorkspaceID: run.WorkspaceID,
	})
	if err != nil {
		return fmt.Errorf("list fan-out children: %w", err)
	}
	for _, child := range children {
		if !IsTerminalStepStatus(StepStatus(child.Status)) {
			return nil
		}
	}
	attempt, err := e.nextAttemptFor(ctx, q, run, join.Key)
	if err != nil {
		return err
	}
	_, err = e.activateNode(ctx, q, activateInput{
		Run: run, Def: def, Node: join, Attempt: attempt,
		ParentStepID: childStep.ParentStepID,
		ActorType:    actorType, ActorID: actorID, Effects: effects,
	})
	if IsIdempotencyConflict(err) {
		return nil
	}
	return err
}

func (e *Engine) executeJoin(ctx context.Context, q *db.Queries, in activateInput, step db.WorkflowStepInstance) (db.WorkflowStepInstance, error) {
	if !step.ParentStepID.Valid {
		return e.blockControlStep(ctx, q, in, step, ReasonInvariantViolation, "join has no fan-out parent")
	}
	children, err := q.ListWorkflowStepChildren(ctx, db.ListWorkflowStepChildrenParams{
		ParentStepID: step.ParentStepID, WorkspaceID: in.Run.WorkspaceID,
	})
	if err != nil {
		return db.WorkflowStepInstance{}, fmt.Errorf("list join children: %w", err)
	}
	allowed := make(map[string]bool, len(in.Node.JoinSources))
	for _, source := range in.Node.JoinSources {
		allowed[source] = true
	}
	matched, failed := 0, 0
	for _, child := range children {
		if !allowed[child.NodeKey] {
			continue
		}
		matched++
		if !IsTerminalStepStatus(StepStatus(child.Status)) {
			return db.WorkflowStepInstance{}, newEngineError(ErrCodeInvalidTransition, "join activated before every child was terminal")
		}
		if child.Status == string(StepFailed) || child.Status == string(StepBlocked) || child.Status == string(StepCancelled) {
			failed++
		}
	}
	if matched == 0 {
		return e.blockControlStep(ctx, q, in, step, ReasonInvariantViolation, "join matched no declared source children")
	}
	policy := in.Node.JoinPolicy
	if policy == "" {
		policy = JoinPolicyFailFast
	}
	if failed == 0 || policy == JoinPolicyContinue {
		passed, err := e.passControlStep(ctx, q, in.Run, step, map[string]any{"children": matched, "failed": failed, "policy": policy})
		if err != nil {
			return db.WorkflowStepInstance{}, err
		}
		if err := e.advanceToNext(ctx, q, in.Run, in.Def, in.Node, in.Effects, in.ActorType, in.ActorID); err != nil {
			return db.WorkflowStepInstance{}, err
		}
		return passed, nil
	}
	detail := fmt.Sprintf("%d of %d fan-out children did not pass", failed, matched)
	if policy == JoinPolicyPause {
		return e.blockControlStep(ctx, q, in, step, ReasonJoinChildFailed, detail)
	}
	failedStep, err := q.MarkWorkflowStepFailed(ctx, db.MarkWorkflowStepFailedParams{
		ID: step.ID, WorkspaceID: in.Run.WorkspaceID,
		FailureReason: ReasonJoinChildFailed, FailureDetail: textOrNull(detail),
	})
	if err != nil {
		return db.WorkflowStepInstance{}, err
	}
	if err := e.recordEvent(ctx, q, eventSpec{WorkspaceID: in.Run.WorkspaceID, RunID: in.Run.ID, StepID: step.ID, Type: EventStepFailed, IdempotencyKey: stepEventKey(step, "failed"), ActorType: "system", Payload: mustJSON(map[string]any{"reason": ReasonJoinChildFailed})}); err != nil {
		return db.WorkflowStepInstance{}, err
	}
	if policy == JoinPolicyRework {
		copyNode := *in.Node
		copyNode.OnFailure = FailurePolicyRework
		if err := e.applyFailurePolicy(ctx, q, in.Run, in.Def, &copyNode, failedStep,
			ReasonJoinChildFailed, detail, in.Effects, in.ActorType, in.ActorID); err != nil {
			return db.WorkflowStepInstance{}, err
		}
		return failedStep, nil
	}
	if _, err := q.FailWorkflowRun(ctx, db.FailWorkflowRunParams{
		ID: in.Run.ID, WorkspaceID: in.Run.WorkspaceID,
		FailureReason: ReasonJoinChildFailed, FailureDetail: textOrNull(detail),
	}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return db.WorkflowStepInstance{}, err
	}
	if err := e.recordEvent(ctx, q, eventSpec{
		WorkspaceID: in.Run.WorkspaceID, RunID: in.Run.ID, StepID: step.ID,
		Type: EventRunFailed, IdempotencyKey: stepEventKey(step, "run-failed"), ActorType: "system",
		Payload: mustJSON(map[string]any{"reason": ReasonJoinChildFailed}),
	}); err != nil {
		return db.WorkflowStepInstance{}, err
	}
	return failedStep, nil
}
