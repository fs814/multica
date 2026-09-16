CREATE TABLE project_memory_binding (
 workspace_id uuid NOT NULL,
 project_id uuid NOT NULL,
 binding jsonb NOT NULL DEFAULT '{"state":"pending"}'::jsonb
);
CREATE TABLE project_memory_scope (
 workspace_id uuid NOT NULL,
 scope_kind text NOT NULL,
 scope_id uuid NOT NULL,
 project_id uuid,
 epoch bigint NOT NULL DEFAULT 1
);
CREATE TABLE project_memory_task (
 task_id uuid NOT NULL,
 workspace_id uuid NOT NULL,
 context jsonb NOT NULL
);
CREATE TABLE project_memory_request (
 id uuid NOT NULL,
 workspace_id uuid NOT NULL,
 project_id uuid NOT NULL,
 owner_daemon_id uuid NOT NULL,
 task_id uuid,
 request jsonb NOT NULL,
 status text NOT NULL DEFAULT 'pending',
 result jsonb,
 expires_at timestamptz NOT NULL DEFAULT now() + interval '60 seconds'
);
