package ship

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// PR7 — flaky CI repair (best-ever rollup).
//
// recomputeCIStatus used to let a failure conclusion dominate the rollup
// unconditionally. A check that ran, passed, and then had a late-arriving
// rerun row mutate its conclusion back to a failure variant would report
// the whole PR as failing even though CI did pass. The fix tracks a
// sticky `ever_succeeded` flag and passes a bestEver flag into the
// rollup. These tests run against the real DB (shared pr6 TestMain
// fixture) because the stickiness lives in the UpsertPullRequestCheck
// SQL — mocking sqlc would only re-assert the calls we wrote.

// pr7InsertOpenPR inserts an open pull_request row and returns it.
func pr7InsertOpenPR(t *testing.T, prNumber int, headSha string) db.PullRequest {
	t.Helper()
	now := time.Now()
	row, err := pr6Queries.UpsertPullRequest(context.Background(), db.UpsertPullRequestParams{
		WorkspaceID: pr6WorkspaceID,
		ProjectID:   pr6ProjectID,
		RepoUrl:     pr6RepoURL,
		PrNumber:    int32(prNumber),
		Title:       "PR7 flaky CI test",
		State:       db.PullRequestStateOpen,
		AuthorLogin: "octocat",
		BaseRef:     pr6DefaultBranch,
		HeadRef:     "feature-flaky",
		HeadSha:     headSha,
		HtmlUrl:     pr6RepoURL + "/pull/1",
		Labels:      []byte(`[]`),
		PrCreatedAt: pgtype.Timestamptz{Time: now.Add(-time.Hour), Valid: true},
		PrUpdatedAt: pgtype.Timestamptz{Time: now, Valid: true},
	})
	if err != nil {
		t.Fatalf("insert open PR #%d: %v", prNumber, err)
	}
	return row
}

// pr7UpsertCheck folds a single check row for the PR's head sha with the
// given conclusion, returning the resulting row (so the test can inspect
// the sticky ever_succeeded flag).
func pr7UpsertCheck(t *testing.T, pr db.PullRequest, name, conclusion string) db.PullRequestCheck {
	t.Helper()
	row, err := pr6Queries.UpsertPullRequestCheck(context.Background(), db.UpsertPullRequestCheckParams{
		WorkspaceID:   pr6WorkspaceID,
		PullRequestID: pr.ID,
		HeadSha:       pr.HeadSha,
		Name:          name,
		Conclusion:    pgtype.Text{String: conclusion, Valid: true},
		Status:        "completed",
	})
	if err != nil {
		t.Fatalf("upsert check %q=%q: %v", name, conclusion, err)
	}
	return row
}

// (a) A check that passed then flipped to failure on a flaky rerun rolls
//
//	up to "success" under the best-ever flag.
func TestRecomputeCIStatus_BestEver_RetryPassedCountsSuccess(t *testing.T) {
	pr6Wipe(t)
	pr := pr7InsertOpenPR(t, 701, "headsha701")

	// The check passed once...
	first := pr7UpsertCheck(t, pr, "build", "success")
	if !first.EverSucceeded {
		t.Fatalf("ever_succeeded should be true after a success conclusion")
	}
	// ...then a late-arriving flaky rerun mutated it back to failure.
	second := pr7UpsertCheck(t, pr, "build", "failure")
	if !second.EverSucceeded {
		t.Fatalf("ever_succeeded must stay sticky after a later failure, got false")
	}

	svc := pr6Service()
	status, err := svc.recomputeCIStatus(context.Background(), pr.ID, pr.HeadSha, true)
	if err != nil {
		t.Fatalf("recomputeCIStatus: %v", err)
	}
	if status != "success" {
		t.Errorf("best-ever rollup = %q, want %q (retry-passed check should count as success)", status, "success")
	}

	// Without the best-ever flag the same data still reports failure —
	// proving the flag is what flips the verdict.
	legacy, err := svc.recomputeCIStatus(context.Background(), pr.ID, pr.HeadSha, false)
	if err != nil {
		t.Fatalf("recomputeCIStatus (legacy): %v", err)
	}
	if legacy != "failure" {
		t.Errorf("non-best-ever rollup = %q, want %q", legacy, "failure")
	}
}

// (b) A check that is failure and has NEVER succeeded still fails the
//
//	rollup even under the best-ever flag.
func TestRecomputeCIStatus_BestEver_NeverSucceededStillFails(t *testing.T) {
	pr6Wipe(t)
	pr := pr7InsertOpenPR(t, 702, "headsha702")

	row := pr7UpsertCheck(t, pr, "build", "failure")
	if row.EverSucceeded {
		t.Fatalf("ever_succeeded should be false for a check that never passed")
	}

	svc := pr6Service()
	status, err := svc.recomputeCIStatus(context.Background(), pr.ID, pr.HeadSha, true)
	if err != nil {
		t.Fatalf("recomputeCIStatus: %v", err)
	}
	if status != "failure" {
		t.Errorf("best-ever rollup = %q, want %q (never-succeeded failure must still fail)", status, "failure")
	}
}
