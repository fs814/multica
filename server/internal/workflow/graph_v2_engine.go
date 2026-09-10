package workflow

import (
	"context"
	"fmt"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// driveGraphV2 is called only within the existing run transaction/row lock.
// Re-read after every decision: synchronous nodes can resolve branch activation,
// and a terminal callback must never enqueue a second attempt for passed work.
func (e *Engine) driveGraphV2(ctx context.Context, q *db.Queries, in activateInput) (db.WorkflowStepInstance, error) {
	var last db.WorkflowStepInstance
	for {
		run, err := q.GetWorkflowRun(ctx, db.GetWorkflowRunParams{ID: in.Run.ID, WorkspaceID: in.Run.WorkspaceID})
		if err != nil {
			return last, err
		}
		if IsTerminalRunStatus(RunStatus(run.Status)) {
			return last, nil
		}
		in.Run = run
		rows, err := q.ListWorkflowStepInstances(ctx, db.ListWorkflowStepInstancesParams{RunID: run.ID, WorkspaceID: run.WorkspaceID})
		if err != nil {
			return last, err
		}
		steps := map[string]GraphStep{}
		for _, row := range rows {
			old, ok := steps[row.NodeKey]
			if ok && old.Attempt >= row.Attempt {
				continue
			}
			input := graphOutput(row.Input)
			reason, _ := input["skip_reason"].(string)
			steps[row.NodeKey] = GraphStep{Status: row.Status, Attempt: row.Attempt, Output: graphOutput(row.Output), SkipReason: reason}
		}
		decisions := PlanGraphV2(in.Def, steps)
		if len(decisions) == 0 {
			failed, ended := false, false
			for _, node := range in.Def.Nodes {
				s, ok := steps[node.Key]
				if !ok || !graphTerminal(s.Status) {
					return last, nil
				}
				if s.Status == "failed" || s.Status == "blocked" || s.Status == "cancelled" {
					failed = true
				}
				if node.Type == NodeTypeEnd && s.Status == "passed" {
					ended = true
				}
			}
			eventType := EventRunCompleted
			var changed db.WorkflowRun
			if failed || !ended {
				changed, err = q.FailWorkflowRun(ctx, db.FailWorkflowRunParams{ID: run.ID, WorkspaceID: run.WorkspaceID, FailureReason: "dependency_failed", FailureDetail: textOrNull("workflow contains a final failure")})
				eventType = EventRunFailed
			} else {
				// An independent acceptance gate may have temporarily parked the run.
				if run.Status != string(RunRunning) {
					if _, err = q.MarkWorkflowRunRunning(ctx, db.MarkWorkflowRunRunningParams{ID: run.ID, WorkspaceID: run.WorkspaceID}); err != nil {
						return last, err
					}
				}
				changed, err = q.CompleteWorkflowRun(ctx, db.CompleteWorkflowRunParams{ID: run.ID, WorkspaceID: run.WorkspaceID})
			}
			if err != nil {
				return last, err
			}
			if in.Effects != nil {
				in.Effects.runChanged(changed)
			}
			return last, e.recordEvent(ctx, q, eventSpec{WorkspaceID: run.WorkspaceID, RunID: run.ID, Type: eventType, IdempotencyKey: "graph-v2:" + uuidString(run.ID) + ":terminal", ActorType: "system"})
		}
		decision := decisions[0]
		node, ok := in.Def.NodeByKey(decision.Node)
		if !ok {
			return last, fmt.Errorf("missing planned node %q", decision.Node)
		}
		next := in
		next.Node = node
		next.Attempt = decision.Attempt
		next.GraphManaged = true
		next.GraphDecision = decision
		last, err = e.activateNode(ctx, q, next)
		if err != nil {
			return last, err
		}
	}
}

func (e *Engine) executeGraphControl(ctx context.Context, q *db.Queries, in activateInput, step db.WorkflowStepInstance) (db.WorkflowStepInstance, error) {
	if in.GraphDecision.Kind == "skip" {
		skipped, err := q.MarkWorkflowStepSkipped(ctx, db.MarkWorkflowStepSkippedParams{ID: step.ID, WorkspaceID: in.Run.WorkspaceID})
		if err != nil {
			return step, err
		}
		return skipped, e.recordEvent(ctx, q, eventSpec{WorkspaceID: in.Run.WorkspaceID, RunID: in.Run.ID, StepID: step.ID, Type: EventStepSkipped, IdempotencyKey: stepEventKey(step, "skipped"), ActorType: "system", Payload: mustJSON(map[string]any{"reason": in.GraphDecision.Reason})})
	}
	if in.GraphDecision.Kind == "fail" {
		failed, err := q.MarkWorkflowStepFailed(ctx, db.MarkWorkflowStepFailedParams{ID: step.ID, WorkspaceID: in.Run.WorkspaceID, FailureReason: "required_input_missing", FailureDetail: textOrNull(in.GraphDecision.Reason)})
		if err != nil {
			return step, err
		}
		return failed, e.recordEvent(ctx, q, eventSpec{WorkspaceID: in.Run.WorkspaceID, RunID: in.Run.ID, StepID: step.ID, Type: EventStepFailed, IdempotencyKey: stepEventKey(step, "failed"), ActorType: "system", Payload: mustJSON(map[string]any{"reason": in.GraphDecision.Reason})})
	}
	output := in.GraphDecision.Inputs
	if output == nil {
		output = map[string]any{}
	}
	if in.Node.Type == NodeTypeInput {
		output = graphOutput(in.Run.Input)
	}
	if in.Node.Type == NodeTypeCondition {
		verdict := "pass"
		if v, ok := output["verdict"].(string); ok {
			verdict = v
		}
		target := selectGraphBranch(in.Node, output, verdict)
		if target == "" {
			return q.MarkWorkflowStepFailed(ctx, db.MarkWorkflowStepFailedParams{ID: step.ID, WorkspaceID: in.Run.WorkspaceID, FailureReason: "condition_no_match"})
		}
		output = map[string]any{"target": target, "verdict": verdict}
	}
	return e.passControlStep(ctx, q, in.Run, step, output)
}

func (e *Engine) advanceGraphV2(ctx context.Context, q *db.Queries, run db.WorkflowRun, def *Definition, effects *txEffects, actorType string, actorID pgtype.UUID) error {
	_, err := e.driveGraphV2(ctx, q, activateInput{Run: run, Def: def, Effects: effects, ActorType: actorType, ActorID: actorID})
	return err
}
