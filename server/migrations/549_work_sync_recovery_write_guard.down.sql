-- Never downgrade a fenced source: removing the guard would reopen old writers.
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM work_sync_recovery_fence WHERE proof IS NOT NULL) THEN
        RAISE EXCEPTION 'cannot remove active recovery fences';
    END IF;
END $$;
DROP TRIGGER IF EXISTS work_sync_recovery_guard ON issue;
DROP TRIGGER IF EXISTS work_sync_recovery_guard ON project;
DROP TRIGGER IF EXISTS work_sync_recovery_guard ON agent;
DROP TRIGGER IF EXISTS work_sync_recovery_guard ON work_sync_scope;
DROP FUNCTION IF EXISTS work_sync_recovery_write_guard();
