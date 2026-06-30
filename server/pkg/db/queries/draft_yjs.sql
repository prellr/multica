-- Drafts slice 3a — the Yjs co-editing update log. The server is a DUMB RELAY:
-- it persists each inbound update frame as opaque bytes (never decoding the
-- CRDT) and replays the log to a joining client. See migration 125 for the
-- log-only / compaction-deferred rationale.

-- name: AppendDraftYjsUpdate :one
-- Append one opaque Yjs update frame to a draft's log. `seq` is assigned by the
-- bigserial default; we return it (and the row) so the relay can log/observe the
-- monotonic position. The draft_id is the resolved draft.ID (the WS endpoint
-- gates access via loadDraftForUser before any append), never a raw URL string.
INSERT INTO draft_yjs_update (draft_id, update)
VALUES ($1, $2)
RETURNING *;

-- name: ListDraftYjsUpdates :many
-- Replay-on-connect: every persisted update for one draft, in insertion order.
-- A fresh client applies these in sequence and converges (Yjs merges
-- idempotently). Satisfied by idx_draft_yjs_update_draft_seq in a single scan.
SELECT * FROM draft_yjs_update
WHERE draft_id = $1
ORDER BY seq ASC;

-- name: CountDraftYjsUpdates :one
-- The log size for one draft. Used only for observability (logging the growing,
-- uncompacted log per the deferred-compaction note); not on any hot path.
SELECT COUNT(*) FROM draft_yjs_update
WHERE draft_id = $1;

-- name: DraftHasYjsState :one
-- Whether a draft has ANY persisted Yjs co-editing state. The draft GET handler
-- returns this so the editor seeds the Y.Doc from markdown ONLY for a brand-new
-- draft — seeding on top of a replayed persisted log duplicates the body. EXISTS
-- short-circuits on the first row, so this is cheaper than COUNT on a long log.
SELECT EXISTS (
  SELECT 1 FROM draft_yjs_update WHERE draft_id = $1
);
