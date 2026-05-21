package ship

import (
	"context"
	"errors"
	"testing"

	gh "github.com/multica-ai/multica/server/pkg/github"
)

// fakeIntrospectorGH stubs IntrospectorGithub. Tests configure dirs +
// files; lookups for missing paths return gh.ErrNotFound so the
// introspector exercises the 404 → "shape signal absent" branches.
type fakeIntrospectorGH struct {
	dirs  map[string][]gh.RepoContentEntry
	files map[string]string
}

func (f *fakeIntrospectorGH) ListRepoDir(_ context.Context, owner, repo, path string) ([]gh.RepoContentEntry, error) {
	key := owner + "/" + repo + ":" + path
	entries, ok := f.dirs[key]
	if !ok {
		return nil, gh.ErrNotFound
	}
	return entries, nil
}

func (f *fakeIntrospectorGH) GetRepoFile(_ context.Context, owner, repo, path string) (string, error) {
	key := owner + "/" + repo + ":" + path
	raw, ok := f.files[key]
	if !ok {
		return "", gh.ErrNotFound
	}
	return raw, nil
}

// Convenience: set up a fake with a single repo's workflows dir + N files.
func withWorkflows(owner, repo string, workflows map[string]string) *fakeIntrospectorGH {
	f := &fakeIntrospectorGH{
		dirs:  map[string][]gh.RepoContentEntry{},
		files: map[string]string{},
	}
	dirKey := owner + "/" + repo + ":.github/workflows"
	entries := make([]gh.RepoContentEntry, 0, len(workflows))
	for name, content := range workflows {
		entries = append(entries, gh.RepoContentEntry{
			Name: name,
			Type: "file",
			Path: ".github/workflows/" + name,
		})
		f.files[owner+"/"+repo+":.github/workflows/"+name] = content
	}
	f.dirs[dirKey] = entries
	return f
}

// TestIntrospect_NoWorkflowsDir → manual_compose. The WineryManager /
// pulse shape: no `.github/workflows/` at all.
func TestIntrospect_NoWorkflowsDir(t *testing.T) {
	t.Parallel()
	gh := &fakeIntrospectorGH{
		dirs:  map[string][]gh.RepoContentEntry{},
		files: map[string]string{},
	}
	cfg, err := IntrospectPipeline(context.Background(), gh, "prellr", "WineryManager")
	if err != nil {
		t.Fatalf("introspect: %v", err)
	}
	if cfg.Shape != "manual_compose" {
		t.Errorf("shape: got %q, want manual_compose", cfg.Shape)
	}
}

// TestIntrospect_DirectToProd → direct_to_prod. The multica-prod shape:
// push to main with `environment: production`.
func TestIntrospect_DirectToProd(t *testing.T) {
	t.Parallel()
	yamlSrc := `
name: Deploy Production
on:
  push:
    branches: [main]
  workflow_dispatch:
jobs:
  deploy:
    runs-on: self-hosted
    environment: production
    steps:
      - run: echo deploy
`
	gh := withWorkflows("prellr", "multica", map[string]string{
		"deploy-production.yml": yamlSrc,
	})
	cfg, err := IntrospectPipeline(context.Background(), gh, "prellr", "multica")
	if err != nil {
		t.Fatalf("introspect: %v", err)
	}
	if cfg.Shape != "direct_to_prod" {
		t.Errorf("shape: got %q, want direct_to_prod", cfg.Shape)
	}
}

// TestIntrospect_DirectToProd_DeployNamedNoEnvironment → direct_to_prod
// via the deployNamed backstop. The real prellr/multica shape: a
// deploy-production.yml triggered by push:main that drives the GitHub
// deployments API from a github-script step, so it never declares a
// job-level `environment:` key. A release.yml with tag triggers also
// exists. Without the backstop the detector would see "tag push + no
// declared deploy workflow" and misclassify the repo as a `library`.
func TestIntrospect_DirectToProd_DeployNamedNoEnvironment(t *testing.T) {
	t.Parallel()
	deployYAML := `
name: Deploy to Production
on:
  push:
    branches: [main]
  workflow_dispatch:
jobs:
  build-and-deploy:
    runs-on: mac-mini-prod
    steps:
      - uses: actions/checkout@v4
      - uses: actions/github-script@v7
        with:
          script: |
            await github.rest.repos.createDeployment({ ...context.repo, ref: 'main' })
      - run: ./scripts/deploy.sh
`
	releaseYAML := `
name: Release CLI
on:
  push:
    tags: ['v*']
jobs:
  goreleaser:
    runs-on: ubuntu-latest
    steps: [{ run: echo release }]
`
	gh := withWorkflows("prellr", "multica", map[string]string{
		"deploy-production.yml": deployYAML,
		"release.yml":           releaseYAML,
	})
	cfg, err := IntrospectPipeline(context.Background(), gh, "prellr", "multica")
	if err != nil {
		t.Fatalf("introspect: %v", err)
	}
	if cfg.Shape != "direct_to_prod" {
		t.Errorf("shape: got %q, want direct_to_prod (deployNamed backstop)", cfg.Shape)
	}
}

// TestIntrospect_StagedStrict → staged_strict. The safra-360 shape:
// promote-main-to-dev triggers deploy-staging via workflow_run; deploy-prod
// is workflow_dispatch only with environment: production.
func TestIntrospect_StagedStrict(t *testing.T) {
	t.Parallel()
	promoteYAML := `
name: Promote main -> dev
on:
  push:
    branches: [main]
jobs:
  promote:
    runs-on: ubuntu-latest
    steps: [{ run: echo promote }]
`
	stagingYAML := `
name: Deploy to Staging
on:
  workflow_run:
    workflows: ["Promote main -> dev"]
    types: [completed]
  workflow_dispatch:
jobs:
  deploy:
    runs-on: ubuntu-latest
    environment: staging
    steps: [{ run: echo deploy }]
`
	prodYAML := `
name: Deploy to Production
on:
  workflow_dispatch:
jobs:
  deploy:
    runs-on: ubuntu-latest
    environment: production
    steps: [{ run: echo deploy }]
`
	gh := withWorkflows("imjenaro", "safra-360", map[string]string{
		"promote-main-to-dev.yml": promoteYAML,
		"deploy-staging.yml":      stagingYAML,
		"deploy-prod.yml":         prodYAML,
	})
	cfg, err := IntrospectPipeline(context.Background(), gh, "imjenaro", "safra-360")
	if err != nil {
		t.Fatalf("introspect: %v", err)
	}
	if cfg.Shape != "staged_strict" {
		t.Errorf("shape: got %q, want staged_strict", cfg.Shape)
	}
}

// TestIntrospect_ManualOnly → manual_only. The control-panel shape:
// CI on PRs, deploy is workflow_dispatch only with a confirmation input,
// no push:main → deploy. Requires `environment: production` on the
// dispatch-only workflow so it's recognized as a "deploy-shaped" workflow.
func TestIntrospect_ManualOnly(t *testing.T) {
	t.Parallel()
	ciYAML := `
name: CI
on:
  pull_request:
  push:
    branches: [main]
jobs:
  test:
    runs-on: ubuntu-latest
    steps: [{ run: echo test }]
`
	deployBackendYAML := `
name: Deploy admin backend (manual)
on:
  workflow_dispatch:
    inputs:
      confirmation:
        required: true
jobs:
  deploy:
    runs-on: ubuntu-latest
    environment: production
    steps: [{ run: echo deploy }]
`
	gh := withWorkflows("imjenaro", "control-panel", map[string]string{
		"ci.yml":            ciYAML,
		"deploy-backend.yml": deployBackendYAML,
	})
	cfg, err := IntrospectPipeline(context.Background(), gh, "imjenaro", "control-panel")
	if err != nil {
		t.Fatalf("introspect: %v", err)
	}
	if cfg.Shape != "manual_only" {
		t.Errorf("shape: got %q, want manual_only", cfg.Shape)
	}
}

// TestIntrospect_Library → library. The Hermes-Multi shape: ci.yml on
// PRs/main, docker-publish.yml on tags, NO `environment:` declarations
// anywhere (the artifact is published, not deployed).
func TestIntrospect_Library(t *testing.T) {
	t.Parallel()
	ciYAML := `
name: CI
on:
  push:
    branches: [main, production]
  pull_request:
jobs:
  test:
    runs-on: ubuntu-latest
    steps: [{ run: echo test }]
`
	publishYAML := `
name: Build & publish Docker image
on:
  push:
    branches: [main]
    tags: ['v*']
jobs:
  publish:
    runs-on: ubuntu-latest
    steps: [{ run: echo publish }]
`
	gh := withWorkflows("prellr", "Hermes-Multi", map[string]string{
		"ci.yml":             ciYAML,
		"docker-publish.yml": publishYAML,
	})
	cfg, err := IntrospectPipeline(context.Background(), gh, "prellr", "Hermes-Multi")
	if err != nil {
		t.Fatalf("introspect: %v", err)
	}
	if cfg.Shape != "library" {
		t.Errorf("shape: got %q, want library", cfg.Shape)
	}
}

// TestIntrospect_ShiphubOverride — `.shiphub.yml` with shape_override
// wins over the workflow-based detection.
func TestIntrospect_ShiphubOverride(t *testing.T) {
	t.Parallel()
	// Workflows would suggest direct_to_prod, but the override forces
	// manual_compose.
	directYAML := `
name: Deploy
on: { push: { branches: [main] } }
jobs:
  deploy:
    runs-on: ubuntu-latest
    environment: production
    steps: [{ run: echo deploy }]
`
	gh := withWorkflows("acme", "weird-repo", map[string]string{
		"deploy.yml": directYAML,
	})
	gh.files["acme/weird-repo:.shiphub.yml"] = "shape_override: manual_compose\n"

	cfg, err := IntrospectPipeline(context.Background(), gh, "acme", "weird-repo")
	if err != nil {
		t.Fatalf("introspect: %v", err)
	}
	if cfg.Shape != "manual_compose" {
		t.Errorf(".shiphub.yml override must win: got %q, want manual_compose", cfg.Shape)
	}
}

// TestIntrospect_ShiphubOverride_UnknownShape — operator typo in
// shape_override. Detection falls through to the workflow-based path.
func TestIntrospect_ShiphubOverride_UnknownShape(t *testing.T) {
	t.Parallel()
	directYAML := `
name: Deploy
on: { push: { branches: [main] } }
jobs:
  deploy:
    runs-on: ubuntu-latest
    environment: production
    steps: [{ run: echo deploy }]
`
	gh := withWorkflows("acme", "typo-repo", map[string]string{
		"deploy.yml": directYAML,
	})
	gh.files["acme/typo-repo:.shiphub.yml"] = "shape_override: not_a_real_shape\n"

	cfg, err := IntrospectPipeline(context.Background(), gh, "acme", "typo-repo")
	if err != nil {
		t.Fatalf("introspect: %v", err)
	}
	if cfg.Shape != "direct_to_prod" {
		t.Errorf("unknown override should fall through; got %q, want direct_to_prod", cfg.Shape)
	}
}

// TestIntrospect_EmptyWorkflowsDir — `.github/workflows/` exists but
// contains no YAML files. Treated as manual_compose (nothing actionable
// from CI/CD).
func TestIntrospect_EmptyWorkflowsDir(t *testing.T) {
	t.Parallel()
	fake := withWorkflows("acme", "empty-wf", map[string]string{})
	// Empty workflows map; the dir still exists. Replace the
	// dir entry with an empty slice so the listing returns []
	// rather than a 404.
	fake.dirs["acme/empty-wf:.github/workflows"] = []gh.RepoContentEntry{}

	cfg, err := IntrospectPipeline(context.Background(), fake, "acme", "empty-wf")
	if err != nil {
		t.Fatalf("introspect: %v", err)
	}
	if cfg.Shape != "manual_compose" {
		t.Errorf("empty workflows dir → manual_compose; got %q", cfg.Shape)
	}
}

// TestIntrospect_FallbackOnUnrecognizedShape — workflows exist but
// nothing matches any heuristic. Falls back to staged_strict (same
// bias as the read shim).
func TestIntrospect_FallbackOnUnrecognizedShape(t *testing.T) {
	t.Parallel()
	// PR-only workflow, no deploys at all → no heuristic fires.
	prOnlyYAML := `
name: PR checks
on:
  pull_request:
jobs:
  lint:
    runs-on: ubuntu-latest
    steps: [{ run: echo lint }]
`
	gh := withWorkflows("acme", "pr-only", map[string]string{
		"pr-checks.yml": prOnlyYAML,
	})
	cfg, err := IntrospectPipeline(context.Background(), gh, "acme", "pr-only")
	if err != nil {
		t.Fatalf("introspect: %v", err)
	}
	if cfg.Shape != "staged_strict" {
		t.Errorf("fallback shape: got %q, want staged_strict", cfg.Shape)
	}
}

// TestIntrospect_PropagatesNonNotFoundError — a genuine GH failure
// (auth, network) on the workflows-dir listing must bubble up.
func TestIntrospect_PropagatesNonNotFoundError(t *testing.T) {
	t.Parallel()
	gh := &errorIntrospectorGH{err: errors.New("network refused")}
	_, err := IntrospectPipeline(context.Background(), gh, "acme", "broken")
	if err == nil {
		t.Fatal("expected error from broken GH client")
	}
}

type errorIntrospectorGH struct{ err error }

func (e *errorIntrospectorGH) ListRepoDir(_ context.Context, _, _, _ string) ([]gh.RepoContentEntry, error) {
	return nil, e.err
}
func (e *errorIntrospectorGH) GetRepoFile(_ context.Context, _, _, _ string) (string, error) {
	return "", e.err
}

// TestParseWorkflow_AllOnShapes — every legal `on:` shape in GitHub
// Actions schema must parse into the right observation flags.
func TestParseWorkflow_AllOnShapes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name           string
		yamlSrc        string
		wantPushMain   bool
		wantDispatch   bool
		wantWorkflowRun bool
		wantTags       bool
	}{
		{
			name:         "scalar push",
			yamlSrc:      "on: push\njobs: { x: { runs-on: ubuntu-latest, steps: [] } }",
			wantPushMain: true,
		},
		{
			name:         "scalar workflow_dispatch",
			yamlSrc:      "on: workflow_dispatch\njobs: { x: { runs-on: ubuntu-latest, steps: [] } }",
			wantDispatch: true,
		},
		{
			name:         "sequence",
			yamlSrc:      "on: [push, workflow_dispatch]\njobs: { x: { runs-on: ubuntu-latest, steps: [] } }",
			wantPushMain: true,
			wantDispatch: true,
		},
		{
			name: "mapping with branches",
			yamlSrc: `
on:
  push:
    branches: [main]
  workflow_dispatch:
jobs: { x: { runs-on: ubuntu-latest, steps: [] } }
`,
			wantPushMain: true,
			wantDispatch: true,
		},
		{
			name: "mapping with tags",
			yamlSrc: `
on:
  push:
    branches: [main]
    tags: ['v*']
jobs: { x: { runs-on: ubuntu-latest, steps: [] } }
`,
			wantPushMain: true,
			wantTags:     true,
		},
		{
			name: "mapping workflow_run",
			yamlSrc: `
on:
  workflow_run:
    workflows: ["Promote main -> dev"]
    types: [completed]
jobs: { x: { runs-on: ubuntu-latest, steps: [] } }
`,
			wantWorkflowRun: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			obs, err := parseWorkflowForObservation("test.yml", c.yamlSrc)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if c.wantPushMain && !containsAny(obs.pushBranches, "main") {
				t.Errorf("expected push main, got branches %v", obs.pushBranches)
			}
			if c.wantDispatch != obs.hasWorkflowDispatch {
				t.Errorf("workflow_dispatch: got %v want %v", obs.hasWorkflowDispatch, c.wantDispatch)
			}
			if c.wantWorkflowRun != obs.hasWorkflowRun {
				t.Errorf("workflow_run: got %v want %v", obs.hasWorkflowRun, c.wantWorkflowRun)
			}
			if c.wantTags != obs.hasTags {
				t.Errorf("tags: got %v want %v", obs.hasTags, c.wantTags)
			}
		})
	}
}
