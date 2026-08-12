ALTER TABLE autopilot_run DROP COLUMN IF EXISTS workflow_run_id;
ALTER TABLE autopilot DROP COLUMN IF EXISTS workflow_template_version_id;
ALTER TABLE autopilot DROP COLUMN IF EXISTS workflow_template_id;
ALTER TABLE agent_task_queue DROP COLUMN IF EXISTS workflow_step_instance_id;
