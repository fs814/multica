-- Explicit whitelist: never copy raw agent configuration or credentials.
CREATE OR REPLACE FUNCTION work_sync_fields(kind text, row_data jsonb) RETURNS jsonb
LANGUAGE sql IMMUTABLE AS $$
    SELECT CASE kind
    WHEN 'issue' THEN jsonb_build_object(
        'title',row_data->'title','description',row_data->'description',
        'priority',row_data->'priority','status',row_data->'status',
        'number',row_data->'number','project_id',row_data->'project_id',
        'parent_issue_id',row_data->'parent_issue_id',
        'assignee_type',row_data->'assignee_type','assignee_id',row_data->'assignee_id')
    WHEN 'project' THEN jsonb_build_object(
        'title',row_data->'title','description',row_data->'description',
        'priority',row_data->'priority','status',row_data->'status','icon',row_data->'icon')
    WHEN 'agent' THEN jsonb_build_object(
        'name',row_data->'name','description',row_data->'description',
        'avatar_url',row_data->'avatar_url','archived_at',row_data->'archived_at')
    END
$$;

CREATE OR REPLACE FUNCTION work_sync_capture_row(kind text, row_data jsonb, is_deleted boolean)
RETURNS void LANGUAGE plpgsql AS $$
DECLARE next_sequence bigint;
BEGIN
    IF kind = 'agent' AND row_data->>'kind' IS DISTINCT FROM 'user' THEN RETURN; END IF;
    -- A row lock held until commit serializes allocation and commit order.
    -- A rolled-back write rolls back both the sequence and its log entry.
    UPDATE work_sync_scope SET sequence = sequence + 1
      WHERE workspace_id = (row_data->>'workspace_id')::uuid
      RETURNING sequence INTO next_sequence;
    IF NOT FOUND THEN RETURN; END IF;
    INSERT INTO work_sync_change(workspace_id,sequence,kind,entity_id,deleted,fields)
    VALUES ((row_data->>'workspace_id')::uuid,next_sequence,kind,(row_data->>'id')::uuid,
            is_deleted,CASE WHEN is_deleted THEN '{}'::jsonb ELSE work_sync_fields(kind,row_data) END);
END
$$;

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
-- Safe to retry if DDL committed before the migration ledger was updated.
DROP TRIGGER IF EXISTS work_sync_issue ON issue;
CREATE TRIGGER work_sync_issue AFTER INSERT OR UPDATE OR DELETE ON issue
FOR EACH ROW EXECUTE FUNCTION work_sync_capture();
DROP TRIGGER IF EXISTS work_sync_project ON project;
CREATE TRIGGER work_sync_project AFTER INSERT OR UPDATE OR DELETE ON project
FOR EACH ROW EXECUTE FUNCTION work_sync_capture();
DROP TRIGGER IF EXISTS work_sync_agent ON agent;
CREATE TRIGGER work_sync_agent AFTER INSERT OR UPDATE OR DELETE ON agent
FOR EACH ROW EXECUTE FUNCTION work_sync_capture();
