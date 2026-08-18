-- name: CreateWorkflowCallbackDestination :one
INSERT INTO workflow_callback_destination (
    workspace_id, name, url, signing_secret_encrypted, created_by_user_id
) VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: GetWorkflowCallbackDestination :one
SELECT * FROM workflow_callback_destination
WHERE id = $1 AND workspace_id = $2;

-- name: ListWorkflowCallbackDestinations :many
SELECT * FROM workflow_callback_destination
WHERE workspace_id = $1
ORDER BY created_at DESC;

-- name: CreateWorkflowCallbackDelivery :one
INSERT INTO workflow_callback_delivery (
    workspace_id, destination_id, workflow_run_id, event_type, event_key, payload
) VALUES ($1, $2, $3, $4, $5, sqlc.arg('payload')::jsonb)
ON CONFLICT (workspace_id, destination_id, event_key) DO NOTHING
RETURNING *;

-- name: ClaimQueuedWorkflowCallbackDelivery :one
WITH candidate AS (
    SELECT id
    FROM workflow_callback_delivery
    WHERE status = 'queued' AND available_at <= now()
    ORDER BY available_at, created_at
    FOR UPDATE SKIP LOCKED
    LIMIT 1
)
UPDATE workflow_callback_delivery d
SET status = 'dispatching',
    attempt_count = d.attempt_count + 1,
    lease_token = gen_random_uuid(),
    lease_expires_at = now() + interval '2 minutes',
    updated_at = now()
FROM candidate
WHERE d.id = candidate.id
RETURNING d.*;

-- name: ReclaimExpiredWorkflowCallbackDeliveries :execrows
UPDATE workflow_callback_delivery
SET status = 'queued', lease_token = NULL, lease_expires_at = NULL,
    available_at = now(), updated_at = now()
WHERE status = 'dispatching' AND lease_expires_at < now();

-- name: RetryClaimedWorkflowCallbackDelivery :one
UPDATE workflow_callback_delivery
SET status = 'queued', available_at = $3, error = $4,
    lease_token = NULL, lease_expires_at = NULL, updated_at = now()
WHERE id = $1 AND lease_token = $2 AND status = 'dispatching'
RETURNING *;

-- name: CompleteClaimedWorkflowCallbackDelivery :one
UPDATE workflow_callback_delivery
SET status = $3, response_status = sqlc.narg('response_status'),
    response_body = sqlc.narg('response_body'), error = sqlc.narg('error'),
    delivered_at = CASE WHEN $3 = 'delivered' THEN now() ELSE delivered_at END,
    lease_token = NULL, lease_expires_at = NULL, updated_at = now()
WHERE id = $1 AND lease_token = $2 AND status = 'dispatching'
RETURNING *;

-- name: GetWorkflowCallbackDelivery :one
SELECT * FROM workflow_callback_delivery
WHERE id = $1 AND workspace_id = $2;

-- name: ReplayWorkflowCallbackDelivery :one
UPDATE workflow_callback_delivery
SET status = 'queued', attempt_count = 0, available_at = now(),
    lease_token = NULL, lease_expires_at = NULL, response_status = NULL,
    response_body = NULL, error = NULL, delivered_at = NULL, updated_at = now()
WHERE id = $1 AND workspace_id = $2
  AND status IN ('delivered', 'failed')
RETURNING *;

-- name: ListWorkflowCallbackDeliveriesForRun :many
SELECT * FROM workflow_callback_delivery
WHERE workflow_run_id = $1 AND workspace_id = $2
ORDER BY created_at DESC;
