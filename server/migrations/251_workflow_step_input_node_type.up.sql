-- Admit 'input' to the workflow node vocabulary.
--
-- WHY a node type at all, rather than a flag on the Definition envelope: the
-- seeded Bug Fix graph is analyze -> implement -> validate -> acceptance -> end
-- with entry_node "analyze", and NOTHING in it represents where the bug report
-- enters. The Run dialog collects a title and a description, but those fields
-- are hardcoded in the dialog, so an author reading the canvas cannot see that
-- the workflow takes an input at all. A node type is the only representation
-- the three parties already agree on: the canvas renders one card per node, the
-- validator has one rule set per node type, and the engine materializes one
-- workflow_step_instance row per node. An envelope flag would be invisible on
-- the canvas (the exact defect being fixed) and would have no step row, so a
-- Run's trace would still not record that a human supplied anything.
--
-- The step row is the reason this needs a migration: migration 235 constrains
-- workflow_step_instance.node_type to the six kinds that existed then, and an
-- input node activates like any other node - it gets a row, it passes through,
-- and the trace shows intake happened. Without widening the CHECK, the first
-- Run of a graph with an input node would fail its INSERT.
--
-- 235's companion constraint workflow_step_instance_task_only_on_agent
-- (task_id IS NULL OR node_type = 'agent') already guarantees at the row level
-- that an input step can never carry an Agent Task, so nothing here restates
-- it. That is deliberate: the DB owns "an intake node never dispatches work",
-- and the engine's explicit passthrough case documents the intent.
--
-- A CHECK cannot be altered in place, so this is a drop and a re-add. The two
-- statements must land together - a committed window in which node_type is
-- unconstrained would let any string into the column - which is why they share
-- one migration file rather than following 197/198's split.
--
-- NOT VALID + VALIDATE, as in 191: the ADD's own verification pass is what
-- would otherwise scan the whole table while holding ACCESS EXCLUSIVE, so NOT
-- VALID makes the ADD metadata-only and VALIDATE does the scan under SHARE
-- UPDATE EXCLUSIVE, which permits concurrent INSERT/UPDATE/DELETE. Honest
-- caveat for whoever writes the next one: the migration runner sends each file
-- as a single simple query, so PostgreSQL wraps these statements in one
-- implicit transaction and the DROP's ACCESS EXCLUSIVE lock is held until the
-- file commits. That is acceptable here for two reasons that will NOT hold for
-- a narrowing swap: this CHECK is being WIDENED, so no existing row can violate
-- it and the scan is a formality; and workflow_step_instance is new as of 235.
-- A future migration that NARROWS a constraint on a large table must split the
-- ADD ... NOT VALID and the VALIDATE into two files the way 197 and 198 do, or
-- it will hold ACCESS EXCLUSIVE for the length of a real scan.
ALTER TABLE workflow_step_instance
    DROP CONSTRAINT IF EXISTS workflow_step_instance_node_type_check;

ALTER TABLE workflow_step_instance
    ADD CONSTRAINT workflow_step_instance_node_type_check
    CHECK (node_type IN ('agent', 'condition', 'fan_out', 'join', 'acceptance', 'end', 'input'))
    NOT VALID;

ALTER TABLE workflow_step_instance
    VALIDATE CONSTRAINT workflow_step_instance_node_type_check;

-- Prove the swap actually took effect, rather than trusting that 235's inline
-- column CHECK was auto-named `workflow_step_instance_node_type_check`.
--
-- The DROP above is `IF EXISTS`, so a name mismatch would not error: it would
-- silently no-op, leave the ORIGINAL six-value CHECK in place beside the new
-- seven-value one, and `migrate up` would report success on a database that
-- still rejects every input step. That failure would surface as a 500 on the
-- first Run of an upgraded template, days later and several layers away.
--
-- "Governs the node_type domain" is matched on the definition rather than the
-- name, because the name is exactly the thing under suspicion. The predicate
-- names 'condition' as well as node_type so it cannot catch
-- workflow_step_instance_task_only_on_agent, which also mentions node_type but
-- only compares it to 'agent'. Matching on the rendered text is fragile in
-- general, which is why the exception prints every definition it found: a wrong
-- assumption here diagnoses itself on the next `migrate up` instead of
-- shipping.
DO $$
DECLARE
    governing_defs TEXT[];
BEGIN
    SELECT coalesce(array_agg(conname || ' => ' || pg_get_constraintdef(oid)), '{}')
      INTO governing_defs
    FROM pg_constraint
    WHERE conrelid = 'workflow_step_instance'::regclass
      AND contype = 'c'
      AND pg_get_constraintdef(oid) LIKE '%node_type%'
      AND pg_get_constraintdef(oid) LIKE '%condition%';

    IF array_length(governing_defs, 1) IS DISTINCT FROM 1 THEN
        RAISE EXCEPTION
            'expected exactly one CHECK constraint governing workflow_step_instance.node_type after the swap, found: %',
            governing_defs;
    END IF;

    IF governing_defs[1] NOT LIKE '%input%' THEN
        RAISE EXCEPTION
            'the surviving node_type CHECK does not admit ''input'', so every intake step would fail to insert: %',
            governing_defs[1];
    END IF;
END
$$;
