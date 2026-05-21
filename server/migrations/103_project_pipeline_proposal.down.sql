-- Reverse of 103_project_pipeline_proposal.up.sql.
--
-- Safe to drop unconditionally: the proposal columns are read-mostly and
-- no downstream FK references them. After this runs, PR8's refresh paths
-- still work — they simply lose the ability to park a destructive
-- proposal (the diff classifier would treat every destructive change as
-- "not auto-applied" with nowhere to store it). The columns being gone
-- only matters once PR8's handler/scheduler code is also reverted.

ALTER TABLE project
    DROP COLUMN IF EXISTS pipeline_config_proposed_at,
    DROP COLUMN IF EXISTS pipeline_config_proposed;
