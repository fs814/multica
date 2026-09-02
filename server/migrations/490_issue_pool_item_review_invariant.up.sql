-- 449 required every non-claimed item to carry a human review. 470 introduced
-- deferred recovery outcomes, which are system decisions and have no reviewer.
ALTER TABLE issue_pool_item
    DROP CONSTRAINT IF EXISTS issue_pool_item_check1,
    DROP CONSTRAINT IF EXISTS issue_pool_item_review_check;

ALTER TABLE issue_pool_item
    ADD CONSTRAINT issue_pool_item_review_check CHECK (
        status IN ('claimed', 'deferred') OR
        (reviewer_id IS NOT NULL AND reviewed_at IS NOT NULL)
    );
