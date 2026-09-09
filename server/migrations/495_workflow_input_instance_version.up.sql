ALTER TABLE workflow_input_instance
    ADD COLUMN template_version_id uuid,
    ADD COLUMN created_by_id uuid;
