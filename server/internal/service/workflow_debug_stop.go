package service

import (
	"context"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// RequestWorkflowDebugStop deliberately rebroadcasts to the original execution
// runtime after cancellation has already committed. A terminal task row is not
// physical stop proof, and an offline daemon may miss any earlier notification.
func (s *TaskService) RequestWorkflowDebugStop(ctx context.Context, task db.AgentTaskQueue, runtimeID pgtype.UUID) error {
	if task.Status != "completed" && task.Status != "failed" && task.Status != "cancelled" {
		if _, err := s.CancelTask(ctx, task.ID); err != nil {
			return err
		}
	}
	task.RuntimeID = runtimeID
	s.broadcastTaskEvent(ctx, protocol.EventTaskCancelled, task)
	return nil
}
