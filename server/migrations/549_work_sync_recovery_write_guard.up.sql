-- Enrollment already takes business table barriers. Backfill under the same
-- barriers so an enrolled workspace always has a preexisting lockable guard.
LOCK TABLE issue, project, agent, work_sync_scope IN SHARE ROW EXCLUSIVE MODE;
INSERT INTO work_sync_recovery_fence(workspace_id)
SELECT workspace_id FROM work_sync_scope ON CONFLICT DO NOTHING;

CREATE OR REPLACE FUNCTION work_sync_recovery_write_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    old_workspace uuid;
    new_workspace uuid;
    item record;
BEGIN
    IF TG_OP <> 'INSERT' THEN old_workspace := OLD.workspace_id; END IF;
    IF TG_OP <> 'DELETE' THEN new_workspace := NEW.workspace_id; END IF;
    IF TG_TABLE_NAME = 'work_sync_scope' AND TG_OP = 'INSERT' THEN
        INSERT INTO work_sync_recovery_fence(workspace_id) VALUES(new_workspace)
        ON CONFLICT DO NOTHING;
    END IF;
    -- Lock the preexisting row, not merely an MVCC existence check. Waiting
    -- writers see a committed fence; stale repeatable-read writers serialize.
    FOR item IN SELECT proof FROM work_sync_recovery_fence
        WHERE workspace_id IN (old_workspace,new_workspace)
        ORDER BY workspace_id FOR SHARE
    LOOP
        IF item.proof IS NOT NULL THEN
            RAISE EXCEPTION 'workspace recovery fence is closed' USING ERRCODE='55000';
        END IF;
    END LOOP;
    IF TG_OP = 'DELETE' THEN RETURN OLD; END IF;
    RETURN NEW;
END $$;

DROP TRIGGER IF EXISTS work_sync_recovery_guard ON issue;
CREATE TRIGGER work_sync_recovery_guard BEFORE INSERT OR UPDATE OR DELETE ON issue
FOR EACH ROW EXECUTE FUNCTION work_sync_recovery_write_guard();
DROP TRIGGER IF EXISTS work_sync_recovery_guard ON project;
CREATE TRIGGER work_sync_recovery_guard BEFORE INSERT OR UPDATE OR DELETE ON project
FOR EACH ROW EXECUTE FUNCTION work_sync_recovery_write_guard();
DROP TRIGGER IF EXISTS work_sync_recovery_guard ON agent;
CREATE TRIGGER work_sync_recovery_guard BEFORE INSERT OR UPDATE OR DELETE ON agent
FOR EACH ROW EXECUTE FUNCTION work_sync_recovery_write_guard();
DROP TRIGGER IF EXISTS work_sync_recovery_guard ON work_sync_scope;
CREATE TRIGGER work_sync_recovery_guard BEFORE INSERT OR UPDATE OR DELETE ON work_sync_scope
FOR EACH ROW EXECUTE FUNCTION work_sync_recovery_write_guard();
