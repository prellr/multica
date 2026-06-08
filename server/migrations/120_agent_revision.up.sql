-- Agent revision tracking.
--
-- Problem: when an agent's behavioral configuration changes (instructions,
-- model, MCP wiring, skills, etc.), any cached provider session ID stored
-- on agent_task_queue.session_id becomes a liability. Resuming a Claude
-- thread with the OLD persona but expecting NEW behavior produces wrong
-- output. Today we only discover this on the next task's first turn (the
-- provider replies as the old persona), and even then only if the
-- difference is observable in the response.
--
-- This table is the proactive signal: every behaviorally-significant
-- change to an agent inserts a new row, and the agent's `current_revision_*`
-- columns point at the latest. Downstream consumers (the daemon's
-- task-claim path most importantly) can compare a task's recorded
-- revision against the agent's current revision and decide to drop a
-- stale session instead of resuming into wrong behavior.
--
-- Naming note: we deliberately did NOT call this `agent_session`.
-- "Session" already means three other things in this codebase
-- (chat_session, daemon_pairing_session, agent_task_queue.session_id for
-- provider session resumption). `agent_revision` is unambiguous.
--
-- What counts as a behavioral change: anything that would invalidate a
-- cached provider session. The handler-side helper carries the
-- authoritative list — see `recordAgentRevisionIfChanged` in
-- internal/handler/agent_revision.go. The current cut is:
--   instructions, model, thinking_level, custom_env, custom_args,
--   mcp_config, runtime_id, runtime_config, AND the skill set.
-- Explicitly NOT triggers: name, description, avatar_url (cosmetic),
-- visibility, status, archived_at (access/lifecycle),
-- max_concurrent_tasks (throughput).
--
-- Downstream wiring (agent_task_queue.revision_id at claim time, memory
-- artifact attribution, channel message authorship) is deliberately NOT
-- included in this migration — those each need their own design pass.
-- The foundation here makes that follow-on a one-line FK add.

CREATE TABLE agent_revision (
    id                UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id      UUID        NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    agent_id          UUID        NOT NULL REFERENCES agent(id) ON DELETE CASCADE,
    -- Monotonic per agent. The UNIQUE constraint below makes "next
    -- revision number" computable as (SELECT MAX(revision_number) + 1)
    -- inside the same transaction that creates the row, race-free
    -- under serializable isolation. The handler today reads the cached
    -- value off agent.current_revision_number + 1, which is correct as
    -- long as the update happens in the same tx.
    revision_number   INT         NOT NULL,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    -- Nullable because the system itself can create revisions (backfill
    -- on this migration, future automated config sync). When a human
    -- operator triggers the change, this carries their user id.
    created_by        UUID        REFERENCES "user"(id) ON DELETE SET NULL,
    -- JSONB shape:
    --   {
    --     "changed_fields": ["instructions", "model"],
    --     "snapshot": {
    --       "instructions":   "...",
    --       "model":          "claude-sonnet-4-7",
    --       "thinking_level": "high",
    --       ...all trigger fields, after the change...
    --       "skill_ids":      ["uuid", "uuid"]
    --     }
    --   }
    -- Storing the full post-change snapshot of every trigger field
    -- (not just the diff) means "what did this agent look like at
    -- revision N" is a single row read — no walking-the-history
    -- replay. Storage cost is bounded: ~10KB worst-case instructions
    -- × N revisions × M agents. At Multica's scale that's MB, not GB.
    change_summary    JSONB       NOT NULL,
    UNIQUE (agent_id, revision_number)
);

-- agent_id+revision_number desc covers "give me the latest revision for
-- this agent" and "page through this agent's history in chronological
-- order" in one index. Workspace-wide listings would need a separate
-- (workspace_id, created_at) index; we don't ship one because no UI
-- needs it yet.
CREATE INDEX agent_revision_agent_idx
    ON agent_revision (agent_id, revision_number DESC);

-- Denormalize the current pointer on the agent row. Reads of
-- "what revision is this agent at right now" are common (daemon
-- task-claim path, future UI badges); a join into agent_revision on
-- every read would amplify the same query that runs whenever an agent
-- is rendered. The pointer is maintained inside the same transaction
-- that inserts the revision row, so it can't diverge.
ALTER TABLE agent
    ADD COLUMN current_revision_id     UUID REFERENCES agent_revision(id),
    -- 0 marks "no revision recorded yet" — only used in the gap between
    -- this migration running and the backfill below completing. After
    -- backfill every row has >= 1. New agents start at 1 via the
    -- handler in the same transaction as INSERT INTO agent.
    ADD COLUMN current_revision_number INT NOT NULL DEFAULT 0;

-- Backfill: every existing agent gets revision 1 with a snapshot of
-- its current trigger-field state. After this migration runs, every
-- agent has a valid current_revision_id pointer.
--
-- Skills are intentionally omitted from this backfill snapshot —
-- pulling them in would need a subquery against agent_skill that
-- inflates the migration runtime on workspaces with many agents.
-- The first real handler-driven revision after this migration will
-- include skill_ids; until then, the backfilled "snapshot" is
-- explicitly partial (the changed_fields="initial" sentinel makes
-- that obvious to anyone reading the row).
WITH inserted AS (
    INSERT INTO agent_revision (workspace_id, agent_id, revision_number,
                                created_at, created_by, change_summary)
    SELECT
        a.workspace_id,
        a.id,
        1,
        NOW(),
        NULL,
        jsonb_build_object(
            'changed_fields', jsonb_build_array('initial'),
            'snapshot', jsonb_build_object(
                'instructions',         COALESCE(a.instructions, ''),
                'model',                COALESCE(a.model, ''),
                'thinking_level',       COALESCE(a.thinking_level, ''),
                'custom_env',           COALESCE(a.custom_env, '{}'::jsonb),
                'custom_args',          COALESCE(a.custom_args, '[]'::jsonb),
                'mcp_config',           a.mcp_config,
                'runtime_id',           a.runtime_id,
                'runtime_config',       COALESCE(a.runtime_config, '{}'::jsonb)
            ),
            'reason', 'backfill on migration 120'
        )
    FROM agent a
    RETURNING agent_revision.id, agent_revision.agent_id
)
UPDATE agent
   SET current_revision_id     = i.id,
       current_revision_number = 1
  FROM inserted i
 WHERE agent.id = i.agent_id;
