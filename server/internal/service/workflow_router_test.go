package service

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/internal/workflow"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Tests for the workflow Step router.
//
// These run against a real Postgres because the router's decisions are joins:
// which agents carry a capability label, whether a runtime is online, whether an
// invocation-target row admits a user. A mocked *db.Queries would let the test
// assert the router's beliefs about those tables rather than the tables. Skipped
// when no database is reachable, matching resolve_originator_test.go.
//
// The property each test defends is stated in its comment, because "the right
// agent ran the step" is not something a caller can check after the fact: a
// workflow that quietly routes to the wrong specialist produces work that looks
// correct.

// workflowRouterEnv is one isolated fixture: its own workspace, member, and
// runtime, so repeated and parallel runs never collide.
type workflowRouterEnv struct {
	pool        *pgxpool.Pool
	q           *db.Queries
	router      *WorkflowRouter
	workspaceID pgtype.UUID
	userID      pgtype.UUID
	// otherUserID is a second workspace member, used to prove that a private
	// agent owned by someone else is unroutable.
	otherUserID pgtype.UUID
	onlineRT    pgtype.UUID
	offlineRT   pgtype.UUID
}

func newWorkflowRouterEnv(t *testing.T) *workflowRouterEnv {
	t.Helper()
	ctx := context.Background()

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://multica:multica@localhost:5432/multica?sslmode=disable"
	}
	tctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	pool, err := pgxpool.New(tctx, dbURL)
	if err != nil {
		t.Skipf("database unavailable: %v", err)
	}
	if err := pool.Ping(tctx); err != nil {
		pool.Close()
		t.Skipf("database unreachable: %v", err)
	}
	t.Cleanup(pool.Close)

	env := &workflowRouterEnv{pool: pool, q: db.New(pool)}
	env.router = NewWorkflowRouter(env.q)
	suffix := fmt.Sprintf("wfr-%d", time.Now().UnixNano())

	mustQueryRow(t, pool, &env.userID,
		`INSERT INTO "user" (name, email) VALUES ($1, $2) RETURNING id`,
		"WF Router Owner", suffix+"-owner@multica.test")
	mustQueryRow(t, pool, &env.otherUserID,
		`INSERT INTO "user" (name, email) VALUES ($1, $2) RETURNING id`,
		"WF Router Other", suffix+"-other@multica.test")
	mustQueryRow(t, pool, &env.workspaceID,
		`INSERT INTO workspace (name, slug) VALUES ($1, $2) RETURNING id`,
		"WF Router", suffix)
	// Both users are workspace members. Membership is what a `public_to
	// workspace` target admits, so without it that case would pass for the wrong
	// reason.
	mustExec(t, pool, `INSERT INTO member (user_id, workspace_id, role) VALUES ($1, $2, 'owner')`,
		env.userID, env.workspaceID)
	mustExec(t, pool, `INSERT INTO member (user_id, workspace_id, role) VALUES ($1, $2, 'member')`,
		env.otherUserID, env.workspaceID)

	mustQueryRow(t, pool, &env.onlineRT,
		`INSERT INTO agent_runtime (workspace_id, name, runtime_mode, provider, status)
		 VALUES ($1, $2, 'local', 'claude', 'online') RETURNING id`,
		env.workspaceID, "rt-online-"+suffix)
	mustQueryRow(t, pool, &env.offlineRT,
		`INSERT INTO agent_runtime (workspace_id, name, runtime_mode, provider, status)
		 VALUES ($1, $2, 'local', 'claude', 'offline') RETURNING id`,
		env.workspaceID, "rt-offline-"+suffix)

	t.Cleanup(func() {
		bg := context.Background()
		for _, stmt := range []string{
			`DELETE FROM agent_to_label WHERE agent_id IN (SELECT id FROM agent WHERE workspace_id = $1)`,
			`DELETE FROM agent_invocation_target WHERE agent_id IN (SELECT id FROM agent WHERE workspace_id = $1)`,
			`DELETE FROM issue_label WHERE workspace_id = $1`,
			`DELETE FROM agent WHERE workspace_id = $1`,
			`DELETE FROM agent_runtime WHERE workspace_id = $1`,
			`DELETE FROM member WHERE workspace_id = $1`,
			`DELETE FROM workspace WHERE id = $1`,
		} {
			if _, err := pool.Exec(bg, stmt, env.workspaceID); err != nil {
				t.Logf("cleanup %q: %v", stmt, err)
			}
		}
		for _, id := range []pgtype.UUID{env.userID, env.otherUserID} {
			if _, err := pool.Exec(bg, `DELETE FROM "user" WHERE id = $1`, id); err != nil {
				t.Logf("cleanup user: %v", err)
			}
		}
	})
	return env
}

func mustQueryRow(t *testing.T, pool *pgxpool.Pool, dest any, sql string, args ...any) {
	t.Helper()
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(dest); err != nil {
		t.Fatalf("query %q: %v", sql, err)
	}
}

func mustExec(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}

// agentSpec describes a fixture agent. Defaults are the permissive case
// (owned by env.userID, public_to workspace, online runtime) so each test only
// states the gate it is exercising.
//
// There is no "no runtime bound" variant: agent.runtime_id is NOT NULL (migration
// 004), so AgentReadiness's !RuntimeID.Valid branch is unreachable through the
// schema and a test for it would have to fabricate a row the database forbids.
// The offline-runtime case covers the same product outcome ("this agent has
// nowhere to execute") through a state that can actually occur.
type agentSpec struct {
	name      string
	labels    []string
	archived  bool
	offlineRT bool
	owner     pgtype.UUID
	// permission defaults to "public_to".
	permission string
	// targets are the invocation-target rows. Only read for permission ==
	// "public_to". targetID is required by the schema; a workspace row stores the
	// workspace id (see migration 130 - NULL would break the UNIQUE dedup).
	targets []invocationTargetSpec
}

type invocationTargetSpec struct {
	targetType string
	targetID   pgtype.UUID
}

func (env *workflowRouterEnv) createAgent(t *testing.T, spec agentSpec) pgtype.UUID {
	t.Helper()

	runtimeID := env.onlineRT
	if spec.offlineRT {
		runtimeID = env.offlineRT
	}
	owner := spec.owner
	if !owner.Valid {
		owner = env.userID
	}
	permission := spec.permission
	if permission == "" {
		permission = "public_to"
	}

	var agentID pgtype.UUID
	mustQueryRow(t, env.pool, &agentID,
		`INSERT INTO agent (workspace_id, name, runtime_mode, runtime_id, owner_id, permission_mode)
		 VALUES ($1, $2, 'local', $3, $4, $5) RETURNING id`,
		env.workspaceID, spec.name, runtimeID, owner, permission)
	if spec.archived {
		mustExec(t, env.pool, `UPDATE agent SET archived_at = now() WHERE id = $1`, agentID)
	}

	if permission == "public_to" {
		targets := spec.targets
		if targets == nil {
			// Default: workspace-wide, so permission is not the gate under test.
			targets = []invocationTargetSpec{{targetType: "workspace", targetID: env.workspaceID}}
		}
		for _, tg := range targets {
			mustExec(t, env.pool,
				`INSERT INTO agent_invocation_target (agent_id, target_type, target_id) VALUES ($1, $2, $3)`,
				agentID, tg.targetType, tg.targetID)
		}
	}

	for _, name := range spec.labels {
		labelID := env.ensureCapabilityLabel(t, name)
		mustExec(t, env.pool,
			`INSERT INTO agent_to_label (agent_id, label_id) VALUES ($1, $2)`, agentID, labelID)
	}
	return agentID
}

// ensureCapabilityLabel get-or-creates an agent-namespace label.
//
// Get-or-create rather than insert: a label name is unique per (workspace,
// resource_type) case-insensitively (idx issue_label_workspace_type_name_lower),
// and a capability is by definition shared - several agents in one test carry the
// same capability, so the second must attach to the existing row. resource_type
// 'agent' is what makes this a capability label rather than an issue label that
// happens to share a name (migration 162).
func (env *workflowRouterEnv) ensureCapabilityLabel(t *testing.T, name string) pgtype.UUID {
	t.Helper()
	ctx := context.Background()
	var labelID pgtype.UUID
	err := env.pool.QueryRow(ctx,
		`SELECT id FROM issue_label
		 WHERE workspace_id = $1 AND resource_type = 'agent' AND LOWER(name) = LOWER($2)`,
		env.workspaceID, name).Scan(&labelID)
	if err == nil {
		return labelID
	}
	mustQueryRow(t, env.pool, &labelID,
		`INSERT INTO issue_label (workspace_id, name, color, resource_type)
		 VALUES ($1, $2, '#000000', 'agent') RETURNING id`,
		env.workspaceID, name)
	return labelID
}

// route runs the router for a node, with the Run's accountable user defaulting to
// the fixture owner.
func (env *workflowRouterEnv) route(t *testing.T, node *workflow.Node, accountable pgtype.UUID, prior map[string]pgtype.UUID) (workflow.RouteResult, error) {
	t.Helper()
	return env.router.Route(context.Background(), env.q, workflow.RouteRequest{
		WorkspaceID:       env.workspaceID,
		Run:               db.WorkflowRun{WorkspaceID: env.workspaceID, AccountableUserID: accountable},
		Node:              node,
		AccountableUserID: accountable,
		PriorAgentByNode:  prior,
	})
}

func capabilityNode(key, capability string) *workflow.Node {
	return &workflow.Node{
		Key:  key,
		Type: workflow.NodeTypeAgent,
		Routing: &workflow.Routing{
			Strategy:   workflow.RoutingCapability,
			Capability: capability,
		},
	}
}

// TestWorkflowRouterCapabilityMatch is the base case: an agent carrying the
// capability label is chosen, and the reason names both the capability and the
// agent so an operator reading the Step can see WHY this agent ran.
func TestWorkflowRouterCapabilityMatch(t *testing.T) {
	env := newWorkflowRouterEnv(t)
	want := env.createAgent(t, agentSpec{name: "Analyst", labels: []string{"bug_analysis"}})
	// A second, unlabelled agent must not be considered: capability routing is a
	// filter, not a preference.
	env.createAgent(t, agentSpec{name: "Unrelated"})

	got, err := env.route(t, capabilityNode("analyze", "bug_analysis"), env.userID, nil)
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if util.UUIDToString(got.AgentID) != util.UUIDToString(want) {
		t.Fatalf("routed to %s, want the labelled agent %s",
			util.UUIDToString(got.AgentID), util.UUIDToString(want))
	}
	if util.UUIDToString(got.RuntimeID) != util.UUIDToString(env.onlineRT) {
		t.Errorf("runtime = %s, want the agent's online runtime", util.UUIDToString(got.RuntimeID))
	}
	if !strings.Contains(got.Reason, "capability:bug_analysis") || !strings.Contains(got.Reason, "Analyst") {
		t.Errorf("reason = %q; an operator needs both the capability and the agent name", got.Reason)
	}
}

// TestWorkflowRouterCapabilityMatchIsCaseInsensitive: label names are user-typed
// free text, so "Bug_Analysis" and "bug_analysis" are the same capability. A
// case-sensitive match would make a template silently unroutable over
// capitalisation.
func TestWorkflowRouterCapabilityMatchIsCaseInsensitive(t *testing.T) {
	env := newWorkflowRouterEnv(t)
	want := env.createAgent(t, agentSpec{name: "Analyst", labels: []string{"Bug_Analysis"}})

	got, err := env.route(t, capabilityNode("analyze", "bug_analysis"), env.userID, nil)
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if util.UUIDToString(got.AgentID) != util.UUIDToString(want) {
		t.Fatalf("routed to %s, want %s", util.UUIDToString(got.AgentID), util.UUIDToString(want))
	}
}

// TestWorkflowRouterCapabilityNoLabelledAgentErrors: no candidate must be an
// ERROR, not a silent substitution. The engine turns the error into a blocked
// Step with routing_no_candidate, which a human can fix; picking an arbitrary
// agent instead would run the wrong specialist and report success.
func TestWorkflowRouterCapabilityNoLabelledAgentErrors(t *testing.T) {
	env := newWorkflowRouterEnv(t)
	// A perfectly healthy agent exists — it simply does not advertise the
	// capability. It must NOT be chosen.
	env.createAgent(t, agentSpec{name: "Generalist"})

	_, err := env.route(t, capabilityNode("analyze", "bug_analysis"), env.userID, nil)
	if err == nil {
		t.Fatal("routing succeeded with no agent carrying the capability; the engine would run the wrong agent")
	}
	if !strings.Contains(err.Error(), "bug_analysis") {
		t.Errorf("error = %q; it must name the missing capability so an operator knows what to add", err)
	}
}

// TestWorkflowRouterSkipsArchivedAgent: an archived agent is retired. Routing to
// it would queue a task nobody will ever claim, which presents as a Run that
// hangs rather than one that reports a problem.
func TestWorkflowRouterSkipsArchivedAgent(t *testing.T) {
	env := newWorkflowRouterEnv(t)
	env.createAgent(t, agentSpec{name: "AAA Archived", labels: []string{"bug_analysis"}, archived: true})
	want := env.createAgent(t, agentSpec{name: "ZZZ Live", labels: []string{"bug_analysis"}})

	// "AAA Archived" sorts first, so a router that did not gate on archive state
	// would pick it.
	got, err := env.route(t, capabilityNode("analyze", "bug_analysis"), env.userID, nil)
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if util.UUIDToString(got.AgentID) != util.UUIDToString(want) {
		t.Fatalf("routed to %s, want the live agent %s",
			util.UUIDToString(got.AgentID), util.UUIDToString(want))
	}
}

// TestWorkflowRouterSkipsOfflineRuntime: the same reasoning as archived, for the
// transient case. The rejection reason must reach the caller so an operator sees
// "bring the runtime up", not "add a label".
func TestWorkflowRouterSkipsOfflineRuntime(t *testing.T) {
	env := newWorkflowRouterEnv(t)
	env.createAgent(t, agentSpec{name: "AAA Offline", labels: []string{"bug_analysis"}, offlineRT: true})
	want := env.createAgent(t, agentSpec{name: "ZZZ Online", labels: []string{"bug_analysis"}})

	got, err := env.route(t, capabilityNode("analyze", "bug_analysis"), env.userID, nil)
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if util.UUIDToString(got.AgentID) != util.UUIDToString(want) {
		t.Fatalf("routed to %s, want the online agent %s",
			util.UUIDToString(got.AgentID), util.UUIDToString(want))
	}
}

// TestWorkflowRouterOfflineOnlyCandidateReportsRuntime: when the ONLY labelled
// agent is offline, the error must say so. "no candidate" alone sends the
// operator looking for a missing label that is already there.
func TestWorkflowRouterOfflineOnlyCandidateReportsRuntime(t *testing.T) {
	env := newWorkflowRouterEnv(t)
	env.createAgent(t, agentSpec{name: "Only Analyst", labels: []string{"bug_analysis"}, offlineRT: true})

	_, err := env.route(t, capabilityNode("analyze", "bug_analysis"), env.userID, nil)
	if err == nil {
		t.Fatal("routed to an agent whose runtime is offline")
	}
	if !strings.Contains(err.Error(), "runtime") {
		t.Errorf("error = %q; it must attribute the rejection to the runtime", err)
	}
}

// TestWorkflowRouterRefusesPrivateAgentOwnedByAnother is the security case.
//
// A private agent runs with its OWNER's credentials and connections. A Run
// started by a different person must not be able to route to it — otherwise
// starting a workflow becomes a way to spend someone else's integrations, which
// is exactly what the invocation-permission model exists to prevent. The
// capability label is present and the agent is perfectly healthy: permission is
// the only thing standing in the way, and it has to be enough.
func TestWorkflowRouterRefusesPrivateAgentOwnedByAnother(t *testing.T) {
	env := newWorkflowRouterEnv(t)
	env.createAgent(t, agentSpec{
		name:       "Someone Elses Private Analyst",
		labels:     []string{"bug_analysis"},
		owner:      env.otherUserID,
		permission: "private",
	})

	// env.userID is a workspace OWNER. Deny must hold anyway: admin grants
	// management and visibility, never the right to run someone's private agent.
	_, err := env.route(t, capabilityNode("analyze", "bug_analysis"), env.userID, nil)
	if err == nil {
		t.Fatal("routed to another user's private agent; a workflow must not be a permission bypass")
	}

	// The owner themselves still routes to it, proving the refusal is about
	// permission rather than a broken candidate.
	got, err := env.route(t, capabilityNode("analyze", "bug_analysis"), env.otherUserID, nil)
	if err != nil {
		t.Fatalf("the agent owner must be able to route to their own private agent: %v", err)
	}
	if !got.AgentID.Valid {
		t.Fatal("owner route returned no agent")
	}
}

// TestWorkflowRouterRefusesMemberScopedAgentForOtherUser: a `public_to` agent
// whose allow-list names one specific person is not routable by anyone else.
// This is the case a coarse "public_to means anyone" reading would get wrong.
func TestWorkflowRouterRefusesMemberScopedAgentForOtherUser(t *testing.T) {
	env := newWorkflowRouterEnv(t)
	env.createAgent(t, agentSpec{
		name:       "Member Scoped Analyst",
		labels:     []string{"bug_analysis"},
		owner:      env.otherUserID,
		permission: "public_to",
		targets:    []invocationTargetSpec{{targetType: "member", targetID: env.otherUserID}},
	})

	if _, err := env.route(t, capabilityNode("analyze", "bug_analysis"), env.userID, nil); err == nil {
		t.Fatal("routed to an agent whose allow-list names a different member")
	}
	if _, err := env.route(t, capabilityNode("analyze", "bug_analysis"), env.otherUserID, nil); err != nil {
		t.Fatalf("the allow-listed member must be able to route to it: %v", err)
	}
}

// TestWorkflowRouterUnattributedRunCannotReachPrivateAgent: a Run with no
// accountable human (autopilot schedule, external intake) is judged as a system
// principal. It must not reach a private agent, and it MAY reach a
// `public_to workspace` one — the scoped, product-approved exception.
func TestWorkflowRouterUnattributedRunCannotReachPrivateAgent(t *testing.T) {
	env := newWorkflowRouterEnv(t)
	env.createAgent(t, agentSpec{
		name:       "Private Analyst",
		labels:     []string{"bug_analysis"},
		permission: "private",
	})

	if _, err := env.route(t, capabilityNode("analyze", "bug_analysis"), pgtype.UUID{}, nil); err == nil {
		t.Fatal("an unattributed run reached a private agent")
	}

	env.createAgent(t, agentSpec{name: "Workspace Analyst", labels: []string{"bug_analysis"}})
	got, err := env.route(t, capabilityNode("analyze", "bug_analysis"), pgtype.UUID{}, nil)
	if err != nil {
		t.Fatalf("an unattributed run must still reach a public_to workspace agent: %v", err)
	}
	if !got.AgentID.Valid {
		t.Fatal("route returned no agent")
	}
}

// TestWorkflowRouterCapabilityIsDeterministic: two equally-eligible candidates
// must always yield the same choice. A replay of the same command after a crash
// has to select the same agent, or the Run's audit trail stops being
// reproducible and "who did this work" becomes unanswerable.
func TestWorkflowRouterCapabilityIsDeterministic(t *testing.T) {
	env := newWorkflowRouterEnv(t)
	env.createAgent(t, agentSpec{name: "beta-analyst", labels: []string{"bug_analysis"}})
	env.createAgent(t, agentSpec{name: "alpha-analyst", labels: []string{"bug_analysis"}})

	first, err := env.route(t, capabilityNode("analyze", "bug_analysis"), env.userID, nil)
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	// Stable ordering is by lowercased name, so alpha wins regardless of
	// insertion order or whatever order the DB happens to return rows in.
	if !strings.Contains(first.Reason, "alpha-analyst") {
		t.Fatalf("reason = %q; expected the name-ordered first candidate", first.Reason)
	}
	for i := 0; i < 5; i++ {
		again, err := env.route(t, capabilityNode("analyze", "bug_analysis"), env.userID, nil)
		if err != nil {
			t.Fatalf("route %d: %v", i, err)
		}
		if util.UUIDToString(again.AgentID) != util.UUIDToString(first.AgentID) {
			t.Fatalf("route %d chose %s, first chose %s; routing must be replay-stable",
				i, util.UUIDToString(again.AgentID), util.UUIDToString(first.AgentID))
		}
	}
}

// TestWorkflowRouterPreviousStepReuse: the whole point of previous_step routing
// is that a fix and its validation land with the same agent, so the validator has
// the implementer's working context.
func TestWorkflowRouterPreviousStepReuse(t *testing.T) {
	env := newWorkflowRouterEnv(t)
	implementer := env.createAgent(t, agentSpec{name: "Implementer"})
	// Another eligible agent exists; previous_step must ignore it entirely.
	env.createAgent(t, agentSpec{name: "AAA Bystander"})

	node := &workflow.Node{
		Key:  "validate",
		Type: workflow.NodeTypeAgent,
		Routing: &workflow.Routing{
			Strategy: workflow.RoutingPreviousStep,
			FromNode: "implement",
		},
	}
	got, err := env.route(t, node, env.userID, map[string]pgtype.UUID{"implement": implementer})
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if util.UUIDToString(got.AgentID) != util.UUIDToString(implementer) {
		t.Fatalf("routed to %s, want the agent that ran implement (%s)",
			util.UUIDToString(got.AgentID), util.UUIDToString(implementer))
	}
	if !strings.Contains(got.Reason, "previous_step:implement") {
		t.Errorf("reason = %q; it must name the node whose agent was reused", got.Reason)
	}
}

// TestWorkflowRouterPreviousStepWithNoPriorAgentErrors: previous_step with no
// prior run of the named node is not a licence to pick anyone. Validate keeps a
// graph from declaring this, so reaching it means the Run's history is not what
// the graph assumed — which a human should see.
func TestWorkflowRouterPreviousStepWithNoPriorAgentErrors(t *testing.T) {
	env := newWorkflowRouterEnv(t)
	env.createAgent(t, agentSpec{name: "Available"})

	node := &workflow.Node{
		Key:  "validate",
		Type: workflow.NodeTypeAgent,
		Routing: &workflow.Routing{
			Strategy: workflow.RoutingPreviousStep,
			FromNode: "implement",
		},
	}
	_, err := env.route(t, node, env.userID, nil)
	if err == nil {
		t.Fatal("previous_step routing succeeded with no prior agent for the named node")
	}
	if !strings.Contains(err.Error(), "implement") {
		t.Errorf("error = %q; it must name the node with no prior agent", err)
	}
}

// TestWorkflowRouterPreviousStepStillGated: reusing the previous agent does not
// exempt it from the gates. If that agent's runtime went offline between the two
// steps, the Step must block rather than queue work nowhere.
func TestWorkflowRouterPreviousStepStillGated(t *testing.T) {
	env := newWorkflowRouterEnv(t)
	implementer := env.createAgent(t, agentSpec{name: "Implementer", offlineRT: true})

	node := &workflow.Node{
		Key:  "validate",
		Type: workflow.NodeTypeAgent,
		Routing: &workflow.Routing{
			Strategy: workflow.RoutingPreviousStep,
			FromNode: "implement",
		},
	}
	if _, err := env.route(t, node, env.userID, map[string]pgtype.UUID{"implement": implementer}); err == nil {
		t.Fatal("previous_step reused an agent whose runtime is offline")
	}
}

// TestWorkflowRouterExplicit: a pinned agent id is honoured.
func TestWorkflowRouterExplicit(t *testing.T) {
	env := newWorkflowRouterEnv(t)
	pinned := env.createAgent(t, agentSpec{name: "Pinned"})
	env.createAgent(t, agentSpec{name: "AAA Other"})

	node := &workflow.Node{
		Key:  "analyze",
		Type: workflow.NodeTypeAgent,
		Routing: &workflow.Routing{
			Strategy: workflow.RoutingExplicit,
			AgentID:  util.UUIDToString(pinned),
		},
	}
	got, err := env.route(t, node, env.userID, nil)
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if util.UUIDToString(got.AgentID) != util.UUIDToString(pinned) {
		t.Fatalf("routed to %s, want the pinned agent %s",
			util.UUIDToString(got.AgentID), util.UUIDToString(pinned))
	}
}

// TestWorkflowRouterExplicitStillGated: pinning an agent in the graph is not an
// authorization decision. A template author must not be able to pin someone
// else's private agent and thereby launder access to it.
func TestWorkflowRouterExplicitStillGated(t *testing.T) {
	env := newWorkflowRouterEnv(t)
	pinned := env.createAgent(t, agentSpec{
		name:       "Pinned Private",
		owner:      env.otherUserID,
		permission: "private",
	})

	node := &workflow.Node{
		Key:  "analyze",
		Type: workflow.NodeTypeAgent,
		Routing: &workflow.Routing{
			Strategy: workflow.RoutingExplicit,
			AgentID:  util.UUIDToString(pinned),
		},
	}
	if _, err := env.route(t, node, env.userID, nil); err == nil {
		t.Fatal("an explicitly pinned private agent bypassed the permission gate")
	}
}

// TestWorkflowRouterFallbackUsedWhenPrimaryFindsNothing: the fallback is the last
// resort of every strategy, and the reason must record that a fallback was used —
// an operator seeing the primary specialist never run deserves to know why.
func TestWorkflowRouterFallbackUsedWhenPrimaryFindsNothing(t *testing.T) {
	env := newWorkflowRouterEnv(t)
	fallback := env.createAgent(t, agentSpec{name: "Fallback"})

	node := capabilityNode("analyze", "bug_analysis")
	node.Routing.FallbackAgentID = util.UUIDToString(fallback)

	got, err := env.route(t, node, env.userID, nil)
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if util.UUIDToString(got.AgentID) != util.UUIDToString(fallback) {
		t.Fatalf("routed to %s, want the fallback %s",
			util.UUIDToString(got.AgentID), util.UUIDToString(fallback))
	}
	if !strings.Contains(got.Reason, "fallback") {
		t.Errorf("reason = %q; it must record that the fallback was used", got.Reason)
	}
}

// TestWorkflowRouterFallbackNotPreferredOverPrimary: the fallback exists for when
// the primary strategy finds nothing, not as an alternative to it. Silently
// preferring the fallback would make capability routing decorative.
func TestWorkflowRouterFallbackNotPreferredOverPrimary(t *testing.T) {
	env := newWorkflowRouterEnv(t)
	// "AAA Fallback" sorts before the specialist, so a router that merged the two
	// candidate sets would pick it.
	fallback := env.createAgent(t, agentSpec{name: "AAA Fallback"})
	specialist := env.createAgent(t, agentSpec{name: "ZZZ Specialist", labels: []string{"bug_analysis"}})

	node := capabilityNode("analyze", "bug_analysis")
	node.Routing.FallbackAgentID = util.UUIDToString(fallback)

	got, err := env.route(t, node, env.userID, nil)
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if util.UUIDToString(got.AgentID) != util.UUIDToString(specialist) {
		t.Fatalf("routed to %s, want the capability match %s",
			util.UUIDToString(got.AgentID), util.UUIDToString(specialist))
	}
}

// TestWorkflowRouterFallbackStillGated: an ineligible fallback is not a fallback.
// Both the primary and the fallback fail here, and the error must mention both so
// the operator does not fix one and find the other.
func TestWorkflowRouterFallbackStillGated(t *testing.T) {
	env := newWorkflowRouterEnv(t)
	fallback := env.createAgent(t, agentSpec{name: "Broken Fallback", offlineRT: true})

	node := capabilityNode("analyze", "bug_analysis")
	node.Routing.FallbackAgentID = util.UUIDToString(fallback)

	_, err := env.route(t, node, env.userID, nil)
	if err == nil {
		t.Fatal("routed to a fallback whose runtime is offline")
	}
	if !strings.Contains(err.Error(), "bug_analysis") || !strings.Contains(err.Error(), "fallback") {
		t.Errorf("error = %q; it must report both the failed primary and the failed fallback", err)
	}
}

// TestWorkflowRouterRejectsForeignWorkspaceAgent: an agent id from another
// workspace must resolve to nothing. Routing decides who receives work and
// credentials, so it is a tenant boundary.
func TestWorkflowRouterRejectsForeignWorkspaceAgent(t *testing.T) {
	env := newWorkflowRouterEnv(t)
	other := newWorkflowRouterEnv(t)
	foreign := other.createAgent(t, agentSpec{name: "Foreign", owner: other.userID})

	node := &workflow.Node{
		Key:  "analyze",
		Type: workflow.NodeTypeAgent,
		Routing: &workflow.Routing{
			Strategy: workflow.RoutingExplicit,
			AgentID:  util.UUIDToString(foreign),
		},
	}
	if _, err := env.route(t, node, env.userID, nil); err == nil {
		t.Fatal("routed to an agent in a different workspace")
	}
}

// TestWorkflowRouterRejectsMalformedRouting locks the defensive paths. Validate
// rejects these at publish time, so reaching them means a hand-written or
// tampered definition row - which must produce a named error rather than a nil
// dereference or an arbitrary agent.
func TestWorkflowRouterRejectsMalformedRouting(t *testing.T) {
	env := newWorkflowRouterEnv(t)
	env.createAgent(t, agentSpec{name: "Available"})

	cases := []struct {
		name string
		node *workflow.Node
	}{
		{"no routing block", &workflow.Node{Key: "analyze", Type: workflow.NodeTypeAgent}},
		{"unknown strategy", &workflow.Node{Key: "analyze", Type: workflow.NodeTypeAgent,
			Routing: &workflow.Routing{Strategy: workflow.RoutingStrategy("telepathy")}}},
		{"capability with empty capability", &workflow.Node{Key: "analyze", Type: workflow.NodeTypeAgent,
			Routing: &workflow.Routing{Strategy: workflow.RoutingCapability}}},
		{"previous_step with no from_node", &workflow.Node{Key: "analyze", Type: workflow.NodeTypeAgent,
			Routing: &workflow.Routing{Strategy: workflow.RoutingPreviousStep}}},
		{"explicit with malformed id", &workflow.Node{Key: "analyze", Type: workflow.NodeTypeAgent,
			Routing: &workflow.Routing{Strategy: workflow.RoutingExplicit, AgentID: "not-a-uuid"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := env.route(t, tc.node, env.userID, nil); err == nil {
				t.Fatalf("%s routed successfully; it must be refused", tc.name)
			}
		})
	}
}

// TestWorkflowRouterRequiresNode / RequiresQueries: the router is called from
// inside the Step-activation transaction, so a nil handle or node is a
// programming error that must surface as an error rather than a panic taking the
// whole activation - and with it the Run - down.
func TestWorkflowRouterRequiresNodeAndQueries(t *testing.T) {
	r := NewWorkflowRouter(nil)
	if _, err := r.Route(context.Background(), nil, workflow.RouteRequest{}); err == nil {
		t.Error("routing with no database handle must be refused, not panic")
	}
	if _, err := r.Route(context.Background(), &db.Queries{}, workflow.RouteRequest{}); err == nil {
		t.Error("routing with no node must be refused, not panic")
	}
}

func TestWorkflowRouterRequiresVisionCapabilityForImageInput(t *testing.T) {
	env := newWorkflowRouterEnv(t)
	// This candidate satisfies the graph's declared capability but must not receive
	// a downloadable image without also explicitly advertising vision support.
	env.createAgent(t, agentSpec{name: "AAA Text Analyst", labels: []string{"bug_analysis"}})
	want := env.createAgent(t, agentSpec{name: "ZZZ Vision Analyst", labels: []string{"bug_analysis", "vision"}})

	got, err := env.router.Route(context.Background(), env.q, workflow.RouteRequest{
		WorkspaceID:       env.workspaceID,
		Run:               db.WorkflowRun{WorkspaceID: env.workspaceID, AccountableUserID: env.userID},
		Node:              capabilityNode("analyze", "bug_analysis"),
		AccountableUserID: env.userID,
		RequiresVision:    true,
	})
	if err != nil {
		t.Fatalf("route image workflow: %v", err)
	}
	if util.UUIDToString(got.AgentID) != util.UUIDToString(want) {
		t.Fatalf("routed image workflow to %s, want vision-capable agent %s", util.UUIDToString(got.AgentID), util.UUIDToString(want))
	}

	env = newWorkflowRouterEnv(t)
	env.createAgent(t, agentSpec{name: "Text Only", labels: []string{"bug_analysis"}})
	_, err = env.router.Route(context.Background(), env.q, workflow.RouteRequest{
		WorkspaceID: env.workspaceID, Run: db.WorkflowRun{WorkspaceID: env.workspaceID, AccountableUserID: env.userID},
		Node: capabilityNode("analyze", "bug_analysis"), AccountableUserID: env.userID, RequiresVision: true,
	})
	if err == nil || !strings.Contains(err.Error(), "vision") {
		t.Fatalf("text-only image route error = %v, want a vision-capability refusal", err)
	}
}
