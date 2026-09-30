-- Collect all touched scopes without taking journal locks during business writes.
-- SET LOCAL is restored by savepoint/transaction rollback and reset after commit.
CREATE OR REPLACE FUNCTION work_sync_register_scope() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    scopes uuid[] := COALESCE(NULLIF(current_setting('multica.work_sync_scopes',true),'')::uuid[], ARRAY[]::uuid[]);
    workspace uuid;
BEGIN
    IF TG_OP <> 'INSERT' THEN
        workspace := OLD.workspace_id;
        IF workspace IS NOT NULL AND NOT workspace = ANY(scopes) THEN scopes := array_append(scopes,workspace); END IF;
    END IF;
    IF TG_OP <> 'DELETE' THEN
        workspace := NEW.workspace_id;
        IF workspace IS NOT NULL AND NOT workspace = ANY(scopes) THEN scopes := array_append(scopes,workspace); END IF;
    END IF;
    PERFORM set_config('multica.work_sync_scopes',scopes::text,true);
    RETURN NULL;
END
$$;

CREATE OR REPLACE FUNCTION work_sync_capture() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    -- Deferred until all business-row writes finish. Acquire every touched scope
    -- in UUID order before allocating any sequence (including cross-tenant moves).
    -- No business-row locks may be acquired after this journal phase begins.
    PERFORM workspace_id FROM work_sync_scope
      WHERE workspace_id = ANY(COALESCE(NULLIF(current_setting('multica.work_sync_scopes',true),'')::uuid[], ARRAY[]::uuid[]))
      ORDER BY workspace_id FOR UPDATE;
    IF TG_OP = 'DELETE' THEN
        PERFORM work_sync_capture_row(TG_TABLE_NAME,to_jsonb(OLD),true);
        RETURN OLD;
    END IF;
    IF TG_OP = 'UPDATE' THEN
        IF TG_TABLE_NAME = 'agent' AND to_jsonb(OLD)->>'kind' IS DISTINCT FROM to_jsonb(NEW)->>'kind' THEN
            PERFORM work_sync_capture_row(TG_TABLE_NAME,to_jsonb(OLD),true);
            PERFORM work_sync_capture_row(TG_TABLE_NAME,to_jsonb(NEW),false);
            RETURN NEW;
        END IF;
        -- Identity moves are not an export of a row into another tenant.
        IF OLD.workspace_id IS DISTINCT FROM NEW.workspace_id OR OLD.id IS DISTINCT FROM NEW.id THEN
            PERFORM work_sync_capture_row(TG_TABLE_NAME,to_jsonb(OLD),true);
        ELSIF work_sync_fields(TG_TABLE_NAME,to_jsonb(OLD)) = work_sync_fields(TG_TABLE_NAME,to_jsonb(NEW)) THEN
            RETURN NEW;
        END IF;
    END IF;
    PERFORM work_sync_capture_row(TG_TABLE_NAME,to_jsonb(NEW),false);
    RETURN NEW;
END
$$;
DROP TRIGGER IF EXISTS work_sync_register_issue ON issue;
CREATE TRIGGER work_sync_register_issue AFTER INSERT OR UPDATE OR DELETE ON issue
FOR EACH ROW EXECUTE FUNCTION work_sync_register_scope();
DROP TRIGGER IF EXISTS work_sync_issue ON issue;
CREATE CONSTRAINT TRIGGER work_sync_issue AFTER INSERT OR UPDATE OR DELETE ON issue
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION work_sync_capture();
DROP TRIGGER IF EXISTS work_sync_register_project ON project;
CREATE TRIGGER work_sync_register_project AFTER INSERT OR UPDATE OR DELETE ON project
FOR EACH ROW EXECUTE FUNCTION work_sync_register_scope();
DROP TRIGGER IF EXISTS work_sync_project ON project;
CREATE CONSTRAINT TRIGGER work_sync_project AFTER INSERT OR UPDATE OR DELETE ON project
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION work_sync_capture();
DROP TRIGGER IF EXISTS work_sync_register_agent ON agent;
CREATE TRIGGER work_sync_register_agent AFTER INSERT OR UPDATE OR DELETE ON agent
FOR EACH ROW EXECUTE FUNCTION work_sync_register_scope();
DROP TRIGGER IF EXISTS work_sync_agent ON agent;
CREATE CONSTRAINT TRIGGER work_sync_agent AFTER INSERT OR UPDATE OR DELETE ON agent
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION work_sync_capture();
