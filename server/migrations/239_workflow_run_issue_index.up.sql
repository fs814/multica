-- Issue -> Run projection lookup (plan section 6): rendering an Issue and
-- cancelling an Issue both need its active Run.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_workflow_run_issue
    ON workflow_run (issue_id, created_at DESC)
    WHERE issue_id IS NOT NULL;
