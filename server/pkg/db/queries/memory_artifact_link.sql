-- name: CreateMemoryArtifactLink :one
-- Idempotent — ON CONFLICT DO NOTHING on the natural key + RETURNING
-- lets the caller treat re-create as "ensure this link exists." If the
-- link already existed the INSERT returns zero rows, which the handler
-- catches and re-reads via the unique-key lookup to keep the API
-- contract (always return the canonical row).
INSERT INTO memory_artifact_link (
    workspace_id, artifact_id,
    target_type, target_id,
    relation_type,
    created_by_type, created_by_id
) VALUES (
    $1, $2, $3, $4, $5, $6, $7
)
ON CONFLICT (artifact_id, target_type, target_id, relation_type)
DO NOTHING
RETURNING *;

-- name: GetMemoryArtifactLink :one
-- Companion to the conflict path above — when the INSERT returned no
-- rows the link already existed; fetch the canonical row via the
-- unique key so the handler still returns it.
SELECT * FROM memory_artifact_link
WHERE artifact_id = $1 AND target_type = $2 AND target_id = $3 AND relation_type = $4;

-- name: GetMemoryArtifactLinkByID :one
-- Used by the DELETE handler for ownership / workspace scoping checks
-- before the destructive op.
SELECT * FROM memory_artifact_link
WHERE id = $1 AND workspace_id = $2;

-- name: ListMemoryArtifactLinks :many
-- Outgoing links — "this artifact links to these things." Powers the
-- detail page's Links section. Ordered by relation_type so the
-- grouping renders without client-side resorting.
SELECT * FROM memory_artifact_link
WHERE workspace_id = $1
  AND artifact_id  = $2
ORDER BY relation_type ASC, created_at DESC;

-- name: ListMemoryArtifactBacklinks :many
-- Incoming links — "these artifacts link to this target." Powers
-- a future "show me everything that references issue X" pivot, and
-- the runtime-injection follow-up that traverses the graph by one hop
-- to surface relevant context.
SELECT * FROM memory_artifact_link
WHERE workspace_id = $1
  AND target_type  = $2
  AND target_id    = $3
ORDER BY created_at DESC
LIMIT $4;

-- name: DeleteMemoryArtifactLink :exec
-- Hard delete by id. Workspace_id is in the WHERE clause as a defense-
-- in-depth check so a leaked link-id in one workspace can't trigger a
-- delete in another.
DELETE FROM memory_artifact_link
WHERE id = $1 AND workspace_id = $2;
