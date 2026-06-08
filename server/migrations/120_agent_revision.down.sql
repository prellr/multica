-- Reverse migration 120. The order matters:
--   1. Drop the columns on agent that FK into agent_revision (otherwise
--      DROP TABLE below fails with a dependency error).
--   2. Drop the agent_revision table (which cascades the index).

ALTER TABLE agent
    DROP COLUMN IF EXISTS current_revision_id,
    DROP COLUMN IF EXISTS current_revision_number;

DROP TABLE IF EXISTS agent_revision;
