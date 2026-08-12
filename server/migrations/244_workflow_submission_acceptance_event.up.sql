-- Workflow control plane, part 3 of 3: Submission, Acceptance, Event log.
--
-- workflow_submission is the stable cross-Agent handoff contract that the
-- current Task result is not (plan section 3, gap 2). The verdict column is the
-- point of the table: an Agent's free-form prose can never imply 'pass'. If the
-- structured payload does not parse, the engine blocks the Step with
-- submission_contract_invalid and stores the raw output plus validation errors
-- here for diagnosis, rather than guessing.
--
-- workflow_acceptance records the human decision that a Run — not a Task — is
-- done. Rejection carries a bounded rework target so the engine can create a new
-- attempt at a specific upstream node instead of restarting the whole chain.
--
-- workflow_event is append-only. State and Event commit atomically in the same
-- transaction (plan section 4), which is what makes replay safe: if the Event is
-- present the state change happened, and the idempotency key stops a second
-- application.

CREATE TABLE workflow_submission (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    run_id UUID NOT NULL,
    step_id UUID NOT NULL,
    -- The Agent Task this submission was bound to; NULL for submissions the
    -- engine synthesizes (e.g. a blocked verdict from an invalid payload).
    task_id UUID,
    schema_version INT NOT NULL DEFAULT 1 CHECK (schema_version > 0),
    -- Three-valued on purpose: 'blocked' is first-class, never a silent unknown
    -- failure (plan section 4).
    verdict TEXT NOT NULL CHECK (verdict IN ('pass', 'fail', 'blocked')),
    -- The business deliverable: {type, summary, references}.
    artifact JSONB NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(artifact) = 'object'),
    rationale TEXT NOT NULL DEFAULT '',
    confidence DOUBLE PRECISION CHECK (confidence IS NULL OR (confidence >= 0 AND confidence <= 1)),
    -- Populated on fail/blocked so a rework attempt can inject why it failed.
    root_cause TEXT,
    -- Verbatim agent output, retained even when parsing succeeded, so a wrong
    -- verdict can be audited against what the Agent actually said. Redaction
    -- happens before write (plan section 10, U3).
    raw_result TEXT,
    -- Non-empty exactly when the payload failed contract validation.
    validation_errors JSONB CHECK (validation_errors IS NULL OR jsonb_typeof(validation_errors) = 'array'),
    submitted_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- A pass must be clean: a verdict of 'pass' alongside validation errors
    -- would be exactly the false-success case this table exists to prevent.
    CONSTRAINT workflow_submission_pass_is_valid CHECK (
        verdict <> 'pass' OR validation_errors IS NULL OR jsonb_array_length(validation_errors) = 0
    ),
    CONSTRAINT workflow_submission_artifact_size CHECK (pg_column_size(artifact) <= 131072)
);

CREATE TABLE workflow_acceptance (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    run_id UUID NOT NULL,
    step_id UUID NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'accepted', 'rejected', 'cancelled')),
    -- Reviewer is NULL while pending and required once decided.
    reviewer_user_id UUID,
    reason TEXT,
    -- Node key to rework on rejection; validated against the pinned
    -- definition's allowed rework targets before the row is written.
    rework_target_node_key TEXT CHECK (rework_target_node_key IS NULL OR length(rework_target_node_key) <= 128),
    -- Structured rejection context injected into the new attempt (plan sec. 8).
    context JSONB NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(context) = 'object'),
    decided_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- A decision is immutable evidence: it must name its reviewer and time.
    CONSTRAINT workflow_acceptance_decision_attributed CHECK (
        status NOT IN ('accepted', 'rejected') OR (
            reviewer_user_id IS NOT NULL AND decided_at IS NOT NULL
        )
    ),
    -- 'Rejection requires a reason and allowed target' (plan section 8).
    CONSTRAINT workflow_acceptance_rejection_explained CHECK (
        status <> 'rejected' OR (reason IS NOT NULL AND reason <> '')
    )
);

CREATE TABLE workflow_event (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    run_id UUID NOT NULL,
    step_id UUID,
    event_type TEXT NOT NULL CHECK (event_type <> '' AND length(event_type) <= 128),
    -- Workspace-scoped command/event dedup (plan section 4). The engine writes
    -- this in the same transaction as the state change, so a replayed command
    -- fails the unique index and cannot advance state twice.
    idempotency_key TEXT NOT NULL CHECK (idempotency_key <> '' AND length(idempotency_key) <= 256),
    actor_type TEXT NOT NULL CHECK (actor_type IN ('member', 'agent', 'system', 'external')),
    actor_id UUID,
    payload JSONB NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(payload) = 'object'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT workflow_event_payload_size CHECK (pg_column_size(payload) <= 131072)
);
