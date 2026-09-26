-- Explicit, history-bound full-projection grants. No automatic enrollment.
CREATE TABLE IF NOT EXISTS work_sync_grant (
    workspace_id UUID NOT NULL,
    group_id UUID NOT NULL,
    epoch UUID NOT NULL,
    actor_id UUID NOT NULL,
    node_id TEXT NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    revoked_at TIMESTAMPTZ
);
