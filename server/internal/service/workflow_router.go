package service

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	obsmetrics "github.com/multica-ai/multica/server/internal/metrics"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/internal/workflow"
	db "github.com/multica-ai/multica/server/pkg/db/generated"

	"github.com/multica-ai/multica/server/pkg/scriptpipeline"
)

// WorkflowRouter chooses the Agent that runs an Agent node.
//
// It satisfies workflow.Router and lives in package service - not package
// workflow - so it can call AgentReadiness and AgentInvokePermitted directly.
// Package workflow has no dependency on package service (service imports
// workflow for the builtin templates), and the router needs both of those gates,
// so this is the only side of the boundary it can live on.
//
// Strategy order, from the node's Routing block:
//
//	explicit       -> the pinned agent id
//	previous_step  -> whoever ran Routing.FromNode on this Run
//	capability     -> agents labelled with Routing.Capability
//	<fallback>     -> Routing.FallbackAgentID, tried only after the primary
//	                  strategy produced no eligible candidate
//
// EVERY candidate, from every strategy including an explicitly pinned one, must
// clear two gates:
//
//	readiness  - not archived, a runtime is bound, and that runtime is online
//	permission - the Run's accountable human may invoke this agent
//
// No eligible candidate returns an ERROR, which the engine turns into a blocked
// Step and a blocked Run with routing_no_candidate (see dispatchAgentStep). That
// is the designed path, and it is deliberately not "pick some other agent": a
// workflow that quietly runs a general-purpose agent where the graph asked for a
// specialist produces plausible-looking wrong work, and a human reading the Run
// has no signal that anything was substituted. A Run that stops and names the
// missing capability is recoverable; one that silently used the wrong agent is
// not.
type WorkflowRouter struct {
	// Queries is unused during routing - Route receives the caller's
	// transaction-scoped *db.Queries and must use that one, so permission and
	// readiness are read inside the same transaction that is activating the Step.
	// The field exists so the router can be constructed alongside every other
	// service and to keep the door open for lookups that are legitimately
	// outside the activation transaction.
	Queries *db.Queries
}

// NewWorkflowRouter builds the router wired into the engine in
// cmd/server/router.go.
func NewWorkflowRouter(q *db.Queries) *WorkflowRouter {
	return &WorkflowRouter{Queries: q}
}

// Compile-time proof the router still satisfies the engine's seam. Without this
// a signature drift in workflow.Router would only surface at wiring time in
// cmd/server, far from the change that caused it.
var _ workflow.Router = (*WorkflowRouter)(nil)

// Route implements workflow.Router.
func (r *WorkflowRouter) Route(ctx context.Context, q *db.Queries, req workflow.RouteRequest) (workflow.RouteResult, error) {
	if q == nil {
		return workflow.RouteResult{}, fmt.Errorf("workflow routing requires a database handle")
	}
	if req.Node == nil {
		return workflow.RouteResult{}, fmt.Errorf("workflow routing requires a node")
	}
	routing := req.Node.Routing
	if routing == nil {
		// Validate rejects an Agent node with no routing block at publish time, so
		// reaching here means a hand-written or tampered definition row.
		return workflow.RouteResult{}, fmt.Errorf("node %q has no routing configuration", req.Node.Key)
	}

	actor := invokeActorForRun(req.WorkspaceID, req.AccountableUserID)

	// rejections accumulates why each near-miss candidate was refused. It is the
	// difference between "routing_no_candidate" and an operator knowing that the
	// right agent exists but its runtime is offline, so it is carried into the
	// returned error and thence onto the Step's failure_detail.
	var rejections []string

	switch routing.Strategy {
	case workflow.RoutingExplicit:
		result, err := r.routeExplicit(ctx, q, req, actor, routing.AgentID, "explicit", req.RequiresVision)
		if err == nil {
			return result, nil
		}
		rejections = append(rejections, err.Error())

	case workflow.RoutingPreviousStep:
		if routing.FromNode == "" {
			return workflow.RouteResult{}, fmt.Errorf("node %q uses previous_step routing with no from_node", req.Node.Key)
		}
		priorAgent, ok := req.PriorAgentByNode[routing.FromNode]
		if !ok || !priorAgent.Valid {
			rejections = append(rejections, fmt.Sprintf("no agent has run node %q on this run yet", routing.FromNode))
		} else {
			result, err := r.routeExplicit(ctx, q, req, actor, util.UUIDToString(priorAgent),
				"previous_step:"+routing.FromNode, req.RequiresVision)
			if err == nil {
				return result, nil
			}
			rejections = append(rejections, err.Error())
		}

	case workflow.RoutingCapability:
		if strings.TrimSpace(routing.Capability) == "" {
			return workflow.RouteResult{}, fmt.Errorf("node %q uses capability routing with no capability", req.Node.Key)
		}
		result, reasons, err := r.routeCapability(ctx, q, req, actor, routing.Capability)
		if err == nil {
			return result, nil
		}
		rejections = append(rejections, reasons...)
		rejections = append(rejections, err.Error())

	default:
		return workflow.RouteResult{}, fmt.Errorf("node %q uses unknown routing strategy %q", req.Node.Key, routing.Strategy)
	}

	// The fallback is the last resort for EVERY strategy, and it is gated exactly
	// like a primary candidate. A fallback that is archived, offline, or not
	// invocable by this Run's human is not a fallback.
	if routing.FallbackAgentID != "" {
		result, err := r.routeExplicit(ctx, q, req, actor, routing.FallbackAgentID, "fallback", req.RequiresVision)
		if err == nil {
			return result, nil
		}
		rejections = append(rejections, err.Error())
	}

	return workflow.RouteResult{}, fmt.Errorf("no eligible agent for node %q (%s routing): %s",
		req.Node.Key, routing.Strategy, strings.Join(rejections, "; "))
}

// routeExplicit resolves one named agent and runs it through both gates. Shared
// by the explicit, previous_step, and fallback strategies because "check this
// specific agent" is the same operation in all three; only the reason label
// differs.
func (r *WorkflowRouter) routeExplicit(
	ctx context.Context,
	q *db.Queries,
	req workflow.RouteRequest,
	actor InvokeActor,
	agentID string,
	reasonPrefix string,
	requiresVision bool,
) (workflow.RouteResult, error) {
	agentUUID, err := util.ParseUUID(agentID)
	if err != nil {
		return workflow.RouteResult{}, fmt.Errorf("%s agent id %q is malformed", reasonPrefix, agentID)
	}
	// GetAgentInWorkspace, not GetAgent: a pinned or inherited agent id from
	// another workspace must resolve to nothing rather than route across the
	// tenant boundary.
	agent, err := q.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{
		ID:          agentUUID,
		WorkspaceID: req.WorkspaceID,
	})
	if err != nil {
		return workflow.RouteResult{}, fmt.Errorf("%s agent %s not found in this workspace", reasonPrefix, agentID)
	}
	if reason, ok := r.eligible(ctx, q, agent, actor, requiresVision, req.Run.ExecutionMode == workflow.ExecutionDraftTest, req.ScriptPipeline); !ok {
		return workflow.RouteResult{}, fmt.Errorf("%s agent %q is not eligible: %s", reasonPrefix, agent.Name, reason)
	}
	return workflow.RouteResult{
		AgentID:   agent.ID,
		RuntimeID: agent.RuntimeID,
		Reason:    fmt.Sprintf("%s -> %s", reasonPrefix, agent.Name),
	}, nil
}

// routeCapability matches agents whose agent-labels include the capability.
//
// Labels are the capability vocabulary (product decision): an agent advertises
// "bug_analysis" by carrying a label with that name, resource_type='agent'. This
// keeps capabilities editable by users in the UI they already use for labels,
// instead of introducing a parallel taxonomy only workflows understand.
//
// Matching is case-insensitive because label names are user-typed free text and
// "Bug_Analysis" vs "bug_analysis" is not a distinction anyone intends.
//
// The returned []string carries per-candidate rejection reasons, so a run blocked
// with routing_no_candidate can say "agent X has the label but its runtime is
// offline" rather than just "nobody matched".
func (r *WorkflowRouter) routeCapability(
	ctx context.Context,
	q *db.Queries,
	req workflow.RouteRequest,
	actor InvokeActor,
	capability string,
) (workflow.RouteResult, []string, error) {
	// ListAllAgents (not ListAgents) so an archived agent is visible here and
	// reported as archived, rather than being invisible and indistinguishable
	// from "the label was never applied". The readiness gate still refuses it.
	agents, err := q.ListAllAgents(ctx, req.WorkspaceID)
	if err != nil {
		return workflow.RouteResult{}, nil, fmt.Errorf("list agents for capability %q: %w", capability, err)
	}
	if len(agents) == 0 {
		return workflow.RouteResult{}, nil, fmt.Errorf("workspace has no agents to satisfy capability %q", capability)
	}

	agentIDs := make([]pgtype.UUID, 0, len(agents))
	for _, a := range agents {
		agentIDs = append(agentIDs, a.ID)
	}
	// Batch: one query for every agent's labels, not one per agent. The reverse
	// index on label_id (migration 169) is what makes this cheap.
	labelRows, err := q.ListLabelsForAgents(ctx, db.ListLabelsForAgentsParams{
		AgentIds:    agentIDs,
		WorkspaceID: req.WorkspaceID,
	})
	if err != nil {
		return workflow.RouteResult{}, nil, fmt.Errorf("list agent labels for capability %q: %w", capability, err)
	}
	labelled := make(map[string]struct{}, len(agents))
	want := strings.ToLower(strings.TrimSpace(capability))
	for _, row := range labelRows {
		if strings.ToLower(strings.TrimSpace(row.Name)) == want {
			labelled[util.UUIDToString(row.AgentID)] = struct{}{}
		}
	}
	if len(labelled) == 0 {
		return workflow.RouteResult{}, nil, fmt.Errorf(
			"no agent in this workspace carries the %q capability label", capability)
	}

	candidates := make([]db.Agent, 0, len(labelled))
	for _, a := range agents {
		if _, ok := labelled[util.UUIDToString(a.ID)]; ok {
			candidates = append(candidates, a)
		}
	}

	// Deterministic order: name, then id as the tiebreak for two agents sharing a
	// name. Determinism matters because a replay of the same command must select
	// the same agent - otherwise the same Run, replayed after a crash, routes
	// somewhere else and the audit trail stops being reproducible.
	sort.Slice(candidates, func(i, j int) bool {
		li, lj := strings.ToLower(candidates[i].Name), strings.ToLower(candidates[j].Name)
		if li != lj {
			return li < lj
		}
		return util.UUIDToString(candidates[i].ID) < util.UUIDToString(candidates[j].ID)
	})

	var rejections []string
	for _, agent := range candidates {
		reason, ok := r.eligible(ctx, q, agent, actor, req.RequiresVision, req.Run.ExecutionMode == workflow.ExecutionDraftTest, req.ScriptPipeline)
		if ok {
			return workflow.RouteResult{
				AgentID:   agent.ID,
				RuntimeID: agent.RuntimeID,
				Reason:    fmt.Sprintf("capability:%s -> %s", capability, agent.Name),
			}, rejections, nil
		}
		rejections = append(rejections, fmt.Sprintf("%s: %s", agent.Name, reason))
	}

	return workflow.RouteResult{}, rejections, fmt.Errorf(
		"every agent carrying the %q capability label is ineligible", capability)
}

// eligible applies both candidate gates and returns the operator-readable reason
// the candidate was refused.
//
// Order is deliberate: readiness first, because "runtime offline" is the common,
// transient, self-healing case and reporting it is more useful than reporting a
// permission problem the operator would then chase for an agent that could not
// have run anyway.
func (r *WorkflowRouter) eligible(ctx context.Context, q *db.Queries, agent db.Agent, actor InvokeActor, requiresVision, requiresDebug bool, scripts ...*scriptpipeline.Config) (string, bool) {
	// Upstream reshaped AgentReadiness to take a RuntimeLookup (so each
	// admission path is distinguishable in multica_agent_runtime_lookup_total)
	// and to return a verdict struct. The lookup MUST carry the caller's
	// transaction-scoped `q`, not r.Queries, so the readiness read sees the
	// same snapshot as the rest of the activating transaction — see the comment
	// on WorkflowRouter.Queries. There is no workflow-specific source label
	// yet, so routing reads are attributed to "other".
	verdict, err := AgentReadiness(ctx, RuntimeLookup{Queries: q, Source: obsmetrics.RuntimeLookupSourceOther}, agent)
	if err != nil {
		// A runtime lookup failure is NOT "ready". Routing decides who gets
		// credentials and work; a candidate we could not verify is refused, and
		// the Run blocks with a reason a human can act on.
		return "could not verify agent runtime: " + err.Error(), false
	}
	if !verdict.Ready() {
		return verdict.Detail, false
	}
	if !AgentInvokePermitted(ctx, q, agent, actor) {
		// Same wording for "private and not yours" and "not on the allow-list":
		// the Step's failure_detail is visible to whoever can see the Run, and
		// enumerating why a specific person is excluded from someone else's
		// agent leaks the allow-list's shape.
		return "the run's accountable user may not invoke this agent", false
	}
	if len(scripts) > 0 && scripts[0] != nil {
		rt, err := q.GetAgentRuntime(ctx, agent.RuntimeID)
		if err != nil {
			return "could not verify script runtime", false
		}
		var metadata struct {
			Capabilities []string `json:"capabilities"`
			OS           string   `json:"os"`
		}
		if json.Unmarshal(rt.Metadata, &metadata) != nil {
			return "upgrade the runtime to support directory script pipelines", false
		}
		capable := false
		for _, c := range metadata.Capabilities {
			if c == scriptpipeline.Capability {
				capable = true
			}
		}
		if !capable {
			return "upgrade the runtime to support directory script pipelines", false
		}
		if metadata.OS == "macos" {
			metadata.OS = "darwin"
		}
		if target := scripts[0].TargetPlatform(); target != "auto" && metadata.OS != target {
			return "script platform does not match the runtime operating system", false
		}
	}
	if requiresDebug {
		runtime, err := q.GetAgentRuntime(ctx, agent.RuntimeID)
		if err != nil {
			return "could not verify draft trial runtime capabilities", false
		}
		var metadata struct {
			Capabilities []string `json:"capabilities"`
		}
		if json.Unmarshal(runtime.Metadata, &metadata) != nil {
			return "runtime lacks draft trial capabilities", false
		}
		supported := map[string]bool{}
		for _, capability := range metadata.Capabilities {
			supported[capability] = true
		}
		if !supported[workflow.DebugStopReceiptCapability] || !supported[workflow.DebugFixedEnvironmentCapability] {
			return "runtime lacks draft trial stop receipts or fixed environments", false
		}
	}
	if requiresVision {
		hasVision, err := agentHasVisionCapability(ctx, q, agent.ID, agent.WorkspaceID)
		if err != nil {
			return "could not verify the agent's vision capability: " + err.Error(), false
		}
		if !hasVision {
			return "does not carry the required \"vision\" capability label", false
		}
	}
	return "", true
}

func agentHasVisionCapability(ctx context.Context, q *db.Queries, agentID, workspaceID pgtype.UUID) (bool, error) {
	labels, err := q.ListLabelsForAgents(ctx, db.ListLabelsForAgentsParams{
		AgentIds:    []pgtype.UUID{agentID},
		WorkspaceID: workspaceID,
	})
	if err != nil {
		return false, err
	}
	for _, label := range labels {
		if strings.EqualFold(strings.TrimSpace(label.Name), "vision") {
			return true, nil
		}
	}
	return false, nil
}
