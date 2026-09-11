ALTER TABLE autopilot_trigger
    ADD COLUMN signing_secret_encrypted BYTEA;

ALTER TABLE workflow_run
    ADD COLUMN request_hash TEXT,
    ADD COLUMN callback_destination_id UUID;

CREATE TABLE workflow_callback_destination (
    id UUID NOT NULL DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    name TEXT NOT NULL,
    url TEXT NOT NULL,
    signing_secret_encrypted BYTEA NOT NULL,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_by_user_id UUID NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE workflow_callback_delivery (
    id UUID NOT NULL DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    destination_id UUID NOT NULL,
    workflow_run_id UUID NOT NULL,
    event_type TEXT NOT NULL,
    event_key TEXT NOT NULL,
    payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    status TEXT NOT NULL DEFAULT 'queued'
        CHECK (status IN ('queued', 'dispatching', 'delivered', 'failed')),
    attempt_count INTEGER NOT NULL DEFAULT 0,
    available_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    lease_token UUID,
    lease_expires_at TIMESTAMPTZ,
    response_status INTEGER,
    response_body TEXT,
    error TEXT,
    delivered_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
