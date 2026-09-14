package service

import (
	"context"
	"testing"

	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/internal/workflow"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/scriptpipeline"
)

func TestWorkflowRouterScriptPlatformAndCapability(t *testing.T) {
	env := newWorkflowRouterEnv(t)
	pinned := env.createAgent(t, agentSpec{name: "Script executor"})
	req := workflow.RouteRequest{WorkspaceID: env.workspaceID, Run: db.WorkflowRun{WorkspaceID: env.workspaceID, AccountableUserID: env.userID}, AccountableUserID: env.userID, Node: &workflow.Node{Key: "execute", Type: workflow.NodeTypeAgent, Routing: &workflow.Routing{Strategy: workflow.RoutingExplicit, AgentID: util.UUIDToString(pinned)}}, ScriptPipeline: &scriptpipeline.Config{Directory: "/macbuild/project", Platform: "darwin", Steps: []string{"run"}, TimeoutSeconds: 30}}
	for _, tc := range []struct {
		name, metadata string
		allowed        bool
	}{
		{"old daemon", `{}`, false},
		{"wrong machine", `{"os":"windows","capabilities":["workflow_script_pipeline_v1"]}`, false},
		{"macOS protocol name", `{"os":"macos","capabilities":["workflow_script_pipeline_v1"]}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mustExec(t, env.pool, `UPDATE agent_runtime SET metadata=$1 WHERE id=$2`, tc.metadata, env.onlineRT)
			_, err := env.router.Route(context.Background(), env.q, req)
			if (err == nil) != tc.allowed {
				t.Fatalf("allowed=%v, error=%v", tc.allowed, err)
			}
		})
	}
}
