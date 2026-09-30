DROP TRIGGER IF EXISTS work_sync_register_issue ON issue;
DROP TRIGGER IF EXISTS work_sync_issue ON issue;
CREATE TRIGGER work_sync_issue AFTER INSERT OR UPDATE OR DELETE ON issue
FOR EACH ROW EXECUTE FUNCTION work_sync_capture();
DROP TRIGGER IF EXISTS work_sync_register_project ON project;
DROP TRIGGER IF EXISTS work_sync_project ON project;
CREATE TRIGGER work_sync_project AFTER INSERT OR UPDATE OR DELETE ON project
FOR EACH ROW EXECUTE FUNCTION work_sync_capture();
DROP TRIGGER IF EXISTS work_sync_register_agent ON agent;
DROP TRIGGER IF EXISTS work_sync_agent ON agent;
CREATE TRIGGER work_sync_agent AFTER INSERT OR UPDATE OR DELETE ON agent
FOR EACH ROW EXECUTE FUNCTION work_sync_capture();
DROP FUNCTION IF EXISTS work_sync_register_scope();
CREATE OR REPLACE FUNCTION work_sync_capture() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
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
