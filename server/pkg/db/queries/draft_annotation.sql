-- Draft annotation layer (Drafts slice 1). A non-destructive overlay on a
-- draft's markdown body. Every query is scoped to a draft_id the handler has
-- already resolved (and authorized) via loadDraftForUser, so these queries take
-- the resolved draft.ID rather than re-checking workspace/owner here — the
-- parent draft is the authorization boundary. workspace_id is denormalized onto
-- the annotation for self-describing rows and is set at create time from the
-- resolved draft.

-- name: ListDraftAnnotations :many
-- All annotations on a draft, oldest-first (creation order = reading order down
-- the margin). Satisfied by idx_draft_annotation_draft_state_created.
SELECT * FROM draft_annotation
WHERE draft_id = $1
ORDER BY created_at ASC;

-- name: GetDraftAnnotation :one
-- One annotation, scoped to its parent draft so an annotation id belonging to a
-- different draft returns no rows (the handler maps that to 404). The draft_id
-- predicate is the authorization tie-back: the handler resolved+authorized the
-- draft, and this guarantees the annotation actually belongs to it.
SELECT * FROM draft_annotation
WHERE id = $1 AND draft_id = $2;

-- name: ListDraftAnnotationMessages :many
-- The thread for a single annotation, oldest message first. message[0] is the
-- initial comment. Satisfied by idx_draft_annotation_message_annotation_created.
SELECT * FROM draft_annotation_message
WHERE annotation_id = $1
ORDER BY created_at ASC;

-- name: ListDraftAnnotationMessagesForDraft :many
-- Bulk thread fetch: every message across all of a draft's annotations, so the
-- list endpoint can assemble annotation+thread in two queries instead of N+1.
-- Ordered by annotation then time so the handler can group in a single pass.
SELECT m.* FROM draft_annotation_message m
JOIN draft_annotation a ON a.id = m.annotation_id
WHERE a.draft_id = $1
ORDER BY m.annotation_id, m.created_at ASC;

-- name: CreateDraftAnnotation :one
-- author_user_id is nullable (NULL for an agent author in slice 2; the slice-1
-- handler always passes the requesting user). type/state are open enums
-- normalized in Go before reaching here.
INSERT INTO draft_annotation (
    draft_id, workspace_id, author_type, author_user_id, type,
    quote, context_before, context_after, pos_hint,
    state, suggestion_before, suggestion_after
) VALUES (
    $1, $2, $3, $4, $5,
    $6, $7, $8, $9,
    $10, $11, $12
) RETURNING *;

-- name: UpdateDraftAnnotation :one
-- Field-by-field optional update via COALESCE/narg. Two callers:
--   - state transitions (resolve/dismiss/acknowledge) patch only `state`.
--   - re-anchoring persists the moved range: quote/context_before/context_after
--     /pos_hint are patched together when the engine reports the anchor shifted.
-- A non-supplied field is left untouched. The draft_id predicate is the
-- authorization tie-back (the handler already resolved+authorized the draft).
UPDATE draft_annotation SET
    state = COALESCE(sqlc.narg('state'), state),
    quote = COALESCE(sqlc.narg('quote'), quote),
    context_before = COALESCE(sqlc.narg('context_before'), context_before),
    context_after = COALESCE(sqlc.narg('context_after'), context_after),
    pos_hint = COALESCE(sqlc.narg('pos_hint'), pos_hint),
    updated_at = now()
WHERE id = $1 AND draft_id = $2
RETURNING *;

-- name: DeleteDraftAnnotation :exec
-- The draft_id predicate is the SQL-layer guard: an annotation id belonging to
-- another draft deletes zero rows rather than destroying an annotation the
-- caller never authorized against. Messages cascade via the FK.
DELETE FROM draft_annotation WHERE id = $1 AND draft_id = $2;

-- name: CreateDraftAnnotationMessage :one
-- Append a message to an annotation thread (reply, or the initial comment that
-- accompanies a non-highlight annotation). author_user_id nullable for the
-- slice-2 agent author; slice 1 always passes the requesting user.
INSERT INTO draft_annotation_message (
    annotation_id, author_type, author_user_id, body
) VALUES (
    $1, $2, $3, $4
) RETURNING *;
