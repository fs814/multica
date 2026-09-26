DROP TRIGGER IF EXISTS work_sync_issue ON issue;
DROP TRIGGER IF EXISTS work_sync_project ON project;
DROP TRIGGER IF EXISTS work_sync_agent ON agent;
DROP FUNCTION IF EXISTS work_sync_capture();
DROP FUNCTION IF EXISTS work_sync_capture_row(text,jsonb,boolean);
DROP FUNCTION IF EXISTS work_sync_fields(text,jsonb);
