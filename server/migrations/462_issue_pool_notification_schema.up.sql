CREATE TABLE issue_pool_notification (
    id UUID NOT NULL DEFAULT gen_random_uuid(),
    cycle_id UUID NOT NULL,
    autopilot_id UUID NOT NULL,
    workspace_id UUID NOT NULL,
    recipient_id UUID NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('review_requested', 'cycle_terminal')),
    inbox_item_id UUID NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
