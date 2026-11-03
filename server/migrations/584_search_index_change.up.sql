-- Local search index change log (MUL-7754).
--
-- Web and Desktop keep a per-workspace copy of issue, comment, and project text
-- for instant search. Clients catch up by asking which entities changed since
-- the database snapshot they last synced to. `updated_at` cannot answer that:
-- issues and comments are hard-deleted (including cascaded comment deletes), and
-- a timestamp is assigned before commit, so a late-committing writer would be
-- skipped. Triggers record one row per entity with the xid of its latest write;
-- readers compare that xid against pg_snapshot values, which is exact under any
-- commit order. This is the same trigger-populated dirty-key shape as migration
-- 077, and it covers every write path (API, CLI, daemon, agents) at once.
--
-- One row per entity keeps the table bounded: later writes overwrite the xid.
-- Rows carry no content; readers join to the live tables, so a missing row (or
-- a tombstoned comment) means "deleted". No foreign keys: workspace deletion
-- sweeps this table explicitly and skips the triggers via the teardown flag.
--
-- The runner sends this file as one implicit transaction. CREATE TRIGGER takes a
-- short SHARE ROW EXCLUSIVE lock on issue/comment/project, so bound the wait and
-- retry on the next start rather than queue writers behind it.
SET LOCAL lock_timeout = '2s';
SET LOCAL statement_timeout = '10s';

CREATE TABLE IF NOT EXISTS search_index_change (
    entity_type  TEXT        NOT NULL CHECK (entity_type IN ('issue', 'comment', 'project')),
    entity_id    UUID        NOT NULL,
    workspace_id UUID        NOT NULL,
    change_xid   xid8        NOT NULL,
    changed_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Highest xid removed by retention pruning. A client whose snapshot could still
-- be missing a pruned change must rebuild its copy instead of catching up.
CREATE TABLE IF NOT EXISTS search_index_prune_mark (
    singleton          BOOLEAN NOT NULL DEFAULT TRUE CHECK (singleton),
    pruned_through_xid xid8    NOT NULL
);

