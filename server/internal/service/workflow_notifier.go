package service

import (
	"context"
	"log/slog"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/internal/workflow"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// WorkflowNotifier is the engine's post-commit notification sink.
//
// It satisfies workflow.Notifier. The engine collects notifications during a
// command and flushes them only after the transaction commits (txEffects), so
// everything here runs against state that is durably visible: a client told to
// refetch will see the change, and a daemon told a task is waiting will find it.
//
// All methods are best-effort and never return an error. The state change they
// announce has already committed; failing here cannot un-commit it, and the
// daemon's poll loop plus the client's own refetch are the fallbacks. Reporting
// an error to the engine would only give it the option to do something wrong.
type WorkflowNotifier struct {
	// Bus fans the run-changed event out to subscribed clients. Nil is valid and
	// silently disables realtime invalidation - the UI then updates on its next
	// poll rather than immediately.
	Bus *events.Bus
	// Tasks reuses TaskService's enqueue notification (empty-claim cache
	// invalidation plus the daemon websocket wakeup) rather than reimplementing
	// it. That is the same path AutopilotService.dispatchRunOnly uses for a task
	// it inserted directly, which is exactly this situation: the engine wrote the
	// row itself, inside its own transaction, so the notification has to be
	// driven from outside TaskService's own enqueue helpers.
	//
	// Nil disables the wakeup; the daemon still finds the task on its next poll,
	// so a workflow Step would start late rather than never.
	Tasks *TaskService
}

// NewWorkflowNotifier builds the notifier wired into the engine in
// cmd/server/router.go.
func NewWorkflowNotifier(bus *events.Bus, tasks *TaskService) *WorkflowNotifier {
	return &WorkflowNotifier{Bus: bus, Tasks: tasks}
}

// WorkflowEvent publishes the exact durable envelope written by the engine.
// Consumers use it as a realtime hint, then GET the authoritative Run state.
func (n *WorkflowNotifier) WorkflowEvent(ctx context.Context, event workflow.EventEnvelope) {
	if n.Bus == nil || event.WorkspaceID == "" {
		return
	}
	n.Bus.Publish(events.Event{
		Type:        protocol.EventWorkflowEvent,
		WorkspaceID: event.WorkspaceID,
		ActorType:   "system",
		Payload:     event,
	})
}

// WorkflowChanged publishes the "this Run changed, refetch it" signal.
//
// One coarse event rather than one per transition: a single engine command can
// move a Step through activated -> queued and then activate the next node, and a
// client that has to reassemble a Run from a stream of fine-grained deltas will
// disagree with the server the first time an event is dropped or reordered. The
// payload carries only ids; the client refetches the Run, which cannot drift.
func (n *WorkflowNotifier) WorkflowChanged(ctx context.Context, workspaceID, runID string) {
	if n.Bus == nil || workspaceID == "" {
		return
	}
	n.Bus.Publish(events.Event{
		Type:        protocol.EventWorkflowRunChanged,
		WorkspaceID: workspaceID,
		ActorType:   "system",
		Payload: map[string]any{
			"run_id": runID,
		},
	})
}

// IssueChanged publishes the canonical issue:updated invalidation after the
// engine commits a Run-to-Issue status projection. The payload matches the
// background Issue update contract used by TaskService so board columns, status
// filters, and counts reconcile immediately.
func (n *WorkflowNotifier) IssueChanged(ctx context.Context, issue db.Issue, prevStatus string) {
	if n.Bus == nil || !issue.WorkspaceID.Valid {
		return
	}
	prefix := ""
	if n.Tasks != nil {
		prefix = n.Tasks.getIssuePrefix(issue.WorkspaceID)
	}
	n.Bus.Publish(events.Event{
		Type:        protocol.EventIssueUpdated,
		WorkspaceID: util.UUIDToString(issue.WorkspaceID),
		ActorType:   "system",
		ActorID:     "",
		Payload: map[string]any{
			"issue":          IssueToMap(issue, prefix),
			"status_changed": prevStatus != issue.Status,
			"prev_status":    prevStatus,
			"source":         "workflow_run",
		},
	})
}

// TaskEnqueued wakes the daemon that owns the runtime a Step's task was queued
// on.
//
// Called only after the activation transaction commits, so the daemon cannot
// claim a task the transaction later rolled back - claiming a phantom task would
// leave the daemon holding a lease on a row that no longer exists.
func (n *WorkflowNotifier) TaskEnqueued(ctx context.Context, task db.AgentTaskQueue) {
	if n.Tasks == nil {
		slog.Debug("workflow: no task service wired, skipping daemon wakeup",
			"task_id", util.UUIDToString(task.ID))
		return
	}
	n.Tasks.NotifyTaskEnqueued(ctx, task)
}
