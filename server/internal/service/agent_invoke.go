package service

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// The agent invocation-permission rule (MUL-3963), extracted so there is exactly
// ONE implementation of it.
//
// It previously lived only as handler.canInvokeAgent, a *Handler method. The
// workflow router runs in package service and cannot call a handler method, and
// the alternative - re-deriving "who may trigger this agent" against the same
// tables - would create a second copy of the most security-sensitive predicate
// in the codebase. Two copies of an authorization rule do not stay equal: one
// gets a fix, the other keeps the hole, and the divergence only surfaces when a
// user triggers the same agent through two different entry points. So the rule
// moved here and handler.canInvokeAgent now delegates.
//
// This is a MOVE, not a reinterpretation. The semantics below are the ones
// canInvokeAgent enforced, unchanged:
//
//   - The agent OWNER may always invoke their own agent.
//   - permission_mode != "public_to" (i.e. private, or any unknown mode) is
//     deny-by-default: NO workspace-admin bypass and NO agent-to-agent bypass.
//     An admin must not be able to spend someone else's Composio/OAuth
//     connections just because they administer the workspace.
//   - permission_mode == "public_to" consults the agent_invocation_target
//     allow-list: a `workspace` target admits any workspace member, a `member`
//     target admits only that user, and `team` targets are reserved and inert.
//   - Agent and system principals are workspace-internal and a `workspace`
//     target admits them even with no resolved human. That exception is scoped
//     tightly to the workspace target: member/team targets still require a
//     matching human, so an unattributed trigger fails closed against a
//     specific-people grant and cannot smuggle itself onto it.
//   - A2A is judged by the top-of-chain human ORIGINATOR, never the immediate
//     agent actor, so agents cannot chain into a channel that bypasses an
//     owner's allow-list.
//   - Any lookup error is a denial.

// InvokeActor identifies who is asking to trigger an agent.
//
// ActorID is the immediate principal; OriginatorUserID is the top-of-chain human
// the action is attributed to. For a member actor they are the same person. For
// an agent or system actor only OriginatorUserID is trusted - an empty value
// means no human could be attributed, which fails closed everywhere except the
// workspace-target exception above.
type InvokeActor struct {
	// ActorType is "member", "agent", or "system".
	ActorType string
	// ActorID is the member user id, or the agent id for an agent actor.
	ActorID string
	// OriginatorUserID is the resolved top-of-chain human, or "" when none.
	OriginatorUserID string
	// WorkspaceID scopes the workspace-membership check.
	WorkspaceID string
}

// MemberInvokeActor builds the actor for a plain human-initiated invocation,
// where the member is their own originator. Used by the workflow router, whose
// invoking principal is the Run's accountable human.
func MemberInvokeActor(userID, workspaceID string) InvokeActor {
	return InvokeActor{
		ActorType:        "member",
		ActorID:          userID,
		OriginatorUserID: userID,
		WorkspaceID:      workspaceID,
	}
}

// SystemInvokeActor builds the actor for an automation-initiated invocation with
// no human in the chain. It is NOT a bypass: it fails closed for private agents
// and for member/team-scoped allow-lists, and only satisfies a `public_to
// workspace` target - the product-approved exception for webhook / schedule /
// workspace-wide automation.
func SystemInvokeActor(workspaceID string) InvokeActor {
	return InvokeActor{ActorType: "system", WorkspaceID: workspaceID}
}

// AgentInvokePermitted reports whether actor may trigger a run for agent.
//
// q is taken as a parameter (rather than read off a service struct) so the
// caller's transaction-scoped *db.Queries is used when there is one: the
// workflow router runs inside the Step-activation transaction, and reading
// permission on a different connection could see an allow-list the surrounding
// transaction has not observed.
func AgentInvokePermitted(ctx context.Context, q *db.Queries, agent db.Agent, actor InvokeActor) bool {
	effectiveUser := actor.ActorID
	if actor.ActorType != "member" {
		// agent / system: never trust the immediate principal, only the resolved
		// human originator at the top of the chain.
		effectiveUser = actor.OriginatorUserID
	}

	if effectiveUser != "" && util.UUIDToString(agent.OwnerID) == effectiveUser {
		return true
	}

	if agent.PermissionMode != "public_to" {
		return false
	}

	targets, err := q.ListAgentInvocationTargets(ctx, agent.ID)
	if err != nil {
		return false
	}

	workspaceBroad := actor.ActorType == "agent" || actor.ActorType == "system"
	isWorkspaceMember := false
	if effectiveUser != "" {
		userUUID, uerr := util.ParseUUID(effectiveUser)
		wsUUID, werr := util.ParseUUID(actor.WorkspaceID)
		if uerr == nil && werr == nil {
			if _, err := q.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{
				UserID:      userUUID,
				WorkspaceID: wsUUID,
			}); err == nil {
				isWorkspaceMember = true
			}
		}
	}

	for _, t := range targets {
		switch t.TargetType {
		case "workspace":
			if isWorkspaceMember || workspaceBroad {
				return true
			}
		case "member":
			// Requires a resolved human. An agent/system trigger with no
			// originator never matches here - fail closed.
			if effectiveUser != "" && util.UUIDToString(t.TargetID) == effectiveUser {
				return true
			}
		case "team":
			// Reserved: team membership does not exist in V1, so team targets
			// admit nobody (also fail-closed for system/agent).
		}
	}
	return false
}

// invokeActorForRun resolves the invoking principal for a workflow Run.
//
// A Run started by a human carries that human as accountable_user_id and is
// judged as that member. A Run with no accountable human (an autopilot schedule,
// an external intake with no attributable user) is judged as a system principal,
// which per the rule above can only reach a `public_to workspace` agent. That is
// the correct posture: an unattended Run must not be able to spend a private
// agent's credentials, and a workflow that quietly ran the wrong agent is worse
// than one that stops and says routing found no candidate.
func invokeActorForRun(workspaceID, accountableUserID pgtype.UUID) InvokeActor {
	ws := util.UUIDToString(workspaceID)
	if accountableUserID.Valid {
		return MemberInvokeActor(util.UUIDToString(accountableUserID), ws)
	}
	return SystemInvokeActor(ws)
}
