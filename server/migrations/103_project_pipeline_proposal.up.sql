-- PR8 of the Ship Hub rebuild — auto-refresh pipeline config from repo
-- state.
--
-- PR5a (migration 100) added `project.pipeline_config` (the structured
-- per-repo pipeline shape) and `pipeline_config_introspected_at` (when
-- the introspector last ran). PR8 makes that config continuously
-- authoritative: three trigger paths (on-demand button, push webhook on
-- a workflow-file change, daily scheduled poll) re-run the introspector
-- and diff the result against the stored config.
--
-- A diff is classified as either:
--   - additive   — a new stage appended at the end, or a new trigger
--                  added to an existing stage. Nothing renamed,
--                  dropped, or reordered. Safe to auto-apply: the
--                  refresh writes the new config straight to
--                  `pipeline_config`.
--   - destructive — a stage renamed / dropped / reordered, a trigger
--                  removed, or the shape changed. NOT auto-applied: the
--                  introspected config is parked here as a *proposal*
--                  and the operator must Accept or Reject it.
--
-- This migration adds the two columns that hold a pending proposal.
-- `pipeline_config_introspected_at` (migration 100) already records the
-- audit's `last_introspected_at` — it is reused, not duplicated.
--
-- The proposal columns travel in lockstep: both NULL means "no pending
-- proposal", both non-NULL means "operator decision required". Accept
-- swaps `pipeline_config_proposed` into `pipeline_config` and clears the
-- proposal; Reject clears the proposal and leaves `pipeline_config`
-- untouched.

ALTER TABLE project
    -- The introspected config awaiting operator Accept/Reject. NULL when
    -- no proposal is pending. Same JSONB shape as pipeline_config.
    ADD COLUMN pipeline_config_proposed JSONB,
    -- When the pending proposal was recorded. NULL when no proposal is
    -- pending. Surfaced in the UI so the operator sees how stale the
    -- pending change is.
    ADD COLUMN pipeline_config_proposed_at TIMESTAMPTZ;
