-- Workflow control plane, part 1 of 3: template + immutable published versions.
--
-- A workflow_template is the durable, workspace-scoped identity of a process
-- ("Bug Fix"). The graph itself never lives on the template — it lives on
-- workflow_template_version.definition, so a Run can pin the exact graph it
-- started with and stay replayable after the template is edited.
--
-- Immutability is the load-bearing invariant here (plan section 4): once a
-- version is published, its definition must never change, because in-flight
-- Runs resolve node semantics through it. Draft rows stay mutable; the
-- published/archived transition is one-way and enforced in the service layer
-- alongside graph validation, which needs the whole graph in memory and cannot
-- be expressed as a row CHECK.
--
-- Per plan section 4 there are deliberately NO foreign keys or cascades
-- anywhere in the workflow tables: relationships are validated and cleaned up
-- in application transactions. Indexes all live in their own follow-up
-- migrations because CREATE INDEX CONCURRENTLY cannot share a migration with
-- other statements.

CREATE TABLE workflow_template (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    -- Stable machine identifier used by external intake (plan section 9) so
    -- callers reference a process by key, not by a UUID they cannot know.
    key TEXT NOT NULL CHECK (key <> '' AND length(key) <= 128),
    name TEXT NOT NULL CHECK (name <> '' AND length(name) <= 200),
    description TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'draft'
        CHECK (status IN ('draft', 'published', 'archived')),
    -- Highest published version number; NULL until the first publish. Runs
    -- start from this version unless the caller pins one explicitly.
    current_version INT CHECK (current_version IS NULL OR current_version > 0),
    created_by_type TEXT NOT NULL CHECK (created_by_type IN ('member', 'agent', 'system')),
    created_by_id UUID NOT NULL,
    archived_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE workflow_template_version (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    template_id UUID NOT NULL,
    version INT NOT NULL CHECK (version > 0),
    -- The validated graph: entry node, nodes, edges, routing, submission
    -- schemas, failure policies, rework targets, hard limits (plan section 6).
    definition JSONB NOT NULL CHECK (jsonb_typeof(definition) = 'object'),
    -- Lets the engine refuse a graph written by a newer server rather than
    -- misinterpret unknown node semantics.
    schema_version INT NOT NULL DEFAULT 1 CHECK (schema_version > 0),
    status TEXT NOT NULL DEFAULT 'draft'
        CHECK (status IN ('draft', 'published', 'archived')),
    published_by_type TEXT CHECK (published_by_type IN ('member', 'agent', 'system')),
    published_by_id UUID,
    published_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- A published row must carry its publisher: acceptance and audit trails
    -- attribute the graph a Run executed to a specific actor.
    CONSTRAINT workflow_template_version_publisher_present CHECK (
        status <> 'published' OR (
            published_by_type IS NOT NULL
            AND published_by_id IS NOT NULL
            AND published_at IS NOT NULL
        )
    ),
    -- Bound the graph so a pathological definition cannot be pinned into every
    -- Run's hot path. Generous vs. the Bug Fix pilot (a few KB).
    CONSTRAINT workflow_template_version_definition_size CHECK (pg_column_size(definition) <= 262144)
);
