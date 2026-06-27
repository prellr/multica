-- Reverse 121: drop the per-env workflow_dispatch inputs column and its
-- shape CHECK. Rolling back loses any stored input maps; envs fall back
-- to the inputless-dispatch behavior.
ALTER TABLE deploy_environment DROP CONSTRAINT IF EXISTS deploy_environment_workflow_inputs_is_object;
ALTER TABLE deploy_environment DROP COLUMN IF EXISTS deploy_workflow_inputs;
