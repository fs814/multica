-- A Run detail is an execution history, not an unordered set of step rows.
-- `created_at` cannot represent that history: PostgreSQL `now()` is fixed for a
-- transaction, while StartRun can activate the entry input and its successor in
-- the same transaction. A tied ORDER BY therefore lets the database return the
-- successor before the entry step.
--
-- Each Run is locked before any post-start activation, and StartRun owns its
-- newly inserted Run until commit, so MAX + 1 is serialized per Run. This makes
-- trace_position a durable, gap-free activation order without a global sequence.
-- The supporting index is deliberately in migration 253: CREATE INDEX
-- CONCURRENTLY must be the only statement in the migration runner's transaction.
ALTER TABLE workflow_step_instance
    ADD COLUMN trace_position BIGINT;

WITH ordered_steps AS (
    SELECT id,
           row_number() OVER (PARTITION BY run_id ORDER BY created_at ASC, id ASC) AS trace_position
    FROM workflow_step_instance
)
UPDATE workflow_step_instance AS step
SET trace_position = ordered_steps.trace_position
FROM ordered_steps
WHERE step.id = ordered_steps.id;

ALTER TABLE workflow_step_instance
    ALTER COLUMN trace_position SET NOT NULL;