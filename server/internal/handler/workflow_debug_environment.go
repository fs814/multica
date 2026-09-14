package handler

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/workflow"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"net/url"
)

type workflowDebugEnvironment struct {
	ProjectID          string                `json:"project_id,omitempty"`
	ProjectTitle       string                `json:"project_title,omitempty"`
	ProjectDescription string                `json:"project_description,omitempty"`
	Resources          []ProjectResourceData `json:"resources"`
	Repos              []RepoData            `json:"repos"`
}

func (h *Handler) ResolveDraftEnvironment(ctx context.Context, q *db.Queries, ws, user pgtype.UUID, project *string) (json.RawMessage, error) {
	var projectID pgtype.UUID
	if project != nil {
		if err := projectID.Scan(*project); err != nil {
			return nil, &workflow.EngineError{Code: "debug_bad_request", Message: "invalid project id"}
		}
		if _, err := q.GetProjectInWorkspace(ctx, db.GetProjectInWorkspaceParams{ID: projectID, WorkspaceID: ws}); errors.Is(err, pgx.ErrNoRows) {
			return nil, &workflow.EngineError{Code: workflow.ErrCodeNotFound, Message: "project not found"}
		} else if err != nil {
			return nil, err
		}
	}
	// Reuse the existing resource interpretation with the transaction's queries;
	// reject a missing project above so it cannot fall back to workspace resources.
	scoped := *h
	scoped.Queries = q
	resolved, err := scoped.resolveClaimProjectContext(ctx, projectID, ws)
	if err != nil {
		return nil, err
	}
	if resolved.Resources == nil {
		resolved.Resources = []ProjectResourceData{}
	}
	if resolved.Repos == nil {
		resolved.Repos = []RepoData{}
	}
	for i, resource := range resolved.Resources {
		if resource.ResourceType == "local_directory" {
			var ref map[string]any
			if err := json.Unmarshal(resource.ResourceRef, &ref); err != nil {
				return nil, err
			}
			mode, _ := ref["execution_mode"].(string)
			if mode == "" {
				ref["execution_mode"] = "in_place"
			} else if mode != "in_place" && mode != "worktree" {
				return nil, &workflow.EngineError{Code: workflow.ErrCodeInvalidDefinition, Message: "unsupported local directory execution mode"}
			}
			normalized, err := json.Marshal(ref)
			if err != nil {
				return nil, err
			}
			resolved.Resources[i].ResourceRef = normalized
		}
	}
	for _, repo := range resolved.Repos {
		parsed, err := url.Parse(repo.URL)
		if err != nil || parsed.User != nil || parsed.RawQuery != "" {
			return nil, &workflow.EngineError{Code: workflow.ErrCodeInvalidDefinition, Message: "repository reference must not contain credentials or signed query parameters"}
		}
	}
	return json.Marshal(workflowDebugEnvironment{resolved.ProjectID, resolved.Title, resolved.Description, resolved.Resources, resolved.Repos})
}
func (h *Handler) RevalidateDebugEnvironment(ctx context.Context, q *db.Queries, run db.WorkflowRun) error {
	snapshot, err := q.GetWorkflowExecutionSnapshot(ctx, db.GetWorkflowExecutionSnapshotParams{ID: run.ExecutionSnapshotID, WorkspaceID: run.WorkspaceID})
	if err != nil {
		return err
	}
	var env workflowDebugEnvironment
	if snapshot.PurgedAt.Valid || json.Unmarshal(snapshot.EnvironmentSnapshot, &env) != nil {
		return &workflow.EngineError{Code: workflow.ErrCodeInvariantViolation, Message: "trial environment is unavailable"}
	}
	if _, err = q.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{UserID: run.AccountableUserID, WorkspaceID: run.WorkspaceID}); err != nil {
		return &workflow.EngineError{Code: "debug_forbidden", Message: "trial resource access was revoked"}
	}
	if env.ProjectID != "" {
		var projectID pgtype.UUID
		if err = projectID.Scan(env.ProjectID); err != nil {
			return err
		}
		if _, err = q.GetProjectInWorkspace(ctx, db.GetProjectInWorkspaceParams{ID: projectID, WorkspaceID: run.WorkspaceID}); err != nil {
			return &workflow.EngineError{Code: "debug_forbidden", Message: "trial project is no longer accessible"}
		}
		resources, err := q.ListProjectResourcesInWorkspace(ctx, db.ListProjectResourcesInWorkspaceParams{ProjectID: projectID, WorkspaceID: run.WorkspaceID})
		if err != nil {
			return err
		}
		available := map[string]bool{}
		for _, resource := range resources {
			available[uuidToString(resource.ID)] = true
		}
		for _, resource := range env.Resources {
			if !available[resource.ID] {
				return &workflow.EngineError{Code: "debug_forbidden", Message: "trial resource is no longer accessible"}
			}
		}
	}
	// The engine separately revalidates the pinned image attachment identity.
	return nil
}
func (h *Handler) applyDebugEnvironment(ctx context.Context, task db.AgentTaskQueue, resp *AgentTaskResponse) error {
	run, err := h.Queries.GetWorkflowDebugTaskRun(ctx, task.ID)
	if err != nil {
		return err
	}
	if err = h.RevalidateDebugEnvironment(ctx, h.Queries, run); err != nil {
		return err
	}
	snapshot, err := h.Queries.GetWorkflowExecutionSnapshot(ctx, db.GetWorkflowExecutionSnapshotParams{ID: run.ExecutionSnapshotID, WorkspaceID: run.WorkspaceID})
	if err != nil {
		return err
	}
	var env workflowDebugEnvironment
	if err = json.Unmarshal(snapshot.EnvironmentSnapshot, &env); err != nil {
		return err
	}
	resp.Repos = env.Repos
	resp.ProjectResources = env.Resources
	resp.ProjectID = env.ProjectID
	resp.ProjectTitle = env.ProjectTitle
	resp.ProjectDescription = env.ProjectDescription
	resp.WorkflowPrompt = "Draft trial run " + uuidToString(run.ID) + ". This is real execution against the confirmed resources. Submit the result only for this workflow step. The platform does not create an issue for this run.\n\n" + resp.WorkflowPrompt
	return nil
}
