-- TES-66 steps 1-4: extend the review pool into a durable Workflow dispatcher.
-- Additive columns accept both the phase-one 449-458 shape and the superseded
-- local prototype shape without deleting application data.
ALTER TABLE autopilot DROP CONSTRAINT IF EXISTS autopilot_execution_mode_check;
ALTER TABLE autopilot ADD CONSTRAINT autopilot_execution_mode_check
    CHECK (execution_mode IN ('create_issue', 'run_only', 'issue_pool'));

ALTER TABLE issue_pool_policy
    ADD COLUMN IF NOT EXISTS workflow_input_mapping JSONB NOT NULL DEFAULT '{}'::JSONB;
ALTER TABLE issue_pool_policy DROP CONSTRAINT IF EXISTS issue_pool_policy_workflow_input_mapping_check;
ALTER TABLE issue_pool_policy ADD CONSTRAINT issue_pool_policy_workflow_input_mapping_check
    CHECK (jsonb_typeof(workflow_input_mapping) = 'object');

ALTER TABLE issue_pool_cycle
    ADD COLUMN IF NOT EXISTS autopilot_run_id UUID,
    ADD COLUMN IF NOT EXISTS workflow_template_id UUID,
    ADD COLUMN IF NOT EXISTS workflow_template_version_id UUID,
    ADD COLUMN IF NOT EXISTS workflow_input_mapping_snapshot JSONB NOT NULL DEFAULT '{}'::JSONB,
    ADD COLUMN IF NOT EXISTS request_hash TEXT,
    ADD COLUMN IF NOT EXISTS approved_count INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS rejected_count INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS completed_count INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS blocked_count INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS failed_count INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS deferred_count INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS started_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS completed_at TIMESTAMPTZ;

UPDATE issue_pool_cycle SET status = 'completed', completed_at = COALESCE(completed_at, reviewed_at, updated_at)
WHERE status = 'reviewed';
ALTER TABLE issue_pool_cycle DROP CONSTRAINT IF EXISTS issue_pool_cycle_status_check;
ALTER TABLE issue_pool_cycle ADD CONSTRAINT issue_pool_cycle_status_check CHECK (status IN (
    'scanning', 'awaiting_review', 'running', 'waiting_acceptance', 'blocked',
    'completed', 'partial', 'failed', 'cancelled'
));
ALTER TABLE issue_pool_cycle DROP CONSTRAINT IF EXISTS issue_pool_cycle_workflow_input_mapping_snapshot_check;
ALTER TABLE issue_pool_cycle ADD CONSTRAINT issue_pool_cycle_workflow_input_mapping_snapshot_check
    CHECK (jsonb_typeof(workflow_input_mapping_snapshot) = 'object');
ALTER TABLE issue_pool_cycle DROP CONSTRAINT IF EXISTS issue_pool_cycle_counts_nonnegative_check;
ALTER TABLE issue_pool_cycle ADD CONSTRAINT issue_pool_cycle_counts_nonnegative_check CHECK (
    approved_count >= 0 AND rejected_count >= 0 AND completed_count >= 0 AND
    blocked_count >= 0 AND failed_count >= 0 AND deferred_count >= 0
);

ALTER TABLE issue_pool_item
    ADD COLUMN IF NOT EXISTS workflow_run_id UUID,
    ADD COLUMN IF NOT EXISTS resolved_input JSONB,
    ADD COLUMN IF NOT EXISTS request_hash TEXT,
    ADD COLUMN IF NOT EXISTS dispatch_attempts INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS failure_code TEXT,
    ADD COLUMN IF NOT EXISTS failure_detail JSONB,
    ADD COLUMN IF NOT EXISTS dispatched_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS waiting_acceptance_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS completed_at TIMESTAMPTZ;

ALTER TABLE issue_pool_item DROP CONSTRAINT IF EXISTS issue_pool_item_status_check;
ALTER TABLE issue_pool_item ADD CONSTRAINT issue_pool_item_status_check CHECK (status IN (
    'claimed', 'approved', 'rejected', 'dispatching', 'running',
    'waiting_acceptance', 'completed', 'blocked', 'failed', 'cancelled', 'deferred'
));
ALTER TABLE issue_pool_item DROP CONSTRAINT IF EXISTS issue_pool_item_resolved_input_check;
ALTER TABLE issue_pool_item ADD CONSTRAINT issue_pool_item_resolved_input_check
    CHECK (resolved_input IS NULL OR jsonb_typeof(resolved_input) = 'object');
ALTER TABLE issue_pool_item DROP CONSTRAINT IF EXISTS issue_pool_item_dispatch_attempts_check;
ALTER TABLE issue_pool_item ADD CONSTRAINT issue_pool_item_dispatch_attempts_check CHECK (dispatch_attempts >= 0);

CREATE TABLE IF NOT EXISTS issue_pool_notification_outbox (
    id UUID NOT NULL DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    autopilot_id UUID NOT NULL,
    cycle_id UUID NOT NULL,
    item_id UUID,
    recipient_id UUID NOT NULL,
    event_type TEXT NOT NULL,
    payload JSONB NOT NULL DEFAULT '{}'::JSONB,
    attempts INTEGER NOT NULL DEFAULT 0,
    available_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    delivered_at TIMESTAMPTZ,
    last_error TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (jsonb_typeof(payload) = 'object'),
    CHECK (attempts >= 0)
);
