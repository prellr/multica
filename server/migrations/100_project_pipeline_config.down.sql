-- Reverse of 100_project_pipeline_config.up.sql.
--
-- Safe to drop unconditionally: no downstream FK references the column,
-- and PR5a's read shim falls back to the legacy `pipeline_kind` enum
-- when pipeline_config is NULL (which it will be again after this).

ALTER TABLE project
    DROP COLUMN IF EXISTS pipeline_config_introspected_at,
    DROP COLUMN IF EXISTS pipeline_config;
