package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// advanceHTTPAssignmentIssue runs inside the task lifecycle transaction. The
// SQL also checks provider, current assignee and status while holding the issue
// row lock, so a human's cancellation/reassignment cannot be overwritten.
func advanceHTTPAssignmentIssue(ctx context.Context, q *db.Queries, task db.AgentTaskQueue, result []byte) (*db.AdvanceHTTPAssignmentIssueStatusRow, error) {
	if !task.IssueID.Valid || task.TriggerCommentID.Valid || task.ChatSessionID.Valid || task.AutopilotRunID.Valid || task.WorkflowStepInstanceID.Valid || task.IsLeaderTask || task.SquadID.Valid || task.HandoffNote.String != "" {
		return nil, nil
	}
	params := db.AdvanceHTTPAssignmentIssueStatusParams{TaskID: task.ID, TaskStatus: task.Status}
	switch task.Status {
	case "running":
		params.PreviousStatuses = []string{"todo", "backlog"}
		params.NextStatus = "in_progress"
	case "completed":
		var payload protocol.TaskCompletedPayload
		if err := json.Unmarshal(result, &payload); err != nil || strings.TrimSpace(payload.Output) == "" {
			return nil, nil
		}
		params.PreviousStatuses = []string{"in_progress"}
		params.NextStatus = "in_review"
	default:
		return nil, nil
	}
	changed, err := q.AdvanceHTTPAssignmentIssueStatus(ctx, params)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("advance HTTP assignment issue status: %w", err)
	}
	return &changed, nil
}
