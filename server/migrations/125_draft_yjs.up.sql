-- Drafts slice 3a — the human CRDT (Yjs) co-editing floor.
--
-- The live truth of a co-edited draft is a Yjs document. The server is a DUMB
-- RELAY: it never decodes the CRDT, it only fans out and PERSISTS the opaque
-- update byte-stream so a fresh or reconnecting client can converge by replaying
-- the log (Yjs merges updates idempotently). `draft.body` markdown remains the
-- snapshot every non-collab surface reads (issue graduation, list previews,
-- Aye's `multica draft get`); it is now DERIVED — serialized client-side on
-- quiescence — not the source of truth.

-- The append-only update log. Each row is one opaque Yjs `update` frame as it
-- arrived over the wire. Replay-on-connect streams every row for a draft, in
-- `seq` order, back to a joining client.
--
-- COMPACTION IS DEFERRED (log-only). This table grows unbounded with edits —
-- that is intentional for 3a and correct (a full replay always converges). A
-- later slice (or a swap to pg_crdt) collapses the log into draft.yjs_state and
-- truncates; the column below is the forward-compat seam for that snapshot.
CREATE TABLE draft_yjs_update (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    draft_id UUID NOT NULL REFERENCES draft(id) ON DELETE CASCADE,
    -- Per-table monotonic ordering. bigserial is globally monotonic, which is
    -- all replay needs: rows for one draft come back in insertion order. (A
    -- strict per-draft counter would require a serializable upsert; global
    -- monotonicity is simpler and sufficient because the replay query filters
    -- by draft_id and only relies on relative order within that draft.)
    seq BIGSERIAL NOT NULL,
    -- The opaque Yjs update bytes. The server NEVER decodes these — keeping them
    -- opaque preserves the option to swap persistence to pg_crdt later with no
    -- protocol change.
    update BYTEA NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Drives replay-on-connect: every update for one draft, in seq order, in a
-- single index scan.
CREATE INDEX idx_draft_yjs_update_draft_seq
    ON draft_yjs_update(draft_id, seq);

-- Forward-compat seam for the deferred compaction: a single compacted snapshot
-- of the draft's Yjs state. NULL in 3a (nothing writes it yet); a later slice
-- fills it and truncates the log it summarizes. Kept opaque (BYTEA) for the
-- same dumb-relay reason as the log.
ALTER TABLE draft
    ADD COLUMN yjs_state BYTEA NULL;
