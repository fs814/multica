-- Replicated descriptive edits must not dispatch automation. The setting is
-- transaction-local and only used by the explicitly enabled Work sync writer.
DROP TRIGGER capture_issue_collaboration_wakeup ON issue;
CREATE TRIGGER capture_issue_collaboration_wakeup AFTER UPDATE ON issue
FOR EACH ROW WHEN (current_setting('multica.work_sync_apply', true) IS DISTINCT FROM 'on')
EXECUTE FUNCTION capture_issue_collaboration_wakeup();
