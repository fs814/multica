-- Submission history for a Step attempt, newest first. Rework preserves prior
-- submissions (plan section 4), so a Step legitimately has several.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_workflow_submission_step
    ON workflow_submission (step_id, submitted_at DESC);
