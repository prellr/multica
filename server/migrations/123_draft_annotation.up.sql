-- Draft annotation layer (Drafts slice 1). A non-destructive overlay on a
-- draft's markdown body: a user (or, in slice 2, an agent) selects a span of
-- text and attaches a typed, anchored, threaded annotation. The body stays
-- clean markdown — the highlight/pin is rendered from the persisted anchor,
-- never inlined into the document. Anchors re-anchor to their text as the body
-- is edited (the engine lives in packages/core/drafts/reanchor.ts); when the
-- range moves, the new quote/context/pos_hint is PATCHed back here.
--
-- Slice 1 is HUMAN-ONLY (every annotation/message is author_type='user'), but
-- author_type + author_user_id exist now so slice 2 (agent authoring/replies)
-- is a drop-in with no migration.
CREATE TABLE draft_annotation (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    draft_id UUID NOT NULL REFERENCES draft(id) ON DELETE CASCADE,
    -- Denormalized from the parent draft so every annotation row carries its
    -- own workspace scope (multi-tenancy). The handler still resolves the
    -- parent draft via loadDraftForUser first; this column makes a future
    -- workspace-scoped sweep / RLS policy cheap and keeps the row
    -- self-describing.
    workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    -- Open enum: 'user' in slice 1, 'agent' in slice 2. Intentionally NOT a
    -- CHECK so a newer server can introduce an author kind an older one never
    -- saw; the Go side switches with a `default` branch, the TS side treats an
    -- unknown value as a generic fallback (enum-drift rule).
    author_type TEXT NOT NULL DEFAULT 'user',
    -- NULL when author_type != 'user' (e.g. an agent author in slice 2).
    author_user_id UUID REFERENCES "user"(id) ON DELETE SET NULL,
    -- Open enum: comment | suggestion | approve | block | highlight. No CHECK
    -- (same forward-compat reasoning as author_type/status). A bare highlight
    -- has zero thread messages; the other types open with an initial message.
    type TEXT NOT NULL DEFAULT 'comment',
    -- Anchor: a quote-plus-context fingerprint of the selected span. The
    -- re-anchoring engine locates this text in a (possibly edited) body and
    -- classifies the result matched/shifted/changed/orphaned. pos_hint is the
    -- char offset of the quote in the body at creation/last-move time.
    quote TEXT NOT NULL DEFAULT '',
    context_before TEXT NOT NULL DEFAULT '',
    context_after TEXT NOT NULL DEFAULT '',
    pos_hint INTEGER NOT NULL DEFAULT 0,
    -- Open enum: open | acknowledged | resolved | dismissed. Lifecycle of the
    -- annotation thread. Same no-CHECK forward-compat posture.
    state TEXT NOT NULL DEFAULT 'open',
    -- Suggestion payload (type='suggestion'): the proposed before/after text.
    -- NULL for non-suggestion types.
    suggestion_before TEXT,
    suggestion_after TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Drives the default per-draft annotation list, filterable by state, ordered
-- oldest-first (creation order = reading order down the document margin). The
-- compound shape satisfies WHERE draft_id [+ state] ORDER BY created_at in a
-- single index scan.
CREATE INDEX idx_draft_annotation_draft_state_created
    ON draft_annotation(draft_id, state, created_at);

-- The annotation thread. The annotation row holds the anchor + type + state;
-- the THREAD is its messages. message[0] is the initial comment; a bare
-- highlight has zero messages. author_type/author_user_id mirror the parent's
-- forward-compat shape so an agent reply in slice 2 is a drop-in.
CREATE TABLE draft_annotation_message (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    annotation_id UUID NOT NULL REFERENCES draft_annotation(id) ON DELETE CASCADE,
    author_type TEXT NOT NULL DEFAULT 'user',
    author_user_id UUID REFERENCES "user"(id) ON DELETE SET NULL,
    body TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Drives the per-annotation thread fetch (and the bulk thread fetch for a whole
-- draft's annotations), oldest message first.
CREATE INDEX idx_draft_annotation_message_annotation_created
    ON draft_annotation_message(annotation_id, created_at);
