-- No scope is enrolled by migration. Replication is disabled by default.
CREATE TABLE IF NOT EXISTS work_sync_scope (
    workspace_id uuid NOT NULL,
    group_id uuid NOT NULL,
    epoch uuid NOT NULL,
    sequence bigint NOT NULL DEFAULT 0 CHECK (sequence >= 0)
);
CREATE TABLE IF NOT EXISTS work_sync_change (
    workspace_id uuid NOT NULL,
    sequence bigint NOT NULL,
    kind text NOT NULL CHECK (kind IN ('issue', 'project', 'agent')),
    entity_id uuid NOT NULL,
    deleted boolean NOT NULL,
    fields jsonb NOT NULL
);
CREATE TABLE IF NOT EXISTS work_sync_receipt (
    workspace_id uuid NOT NULL,
    operation_id uuid NOT NULL,
    node_id text NOT NULL,
    incarnation uuid NOT NULL,
    sequence bigint NOT NULL,
    actor_id text NOT NULL,
    payload_hash text NOT NULL,
    receipt jsonb NOT NULL
);
