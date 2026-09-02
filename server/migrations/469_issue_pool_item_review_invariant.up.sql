ALTER TABLE issue_pool_item DROP CONSTRAINT issue_pool_item_check1;

ALTER TABLE issue_pool_item ADD CONSTRAINT issue_pool_item_review_check
    CHECK (
        status IN ('claimed', 'deferred')
        OR (reviewer_id IS NOT NULL AND reviewed_at IS NOT NULL)
    );
