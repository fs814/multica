ALTER TABLE workflow_input_instance
 ADD COLUMN description text NOT NULL DEFAULT '',
 ADD COLUMN input_node jsonb,
 ADD COLUMN image_attachment_id text,
 ADD COLUMN updated_by_id uuid,
 ADD COLUMN archived_at timestamptz,
 ADD COLUMN idempotency_key text,
 ADD COLUMN request_hash text;
ALTER TABLE workflow_run
 ADD COLUMN input_instance_id uuid,
 ADD COLUMN input_instance_revision bigint,
 ADD COLUMN input_instance_name text,
 ADD COLUMN input_source text,
 ADD COLUMN input_project_id uuid;
