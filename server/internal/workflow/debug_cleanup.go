package workflow

import (
	"context"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// DebugObjectDeleter must confirm object deletion and expiry/revocation of all
// existing download capabilities before returning nil. Fire-and-forget storage
// deletion is not sufficient to complete the durable cleanup manifest.
type DebugObjectDeleter func(context.Context, db.WorkflowDebugCleanupObject) error

func (e *Engine) PurgeDebugRun(ctx context.Context, ws, runID pgtype.UUID) error {
	if err := e.MaintainDebugRun(ctx, ws, runID); err != nil {
		return err
	}
	err := e.runInTx(ctx, &txEffects{}, func(ctx context.Context, q *db.Queries) error {
		if _, err := q.LockWorkflowDebugQuota(ctx, ws); err != nil {
			return err
		}
		run, err := q.GetWorkflowRunForUpdate(ctx, db.GetWorkflowRunForUpdateParams{ID: runID, WorkspaceID: ws})
		if err != nil {
			return err
		}
		if run.ExecutionMode != ExecutionDraftTest || run.DetailsPurgedAt.Valid || !IsTerminalRunStatus(RunStatus(run.Status)) {
			return nil
		}
		now, err := q.WorkflowDebugDatabaseTime(ctx)
		if err != nil {
			return err
		}
		if !run.DebugPurgeAfter.Valid || run.DebugPurgeAfter.Time.After(now.Time) {
			return nil
		}
		if _, err = q.ListWorkflowStepsForUpdate(ctx, db.ListWorkflowStepsForUpdateParams{RunID: runID, WorkspaceID: ws}); err != nil {
			return err
		}
		tasks, err := q.LockWorkflowDebugTasksForRun(ctx, db.LockWorkflowDebugTasksForRunParams{RunID: runID, WorkspaceID: ws})
		if err != nil {
			return err
		}
		claims, err := q.ListWorkflowDebugExecutions(ctx, db.ListWorkflowDebugExecutionsParams{RunID: runID, WorkspaceID: ws})
		if err != nil {
			return err
		}
		proof := map[pgtype.UUID]bool{}
		for _, claim := range claims {
			locked, err := q.GetWorkflowDebugExecutionForUpdate(ctx, db.GetWorkflowDebugExecutionForUpdateParams{ID: claim.ID, WorkspaceID: ws})
			if err != nil {
				return err
			}
			if !locked.DeliveryDrainedAt.Valid {
				return q.SetWorkflowDebugWaitingStop(ctx, db.SetWorkflowDebugWaitingStopParams{ID: runID, WorkspaceID: ws})
			}
			proof[claim.TaskID] = true
			uploads, err := q.CountWorkflowDebugPendingUploads(ctx, claim.ID)
			if err != nil {
				return err
			}
			if uploads > 0 {
				return q.SetWorkflowDebugWaitingStop(ctx, db.SetWorkflowDebugWaitingStopParams{ID: runID, WorkspaceID: ws})
			}
		}
		for _, task := range tasks {
			if !proof[task.ID] && !task.DebugNeverDispatchedAt.Valid {
				return q.SetWorkflowDebugWaitingStop(ctx, db.SetWorkflowDebugWaitingStopParams{ID: runID, WorkspaceID: ws})
			}
		}
		// The CAS, payload erasure, object manifest and byte decrement share this
		// transaction. Any fault leaves all four unchanged.
		if _, err = q.MarkWorkflowDebugDatabasePurged(ctx, db.MarkWorkflowDebugDatabasePurgedParams{ID: runID, WorkspaceID: ws}); errors.Is(err, pgx.ErrNoRows) {
			return nil
		} else if err != nil {
			return err
		}
		if err = q.QueueWorkflowDebugAttachmentCleanup(ctx, db.QueueWorkflowDebugAttachmentCleanupParams{RunID: runID, WorkspaceID: ws}); err != nil {
			return err
		}
		if err = q.UnlinkWorkflowDebugSharedAttachments(ctx, db.UnlinkWorkflowDebugSharedAttachmentsParams{RunID: runID, WorkspaceID: ws}); err != nil {
			return err
		}
		if err = q.PurgeWorkflowDebugSnapshot(ctx, db.PurgeWorkflowDebugSnapshotParams{ID: run.ExecutionSnapshotID, WorkspaceID: ws}); err != nil {
			return err
		}
		if err = q.PurgeWorkflowDebugTasks(ctx, db.PurgeWorkflowDebugTasksParams{RunID: runID, WorkspaceID: ws}); err != nil {
			return err
		}
		if err = q.PurgeWorkflowDebugSubmissions(ctx, db.PurgeWorkflowDebugSubmissionsParams{RunID: runID, WorkspaceID: ws}); err != nil {
			return err
		}
		if err = q.PurgeWorkflowDebugAcceptances(ctx, db.PurgeWorkflowDebugAcceptancesParams{RunID: runID, WorkspaceID: ws}); err != nil {
			return err
		}
		if err = q.PurgeWorkflowDebugEvents(ctx, db.PurgeWorkflowDebugEventsParams{RunID: runID, WorkspaceID: ws}); err != nil {
			return err
		}
		if err = q.PurgeWorkflowDebugMessages(ctx, db.PurgeWorkflowDebugMessagesParams{RunID: runID, WorkspaceID: ws}); err != nil {
			return err
		}
		if err = q.PurgeWorkflowDebugSteps(ctx, db.PurgeWorkflowDebugStepsParams{RunID: runID, WorkspaceID: ws}); err != nil {
			return err
		}
		n, err := q.AddWorkflowDebugPayloadBytes(ctx, db.AddWorkflowDebugPayloadBytesParams{WorkspaceID: ws, Bytes: -run.DebugPayloadBytes.Int64})
		if err != nil {
			return err
		}
		if n != 1 {
			return fmt.Errorf("draft trial payload accounting invariant violated")
		}
		return nil
	})
	if err != nil {
		return err
	}
	objects, err := e.Queries.ListWorkflowDebugCleanupObjects(ctx, db.ListWorkflowDebugCleanupObjectsParams{RunID: runID, WorkspaceID: ws})
	if err != nil {
		return err
	}
	for _, object := range objects {
		if e.DeleteDebugObject == nil {
			continue
		}
		if err = e.DeleteDebugObject(ctx, object); err != nil {
			if retryErr := e.Queries.RetryWorkflowDebugCleanupObject(ctx, db.RetryWorkflowDebugCleanupObjectParams{ID: object.ID, WorkspaceID: ws}); retryErr != nil {
				return retryErr
			}
			continue
		}
		if err = e.Queries.CompleteWorkflowDebugCleanupObject(ctx, db.CompleteWorkflowDebugCleanupObjectParams{ID: object.ID, WorkspaceID: ws}); err != nil {
			return err
		}
	}
	_, err = e.Queries.CompleteWorkflowDebugPurge(ctx, db.CompleteWorkflowDebugPurgeParams{ID: runID, WorkspaceID: ws})
	return err
}
