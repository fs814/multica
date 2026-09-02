ALTER TABLE issue_pool_item
    DROP CONSTRAINT IF EXISTS issue_pool_item_review_check;

ALTER TABLE issue_pool_item
    ADD CONSTRAINT issue_pool_item_check1 CHECK (
        status = 'claimed' OR (reviewer_id IS NOT NULL AND reviewed_at IS NOT NULL)
    ) NOT VALID;
