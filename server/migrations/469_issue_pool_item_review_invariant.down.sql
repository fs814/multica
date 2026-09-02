ALTER TABLE issue_pool_item DROP CONSTRAINT issue_pool_item_review_check;

UPDATE issue_pool_item
SET status = 'claimed',
    failure_reason = NULL,
    completed_at = NULL,
    updated_at = now()
WHERE status = 'deferred' AND reviewer_id IS NULL;

ALTER TABLE issue_pool_item ADD CONSTRAINT issue_pool_item_check1
    CHECK (status = 'claimed' OR (reviewer_id IS NOT NULL AND reviewed_at IS NOT NULL));
