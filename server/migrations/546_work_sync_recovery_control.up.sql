-- Operator provisioned only. No identity, grant or fence is activated by migration.
CREATE TABLE IF NOT EXISTS work_sync_recovery_deployment (
    deployment_id uuid NOT NULL
);
CREATE TABLE IF NOT EXISTS work_sync_recovery_authority (
    token_hash text NOT NULL,
    workspace_id uuid NOT NULL,
    owner_id uuid NOT NULL,
    operator_id text NOT NULL,
    source_deployment_id uuid NOT NULL,
    target_deployment_id uuid NOT NULL,
    plan jsonb NOT NULL,
    approved_report_hash text NOT NULL DEFAULT '',
    expires_at timestamptz NOT NULL CHECK (isfinite(expires_at)),
    revoked_at timestamptz
);
-- A separate row avoids upgrading shared writer locks on the capture scope.
-- Keep fenced rows even after workspace deletion; UUID reuse must not reopen it.
CREATE TABLE IF NOT EXISTS work_sync_recovery_fence (
    workspace_id uuid NOT NULL,
    proof jsonb
);
