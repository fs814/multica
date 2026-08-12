-- Roll back the node_type CHECK to the six kinds migration 235 declared.
--
-- This is only safe if no step row of type 'input' exists. Narrowing the CHECK
-- while such rows are present would leave the table in a state where the
-- constraint is satisfied by nothing that could be re-inserted: every existing
-- input step becomes un-updatable (any UPDATE re-checks the row), the Runs that
-- own those steps become un-advanceable, and a `pg_dump | psql` restore fails on
-- the COPY. Postgres will not tell us this: ADD CONSTRAINT ... NOT VALID
-- accepts violating rows by design, and the VALIDATE is what would notice.
--
-- So this migration FAILS LOUDLY instead. The alternatives are worse:
--   - Omitting NOT VALID and letting VALIDATE reject would work, but the error
--     names a constraint, not a cause, and an operator would have to reverse
--     engineer which rows and which Runs are implicated.
--   - Deleting the offending rows would silently destroy the audit trail of
--     Runs that legitimately executed under the newer schema. A down migration
--     must never make a Run's history a lie.
--   - Leaving the wider CHECK in place would make the rollback a no-op that
--     claims to have rolled back.
-- Refusing, and naming exactly what to do, is the only honest option: an
-- operator who really wants this must first cancel or delete the affected Runs,
-- which is a decision only they can make.
DO $$
DECLARE
    input_steps INT;
BEGIN
    SELECT count(*) INTO input_steps
    FROM workflow_step_instance
    WHERE node_type = 'input';

    IF input_steps > 0 THEN
        RAISE EXCEPTION
            'refusing to roll back migration 251: % workflow_step_instance row(s) have node_type=''input''. Narrowing the CHECK would leave those rows un-updatable and their Runs un-advanceable, and a dump/restore would fail. Cancel or delete the owning Runs first, then re-run this migration.',
            input_steps;
    END IF;
END
$$;

ALTER TABLE workflow_step_instance
    DROP CONSTRAINT IF EXISTS workflow_step_instance_node_type_check;

-- Re-added NOT VALID + VALIDATE for the same lock reason as the up migration.
-- The VALIDATE also serves as a second line of defence: if the guard above were
-- ever weakened, the scan would still refuse to mark a constraint valid that
-- existing rows violate.
ALTER TABLE workflow_step_instance
    ADD CONSTRAINT workflow_step_instance_node_type_check
    CHECK (node_type IN ('agent', 'condition', 'fan_out', 'join', 'acceptance', 'end'))
    NOT VALID;

ALTER TABLE workflow_step_instance
    VALIDATE CONSTRAINT workflow_step_instance_node_type_check;
