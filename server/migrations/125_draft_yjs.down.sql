-- Reverse slice 3a's Yjs persistence. Dropping the update log discards the live
-- CRDT history; draft.body markdown (the client-serialized snapshot) survives on
-- the draft table, so a downgrade loses co-edit history but not draft content.
ALTER TABLE draft
    DROP COLUMN IF EXISTS yjs_state;

DROP INDEX IF EXISTS idx_draft_yjs_update_draft_seq;
DROP TABLE IF EXISTS draft_yjs_update;
