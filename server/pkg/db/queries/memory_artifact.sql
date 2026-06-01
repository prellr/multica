-- name: ListMemoryArtifacts :many
-- Default list path: workspace-scoped, optional kind / parent / tags
-- filters, archived hidden by default.
--
-- The narg-and-bool pattern (sqlc.arg + sqlc.narg) lets callers pass
-- "no filter" without coercing nil to a specific value. include_archived
-- defaults FALSE so paginated UI doesn't accidentally surface archived
-- rows after a redeploy.
--
-- Tags: when non-null, the filter uses the `&&` overlap operator so
-- the caller can pass either a single tag (matches rows that have it)
-- or several tags (matches rows that have at least one — OR semantics).
-- AND semantics across multiple tags would use `@>` but isn't needed
-- by current callers; revisit if a "must have all these tags" surface
-- shows up. The `tags` column has a GIN index from migration 068 so
-- this stays cheap at scale.
-- Anchor filter (anchor_type + anchor_id) lets the UI pivot to "everything
-- about issue X" and compose that with kind/tags — without the separate
-- by-anchor endpoint. include_system hides the high-volume orchestrator log
-- kinds (session, dispatch_event) from the default human view: they're
-- excluded UNLESS the caller passes include_system=true OR explicitly filters
-- by a kind (asking for kind=session means you want sessions). Without this,
-- a workspace running squads buries its wikis/runbooks under dispatch logs.
SELECT * FROM memory_artifact
WHERE workspace_id = $1
  AND (sqlc.narg('kind')::text IS NULL OR kind = sqlc.narg('kind'))
  AND (sqlc.narg('parent_id')::uuid IS NULL OR parent_id = sqlc.narg('parent_id'))
  AND (sqlc.narg('anchor_type')::text IS NULL OR anchor_type = sqlc.narg('anchor_type'))
  AND (sqlc.narg('anchor_id')::uuid IS NULL OR anchor_id = sqlc.narg('anchor_id'))
  AND (sqlc.narg('tags')::text[] IS NULL OR tags && sqlc.narg('tags')::text[])
  AND (sqlc.arg('include_archived')::bool OR archived_at IS NULL)
  AND (
    sqlc.arg('include_system')::bool
    OR sqlc.narg('kind')::text IS NOT NULL
    OR kind NOT IN ('session', 'dispatch_event')
  )
  AND (NOT sqlc.arg('unverified_only')::bool OR verified_at IS NULL)
ORDER BY created_at DESC
LIMIT  sqlc.arg('limit')::int
OFFSET sqlc.arg('offset')::int;

-- name: CountMemoryArtifacts :one
-- Companion to ListMemoryArtifacts so the UI can paginate without an
-- extra round-trip per page. Filter clauses must mirror the list query
-- exactly or the totals desync.
SELECT count(*) FROM memory_artifact
WHERE workspace_id = $1
  AND (sqlc.narg('kind')::text IS NULL OR kind = sqlc.narg('kind'))
  AND (sqlc.narg('parent_id')::uuid IS NULL OR parent_id = sqlc.narg('parent_id'))
  AND (sqlc.narg('anchor_type')::text IS NULL OR anchor_type = sqlc.narg('anchor_type'))
  AND (sqlc.narg('anchor_id')::uuid IS NULL OR anchor_id = sqlc.narg('anchor_id'))
  AND (sqlc.narg('tags')::text[] IS NULL OR tags && sqlc.narg('tags')::text[])
  AND (sqlc.arg('include_archived')::bool OR archived_at IS NULL)
  AND (
    sqlc.arg('include_system')::bool
    OR sqlc.narg('kind')::text IS NOT NULL
    OR kind NOT IN ('session', 'dispatch_event')
  )
  AND (NOT sqlc.arg('unverified_only')::bool OR verified_at IS NULL);

-- name: GetMemoryArtifact :one
SELECT * FROM memory_artifact
WHERE id = $1 AND workspace_id = $2;

-- name: GetMemoryArtifactBySlug :one
-- Stable URL lookup: /memory/<kind>/<slug>. Returns at most one row
-- thanks to the (workspace_id, kind, slug) uniqueness constraint.
SELECT * FROM memory_artifact
WHERE workspace_id = $1 AND kind = $2 AND slug = $3;

-- name: ListMemoryArtifactsByAnchor :many
-- "Show me everything anchored to issue X" — the query that powers
-- runtime context injection (when an agent claims a task on issue X,
-- the daemon fetches its memory artifacts and embeds them in CLAUDE.md).
SELECT * FROM memory_artifact
WHERE workspace_id  = $1
  AND anchor_type   = $2
  AND anchor_id     = $3
  AND archived_at IS NULL
ORDER BY created_at DESC
LIMIT $4;

-- name: SearchMemoryArtifacts :many
-- Full-text search via the generated tsvector + GIN index.
-- websearch_to_tsquery accepts user-friendly syntax (quotes, OR, -)
-- without erroring on unbalanced quotes the way to_tsquery does.
-- ts_rank_cd weighs cover density so "exact phrase match" outranks
-- "two terms in different parts of a long doc."
SELECT *,
       ts_rank_cd(content_tsv, websearch_to_tsquery('english', $2)) AS rank
FROM memory_artifact
WHERE workspace_id = $1
  AND archived_at IS NULL
  AND content_tsv @@ websearch_to_tsquery('english', $2)
  AND (sqlc.narg('kind')::text IS NULL OR kind = sqlc.narg('kind'))
  AND (sqlc.narg('tags')::text[] IS NULL OR tags && sqlc.narg('tags')::text[])
  AND (
    sqlc.arg('include_system')::bool
    OR sqlc.narg('kind')::text IS NOT NULL
    OR kind NOT IN ('session', 'dispatch_event')
  )
ORDER BY rank DESC, created_at DESC
LIMIT  sqlc.arg('limit')::int
OFFSET sqlc.arg('offset')::int;

-- name: CountSearchMemoryArtifacts :one
-- Companion total for SearchMemoryArtifacts so the UI can show "N results"
-- and paginate. Filter clauses must mirror the search query exactly (minus
-- rank/order/limit) or the total desyncs from the page.
SELECT count(*) FROM memory_artifact
WHERE workspace_id = $1
  AND archived_at IS NULL
  AND content_tsv @@ websearch_to_tsquery('english', $2)
  AND (sqlc.narg('kind')::text IS NULL OR kind = sqlc.narg('kind'))
  AND (sqlc.narg('tags')::text[] IS NULL OR tags && sqlc.narg('tags')::text[])
  AND (
    sqlc.arg('include_system')::bool
    OR sqlc.narg('kind')::text IS NOT NULL
    OR kind NOT IN ('session', 'dispatch_event')
  );

-- name: ListMemoryTags :many
-- Top tags by frequency for the active workspace, powering the filter
-- bar's tag autocomplete. Non-archived only — archived rows shouldn't
-- suggest stale tags. The cross join with unnest(tags) expands each row's
-- tag array into rows before grouping; cheap thanks to the GIN index on
-- tags (migration 068) keeping the scan tight.
SELECT t.tag::text AS tag, count(*) AS count
FROM memory_artifact m, unnest(m.tags) AS t(tag)
WHERE m.workspace_id = $1
  AND m.archived_at IS NULL
GROUP BY t.tag
ORDER BY count DESC, t.tag ASC
LIMIT sqlc.arg('limit')::int;

-- name: CreateMemoryArtifact :one
-- Author validation (member vs agent existence) happens in the service
-- layer; the SQL just records what it's told. Slug optional — pass an
-- empty TEXT (validated as null in service) for "no slug."
INSERT INTO memory_artifact (
    workspace_id, kind, parent_id,
    title, content, slug,
    anchor_type, anchor_id,
    author_type, author_id,
    tags, metadata,
    always_inject_at_runtime
) VALUES (
    $1, $2, sqlc.narg('parent_id'),
    $3, $4, sqlc.narg('slug'),
    sqlc.narg('anchor_type'), sqlc.narg('anchor_id'),
    $5, $6,
    $7, $8,
    COALESCE(sqlc.narg('always_inject_at_runtime')::bool, false)
) RETURNING *;

-- name: UpdateMemoryArtifact :one
-- Partial update via narg COALESCE. Only fields the caller passes get
-- updated. Tags and metadata pass through directly because they're
-- already nullable JSONB / array — narg semantics handled by sqlc.
UPDATE memory_artifact SET
    title                    = COALESCE(sqlc.narg('title'), title),
    content                  = COALESCE(sqlc.narg('content'), content),
    slug                     = sqlc.narg('slug'),
    parent_id                = sqlc.narg('parent_id'),
    anchor_type              = sqlc.narg('anchor_type'),
    anchor_id                = sqlc.narg('anchor_id'),
    tags                     = COALESCE(sqlc.narg('tags')::text[], tags),
    metadata                 = COALESCE(sqlc.narg('metadata')::jsonb, metadata),
    always_inject_at_runtime = COALESCE(sqlc.narg('always_inject_at_runtime')::bool, always_inject_at_runtime),
    updated_at               = now()
WHERE id = $1 AND workspace_id = $2
RETURNING *;

-- name: ListAlwaysInjectArtifacts :many
-- Workspace-wide artifacts the user has flagged for injection into every
-- agent task. Used by the runtime context path alongside the per-anchor
-- lookups. Active (non-archived) only; ordered most-recently-updated
-- first so a recently-edited "How we deploy" beats a stale entry of the
-- same kind.
SELECT * FROM memory_artifact
WHERE workspace_id              = $1
  AND always_inject_at_runtime  = true
  AND archived_at              IS NULL
ORDER BY updated_at DESC
LIMIT $2;

-- name: ArchiveMemoryArtifact :one
-- Soft-delete: stamps archived_at + archived_by. Idempotent — re-archive
-- is a no-op the handler converts to 409.
UPDATE memory_artifact
SET archived_at = now(),
    archived_by = $3,
    updated_at  = now()
WHERE id = $1 AND workspace_id = $2
RETURNING *;

-- name: RestoreMemoryArtifact :one
UPDATE memory_artifact
SET archived_at = NULL,
    archived_by = NULL,
    updated_at  = now()
WHERE id = $1 AND workspace_id = $2
RETURNING *;

-- name: VerifyMemoryArtifact :one
-- Stamps verified_at without touching content or creating a revision.
-- Does NOT update updated_at — content didn't change.
UPDATE memory_artifact
SET verified_at = now()
WHERE id = $1 AND workspace_id = $2
RETURNING *;

-- name: DeleteMemoryArtifact :exec
-- Hard delete. Available but not the default — UI surfaces archive
-- as the primary action; delete is reserved for admins.
DELETE FROM memory_artifact
WHERE id = $1 AND workspace_id = $2;
