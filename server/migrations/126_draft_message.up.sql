-- Draft conversation rail (Drafts "global germination rail", Rail-1). A
-- draft-level, UN-anchored conversation surface — a flat per-draft chat log,
-- distinct from the anchored annotation threads (migration 123). Where an
-- annotation message is scoped to a span via a parent draft_annotation, a
-- draft_message hangs directly off the draft: it's the document-wide back-and-
-- forth, not a margin note.
--
-- Field-identical to draft_annotation_message (123) plus draft_id +
-- workspace_id, since these messages have no annotation parent to carry scope.
--
-- Rail-1 is HUMAN-ONLY (every message is author_type='user'), but author_type +
-- author_user_id exist now so Rail-2 (Aye authoring via `multica draft say`) is
-- a drop-in with no migration — agent-authored rows write author_type='agent'
-- with a NULL author_user_id and render with agent styling.
CREATE TABLE draft_message (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    draft_id UUID NOT NULL REFERENCES draft(id) ON DELETE CASCADE,
    -- Denormalized from the parent draft so every message row carries its own
    -- workspace scope (multi-tenancy). The handler still resolves the parent
    -- draft via loadDraftForUser first; this column makes a future
    -- workspace-scoped sweep / RLS policy cheap and keeps the row
    -- self-describing. Set at create time from the resolved draft.
    workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    -- Open enum: 'user' in Rail-1, 'agent' in Rail-2. Intentionally NOT a CHECK
    -- so a newer server can introduce an author kind an older one never saw; the
    -- Go side switches with a `default` branch, the TS side treats an unknown
    -- value as a generic fallback (enum-drift rule).
    author_type TEXT NOT NULL DEFAULT 'user',
    -- NULL when author_type != 'user' (e.g. an agent author in Rail-2).
    author_user_id UUID REFERENCES "user"(id) ON DELETE SET NULL,
    body TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Drives the default per-draft conversation fetch, oldest-first (creation order
-- = reading order down the rail). Satisfies WHERE draft_id ORDER BY created_at
-- in a single index scan.
CREATE INDEX idx_draft_message_draft_created
    ON draft_message(draft_id, created_at);
