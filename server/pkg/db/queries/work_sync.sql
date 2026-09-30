-- name: LockWorkSyncScope :one
SELECT * FROM work_sync_scope WHERE workspace_id = $1 FOR UPDATE;

-- name: GetWorkSyncScope :one
SELECT * FROM work_sync_scope WHERE workspace_id = $1;

-- name: ListWorkSyncChanges :many
SELECT * FROM work_sync_change
WHERE workspace_id = $1 AND sequence > $2
ORDER BY sequence LIMIT $3;

-- name: GetWorkSyncCurrent :one
SELECT * FROM work_sync_change
WHERE workspace_id = $1 AND kind = $2 AND entity_id = $3
ORDER BY sequence DESC LIMIT 1;

-- name: GetWorkSyncBase :one
SELECT * FROM work_sync_change
WHERE workspace_id = $1 AND kind = $2 AND entity_id = $3 AND sequence = $4;

-- name: ListWorkSyncSnapshot :many
SELECT DISTINCT ON (kind, entity_id) * FROM work_sync_change
WHERE workspace_id = $1
ORDER BY kind, entity_id, sequence DESC LIMIT $2;

-- name: GetWorkSyncReceipt :one
SELECT * FROM work_sync_receipt WHERE workspace_id = $1 AND operation_id = $2;

-- name: GetWorkSyncNodeSequence :one
SELECT COALESCE(max(sequence), 0)::bigint FROM work_sync_receipt
WHERE workspace_id = $1 AND node_id = $2 AND incarnation = $3;

-- name: SaveWorkSyncReceipt :exec
INSERT INTO work_sync_receipt(workspace_id, operation_id, node_id, incarnation, sequence, actor_id, payload_hash, receipt)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8);
