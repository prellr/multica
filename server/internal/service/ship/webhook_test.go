package ship

import (
	"testing"
	"time"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
	gh "github.com/multica-ai/multica/server/pkg/github"
)

func TestMapDeploymentStatusState(t *testing.T) {
	cases := map[string]db.DeployStatus{
		"success":     db.DeployStatusSucceeded,
		"failure":     db.DeployStatusFailed,
		"error":       db.DeployStatusFailed,
		"in_progress": db.DeployStatusInProgress,
		"queued":      db.DeployStatusPending,
		"pending":     db.DeployStatusPending,
		"inactive":    db.DeployStatusRolledBack,
		// Unknown values must NOT crash — the GitHub enum could grow.
		"galactic": db.DeployStatusPending,
		"":         db.DeployStatusPending,
	}
	for input, want := range cases {
		if got := mapDeploymentStatusState(input); got != want {
			t.Errorf("mapDeploymentStatusState(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestMapStatusToConclusion(t *testing.T) {
	cases := map[string]string{
		"success": "success",
		"failure": "failure",
		"error":   "failure",
		"pending": "",
		// Unknown / unset → empty (treated as "not yet conclusive" by
		// the rollup so we don't lock in a wrong final status).
		"weirdo": "",
	}
	for input, want := range cases {
		if got := mapStatusToConclusion(input); got != want {
			t.Errorf("mapStatusToConclusion(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestMapPRStateToGitHubPanel(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name string
		pr   gh.PullRequest
		want string
	}{
		{"open", gh.PullRequest{State: "open"}, "open"},
		{"draft", gh.PullRequest{State: "open", Draft: true}, "draft"},
		{"closed", gh.PullRequest{State: "closed"}, "closed"},
		{"merged", gh.PullRequest{State: "closed", MergedAt: &now}, "merged"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mapPRStateToGitHubPanel(tc.pr); got != tc.want {
				t.Errorf("mapPRStateToGitHubPanel(%q, draft=%v, merged=%v) = %q, want %q",
					tc.pr.State, tc.pr.Draft, tc.pr.MergedAt != nil, got, tc.want)
			}
		})
	}
}

// TestPushTouchesPipelineFiles pins the PR8 webhook trigger: a push is
// only worth re-introspecting when it changed a workflow YAML or
// `.shiphub.yml`. The added / modified / removed lists are all scanned.
func TestPushTouchesPipelineFiles(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		commit *gh.PushCommit
		want   bool
	}{
		{"nil head commit", nil, false},
		{
			"unrelated source file",
			&gh.PushCommit{Modified: []string{"server/internal/handler/ship.go"}},
			false,
		},
		{
			"added workflow yml",
			&gh.PushCommit{Added: []string{".github/workflows/deploy.yml"}},
			true,
		},
		{
			"modified workflow yaml",
			&gh.PushCommit{Modified: []string{".github/workflows/ci.yaml"}},
			true,
		},
		{
			"removed workflow",
			&gh.PushCommit{Removed: []string{".github/workflows/old-deploy.yml"}},
			true,
		},
		{
			"shiphub override at root",
			&gh.PushCommit{Modified: []string{".shiphub.yml"}},
			true,
		},
		{
			"workflows dir but non-yaml file",
			&gh.PushCommit{Modified: []string{".github/workflows/README.md"}},
			false,
		},
		{
			"shiphub-named file in a subdir is not the root override",
			&gh.PushCommit{Modified: []string{"docs/.shiphub.yml"}},
			false,
		},
		{
			"leading ./ is tolerated",
			&gh.PushCommit{Added: []string{"./.github/workflows/deploy.yml"}},
			true,
		},
		{
			"mixed: one relevant path among many",
			&gh.PushCommit{
				Modified: []string{"README.md", "src/main.go", ".github/workflows/deploy.yml"},
			},
			true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload := gh.PushEvent{HeadCommit: tc.commit}
			if got := pushTouchesPipelineFiles(payload); got != tc.want {
				t.Errorf("pushTouchesPipelineFiles(%+v) = %v, want %v", tc.commit, got, tc.want)
			}
		})
	}
}
