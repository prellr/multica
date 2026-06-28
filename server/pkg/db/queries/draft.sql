-- Draft CRUD. Drafts are a standalone, workspace-scoped, owner-scoped entity
-- (their own table — unlike tasks, which share the issue table). Every query
-- binds workspace_id (multi-tenancy) and owner_user_id (slice-0 single-author
-- scoping) so a draft can never be read or mutated across either boundary.

-- name: ListDraftsForUser :many
-- One owner's drafts in one workspace, newest edit first. Satisfied by
-- idx_draft_workspace_owner_updated in a single index scan.
SELECT * FROM draft
WHERE workspace_id = $1 AND owner_user_id = $2
ORDER BY updated_at DESC;

-- name: GetDraft :one
-- Scoped to workspace + owner: a draft UUID belonging to another user (or
-- another workspace) returns no rows, which the handler maps to 404.
SELECT * FROM draft
WHERE id = $1 AND workspace_id = $2 AND owner_user_id = $3;

-- name: CreateDraft :one
-- Owner is the requesting user. title/body/status fall back to their column
-- defaults when the caller omits them (body '', status 'draft').
INSERT INTO draft (
    workspace_id, owner_user_id, title, body, status
) VALUES (
    $1, $2, $3, $4, $5
) RETURNING *;

-- name: UpdateDraft :one
-- Field-by-field optional update via COALESCE/narg so a partial PATCH (e.g.
-- body-only autosave) leaves title and status untouched. The workspace_id +
-- owner_user_id predicate is the SQL-layer guard: a UUID belonging to another
-- user or workspace updates zero rows rather than mutating someone else's draft.
UPDATE draft SET
    title = COALESCE(sqlc.narg('title'), title),
    body = COALESCE(sqlc.narg('body'), body),
    status = COALESCE(sqlc.narg('status'), status),
    updated_at = now()
WHERE id = $1 AND workspace_id = $2 AND owner_user_id = $3
RETURNING *;

-- name: DeleteDraft :exec
-- Defense-in-depth: workspace_id + owner_user_id are SQL-layer guards (see
-- DeleteProject / DeleteIssue for the rationale). A handler bug routing a
-- foreign UUID through this delete silently no-ops rather than destroying
-- another user's draft.
DELETE FROM draft WHERE id = $1 AND workspace_id = $2 AND owner_user_id = $3;
