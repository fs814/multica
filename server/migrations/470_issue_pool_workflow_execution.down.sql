-- Intentionally non-destructive. Once issue-pool Workflow execution has
-- durable runs/outbox rows, removing its columns would destroy recovery state.
-- Operators rolling back the binary keep the additive schema; legacy writers
-- remain compatible because every new column is nullable or defaulted.
SELECT 1;
