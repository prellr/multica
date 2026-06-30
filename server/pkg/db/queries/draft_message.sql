-- Draft conversation rail (Rail-1). A draft-level, un-anchored conversation
-- surface: a flat per-draft message log, distinct from the anchored annotation
-- threads. Every query is scoped to a draft_id the handler has already resolved
-- (and authorized) via loadDraftForUser, so these queries take the resolved
-- draft.ID rather than re-checking workspace/owner here — the parent draft is
-- the authorization boundary. workspace_id is denormalized onto the message for
-- self-describing rows and is set at create time from the resolved draft.

-- name: ListDraftMessages :many
-- The whole conversation for a draft, oldest-first (creation order = reading
-- order down the rail). Satisfied by idx_draft_message_draft_created.
SELECT * FROM draft_message
WHERE draft_id = $1
ORDER BY created_at ASC;

-- name: CreateDraftMessage :one
-- Append a message to a draft's conversation. author_user_id is nullable (NULL
-- for an agent author in Rail-2); the Rail-1 handler always passes the
-- requesting user. author_type is an open enum normalized in Go before reaching
-- here.
INSERT INTO draft_message (
    draft_id, workspace_id, author_type, author_user_id, body
) VALUES (
    $1, $2, $3, $4, $5
) RETURNING *;
