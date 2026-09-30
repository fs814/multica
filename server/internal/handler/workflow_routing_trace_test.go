package handler

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/multica-ai/multica/server/internal/workflow"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Capture the routing inputs inside the activation transaction. A query after
// completion cannot reconstruct which attempts the router actually observed.
// Keep this test-only and omit prompts, inputs, credentials, and runtime config.
type workflowRoutingTrace struct {
	t      *testing.T
	router workflow.Router
}

func (r workflowRoutingTrace) Route(ctx context.Context, q *db.Queries, req workflow.RouteRequest) (workflow.RouteResult, error) {
	steps, err := q.ListWorkflowStepInstances(ctx, db.ListWorkflowStepInstancesParams{
		RunID: req.Run.ID, WorkspaceID: req.WorkspaceID,
	})
	if err != nil {
		r.t.Fatalf("routing trace: list transaction steps: %v", err)
	}
	type stepTrace struct {
		ID       string `json:"id"`
		Node     string `json:"node"`
		Attempt  int32  `json:"attempt"`
		Position int64  `json:"position"`
		Status   string `json:"status"`
		Agent    string `json:"agent"`
	}
	trace := make([]stepTrace, 0, len(steps))
	for _, step := range steps {
		trace = append(trace, stepTrace{uuidToString(step.ID), step.NodeKey, step.Attempt,
			step.TracePosition, step.Status, uuidToString(step.AgentID)})
	}
	prior := make(map[string]string, len(req.PriorAgentByNode))
	for node, agent := range req.PriorAgentByNode {
		prior[node] = uuidToString(agent)
	}
	pinned, err := workflow.ResolveRunDefinition(ctx, q, req.WorkspaceID, req.Run)
	if err != nil {
		r.t.Fatalf("routing trace: resolve pinned definition: %v", err)
	}
	pinnedNode, ok := pinned.NodeByKey(req.Node.Key)
	if !ok {
		r.t.Fatalf("routing trace: pinned definition has no node %q", req.Node.Key)
	}
	result, routeErr := r.router.Route(ctx, q, req)
	detail := ""
	if routeErr != nil {
		detail = routeErr.Error()
	}
	raw, err := json.Marshal(map[string]any{
		"workspace": uuidToString(req.WorkspaceID), "run": uuidToString(req.Run.ID),
		"template_version": uuidToString(req.Run.TemplateVersionID),
		"node":             req.Node.Key, "routing": req.Node.Routing, "pinned_routing": pinnedNode.Routing,
		"prior_agents": prior, "transaction_steps": trace,
		"agent": uuidToString(result.AgentID), "reason": result.Reason, "error": detail,
	})
	if err != nil {
		r.t.Fatal(err)
	}
	r.t.Logf("routing transaction: %s", raw)
	return result, routeErr
}
