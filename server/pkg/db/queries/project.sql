-- name: ListProjects :many
-- include_archived defaults FALSE — archived projects are hidden from
-- the default list. Pass true to include them (used by an admin "Show
-- archived" toggle on the projects page).
SELECT * FROM project
WHERE workspace_id = $1
  AND (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status'))
  AND (sqlc.narg('priority')::text IS NULL OR priority = sqlc.narg('priority'))
  AND (sqlc.arg('include_archived')::bool OR archived_at IS NULL)
ORDER BY created_at DESC;

-- name: GetProject :one
SELECT * FROM project
WHERE id = $1;

-- name: GetProjectInWorkspace :one
SELECT * FROM project
WHERE id = $1 AND workspace_id = $2;

-- name: CreateProject :one
INSERT INTO project (
    workspace_id, title, description, icon, status,
    lead_type, lead_id, priority
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8
) RETURNING *;

-- name: UpdateProject :one
UPDATE project SET
    title = COALESCE(sqlc.narg('title'), title),
    description = sqlc.narg('description'),
    icon = sqlc.narg('icon'),
    status = COALESCE(sqlc.narg('status'), status),
    priority = COALESCE(sqlc.narg('priority'), priority),
    lead_type = sqlc.narg('lead_type'),
    lead_id = sqlc.narg('lead_id'),
    pipeline_kind = COALESCE(sqlc.narg('pipeline_kind')::project_pipeline_kind, pipeline_kind),
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: DeleteProject :exec
-- Defense-in-depth: workspace_id is a SQL-layer tenant guard. See DeleteIssue.
DELETE FROM project WHERE id = $1 AND workspace_id = $2;

-- name: UpdateProjectPipelineConfig :one
-- PR5a of the Ship Hub rebuild — write the per-project pipeline config
-- JSONB after the introspector has run against the repo's workflows
-- (PR8 will hook the introspector into webhook/scheduled refreshes).
-- Stamps pipeline_config_introspected_at so the read shim knows to
-- prefer this value over the legacy pipeline_kind enum.
--
-- Pass NULL to reset back to "use the legacy enum" (a manual override
-- path for the rare operator who wants to undo a bad introspection).
UPDATE project SET
    pipeline_config = sqlc.narg('pipeline_config')::jsonb,
    pipeline_config_introspected_at = CASE
        WHEN sqlc.narg('pipeline_config') IS NULL THEN NULL
        ELSE now()
    END,
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: SetProjectPipelineProposal :one
-- PR8 of the Ship Hub rebuild — park a destructively-differing
-- introspected config as a pending proposal. Stamps
-- pipeline_config_introspected_at too: the introspector DID run, the
-- operator just hasn't accepted its output yet. `pipeline_config` is
-- deliberately left untouched — the kanban keeps rendering the current
-- (operator-trusted) shape until Accept swaps the proposal in.
UPDATE project SET
    pipeline_config_proposed = sqlc.arg('pipeline_config_proposed')::jsonb,
    pipeline_config_proposed_at = now(),
    pipeline_config_introspected_at = now(),
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: ClearProjectPipelineProposal :one
-- PR8 — clear a pending proposal without touching pipeline_config.
-- Used by Reject (operator declined the change) and by the refresh
-- paths when a fresh introspection finds no diff / an additive diff,
-- which makes any previously-parked proposal stale.
UPDATE project SET
    pipeline_config_proposed = NULL,
    pipeline_config_proposed_at = NULL,
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: ApplyProjectPipelineProposal :one
-- PR8 — Accept: swap the pending proposal into pipeline_config, stamp
-- introspected_at, and clear the proposal columns in one statement so
-- there is no window where both the live config and the proposal are
-- visible. No-ops cleanly if no proposal is pending (the COALESCE keeps
-- pipeline_config as-is and the proposal columns are already NULL).
UPDATE project SET
    pipeline_config = COALESCE(pipeline_config_proposed, pipeline_config),
    pipeline_config_introspected_at = now(),
    pipeline_config_proposed = NULL,
    pipeline_config_proposed_at = NULL,
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: StampProjectPipelineIntrospectedAt :one
-- PR8 — record that the introspector ran and found nothing to change.
-- Only bumps the audit timestamp; pipeline_config and the proposal
-- columns are left exactly as they were.
UPDATE project SET
    pipeline_config_introspected_at = now(),
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: ArchiveProject :one
-- Soft-delete: stamps archived_at + archived_by. Idempotent — re-archiving
-- a row that's already archived just refreshes the timestamp.
UPDATE project
SET archived_at = now(),
    archived_by = $2,
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: RestoreProject :one
-- Reverses ArchiveProject. archived_by is cleared so the next archive
-- record reflects who actually re-archived.
UPDATE project
SET archived_at = NULL,
    archived_by = NULL,
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: CountIssuesByProject :one
SELECT count(*) FROM issue
WHERE project_id = $1;

-- name: GetProjectIssueStats :many
SELECT project_id,
       count(*)::bigint AS total_count,
       count(*) FILTER (WHERE status IN ('done', 'cancelled'))::bigint AS done_count
FROM issue
WHERE project_id = ANY(sqlc.arg('project_ids')::uuid[])
GROUP BY project_id;
