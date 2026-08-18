DO $migration$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conname = 'workflow_callback_destination_pkey'
          AND conrelid = 'workflow_callback_destination'::regclass
    ) THEN
        ALTER TABLE workflow_callback_destination
            ADD CONSTRAINT workflow_callback_destination_pkey
            PRIMARY KEY USING INDEX workflow_callback_destination_pkey;
    END IF;
END
$migration$;
