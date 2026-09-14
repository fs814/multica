package workflow

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const ExecutionPublished = "published"
const ExecutionDraftTest = "draft_test"

// ResolveRunDefinition is the only graph-source resolver for an existing run.
// Callers must authorize and lock the identity, then check terminal state before
// calling it to advance a graph. History readers may read retained terminal runs.
// A purged snapshot never falls back to a template's current version.
func ResolveRunDefinition(ctx context.Context, q *db.Queries, workspaceID pgtype.UUID, run db.WorkflowRun) (*Definition, error) {
	if run.WorkspaceID != workspaceID {
		return nil, newEngineError(ErrCodeNotFound, "workflow run not found")
	}
	if run.DetailsPurgedAt.Valid {
		return nil, newEngineError("debug_details_expired", "draft trial details have expired")
	}
	var raw []byte
	switch run.ExecutionMode {
	case ExecutionPublished:
		if !run.TemplateVersionID.Valid || run.ExecutionSnapshotID.Valid {
			return nil, newEngineError(ErrCodeInvariantViolation, "invalid published execution source")
		}
		version, err := q.GetWorkflowTemplateVersion(ctx, db.GetWorkflowTemplateVersionParams{ID: run.TemplateVersionID, WorkspaceID: workspaceID})
		if err != nil {
			return nil, fmt.Errorf("load pinned version: %w", err)
		}
		if version.TemplateID != run.TemplateID || version.Status != "published" {
			return nil, newEngineError(ErrCodeInvariantViolation, "invalid pinned version ownership or status")
		}
		raw = version.Definition
	case ExecutionDraftTest:
		if run.TemplateVersionID.Valid || !run.ExecutionSnapshotID.Valid {
			return nil, newEngineError(ErrCodeInvariantViolation, "invalid draft trial execution source")
		}
		snapshot, err := q.GetWorkflowExecutionSnapshot(ctx, db.GetWorkflowExecutionSnapshotParams{ID: run.ExecutionSnapshotID, WorkspaceID: workspaceID})
		if err != nil {
			return nil, fmt.Errorf("load execution snapshot: %w", err)
		}
		if snapshot.TemplateID != run.TemplateID || snapshot.PurgedAt.Valid || len(snapshot.Definition) == 0 {
			return nil, newEngineError(ErrCodeInvariantViolation, "execution snapshot is unavailable")
		}
		raw = snapshot.Definition
	default:
		return nil, newEngineError(ErrCodeInvariantViolation, "unknown execution source")
	}
	return ParseDefinition(raw)
}
