-- Workflow control plane, part 2 of 3: Run and Step Instance.
--
-- workflow_run is the durable execution of one pinned template version. The
-- central invariant (plan section 4) is that an Agent Task completing is NOT
-- business acceptance: a Run reaches 'completed' only by transitioning through
-- an End node. Nothing in this schema can enforce that reachability property,
-- so it lives in the engine's transition table; what the schema does enforce is
-- that terminal rows carry the evidence needed to explain themselves.
--
-- workflow_step_instance is one attempt at one node. Rework never mutates a
-- prior attempt — it inserts a new row with attempt = N+1, which is why the
-- uniqueness key is (run, node, attempt) and why completed attempts are
-- immutable. That preserves the full Submission history for audit and for
-- injecting rejection context into the retry.
--
-- No foreign keys or cascades (plan section 4); logical references only,
-- validated in application transactions. Indexes ship in their own migrations.

CREATE TABLE workflow_run (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    -- Nullable: a Run is normally the execution behind an Issue, but intake may
    -- create the Run before/without one, and Issue is a projection of Run state
    -- (plan section 6) rather than its owner.
    issue_id UUID,
    template_id UUID NOT NULL,
    -- The pinned version. Never resolve node semantics through the template's
    -- current_version at execution time, or an edit mid-flight would silently
    -- change the meaning of a running graph.
    template_version_id UUID NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'running', 'waiting_acceptance', 'blocked', 'completed', 'failed', 'cancelled')),
    source TEXT NOT NULL
        CHECK (source IN ('manual', 'autopilot', 'external', 'api', 'agent')),
    -- Dedup handle from the originating system (plan section 9): the external
    -- event id, autopilot run id, etc. Kept alongside idempotency_key because
    -- the key is ours and this is theirs.
    source_event_id TEXT CHECK (source_event_id IS NULL OR length(source_event_id) <= 512),
    -- Workspace-scoped idempotency (plan section 4): replaying an intake event
    -- must not start a second Run.
    idempotency_key TEXT NOT NULL CHECK (idempotency_key <> '' AND length(idempotency_key) <= 256),
    -- The human answerable for this Run. Routing reuses invocation permissions
    -- and accountability checks, so this must survive for the Run's lifetime.
    accountable_user_id UUID,
    input JSONB NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(input) = 'object'),
    context JSONB NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(context) = 'object'),
    -- Snapshot of the effective limits (retry/rework/fan-out/duration/token/
    -- cost) at start. Snapshotted, not looked up live, so tightening workspace
    -- policy cannot retroactively make an in-flight Run illegal.
    policy JSONB NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(policy) = 'object'),
    -- Classified reason for blocked/failed. 'Blocked is first-class and never
    -- silently becomes unknown failure' (plan section 4).
    blocked_reason TEXT CHECK (blocked_reason IS NULL OR length(blocked_reason) <= 128),
    failure_reason TEXT CHECK (failure_reason IS NULL OR length(failure_reason) <= 128),
    failure_detail TEXT,
    started_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Every failed/blocked terminal Run has a classified reason (plan section
    -- 12 SLO). Enforced at the row level so no code path can produce an
    -- unexplained terminal Run.
    CONSTRAINT workflow_run_terminal_reason_present CHECK (
        (status <> 'failed'  OR failure_reason IS NOT NULL) AND
        (status <> 'blocked' OR blocked_reason IS NOT NULL)
    ),
    CONSTRAINT workflow_run_completed_at_present CHECK (
        status NOT IN ('completed', 'failed', 'cancelled') OR completed_at IS NOT NULL
    ),
    CONSTRAINT workflow_run_input_size CHECK (pg_column_size(input) <= 65536),
    CONSTRAINT workflow_run_context_size CHECK (pg_column_size(context) <= 131072)
);

CREATE TABLE workflow_step_instance (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    run_id UUID NOT NULL,
    -- Node identity within the pinned definition, not a row reference: the
    -- graph lives in JSONB, so steps address nodes by key.
    node_key TEXT NOT NULL CHECK (node_key <> '' AND length(node_key) <= 128),
    node_type TEXT NOT NULL
        CHECK (node_type IN ('agent', 'condition', 'fan_out', 'join', 'acceptance', 'end')),
    -- 1-based attempt counter; rework inserts attempt+1 rather than mutating.
    attempt INT NOT NULL DEFAULT 1 CHECK (attempt > 0),
    status TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN (
            'pending', 'ready', 'queued', 'running', 'submitted',
            'passed', 'failed', 'blocked', 'waiting_acceptance', 'skipped', 'cancelled'
        )),
    -- Set on fan-out children; identifies the parent fan_out step so AND Join
    -- can derive completion from durable state alone (plan section 8).
    parent_step_id UUID,
    -- Deterministic expansion key for fan-out children, so re-expanding after a
    -- crash produces the same children instead of duplicates.
    expansion_key TEXT CHECK (expansion_key IS NULL OR length(expansion_key) <= 256),
    -- Agent nodes only. agent_id is the routing outcome; task_id is the single
    -- active Agent Task for this attempt (plan section 4: at most one).
    agent_id UUID,
    task_id UUID,
    -- Why this Agent won the routing decision: explicit, previous-step,
    -- capability match, or fallback (plan section 8). Persisted for audit.
    routing_reason TEXT CHECK (routing_reason IS NULL OR length(routing_reason) <= 256),
    input JSONB NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(input) = 'object'),
    output JSONB CHECK (output IS NULL OR jsonb_typeof(output) = 'object'),
    failure_reason TEXT CHECK (failure_reason IS NULL OR length(failure_reason) <= 128),
    failure_detail TEXT,
    -- Activation deadline; the reconciler repairs ready steps that never queued
    -- (plan section 7).
    activation_timeout_at TIMESTAMPTZ,
    ready_at TIMESTAMPTZ,
    started_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- A blocked/failed Step must explain itself, mirroring the Run constraint.
    CONSTRAINT workflow_step_instance_terminal_reason_present CHECK (
        status NOT IN ('failed', 'blocked') OR failure_reason IS NOT NULL
    ),
    -- Only Agent steps dispatch Agent Tasks; a task on a condition/join/end row
    -- would mean the engine mis-routed.
    CONSTRAINT workflow_step_instance_task_only_on_agent CHECK (
        task_id IS NULL OR node_type = 'agent'
    ),
    CONSTRAINT workflow_step_instance_input_size CHECK (pg_column_size(input) <= 131072)
);
