DO $migration$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conname = 'workflow_callback_delivery_pkey'
          AND conrelid = 'workflow_callback_delivery'::regclass
    ) THEN
        ALTER TABLE workflow_callback_delivery
            ADD CONSTRAINT workflow_callback_delivery_pkey
            PRIMARY KEY USING INDEX workflow_callback_delivery_pkey;
    END IF;
END
$migration$;
