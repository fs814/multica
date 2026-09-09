-- name: AdvanceHTTPAssignmentIssueStatus :one
-- The row lock and status/assignee guards preserve concurrent human changes.
-- This writes no assignment event, so lifecycle synchronization cannot enqueue
-- another run. The service publishes issue:updated after the transaction commits.
WITH eligible AS (
    SELECT issue.id, issue.status
    FROM issue
    JOIN agent_task_queue task ON task.issue_id = issue.id
    JOIN agent_runtime runtime ON runtime.id = task.runtime_id
    WHERE task.id = sqlc.arg(task_id)
      AND runtime.provider = 'knot-http'
      AND runtime.workspace_id = issue.workspace_id
      AND issue.assignee_type = 'agent'
      AND issue.assignee_id = task.agent_id
      AND task.trigger_comment_id IS NULL
      AND task.chat_session_id IS NULL
      AND task.autopilot_run_id IS NULL
      AND task.workflow_step_instance_id IS NULL
      AND task.is_leader_task = false
      AND task.squad_id IS NULL
      AND COALESCE(task.handoff_note, '') = ''
      AND COALESCE(task.context->>'type', '') <> 'quick_create'
      AND task.status = sqlc.arg(task_status)
      AND issue.status = ANY(sqlc.arg(previous_statuses)::text[])
    FOR UPDATE OF issue
)
UPDATE issue
SET status = sqlc.arg(next_status), updated_at = now()
FROM eligible
WHERE issue.id = eligible.id
RETURNING sqlc.embed(issue), eligible.status AS previous_status;