-- =====================
-- MCP Server Registry
-- =====================

-- name: InsertMCPServer :one
INSERT INTO mcp_server (
    workspace_id, name, transport, url, command, args, scope, agent_id,
    required, read_only, approval_required_for
) VALUES (
    $1, $2, $3, sqlc.narg('url'), sqlc.narg('command'), sqlc.narg('args')::text[],
    $4, sqlc.narg('agent_id')::uuid, $5, $6, $7
) RETURNING *;

-- name: GetMCPServer :one
SELECT * FROM mcp_server
WHERE id = $1 AND workspace_id = $2;

-- name: GetMCPServerByName :one
SELECT * FROM mcp_server
WHERE workspace_id = $1 AND name = $2;

-- name: ListMCPServers :many
SELECT * FROM mcp_server
WHERE workspace_id = $1
  AND (sqlc.narg('name')::text IS NULL OR name = sqlc.narg('name'))
ORDER BY name;

-- name: UpdateMCPServer :one
UPDATE mcp_server SET
    name = COALESCE(sqlc.narg('name'), name),
    transport = COALESCE(sqlc.narg('transport'), transport),
    url = CASE WHEN sqlc.narg('url_set')::boolean THEN sqlc.narg('url') ELSE url END,
    command = CASE WHEN sqlc.narg('command_set')::boolean THEN sqlc.narg('command') ELSE command END,
    args = CASE WHEN sqlc.narg('args_set')::boolean THEN sqlc.narg('args')::text[] ELSE args END,
    scope = COALESCE(sqlc.narg('scope'), scope),
    agent_id = CASE WHEN sqlc.narg('agent_id_set')::boolean THEN sqlc.narg('agent_id')::uuid ELSE agent_id END,
    required = COALESCE(sqlc.narg('required')::boolean, required),
    read_only = COALESCE(sqlc.narg('read_only')::boolean, read_only),
    approval_required_for = COALESCE(sqlc.narg('approval_required_for'), approval_required_for),
    updated_at = now()
WHERE id = $1 AND workspace_id = $2
RETURNING *;

-- name: DeleteMCPServer :exec
DELETE FROM mcp_server
WHERE id = $1 AND workspace_id = $2;

-- name: ListMCPServersForAgent :many
SELECT * FROM mcp_server
WHERE workspace_id = $1
  AND (scope = 'workspace' OR (scope = 'agent' AND agent_id = $2))
ORDER BY name;

-- name: UpsertMCPServerSecret :one
INSERT INTO mcp_server_secret (server_id, key, value_encrypted)
VALUES ($1, $2, $3)
ON CONFLICT (server_id, key) DO UPDATE SET
    value_encrypted = EXCLUDED.value_encrypted,
    updated_at = now()
RETURNING id, server_id, key, updated_at;

-- name: ListMCPServerSecretKeys :many
SELECT id, server_id, key, updated_at
FROM mcp_server_secret
WHERE server_id = $1
ORDER BY key;

-- name: ListMCPServerSecretsForServer :many
SELECT id, server_id, key, value_encrypted, updated_at
FROM mcp_server_secret
WHERE server_id = $1
ORDER BY key;

-- name: DeleteMCPServerSecret :exec
DELETE FROM mcp_server_secret
WHERE server_id = $1 AND key = $2;

-- name: UpsertMCPServerToolAllowlist :one
INSERT INTO mcp_server_tool_allowlist (server_id, tool_name)
VALUES ($1, $2)
ON CONFLICT (server_id, tool_name) DO NOTHING
RETURNING *;

-- name: DeleteMCPServerToolAllowlistEntry :exec
DELETE FROM mcp_server_tool_allowlist
WHERE server_id = $1 AND tool_name = $2;

-- name: ListMCPServerToolAllowlist :many
SELECT * FROM mcp_server_tool_allowlist
WHERE server_id = $1
ORDER BY tool_name;

-- name: InsertMCPToolCallLog :one
INSERT INTO mcp_tool_call_log (
    workspace_id, server_id, server_name, namespaced_tool, classification,
    agent_id, run_id, issue_id, channel_id, arguments_json, result_status, approval_status
) VALUES (
    $1, sqlc.narg('server_id')::uuid, $2, $3, $4,
    sqlc.narg('agent_id')::uuid, sqlc.narg('run_id')::uuid, sqlc.narg('issue_id')::uuid,
    sqlc.narg('channel_id')::uuid, sqlc.narg('arguments_json'), $5, sqlc.narg('approval_status')
) RETURNING id;
