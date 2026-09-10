-- name: ListWorkflowInputInstances :many
SELECT * FROM workflow_input_instance
WHERE workspace_id = $1 AND template_id = $2
AND archived_at IS NULL
ORDER BY updated_at DESC, id;

-- name: CreateWorkflowInputInstance :one
INSERT INTO workflow_input_instance (workspace_id, template_id, name, input, project_id, template_version_id, created_by_id, description, input_node, image_attachment_id, updated_by_id, idempotency_key, request_hash)
VALUES ($1, $2, $3, $4, $5, $6, $7, COALESCE(sqlc.narg(description)::text, ''), sqlc.narg(input_node)::jsonb,
 sqlc.narg(image_attachment_id)::text, $7, sqlc.narg(idempotency_key)::text, sqlc.narg(request_hash)::text)
ON CONFLICT (workspace_id, idempotency_key) WHERE idempotency_key IS NOT NULL
DO UPDATE SET id = workflow_input_instance.id
RETURNING *;

-- name: UpdateWorkflowInputInstance :one
UPDATE workflow_input_instance
SET name = $4, input = $5, project_id = $6, template_version_id = sqlc.narg(template_version_id)::uuid, description = COALESCE(sqlc.narg(description)::text, ''),
 input_node = sqlc.narg(input_node)::jsonb, image_attachment_id = sqlc.narg(image_attachment_id)::text,
 updated_by_id = sqlc.narg(updated_by_id)::uuid, revision = revision + 1, updated_at = now()
WHERE id = $1 AND workspace_id = $2 AND template_id = $3
  AND revision = sqlc.arg(expected_revision)::bigint AND archived_at IS NULL
RETURNING *;

-- name: DeleteWorkflowInputInstance :execrows
UPDATE workflow_input_instance SET archived_at = now(), revision = revision + 1, updated_at = now()
WHERE id = $1 AND workspace_id = $2 AND template_id = $3;


-- name: GetWorkflowInputInstance :one
SELECT * FROM workflow_input_instance WHERE workspace_id = $1 AND id = $2;

-- name: LockWorkflowInputInstance :one
SELECT * FROM workflow_input_instance WHERE workspace_id = $1 AND id = $2 FOR UPDATE;

-- name: BrowseWorkflowInputInstances :many
SELECT sqlc.embed(i), COALESCE(t.name, '')::text AS template_name,
 COALESCE(v.version, 0)::int AS version_number,
 COALESCE(u.name, '')::text AS editor_name,
 COALESCE(recent.id::text, '')::text AS latest_run_id,
 COALESCE(recent.status, '')::text AS latest_run_status
FROM workflow_input_instance i
LEFT JOIN workflow_template t ON t.id=i.template_id AND t.workspace_id=i.workspace_id
LEFT JOIN workflow_template_version v ON v.id=i.template_version_id AND v.workspace_id=i.workspace_id AND v.template_id=i.template_id
LEFT JOIN member m ON m.user_id=COALESCE(i.updated_by_id,i.created_by_id) AND m.workspace_id=i.workspace_id
LEFT JOIN "user" u ON u.id=m.user_id
LEFT JOIN LATERAL (
 SELECT r.id,r.status FROM workflow_run r
 WHERE r.workspace_id=i.workspace_id AND r.input_instance_id=i.id
 ORDER BY r.created_at DESC,r.id LIMIT 1
) recent ON true
WHERE i.workspace_id = $1
 AND (sqlc.narg(template_id)::uuid IS NULL OR i.template_id = sqlc.narg(template_id)::uuid)
 AND (sqlc.arg(include_archived)::boolean OR i.archived_at IS NULL)
 AND (sqlc.arg(search)::text = '' OR i.name ILIKE '%' || sqlc.arg(search)::text || '%')
ORDER BY i.updated_at DESC, i.id
LIMIT sqlc.arg(limit_count)::int OFFSET sqlc.arg(offset_count)::int;

-- name: CountWorkflowInputInstances :one
SELECT count(*) FROM workflow_input_instance
WHERE workspace_id = $1
 AND (sqlc.narg(template_id)::uuid IS NULL OR template_id = sqlc.narg(template_id)::uuid)
 AND (sqlc.arg(include_archived)::boolean OR archived_at IS NULL)
 AND (sqlc.arg(search)::text = '' OR name ILIKE '%' || sqlc.arg(search)::text || '%');

-- name: ArchiveWorkflowInputInstance :one
UPDATE workflow_input_instance SET archived_at = CASE WHEN sqlc.arg(archive)::boolean THEN now() ELSE NULL END,
 revision = revision + 1, updated_at = now(), updated_by_id = sqlc.arg(updated_by_id)::uuid
WHERE workspace_id = $1 AND id = $2 AND revision = sqlc.arg(expected_revision)::bigint
RETURNING *;

-- name: ListWorkflowInstanceRuns :many
SELECT * FROM workflow_run WHERE workspace_id = $1 AND input_instance_id = $2
ORDER BY created_at DESC, id LIMIT sqlc.arg(limit_count)::int OFFSET sqlc.arg(offset_count)::int;

-- name: CountWorkflowInstanceRuns :one
SELECT count(*) FROM workflow_run WHERE workspace_id = $1 AND input_instance_id = $2;
