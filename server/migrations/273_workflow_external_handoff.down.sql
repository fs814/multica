DROP TABLE IF EXISTS workflow_callback_delivery;
DROP TABLE IF EXISTS workflow_callback_destination;

ALTER TABLE workflow_run
    DROP COLUMN IF EXISTS callback_destination_id,
    DROP COLUMN IF EXISTS request_hash;

ALTER TABLE autopilot_trigger
    DROP COLUMN IF EXISTS signing_secret_encrypted;
