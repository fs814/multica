CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS issue_pull_request_exclusion_pkey ON issue_pull_request_exclusion (issue_id, pull_request_id);
