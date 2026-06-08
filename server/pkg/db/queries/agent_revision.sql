-- Agent revision tracking queries. See migration 120 for the
-- table definition and the rationale for why this exists.

-- name: CreateAgentRevision :one
-- Insert a new revision row. Called by the handler-side
-- recordAgentRevisionIfChanged helper inside the same transaction that
-- updates the agent row. The caller computes revision_number as
-- (agent.current_revision_number + 1) — atomic under the UNIQUE
-- constraint on (agent_id, revision_number).
INSERT INTO agent_revision (
    workspace_id, agent_id, revision_number, created_by, change_summary
)
VALUES (
    $1, $2, $3, $4, $5
)
RETURNING *;

-- name: SetAgentCurrentRevision :exec
-- Update the denormalized pointer on the agent row. Called immediately
-- after CreateAgentRevision in the same transaction so the pointer
-- can never be out of sync.
UPDATE agent
   SET current_revision_id     = $2,
       current_revision_number = $3,
       updated_at              = NOW()
 WHERE id = $1;

-- name: ListAgentRevisions :many
-- Page through one agent's revision history, most recent first.
-- LIMIT/OFFSET style; cursor pagination is overkill for what's
-- expected to be tens of rows per agent at most.
SELECT *
  FROM agent_revision
 WHERE agent_id = $1
 ORDER BY revision_number DESC
 LIMIT $2 OFFSET $3;

-- name: GetAgentRevision :one
-- Fetch a single revision by id. Used by future audit/UI paths;
-- not load-bearing for the handler logic itself.
SELECT *
  FROM agent_revision
 WHERE id = $1;

-- name: GetLatestAgentRevision :one
-- Fetch the most recent revision for an agent. Equivalent to reading
-- agent.current_revision_id and joining, but cheaper when the caller
-- only has the agent_id handy.
SELECT *
  FROM agent_revision
 WHERE agent_id = $1
 ORDER BY revision_number DESC
 LIMIT 1;
