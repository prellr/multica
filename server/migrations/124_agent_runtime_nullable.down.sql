-- Restore the NOT NULL constraint on agent.runtime_id. This will fail if any
-- agent row has a NULL runtime_id (e.g. a seeded Aye whose workspace never
-- connected a daemon); such rows must be backfilled or removed before
-- rolling back.
ALTER TABLE agent
    ALTER COLUMN runtime_id SET NOT NULL;
