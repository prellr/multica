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
