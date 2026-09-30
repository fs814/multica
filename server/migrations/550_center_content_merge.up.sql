-- Content baselines are separate from auth/session data and replica checkpoints.
CREATE TABLE center_content_merge (
    workspace_id UUID NOT NULL,
    owner_id UUID NOT NULL,
    peer_origin TEXT NOT NULL,
    record_key TEXT NOT NULL,
    baseline JSONB NOT NULL
);
