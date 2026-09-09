-- name: ListWorkflowInputInstances :many
SELECT * FROM workflow_input_instance
WHERE workspace_id = $1 AND template_id = $2
ORDER BY updated_at DESC, id;

-- name: CreateWorkflowInputInstance :one
INSERT INTO workflow_input_instance (workspace_id, template_id, name, input, project_id, template_version_id, created_by_id)
VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING *;

-- name: UpdateWorkflowInputInstance :one
UPDATE workflow_input_instance
SET name = $4, input = $5, project_id = $6, template_version_id = sqlc.narg(template_version_id)::uuid, revision = revision + 1, updated_at = now()
WHERE id = $1 AND workspace_id = $2 AND template_id = $3
  AND revision = sqlc.arg(expected_revision)::bigint
RETURNING *;

-- name: DeleteWorkflowInputInstance :execrows
DELETE FROM workflow_input_instance
WHERE id = $1 AND workspace_id = $2 AND template_id = $3;
