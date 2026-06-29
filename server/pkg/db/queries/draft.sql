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

-- name: GetDraftByID :one
-- Id-only lookup, NOT owner/workspace-scoped. ONLY for trusted server-internal
-- paths that have already established access by other means — e.g. the daemon
-- task-dispatch hydrator, which resolves the draft from a task it already owns
-- and needs only the title for the prompt. Never reachable from a user request
-- boundary (all user-facing reads go through GetDraft).
SELECT * FROM draft WHERE id = $1;

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

-- name: CreateDraftTurnTask :one
-- Drafts slice 2 — task created when a human clicks Send on a draft. Like the
-- channel-mention task it has neither issue_id nor chat_session_id; the daemon
-- detects this variant via context.type == "draft_turn" and reads the draft +
-- open-annotation queue at execution time. Mirrors CreateChannelMentionTask.
INSERT INTO agent_task_queue (
    agent_id, runtime_id, issue_id, status, priority, context
) VALUES ($1, $2, NULL, 'queued', $3, $4)
RETURNING *;
