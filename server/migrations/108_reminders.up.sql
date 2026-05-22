CREATE TABLE reminder (
    id UUID DEFAULT gen_random_uuid() PRIMARY KEY,
    workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    creator_type TEXT NOT NULL CHECK (creator_type IN ('member', 'agent')),
    creator_id UUID NOT NULL,
    recipient_type TEXT NOT NULL CHECK (recipient_type IN ('member', 'agent')),
    recipient_id UUID NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('system', 'task', 'check_in')),
    title TEXT NOT NULL,
    body TEXT,
    issue_id UUID REFERENCES issue(id) ON DELETE SET NULL,
    remind_at TIMESTAMPTZ,
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'delivered', 'cancelled')),
    delivered_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX reminder_workspace_idx ON reminder (workspace_id);
CREATE INDEX reminder_recipient_idx ON reminder (workspace_id, recipient_type, recipient_id, status);
CREATE INDEX reminder_due_idx ON reminder (remind_at) WHERE status = 'pending' AND remind_at IS NOT NULL;
