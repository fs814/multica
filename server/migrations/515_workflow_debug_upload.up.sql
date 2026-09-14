-- A pending upload prevents delivery receipts until finalize or abort settles it.
CREATE TABLE workflow_debug_upload (
 id uuid NOT NULL DEFAULT gen_random_uuid(), workspace_id uuid NOT NULL, run_id uuid NOT NULL,
 claim_id uuid NOT NULL, task_id uuid NOT NULL, attachment_id uuid,
 state text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending','finalized','aborted')),
 created_at timestamptz NOT NULL DEFAULT now(), settled_at timestamptz
);
