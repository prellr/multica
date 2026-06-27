-- Per-environment workflow_dispatch inputs.
--
-- A real deploy workflow often declares `on.workflow_dispatch.inputs`
-- with `required: true` fields — the canonical example is a confirm
-- guard (`if inputs.confirm != 'deploy-prod' → exit 1`). When Ship's
-- env-level promote / auto-dispatch fires workflow_dispatch with NO
-- inputs, GitHub rejects it with HTTP 422 ("Required input 'confirm'
-- not provided"), which surfaces to the UI as a 502.
--
-- This column lets each deploy environment store the flat
-- string→string input map its workflow requires (e.g.
-- {"confirm":"deploy-prod"}); the dispatch path unmarshals it and
-- passes it to DispatchWorkflow instead of nil.
--
-- Nullable (no default): a null/absent value means "no inputs", which
-- preserves the existing inputless-dispatch behavior for envs whose
-- workflows declare no required inputs. The handler validates the
-- shape (flat object of string values) before storing; the CHECK
-- below is defense-in-depth against direct SQL writes that bypass the
-- API surface.
ALTER TABLE deploy_environment
    ADD COLUMN deploy_workflow_inputs JSONB;

ALTER TABLE deploy_environment ADD CONSTRAINT deploy_environment_workflow_inputs_is_object
    CHECK (deploy_workflow_inputs IS NULL OR jsonb_typeof(deploy_workflow_inputs) = 'object');
