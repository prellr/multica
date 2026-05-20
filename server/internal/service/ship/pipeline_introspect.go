// Package ship — PR5a phase 2: pipeline introspector.
//
// The introspector inspects a GitHub repository's actual deploy story
// and emits a PipelineConfig describing it. PR5a phase 1 added the
// schema + Go types + read shim; this file fills in the "where does
// the config actually come from" gap by reading workflow YAML +
// optional `.shiphub.yml` overrides.
//
// Detection is heuristic and intentionally conservative. The matrix
// (in priority order):
//
//   1. Repo has a `.shiphub.yml` at root → honor its `shape_override`
//      if set. Operator's explicit declaration always wins.
//   2. No `.github/workflows/` directory → `manual_compose`. Repo has
//      no CI/CD at all; operator deploys by hand (WineryManager, pulse).
//   3. A workflow with tag-pattern push AND no `environment:` step →
//      `library`. The repo publishes an artifact, no deploy stage
//      (Hermes-Multi).
//   4. A workflow triggered by `workflow_run` chaining off another
//      workflow + `workflow_dispatch`-only prod workflow with a
//      confirmation input → `staged_strict` (safra-360).
//   5. Every deploy-shaped workflow is `workflow_dispatch`-only AND
//      no `push: main` triggers a deploy → `manual_only` (control-panel).
//   6. A workflow with `on: push: branches: [main]` that ALSO has an
//      `environment: production` step → `direct_to_prod` (multica-prod).
//   7. Default fallback if no heuristic matches: `staged_strict`. Same
//      bias as the read shim — exposing extra columns is safer than
//      hiding them.
//
// The introspector is read-only against GitHub; the caller is
// responsible for persisting the result via UpdateProjectPipelineConfig.
//
// What this file is NOT:
//   - It does NOT auto-introspect on a schedule or on push events.
//     That's PR8.
//   - It does NOT seed every existing project automatically. The
//     handler endpoint added here is operator-triggered; a one-shot
//     migration that calls IntrospectPipelineForProject for every
//     project on first deploy is a follow-up.
//   - It does NOT touch the kanban renderer. That's PR5b.
package ship

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	gh "github.com/multica-ai/multica/server/pkg/github"
	"gopkg.in/yaml.v3"
)

// IntrospectorGithub is the narrow GH client surface the introspector
// needs. Defined as an interface so tests can stand in a fake without
// pulling the full *gh.Client (with its auth, HTTP, etc.).
type IntrospectorGithub interface {
	ListRepoDir(ctx context.Context, owner, repo, path string) ([]gh.RepoContentEntry, error)
	GetRepoFile(ctx context.Context, owner, repo, path string) (string, error)
}

// shiphubOverride is the optional repo-level config file the operator
// can drop at `/.shiphub.yml`. Empty fields mean "no override". The
// schema is intentionally tiny — Ship Hub introspects the rest from
// the repo's actual workflows.
type shiphubOverride struct {
	// ShapeOverride forces a specific shape regardless of what the
	// heuristic detection finds. Useful for repos whose workflows
	// don't fit the detection patterns (e.g. complex multi-env
	// promotion chains).
	ShapeOverride string `yaml:"shape_override"`
}

// workflowFile is the minimal YAML shape we need to detect pipeline
// triggers. GitHub Actions workflow schema is large; we only care about
// the trigger axis + the `environment:` declaration that signals "this
// job deploys to a tracked env".
type workflowFile struct {
	Name string `yaml:"name"`
	// On is the trigger block. GitHub Actions accepts three shapes:
	//   on: push
	//   on: [push, pull_request]
	//   on: { push: { branches: [main] }, workflow_dispatch: {} }
	// We decode into a yaml.Node and walk it because the union
	// can't be expressed as a single Go type without losing fidelity.
	On yaml.Node `yaml:"on"`
	// Jobs is map[string]job; we only care about whether any job
	// declares an `environment:` and what name it sets.
	Jobs map[string]workflowJob `yaml:"jobs"`
}

type workflowJob struct {
	// Environment can be a plain string or an object {name, url};
	// for detection we just want the name. Use yaml.Node again.
	Environment yaml.Node `yaml:"environment"`
}

// triggerObservation summarizes one workflow file's relevance for shape
// detection. The detector aggregates these across all workflows in
// `.github/workflows/`.
type triggerObservation struct {
	filename string
	// Trigger axes we extracted from `on:`.
	pushBranches      []string
	hasTags           bool
	hasWorkflowRun    bool
	parentWorkflows   []string // for workflow_run, the parent workflow filenames
	hasWorkflowDispatch bool
	// Environment names this workflow declares jobs for.
	environments []string
}

// IntrospectPipeline scans a repo's workflows + optional `.shiphub.yml`
// and emits the canonical PipelineConfig for the inferred shape. The
// emitted config is one of the five DefaultXxxConfig values from
// pipeline_config.go — there are no per-repo bespoke configs in phase
// 2; that's intentional, because the kanban render code in PR5b
// branches on the shape name.
//
// Operator overrides via `.shiphub.yml`'s shape_override field win
// outright. Otherwise the detector applies its priority matrix.
//
// Returns an error only when the GH calls themselves fail in a way the
// caller should propagate (not 404 — those branch into the shape
// detection). A repo with no workflows AND no `.shiphub.yml` is a
// valid "manual_compose" result, not an error.
func IntrospectPipeline(ctx context.Context, ghc IntrospectorGithub, owner, repo string) (PipelineConfig, error) {
	// (1) Operator override always wins.
	if override, err := readShiphubOverride(ctx, ghc, owner, repo); err == nil {
		if cfg, ok := configForShapeName(override.ShapeOverride); ok {
			slog.Info("ship: pipeline introspect → shape override from .shiphub.yml",
				"owner", owner, "repo", repo, "shape", override.ShapeOverride)
			return cfg, nil
		}
	} else if !isNotFound(err) {
		// A real failure (network, auth, parse). Log but continue —
		// the workflow-based detection can still succeed.
		slog.Warn("ship: pipeline introspect — .shiphub.yml read failed",
			"owner", owner, "repo", repo, "error", err)
	}

	// (2) Enumerate workflows. 404 here means no `.github/workflows/`
	// — the manual_compose shape.
	entries, err := ghc.ListRepoDir(ctx, owner, repo, ".github/workflows")
	if err != nil {
		if isNotFound(err) {
			slog.Info("ship: pipeline introspect → manual_compose (no workflows dir)",
				"owner", owner, "repo", repo)
			return DefaultManualComposeConfig(), nil
		}
		return PipelineConfig{}, fmt.Errorf("introspect: list workflows: %w", err)
	}

	// (3) Read every .yml/.yaml file in the workflows dir.
	observations := make([]triggerObservation, 0, len(entries))
	for _, e := range entries {
		if e.Type != "file" {
			continue
		}
		lower := strings.ToLower(e.Name)
		if !strings.HasSuffix(lower, ".yml") && !strings.HasSuffix(lower, ".yaml") {
			continue
		}
		raw, err := ghc.GetRepoFile(ctx, owner, repo, e.Path)
		if err != nil {
			// One unreadable workflow isn't fatal; skip and log.
			slog.Warn("ship: pipeline introspect — workflow fetch failed",
				"owner", owner, "repo", repo, "file", e.Path, "error", err)
			continue
		}
		obs, err := parseWorkflowForObservation(e.Name, raw)
		if err != nil {
			slog.Warn("ship: pipeline introspect — workflow parse failed",
				"owner", owner, "repo", repo, "file", e.Path, "error", err)
			continue
		}
		observations = append(observations, obs)
	}

	// (4) Apply the heuristic matrix to the aggregated observations.
	shape := detectShape(observations)
	slog.Info("ship: pipeline introspect → detected shape",
		"owner", owner, "repo", repo, "shape", shape, "workflow_count", len(observations))
	cfg, ok := configForShapeName(shape)
	if !ok {
		// Shouldn't happen — detectShape only returns names in the
		// configForShapeName switch. Defensive fallback.
		return DefaultStagedStrictConfig(), nil
	}
	return cfg, nil
}

// readShiphubOverride fetches `/.shiphub.yml` from the repo root if
// present. Returns ErrNotFound if absent (a 404 — not an error from
// the caller's perspective, just "no override configured").
func readShiphubOverride(ctx context.Context, ghc IntrospectorGithub, owner, repo string) (shiphubOverride, error) {
	raw, err := ghc.GetRepoFile(ctx, owner, repo, ".shiphub.yml")
	if err != nil {
		return shiphubOverride{}, err
	}
	var override shiphubOverride
	if err := yaml.Unmarshal([]byte(raw), &override); err != nil {
		return shiphubOverride{}, fmt.Errorf("parse .shiphub.yml: %w", err)
	}
	return override, nil
}

// configForShapeName maps a shape string to its canonical default
// PipelineConfig. Returns ok=false for unrecognized names so the
// caller can decide whether to fall back or error out.
func configForShapeName(shape string) (PipelineConfig, bool) {
	switch shape {
	case "direct_to_prod":
		return DefaultDirectToProdConfig(), true
	case "staged_strict":
		return DefaultStagedStrictConfig(), true
	case "manual_only":
		return DefaultManualOnlyConfig(), true
	case "library":
		return DefaultLibraryConfig(), true
	case "manual_compose":
		return DefaultManualComposeConfig(), true
	}
	return PipelineConfig{}, false
}

// detectShape applies the priority matrix (see file header) to a list
// of workflow observations and returns the matching shape name.
func detectShape(obs []triggerObservation) string {
	if len(obs) == 0 {
		// Workflows dir exists but contains nothing actionable. Treat
		// like manual_compose — the repo's CI/CD doesn't fit any
		// auto-deploy pattern.
		return "manual_compose"
	}

	// (a) Library: tag-pattern push or workflow named "publish" with
	// no environment declarations across the whole workflow set. The
	// "no environment" check distinguishes a library from a staged
	// project that happens to also publish artifacts.
	hasAnyEnv := false
	hasTagPush := false
	for _, o := range obs {
		if len(o.environments) > 0 {
			hasAnyEnv = true
		}
		if o.hasTags {
			hasTagPush = true
		}
	}
	if hasTagPush && !hasAnyEnv {
		return "library"
	}

	// (b) Direct-to-prod: any workflow has `push: branches: [main]`
	// AND declares `environment: production` on a job. The
	// multica-prod shape.
	for _, o := range obs {
		if containsAny(o.pushBranches, "main", "master") && containsAny(o.environments, "production", "prod") {
			return "direct_to_prod"
		}
	}

	// (c) Staged-strict: at least one workflow triggered by
	// `workflow_run` chaining off a "promote" / "main" workflow AND
	// at least one workflow that's `workflow_dispatch`-only and
	// declares `environment: production`. The safra-360 shape.
	hasPromoteChain := false
	hasManualProd := false
	for _, o := range obs {
		if o.hasWorkflowRun {
			hasPromoteChain = true
		}
		if o.hasWorkflowDispatch && !containsAny(o.pushBranches, "main", "master") && containsAny(o.environments, "production", "prod") {
			hasManualProd = true
		}
	}
	if hasPromoteChain && hasManualProd {
		return "staged_strict"
	}

	// (d) Manual-only: every deploy-shaped workflow (any with an
	// `environment:` declaration) is workflow_dispatch only.
	if hasAnyEnv {
		allManual := true
		for _, o := range obs {
			if len(o.environments) == 0 {
				continue
			}
			if !o.hasWorkflowDispatch || len(o.pushBranches) > 0 {
				allManual = false
				break
			}
		}
		if allManual {
			return "manual_only"
		}
	}

	// (e) Fallback. Same bias as the read shim — staged_strict shows
	// the operator more controls than they might need, vs. hiding
	// ones they do need.
	return "staged_strict"
}

// parseWorkflowForObservation extracts the trigger axes + environment
// declarations from one workflow file's YAML. Returns the empty
// observation (with filename set) when the YAML parses but doesn't
// carry any of the signals we care about.
//
// The `on:` field in GitHub Actions accepts three shapes; we walk the
// raw yaml.Node and handle each:
//   1. String scalar:  `on: push`
//   2. Sequence:       `on: [push, pull_request]`
//   3. Mapping:        `on: { push: { branches: [main] }, workflow_dispatch: ... }`
func parseWorkflowForObservation(name, raw string) (triggerObservation, error) {
	obs := triggerObservation{filename: name}
	var wf workflowFile
	if err := yaml.Unmarshal([]byte(raw), &wf); err != nil {
		return obs, fmt.Errorf("yaml unmarshal: %w", err)
	}
	walkOnNode(wf.On, &obs)
	for _, j := range wf.Jobs {
		if env := extractEnvironmentName(j.Environment); env != "" {
			obs.environments = append(obs.environments, env)
		}
	}
	return obs, nil
}

// walkOnNode handles the three shapes the `on:` field can take.
func walkOnNode(n yaml.Node, obs *triggerObservation) {
	switch n.Kind {
	case yaml.ScalarNode:
		// e.g. `on: push` — flag the trigger kind; no branches/tags.
		if n.Value == "push" {
			// No filter info; treat as "any push" — record as main so
			// direct_to_prod detection still fires if combined with a
			// production environment declaration. This is a deliberate
			// over-match; the worst case is misclassifying a no-deploy
			// publish workflow as direct_to_prod, which the
			// environment check below filters out.
			obs.pushBranches = append(obs.pushBranches, "main")
		}
		if n.Value == "workflow_dispatch" {
			obs.hasWorkflowDispatch = true
		}
	case yaml.SequenceNode:
		// e.g. `on: [push, workflow_dispatch]`
		for _, c := range n.Content {
			walkOnNode(*c, obs)
		}
	case yaml.MappingNode:
		// e.g. `on: { push: { branches: [main] }, workflow_dispatch: {} }`
		for i := 0; i+1 < len(n.Content); i += 2 {
			key := n.Content[i].Value
			val := n.Content[i+1]
			switch key {
			case "push":
				extractPushFilters(*val, obs)
			case "workflow_run":
				obs.hasWorkflowRun = true
				extractWorkflowRunParents(*val, obs)
			case "workflow_dispatch":
				obs.hasWorkflowDispatch = true
			}
		}
	}
}

// extractPushFilters reads `branches:` and `tags:` from a push trigger
// config. Missing branches list = "any push" so we record "main".
func extractPushFilters(n yaml.Node, obs *triggerObservation) {
	if n.Kind != yaml.MappingNode {
		// `push: null` — any push to default branch.
		obs.pushBranches = append(obs.pushBranches, "main")
		return
	}
	sawBranches := false
	for i := 0; i+1 < len(n.Content); i += 2 {
		key := n.Content[i].Value
		val := n.Content[i+1]
		switch key {
		case "branches":
			sawBranches = true
			if val.Kind == yaml.SequenceNode {
				for _, b := range val.Content {
					obs.pushBranches = append(obs.pushBranches, b.Value)
				}
			}
		case "tags":
			obs.hasTags = true
		}
	}
	if !sawBranches {
		// `push: { paths: [...] }` without a branches filter = default branch.
		obs.pushBranches = append(obs.pushBranches, "main")
	}
}

// extractWorkflowRunParents records the parent workflow names from a
// `workflow_run:` trigger config.
func extractWorkflowRunParents(n yaml.Node, obs *triggerObservation) {
	if n.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		key := n.Content[i].Value
		val := n.Content[i+1]
		if key == "workflows" && val.Kind == yaml.SequenceNode {
			for _, w := range val.Content {
				obs.parentWorkflows = append(obs.parentWorkflows, w.Value)
			}
		}
	}
}

// extractEnvironmentName reads either the plain string form or the
// `{ name: foo }` mapping form of a job's `environment:` field.
func extractEnvironmentName(n yaml.Node) string {
	switch n.Kind {
	case yaml.ScalarNode:
		return n.Value
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			if n.Content[i].Value == "name" && n.Content[i+1].Kind == yaml.ScalarNode {
				return n.Content[i+1].Value
			}
		}
	}
	return ""
}

func containsAny(haystack []string, needles ...string) bool {
	for _, h := range haystack {
		for _, n := range needles {
			if strings.EqualFold(h, n) {
				return true
			}
		}
	}
	return false
}

// isNotFound recognises the gh.ErrNotFound sentinel without dragging
// the full error type machinery into this package. The introspector
// treats 404s as "shape signal absent", not as failures.
func isNotFound(err error) bool {
	return errors.Is(err, gh.ErrNotFound)
}
