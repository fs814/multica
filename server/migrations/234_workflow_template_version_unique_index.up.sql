-- One row per (template, version): the version number is the caller-visible
-- handle for a pinned graph, so a duplicate would make "version 3" ambiguous.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_workflow_template_version_template_version
    ON workflow_template_version (template_id, version);
