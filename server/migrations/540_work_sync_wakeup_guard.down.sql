DROP TRIGGER capture_issue_collaboration_wakeup ON issue;
CREATE TRIGGER capture_issue_collaboration_wakeup AFTER UPDATE ON issue
FOR EACH ROW EXECUTE FUNCTION capture_issue_collaboration_wakeup();
