-- Restore the NOT NULL constraint on agent.runtime_id.
--
-- After migration 124 the only agents that legitimately have a NULL runtime_id
-- are the built-in seeds (Aye, seeded into every workspace before any daemon
-- connects — see handler/aye.go). Re-adding the NOT NULL constraint while those
-- rows exist hard-fails ("column runtime_id contains null values"), so the down
-- must self-heal: drop the NULL-runtime rows first, then re-add the constraint.
--
-- Deleting Aye on a DOWNGRADE to a pre-Aye schema is correct — the older schema
-- predates the built-in agent, and the cascade (agent_skill, agent_task_queue
-- via FKs) cleans up her attachments. A re-upgrade re-seeds her on the next
-- workspace create; existing workspaces would need a backfill, which is out of
-- scope for a schema rollback.
DELETE FROM agent WHERE runtime_id IS NULL;

ALTER TABLE agent
    ALTER COLUMN runtime_id SET NOT NULL;
