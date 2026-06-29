-- Drafts slice 2: allow agent.runtime_id to be NULL.
--
-- Built-in agents (Aye, seeded into every workspace at create time — see
-- handler/aye.go) exist BEFORE any daemon/runtime is connected: the workspace
-- owner attaches a runtime when they register a daemon, exactly as the runtime
-- research describes. A NOT NULL runtime_id forced every agent to reference a
-- live agent_runtime row, which made pre-daemon seeding impossible.
--
-- The application code already treats runtime_id as nullable — every Enqueue*
-- path guards on `agent.RuntimeID.Valid` and fails fast with "agent has no
-- runtime" when it's unset (service/task.go). This migration makes the schema
-- match that long-standing assumption. The FK (ON DELETE RESTRICT) stays: a
-- non-NULL value must still reference a real runtime.
ALTER TABLE agent
    ALTER COLUMN runtime_id DROP NOT NULL;
