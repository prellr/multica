-- =====================
-- MCP Server Directory
-- =====================

-- name: UpsertMCPServerDirectoryEntry :one
INSERT INTO mcp_server_directory (
    id, name, slug, description, transport_types, publisher_name, homepage, stars, last_fetched_at
) VALUES (
    $1, $2, $3, sqlc.narg('description'), $4, sqlc.narg('publisher_name'), sqlc.narg('homepage'), $5, now()
)
ON CONFLICT (id) DO UPDATE SET
    name = EXCLUDED.name,
    slug = EXCLUDED.slug,
    description = EXCLUDED.description,
    transport_types = EXCLUDED.transport_types,
    publisher_name = EXCLUDED.publisher_name,
    homepage = EXCLUDED.homepage,
    stars = EXCLUDED.stars,
    last_fetched_at = now()
RETURNING *;

-- name: SearchMCPServerDirectory :many
SELECT
    id,
    name,
    slug,
    description,
    transport_types,
    publisher_name,
    homepage,
    stars,
    last_fetched_at,
    COUNT(*) OVER()::int AS total_count
FROM mcp_server_directory
WHERE (
        sqlc.arg('query')::text = ''
        OR search_vector @@ websearch_to_tsquery('english', sqlc.arg('query')::text)
    )
  AND (
        sqlc.narg('transport')::text IS NULL
        OR transport_types @> ARRAY[sqlc.narg('transport')::text]
    )
ORDER BY
    CASE WHEN sqlc.arg('query')::text = '' THEN 0 ELSE ts_rank(search_vector, websearch_to_tsquery('english', sqlc.arg('query')::text)) END DESC,
    stars DESC,
    name ASC
LIMIT $1 OFFSET $2;

-- name: GetMCPServerDirectoryLastFetchedAt :one
SELECT MAX(last_fetched_at)::timestamptz AS last_fetched_at
FROM mcp_server_directory;

-- name: TruncateMCPServerDirectory :exec
TRUNCATE mcp_server_directory;
