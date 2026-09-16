package service

import (
	"context"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/internal/workflow"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"testing"
)

func TestWorkflowRouterTransactionalRevocationAllStrategies(t *testing.T) {
	env := newWorkflowRouterEnv(t)
	ctx := context.Background()
	id := env.createAgent(t, agentSpec{name: "Revocable", owner: env.otherUserID, labels: []string{"transactional"}})
	requests := []workflow.RouteRequest{
		{Node: &workflow.Node{Key: "explicit", Type: workflow.NodeTypeAgent, Routing: &workflow.Routing{Strategy: workflow.RoutingExplicit, AgentID: util.UUIDToString(id)}}},
		{Node: capabilityNode("capability", "transactional")},
		{Node: &workflow.Node{Key: "previous", Type: workflow.NodeTypeAgent, Routing: &workflow.Routing{Strategy: workflow.RoutingPreviousStep, FromNode: "before"}}, PriorAgentByNode: map[string]pgtype.UUID{"before": id}},
		{Node: &workflow.Node{Key: "fallback", Type: workflow.NodeTypeAgent, Routing: &workflow.Routing{Strategy: workflow.RoutingCapability, Capability: "missing", FallbackAgentID: util.UUIDToString(id)}}},
	}
	var countBefore int
	if err := env.pool.QueryRow(ctx, "SELECT count(*) FROM agent_task_queue WHERE agent_id=$1", id).Scan(&countBefore); err != nil {
		t.Fatal(err)
	}
	tx, err := env.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "DELETE FROM agent_invocation_target WHERE agent_id=$1", id); err != nil {
		t.Fatal(err)
	}
	for _, req := range requests {
		t.Run(req.Node.Key, func(t *testing.T) {
			req.WorkspaceID = env.workspaceID
			req.AccountableUserID = env.userID
			req.Run = db.WorkflowRun{WorkspaceID: env.workspaceID, AccountableUserID: env.userID}
			if _, err := env.router.Route(ctx, env.q.WithTx(tx), req); err == nil {
				t.Fatal("transactional revoke bypassed")
			}
			if _, err := env.router.Route(ctx, env.q, req); err != nil {
				t.Fatalf("pool should see original grant: %v", err)
			}
		})
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	for _, req := range requests {
		req.WorkspaceID = env.workspaceID
		req.AccountableUserID = env.userID
		if _, err := env.router.Route(ctx, env.q, req); err == nil {
			t.Fatalf("%s reused revoked permission on next route", req.Node.Key)
		}
	}
	var countAfter int
	if err := env.pool.QueryRow(ctx, "SELECT count(*) FROM agent_task_queue WHERE agent_id=$1", id).Scan(&countAfter); err != nil {
		t.Fatal(err)
	}
	if countBefore != countAfter {
		t.Fatal("routing refusal enqueued work")
	}
}

func TestWorkflowIntakeCannotBorrowAccountableMemberPermission(t *testing.T) {
	env := newWorkflowRouterEnv(t)
	ctx := context.Background()
	fx := testutil.New(env.pool, util.UUIDToString(env.workspaceID), util.UUIDToString(env.userID))
	issue := fx.Issue(t, "External intake", testutil.Cols{"creator_type": "member", "creator_id": util.UUIDToString(env.userID)})
	agentID := env.createAgent(t, agentSpec{name: "Private to accountable member", owner: env.otherUserID, permission: "private", labels: []string{"intake"}})
	requests := []workflow.RouteRequest{
		{Node: &workflow.Node{Key: "explicit", Type: workflow.NodeTypeAgent, Routing: &workflow.Routing{Strategy: workflow.RoutingExplicit, AgentID: util.UUIDToString(agentID)}}},
		{Node: capabilityNode("capability", "intake")},
		{Node: &workflow.Node{Key: "previous", Type: workflow.NodeTypeAgent, Routing: &workflow.Routing{Strategy: workflow.RoutingPreviousStep, FromNode: "before"}}, PriorAgentByNode: map[string]pgtype.UUID{"before": agentID}},
		{Node: &workflow.Node{Key: "fallback", Type: workflow.NodeTypeAgent, Routing: &workflow.Routing{Strategy: workflow.RoutingCapability, Capability: "missing", FallbackAgentID: util.UUIDToString(agentID)}}},
	}
	issueID, err := util.ParseUUID(issue)
	if err != nil {
		t.Fatal(err)
	}
	for _, req := range requests {
		req.WorkspaceID = env.workspaceID
		req.AccountableUserID = env.otherUserID
		req.Run = db.WorkflowRun{WorkspaceID: env.workspaceID, IssueID: issueID, Source: "external"}
		if _, err := env.router.Route(ctx, env.q, req); err == nil {
			t.Fatalf("%s borrowed the assignee's private permission", req.Node.Key)
		}
	}
	if _, err := env.pool.Exec(ctx, "UPDATE agent SET permission_mode='public_to' WHERE id=$1", agentID); err != nil {
		t.Fatal(err)
	}
	fx.Insert(t, "agent_invocation_target", testutil.Cols{"agent_id": util.UUIDToString(agentID), "target_type": "member", "target_id": util.UUIDToString(env.userID)})
	for _, req := range requests {
		req.WorkspaceID = env.workspaceID
		req.AccountableUserID = env.otherUserID
		req.Run = db.WorkflowRun{WorkspaceID: env.workspaceID, IssueID: issueID, Source: "external"}
		if _, err := env.router.Route(ctx, env.q, req); err != nil {
			t.Fatalf("%s: both allowed but rejected: %v", req.Node.Key, err)
		}
		tx, err := env.pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, "DELETE FROM agent_invocation_target WHERE agent_id=$1", agentID); err != nil {
			t.Fatal(err)
		}
		_, routeErr := env.router.Route(ctx, env.q.WithTx(tx), req)
		_ = tx.Rollback(ctx)
		if routeErr == nil {
			t.Fatalf("%s ignored initiating member revocation", req.Node.Key)
		}
	}
}
