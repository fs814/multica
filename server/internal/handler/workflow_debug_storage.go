package handler

import (
	"context"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/workflow"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func (h *Handler) isWorkflowDebugAttachment(a db.Attachment) bool {
	if !a.TaskID.Valid {
		return false
	}
	run, err := h.Queries.GetWorkflowDebugTaskRun(context.Background(), a.TaskID)
	return err == nil && run.ExecutionMode == workflow.ExecutionDraftTest
}
func (h *Handler) DeleteDebugObject(ctx context.Context, object db.WorkflowDebugCleanupObject) error {
	if object.ObjectKind != "attachment" {
		return fmt.Errorf("unsupported debug cleanup object kind")
	}
	// This private reader is intentionally separate from public tombstone-aware
	// attachment queries: the manifest still needs the object locator to delete it.
	att, err := h.Queries.GetWorkflowDebugCleanupAttachment(ctx, db.GetWorkflowDebugCleanupAttachmentParams{ID: object.ObjectID, WorkspaceID: object.WorkspaceID})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if att.IssueID.Valid || att.CommentID.Valid || att.ChatSessionID.Valid || att.ChatMessageID.Valid || att.SourceContextID.Valid {
		return h.Queries.UnlinkWorkflowDebugSharedAttachments(ctx, db.UnlinkWorkflowDebugSharedAttachmentsParams{RunID: object.RunID, WorkspaceID: object.WorkspaceID})
	}
	if h.Storage == nil {
		return fmt.Errorf("object storage unavailable")
	}
	if err = h.Storage.DeleteObject(ctx, h.Storage.KeyFromURL(att.Url)); err != nil {
		return err
	}
	// Debug download URLs always use the authenticated/proxied endpoint, so the
	// tombstone check revoked access before this external delete began.
	_, err = h.Queries.DeleteAttachment(ctx, db.DeleteAttachmentParams{ID: att.ID, WorkspaceID: att.WorkspaceID})
	return err
}
