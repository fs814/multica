-- Additive, nullable workflow linkage on existing tables (plan section 5).
--
-- Every column here is nullable with no default and no foreign key, which is
-- what keeps the workflow layer backward compatible: existing Issue, Chat,
-- Quick Create, Squad, and Autopilot paths continue to write NULL and behave
-- exactly as before (plan section 2, compatibility).
--
-- agent_task_queue.workflow_step_instance_id is the link the engine uses to map
-- a terminal Agent Task back to the Step attempt that created it. The reverse
-- link (workflow_step_instance.task_id) already exists and is uniquely indexed;
-- both directions are maintained in the same transaction.
ALTER TABLE agent_task_queue ADD COLUMN IF NOT EXISTS workflow_step_instance_id UUID;

-- Autopilot may bind a published template (plan section 9). When both are NULL
-- the autopilot keeps its existing create_issue/run_only behavior untouched.
ALTER TABLE autopilot ADD COLUMN IF NOT EXISTS workflow_template_id UUID;
ALTER TABLE autopilot ADD COLUMN IF NOT EXISTS workflow_template_version_id UUID;

-- Links one autopilot execution to the Run it started, so the existing
-- autopilot run history can show workflow progress without a schema rewrite.
ALTER TABLE autopilot_run ADD COLUMN IF NOT EXISTS workflow_run_id UUID;
