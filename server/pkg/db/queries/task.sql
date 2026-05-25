-- Task CRUD. Tasks live in the same `issue` table as issues, discriminated
-- by `kind = 'task'`. Every query in this file binds the kind explicitly so
-- the boundary cannot be crossed by accident:
--   * Reads filter `kind = 'task'` so an issue row can never be returned.
--   * Writes INSERT `kind = 'task'` so creates never accidentally become
--     issues. Updates and deletes match on `kind = 'task'` so a typo'd id
--     pointing at an issue is a no-op rather than a destructive mutation.
--
-- Tasks omit `number` (no human-readable identifier) and `priority` is left
-- at its column default — neither is exposed on the task surface, and the
-- column-level defaults keep INSERTs short. Status is required from the
-- caller; the handler validates it against the task-status subset before
-- this query is invoked.

-- name: CreateTask :one
INSERT INTO issue (
    workspace_id, title, description, status,
    assignee_type, assignee_id, creator_type, creator_id,
    parent_issue_id, position, start_date, due_date, project_id, kind
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, 'task'
) RETURNING *;

-- name: GetTask :one
SELECT * FROM issue
WHERE id = $1 AND workspace_id = $2 AND kind = 'task';

-- name: ListTasks :many
-- Workspace-scoped task list. The shape is intentionally simpler than
-- ListIssues — no priority filter, no involves_user_id squad fan-out, no
-- metadata filter. Tasks are a lighter surface; if those filters become
-- needed they can be added without breaking callers (sqlc.narg semantics).
SELECT i.id, i.workspace_id, i.title, i.description, i.status,
       i.assignee_type, i.assignee_id, i.creator_type, i.creator_id,
       i.parent_issue_id, i.position, i.start_date, i.due_date,
       i.created_at, i.updated_at, i.project_id, i.metadata, i.kind
FROM issue i
WHERE i.workspace_id = $1
  AND i.kind = 'task'
  AND (sqlc.narg('status')::text IS NULL OR i.status = sqlc.narg('status'))
  AND (sqlc.narg('assignee_id')::uuid IS NULL OR i.assignee_id = sqlc.narg('assignee_id'))
  AND (sqlc.narg('creator_id')::uuid IS NULL OR i.creator_id = sqlc.narg('creator_id'))
  AND (sqlc.narg('project_id')::uuid IS NULL OR i.project_id = sqlc.narg('project_id'))
  AND (sqlc.narg('parent_issue_id')::uuid IS NULL OR i.parent_issue_id = sqlc.narg('parent_issue_id'))
ORDER BY i.position ASC, i.created_at DESC
LIMIT $2 OFFSET $3;

-- name: CountTasks :one
SELECT count(*) FROM issue i
WHERE i.workspace_id = $1
  AND i.kind = 'task'
  AND (sqlc.narg('status')::text IS NULL OR i.status = sqlc.narg('status'))
  AND (sqlc.narg('assignee_id')::uuid IS NULL OR i.assignee_id = sqlc.narg('assignee_id'))
  AND (sqlc.narg('creator_id')::uuid IS NULL OR i.creator_id = sqlc.narg('creator_id'))
  AND (sqlc.narg('project_id')::uuid IS NULL OR i.project_id = sqlc.narg('project_id'))
  AND (sqlc.narg('parent_issue_id')::uuid IS NULL OR i.parent_issue_id = sqlc.narg('parent_issue_id'));

-- name: UpdateTask :one
-- Field-by-field optional update. The `kind = 'task'` predicate is the
-- defense-in-depth that prevents an issue UUID typo from mutating an issue
-- row through the task endpoint. Status validation is the handler's job.
UPDATE issue SET
    title = COALESCE(sqlc.narg('title'), title),
    description = COALESCE(sqlc.narg('description'), description),
    status = COALESCE(sqlc.narg('status'), status),
    assignee_type = sqlc.narg('assignee_type'),
    assignee_id = sqlc.narg('assignee_id'),
    position = COALESCE(sqlc.narg('position'), position),
    start_date = sqlc.narg('start_date'),
    due_date = sqlc.narg('due_date'),
    parent_issue_id = sqlc.narg('parent_issue_id'),
    project_id = sqlc.narg('project_id'),
    updated_at = now()
WHERE id = $1 AND workspace_id = $2 AND kind = 'task'
RETURNING *;

-- name: DeleteTask :exec
-- Both the workspace_id tenant guard and the kind discriminator are SQL-layer
-- safety nets (see DeleteIssue in issue.sql for the workspace_id rationale).
-- A handler bug routing an issue UUID through the task delete endpoint
-- silently no-ops rather than destroying an issue row.
DELETE FROM issue WHERE id = $1 AND workspace_id = $2 AND kind = 'task';

-- name: PromoteTaskToIssue :one
-- Promote a task to a full issue. Flips kind='task' -> 'issue' and stamps the
-- row with a workspace-scoped number. The handler does the IncrementIssueCounter
-- bump alongside this UPDATE inside a single transaction, so a crash between
-- the two would leave the workspace counter advanced but no row claiming it
-- (acceptable — the counter is monotonic, gaps are fine and far less
-- disruptive than a duplicate number).
--
-- The `kind = 'task'` predicate is the safety net: passing an issue UUID
-- through the promote endpoint is a no-op (zero rows updated). This mirrors
-- the safety pattern used by UpdateTask / DeleteTask.
UPDATE issue SET
    kind = 'issue',
    number = $3,
    updated_at = now()
WHERE id = $1 AND workspace_id = $2 AND kind = 'task'
RETURNING *;
