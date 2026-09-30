-- Staging only: no workspace is enrolled and no recovery identity is granted.
CREATE TABLE IF NOT EXISTS work_sync_recovery (
    id uuid NOT NULL,
    workspace_id uuid NOT NULL,
    report_hash text NOT NULL,
    report jsonb NOT NULL,
    activated boolean NOT NULL DEFAULT false
);
