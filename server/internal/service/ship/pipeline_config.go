// Package ship — pipeline configuration types + read shim.
//
// PR5a of the Ship Hub rebuild. The audit identified that the existing
// `project.pipeline_kind` enum (two values: `staged`, `direct_to_prod`)
// can't represent the five real pipeline shapes Ship Hub currently
// manages across the operator's repos. This file defines the structured
// config that PR5b will render the kanban from, plus the read shim that
// makes the kanban keep working for projects whose pipeline_config
// column is still NULL.
//
// Authoring guidance for new pipeline shapes: prefer adding a TriggerKind
// value to the existing union rather than introducing a parallel struct.
// Every pipeline reduces to "ordered stages, each with a list of
// triggers." The structure is intentionally small.
package ship

import (
	"encoding/json"
	"fmt"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// TriggerKind is the closed enum of how a release can advance into a
// given stage. Each pipeline stage declares one OR MORE triggers; ANY
// of them firing moves the release into the stage.
//
// Adding a new kind is a Go-only change: extend this enum, extend the
// `Validate` switch, extend the kanban renderer in PR5b. No migration
// needed.
type TriggerKind string

const (
	// TriggerPushBranch — release advances when a commit is pushed to
	// a named branch. config.branch is required.
	TriggerPushBranch TriggerKind = "push_branch"

	// TriggerWorkflowRun — release advances when a named GitHub
	// Actions workflow completes successfully. config.workflow is
	// required; config.parent_workflow is optional (used to model
	// "workflow_run chains" like promote-main-to-dev -> deploy-staging).
	TriggerWorkflowRun TriggerKind = "workflow_run"

	// TriggerWorkflowDispatch — release advances when an operator (or
	// Ship Hub itself) manually dispatches a named workflow.
	// config.workflow is required.
	TriggerWorkflowDispatch TriggerKind = "workflow_dispatch"

	// TriggerDeploymentStatus — release advances when GitHub emits a
	// deployment_status event for a specific environment.
	// config.environment is required.
	TriggerDeploymentStatus TriggerKind = "deployment_status"

	// TriggerManualAck — release advances when an operator clicks
	// "Mark verified" / "Mark deployed" in the Ship UI. No config
	// fields required.
	TriggerManualAck TriggerKind = "manual_ack"

	// TriggerImagePublishTag — release advances when a versioned image
	// tag is pushed to the registry (library shape). config.tag_pattern
	// is optional and defaults to /^v\d/ (semver tags).
	TriggerImagePublishTag TriggerKind = "image_publish_tag"
)

// PipelineTrigger is one observable event that advances a release into
// the parent stage. Fields are populated based on Kind; unused fields
// stay zero.
type PipelineTrigger struct {
	Kind   TriggerKind `json:"kind"`
	Config TriggerConfig `json:"config,omitempty"`
}

// TriggerConfig is the bag of per-trigger-kind metadata. Keeping it as
// one struct rather than per-kind subtypes keeps JSON round-tripping
// trivial and lets the kanban renderer ignore fields it doesn't care
// about. Validation enforces the per-kind required-fields rule.
type TriggerConfig struct {
	Branch          string `json:"branch,omitempty"`
	Workflow        string `json:"workflow,omitempty"`
	ParentWorkflow  string `json:"parent_workflow,omitempty"`
	Environment     string `json:"environment,omitempty"`
	TagPattern      string `json:"tag_pattern,omitempty"`
}

// PipelineStage is one column in the kanban. Stages have an ordered
// position; the kanban renders left-to-right by Position.
type PipelineStage struct {
	// Stable identifier within a project. The kanban uses it as the
	// React key + the dnd drop target id. Always lowercase + snake_case
	// so locale display names can diverge without breaking refs.
	ID string `json:"id"`

	// Human-readable display name. PR5b will route this through the
	// existing locale resources via a fallback chain: pipeline-config
	// override -> locale key -> ID titlecased.
	Name string `json:"name"`

	// Position is the 0-indexed kanban column position. Stages MUST be
	// stored in ascending position order (Validate enforces this).
	Position int `json:"position"`

	// IsTerminal — once a release reaches this stage, the kanban
	// renders it in the "Done" / archive column and IsTerminalDerived-
	// Stage returns true for the equivalent derived value. Exactly one
	// stage MUST be terminal per pipeline.
	IsTerminal bool `json:"is_terminal,omitempty"`

	// RequiresHumanAck — surface a "Mark verified / deployed" button
	// instead of waiting for automation. Used by stages whose trigger
	// is TriggerManualAck (and optionally as a gate on auto-trigger
	// stages too, e.g. "auto-promote after manual smoke verify").
	RequiresHumanAck bool `json:"requires_human_ack,omitempty"`

	// DeployEnvironmentID links the stage to a deploy_environment row
	// when the trigger is TriggerDeploymentStatus. Empty when the stage
	// isn't tied to a tracked deploy environment.
	DeployEnvironmentID string `json:"deploy_environment_id,omitempty"`

	// Triggers — ANY of these firing advances a release into this
	// stage. Empty slice means "only reachable by explicit advance"
	// (e.g. a terminal stage that nothing automatic should advance
	// into, only manual_ack).
	Triggers []PipelineTrigger `json:"triggers,omitempty"`
}

// PipelineConfig captures one project's pipeline shape. Stored as a
// single JSONB blob on `project.pipeline_config`; round-trips through
// encoding/json with no custom marshalers.
type PipelineConfig struct {
	// Shape is the canonical name of the underlying pipeline pattern.
	// One of: direct_to_prod, staged_strict, manual_only, library,
	// manual_compose. The kanban can specialize copy or chip behavior
	// by shape without re-walking the stage list.
	Shape string `json:"shape"`

	// Stages — ordered list of kanban columns. Must contain at least
	// one terminal stage. Position values must form a 0..N-1 sequence
	// with no gaps (Validate enforces).
	Stages []PipelineStage `json:"stages"`
}

// MarshalJSON / UnmarshalJSON are the defaults; declared empty here
// only to call attention to the fact that PipelineConfig is stored as
// the literal JSON of its Go representation. A future change that
// renames a JSON field is a wire break — handle with a migration that
// rewrites the JSONB.

// Validate enforces the structural invariants the kanban renderer in
// PR5b will rely on. Catches malformed JSON written through a manual
// override before it can produce a render error in the UI.
func (c PipelineConfig) Validate() error {
	if c.Shape == "" {
		return fmt.Errorf("pipeline_config: shape is required")
	}
	if len(c.Stages) == 0 {
		return fmt.Errorf("pipeline_config: stages must be non-empty")
	}
	seenID := map[string]bool{}
	terminalCount := 0
	for i, s := range c.Stages {
		if s.ID == "" {
			return fmt.Errorf("pipeline_config: stages[%d].id is required", i)
		}
		if seenID[s.ID] {
			return fmt.Errorf("pipeline_config: stages[%d].id %q is duplicate", i, s.ID)
		}
		seenID[s.ID] = true
		if s.Name == "" {
			return fmt.Errorf("pipeline_config: stages[%d].name is required", i)
		}
		if s.Position != i {
			return fmt.Errorf("pipeline_config: stages[%d].position must equal index (got %d)", i, s.Position)
		}
		if s.IsTerminal {
			terminalCount++
		}
		for j, tr := range s.Triggers {
			if err := tr.Validate(); err != nil {
				return fmt.Errorf("pipeline_config: stages[%d].triggers[%d]: %w", i, j, err)
			}
		}
	}
	if terminalCount != 1 {
		return fmt.Errorf("pipeline_config: exactly one terminal stage required (got %d)", terminalCount)
	}
	return nil
}

// Validate enforces the per-kind required-fields rule. Kept on
// PipelineTrigger rather than TriggerConfig so the error message can
// reference the kind.
func (t PipelineTrigger) Validate() error {
	switch t.Kind {
	case TriggerPushBranch:
		if t.Config.Branch == "" {
			return fmt.Errorf("trigger %q: branch is required", t.Kind)
		}
	case TriggerWorkflowRun, TriggerWorkflowDispatch:
		if t.Config.Workflow == "" {
			return fmt.Errorf("trigger %q: workflow is required", t.Kind)
		}
	case TriggerDeploymentStatus:
		if t.Config.Environment == "" {
			return fmt.Errorf("trigger %q: environment is required", t.Kind)
		}
	case TriggerManualAck:
		// No required fields.
	case TriggerImagePublishTag:
		// tag_pattern is optional; defaults to /^v\d/ at render time.
	default:
		return fmt.Errorf("trigger: unknown kind %q", t.Kind)
	}
	return nil
}

// FromJSON decodes a PipelineConfig from the JSONB blob stored on
// `project.pipeline_config`. Returns an error if the JSON parse fails
// OR if the decoded value fails Validate.
//
// Callers should treat a parse failure as "config is corrupt; fall
// back to the read shim's enum-derived default" rather than crashing
// the kanban — see EffectivePipelineConfig.
func FromJSON(raw []byte) (PipelineConfig, error) {
	var cfg PipelineConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return PipelineConfig{}, fmt.Errorf("pipeline_config: parse: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// ToJSON encodes a PipelineConfig for storage. Returns an error if the
// config fails Validate — we never want a malformed config to land in
// the column. The handler that writes UpdateProjectPipelineConfig
// should call this and propagate errors up to the API surface.
func (c PipelineConfig) ToJSON() ([]byte, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(c)
}

// EffectivePipelineConfig is the read shim that lets PR5b's kanban
// keep working for projects whose pipeline_config column is still NULL.
//
// Priority:
//   1. If pipelineConfigJSON parses + validates, return it. This is
//      the "introspector has run" path that becomes the default once
//      PR8 lands.
//   2. Else, synthesize a config from the legacy pipeline_kind enum.
//      `staged` → staged_strict shape; `direct_to_prod` → direct shape.
//      This keeps the kanban rendering through the transition window
//      between PR5a (column added, no seeding) and PR8 (auto-introspect).
//
// Returns an error only when the JSONB is present AND malformed AND we
// can't recover via the legacy enum (shouldn't happen — pipeline_kind
// is non-null per migration 095). Callers should log and continue with
// the default in that case.
func EffectivePipelineConfig(p db.Project) (PipelineConfig, error) {
	if len(p.PipelineConfig) > 0 {
		cfg, err := FromJSON(p.PipelineConfig)
		if err == nil {
			return cfg, nil
		}
		// Corrupt JSONB — fall through to the legacy enum synthesis
		// rather than crash the kanban. The caller (the kanban
		// loader) should log this so it gets noticed.
		_ = err
	}
	return DefaultPipelineConfigFromLegacyKind(string(p.PipelineKind)), nil
}

// DefaultPipelineConfigFromLegacyKind returns the canonical config the
// read shim synthesizes when only the legacy `pipeline_kind` enum
// value is available. Exported so tests + the seed migration in PR5b
// can reuse the exact same defaults.
//
// Supported legacy values:
//   - "direct_to_prod" → direct CD on push to main, terminal after deploy
//   - "staged" (or unrecognized) → strict staged shape with manual prod gate
//
// The five "real" shapes documented in audit Section 4.2
// (direct_to_prod, staged_strict, manual_only, library, manual_compose)
// are all reachable from a successful introspection (PR5a introspector
// + PR8 auto-refresh) — this fallback only covers the two the legacy
// enum could encode.
func DefaultPipelineConfigFromLegacyKind(kind string) PipelineConfig {
	switch kind {
	case "direct_to_prod":
		return DefaultDirectToProdConfig()
	default:
		// Includes "staged" and any unrecognized value — defaulting to
		// staged is the safer pick because it exposes the
		// "verify-then-promote" controls; if a direct_to_prod project
		// shows up here by mistake the worst case is an extra column
		// the operator never uses.
		return DefaultStagedStrictConfig()
	}
}

// DefaultDirectToProdConfig — canonical shape for projects that
// auto-deploy every push to main (e.g. multica-prod via the Mac mini
// self-hosted runner). Three columns: in_review → in_production →
// done. No staging column because there's no staging environment;
// no manual gate because the CD is automatic.
func DefaultDirectToProdConfig() PipelineConfig {
	return PipelineConfig{
		Shape: "direct_to_prod",
		Stages: []PipelineStage{
			{
				ID: "in_review", Name: "In Review", Position: 0,
				// PRs land here on creation; no automated trigger
				// advances into this stage — it's the entry point.
			},
			{
				ID: "in_production", Name: "In Production", Position: 1,
				Triggers: []PipelineTrigger{
					{Kind: TriggerPushBranch, Config: TriggerConfig{Branch: "main"}},
				},
			},
			{
				ID: "done", Name: "Done", Position: 2,
				IsTerminal: true,
				// Auto-graduates via DeriveReleaseStage's 24-hour rule.
			},
		},
	}
}

// DefaultStagedStrictConfig — canonical shape for projects with a
// promote-to-prod gate that requires manual operator action
// (e.g. safra-360: push to main → auto staging via workflow_run, prod
// via workflow_dispatch). Four columns capture the lifecycle the kanban
// can actually render.
//
// The "merged" (Merged to main) and "staging_verified" (Staging
// verified) stages were dropped: their ids are not in the kanban's
// known-bucket set (COLUMN_ACCENT / use-pr-state.ts), so no PR ever
// mapped to them and they rendered as permanently-empty columns. They
// are display-only stages — nothing in the backend release state
// machine (DeriveReleaseStage, the deploy-link path, the AcceptPipeline-
// Proposal safety check) keys off these pipeline-config stage ids, so
// removing them changes only what the board renders, not progression.
func DefaultStagedStrictConfig() PipelineConfig {
	return PipelineConfig{
		Shape: "staged_strict",
		Stages: []PipelineStage{
			{ID: "in_review", Name: "In Review", Position: 0},
			{ID: "in_staging", Name: "In Staging", Position: 1, Triggers: []PipelineTrigger{
				{Kind: TriggerWorkflowRun, Config: TriggerConfig{Workflow: "deploy-staging.yml"}},
				{Kind: TriggerDeploymentStatus, Config: TriggerConfig{Environment: "staging"}},
			}},
			{ID: "in_production", Name: "In Production", Position: 2, Triggers: []PipelineTrigger{
				{Kind: TriggerWorkflowDispatch, Config: TriggerConfig{Workflow: "deploy-prod.yml"}},
				{Kind: TriggerDeploymentStatus, Config: TriggerConfig{Environment: "production"}},
			}},
			{ID: "done", Name: "Done", Position: 3, IsTerminal: true},
		},
	}
}

// DefaultManualOnlyConfig — canonical shape for projects with no
// auto-deploy at all (e.g. control-panel: ci.yml on PRs, deploy via
// workflow_dispatch with a confirmation string). Renders a "Mark
// dispatched" button rather than waiting on automation that never
// fires.
func DefaultManualOnlyConfig() PipelineConfig {
	return PipelineConfig{
		Shape: "manual_only",
		Stages: []PipelineStage{
			{ID: "in_review", Name: "In Review", Position: 0},
			{
				ID: "awaiting_staging_deploy", Name: "Awaiting staging deploy", Position: 1,
				RequiresHumanAck: true,
				Triggers: []PipelineTrigger{
					{Kind: TriggerWorkflowDispatch, Config: TriggerConfig{Workflow: "deploy-backend.yml"}},
				},
			},
			{ID: "in_staging", Name: "In Staging", Position: 2, Triggers: []PipelineTrigger{
				{Kind: TriggerDeploymentStatus, Config: TriggerConfig{Environment: "staging"}},
			}},
			{
				ID: "in_production", Name: "In Production", Position: 3,
				RequiresHumanAck: true,
				Triggers: []PipelineTrigger{
					{Kind: TriggerWorkflowDispatch, Config: TriggerConfig{Workflow: "deploy-backend.yml"}},
					{Kind: TriggerDeploymentStatus, Config: TriggerConfig{Environment: "production"}},
				},
			},
			{ID: "done", Name: "Done", Position: 4, IsTerminal: true},
		},
	}
}

// DefaultLibraryConfig — canonical shape for projects that produce a
// published artifact (Docker image, npm package, library) rather than
// a deployed service. Three columns: in_review → image_published →
// done. The "Promote to production" prompt the staged kanban shows
// is meaningless for these repos and would mislead the operator.
func DefaultLibraryConfig() PipelineConfig {
	return PipelineConfig{
		Shape: "library",
		Stages: []PipelineStage{
			{ID: "in_review", Name: "In Review", Position: 0},
			{ID: "merged", Name: "Merged to main", Position: 1, Triggers: []PipelineTrigger{
				{Kind: TriggerPushBranch, Config: TriggerConfig{Branch: "main"}},
			}},
			{ID: "image_published", Name: "Image published", Position: 2, Triggers: []PipelineTrigger{
				{Kind: TriggerImagePublishTag, Config: TriggerConfig{TagPattern: "^v\\d"}},
				{Kind: TriggerWorkflowRun, Config: TriggerConfig{Workflow: "docker-publish.yml"}},
			}},
			{ID: "done", Name: "Done", Position: 3, IsTerminal: true},
		},
	}
}

// DefaultManualComposeConfig — canonical shape for projects with no
// CI/CD at all (e.g. WineryManager, pulse — docker-compose stacks
// deployed by SSH). Renders a single "Mark deployed" ack button so the
// operator can track the manual deploy in the kanban without Ship Hub
// claiming responsibility for automating it.
func DefaultManualComposeConfig() PipelineConfig {
	return PipelineConfig{
		Shape: "manual_compose",
		Stages: []PipelineStage{
			{ID: "in_review", Name: "In Review", Position: 0},
			{ID: "merged", Name: "Merged to main", Position: 1, Triggers: []PipelineTrigger{
				{Kind: TriggerPushBranch, Config: TriggerConfig{Branch: "main"}},
			}},
			{
				ID: "manually_deployed", Name: "Manually deployed", Position: 2,
				RequiresHumanAck: true,
				Triggers: []PipelineTrigger{
					{Kind: TriggerManualAck},
				},
			},
			{ID: "done", Name: "Done", Position: 3, IsTerminal: true},
		},
	}
}
