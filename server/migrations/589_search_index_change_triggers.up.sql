-- Keep this function trivial: it runs inside every issue/comment/project write,
-- so an error here would fail the user's write. All inputs are NOT NULL columns.
CREATE OR REPLACE FUNCTION record_search_index_change()
RETURNS TRIGGER LANGUAGE plpgsql AS $function$
DECLARE
    changed_id UUID;
    changed_workspace_id UUID;
BEGIN
    IF TG_OP = 'DELETE' THEN
        changed_id := OLD.id;
        changed_workspace_id := OLD.workspace_id;
    ELSE
        changed_id := NEW.id;
        changed_workspace_id := NEW.workspace_id;
    END IF;

    INSERT INTO search_index_change (entity_type, entity_id, workspace_id, change_xid, changed_at)
    VALUES (TG_ARGV[0], changed_id, changed_workspace_id, pg_current_xact_id(), now())
    ON CONFLICT (entity_type, entity_id) DO UPDATE
        SET workspace_id = EXCLUDED.workspace_id,
            change_xid = EXCLUDED.change_xid,
            changed_at = EXCLUDED.changed_at;
    RETURN NULL;
END
$function$;

-- Every issue column is part of the indexed search result, so any update counts.
DROP TRIGGER IF EXISTS trg_issue_search_index_change ON issue;
CREATE TRIGGER trg_issue_search_index_change
AFTER INSERT OR UPDATE OR DELETE ON issue
FOR EACH ROW
WHEN (current_setting('multica.workspace_teardown', true) IS DISTINCT FROM 'on')
EXECUTE FUNCTION record_search_index_change('issue');

-- Comments only contribute their text and placement; resolve/revision bumps do
-- not change what search can match.
DROP TRIGGER IF EXISTS trg_comment_search_index_change ON comment;
CREATE TRIGGER trg_comment_search_index_change
AFTER INSERT OR DELETE ON comment
FOR EACH ROW
WHEN (current_setting('multica.workspace_teardown', true) IS DISTINCT FROM 'on')
EXECUTE FUNCTION record_search_index_change('comment');

DROP TRIGGER IF EXISTS trg_comment_update_search_index_change ON comment;
CREATE TRIGGER trg_comment_update_search_index_change
AFTER UPDATE OF content, deleted_at, issue_id, created_at ON comment
FOR EACH ROW
WHEN (current_setting('multica.workspace_teardown', true) IS DISTINCT FROM 'on')
EXECUTE FUNCTION record_search_index_change('comment');

DROP TRIGGER IF EXISTS trg_project_search_index_change ON project;
CREATE TRIGGER trg_project_search_index_change
AFTER INSERT OR UPDATE OR DELETE ON project
FOR EACH ROW
WHEN (current_setting('multica.workspace_teardown', true) IS DISTINCT FROM 'on')
EXECUTE FUNCTION record_search_index_change('project');
