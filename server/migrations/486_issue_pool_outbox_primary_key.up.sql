DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'issue_pool_notification_outbox'::regclass
          AND contype = 'p'
    ) THEN
        ALTER TABLE issue_pool_notification_outbox
            ADD CONSTRAINT issue_pool_notification_outbox_pkey
            PRIMARY KEY USING INDEX issue_pool_notification_outbox_pkey_candidate;
    END IF;
END $$;
