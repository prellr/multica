package ship

import (
	"context"
	"testing"
	"time"

	gh "github.com/multica-ai/multica/server/pkg/github"
)

// ROA-946 — SyncProject should enrich open PRs from the single GraphQL batch
// query and NOT make per-PR GetCIStatus calls when the batch succeeds; when
// the batch is unavailable it must fall back to the per-PR REST path. These
// run against the shared PR6 Postgres harness (see webhook_pr6_test.go).

func openPRForGraphQLTest(number int, headSHA string) gh.PullRequest {
	pr := gh.PullRequest{Number: number, State: "open", Title: "graphql-path PR"}
	pr.Head.SHA = headSHA
	pr.Head.Ref = "feature"
	pr.Base.Ref = pr6DefaultBranch
	// pull_request.pr_created_at / pr_updated_at are NOT NULL — set real
	// timestamps so upsertPR doesn't write a NULL the constraint rejects.
	pr.CreatedAt = time.Now().Add(-time.Hour)
	pr.UpdatedAt = time.Now()
	return pr
}

func TestSyncProject_UsesGraphQLEnrichmentInsteadOfPerPRCalls(t *testing.T) {
	if pr6Pool == nil {
		t.Skip("database not available")
	}
	pr6Wipe(t)
	ctx := context.Background()

	const prNumber = 4242
	ciCalls := 0
	enrichedCalls := 0
	fake := &fakeGithub{
		// SyncProject lists open then closed.
		responses: []ghResponse{
			{prs: []gh.PullRequest{openPRForGraphQLTest(prNumber, "sha-gql")}},
			{prs: nil},
		},
		getCIStatusFn: func(context.Context, string, string, string) (string, error) {
			ciCalls++
			return "success", nil
		},
		listEnrichedFn: func(_ context.Context, _, _ string, _ int) ([]gh.PullRequestEnriched, error) {
			enrichedCalls++
			return []gh.PullRequestEnriched{
				{Number: prNumber, HeadSHA: "sha-gql", Mergeable: "CONFLICTING", CIStatus: "failure"},
			}, nil
		},
	}
	svc := &Service{Q: pr6Queries, Github: fake}

	if _, err := svc.SyncProject(ctx, pr6WorkspaceID, pr6ProjectID); err != nil {
		t.Fatalf("SyncProject: %v", err)
	}

	if enrichedCalls != 1 {
		t.Errorf("expected exactly 1 GraphQL enrichment call, got %d", enrichedCalls)
	}
	if ciCalls != 0 {
		t.Errorf("expected 0 per-PR GetCIStatus calls when GraphQL batch succeeds, got %d", ciCalls)
	}

	var ciStatus, mergeable string
	if err := pr6Pool.QueryRow(ctx,
		`SELECT ci_status, mergeable FROM pull_request WHERE workspace_id=$1 AND pr_number=$2`,
		pr6WorkspaceID, prNumber).Scan(&ciStatus, &mergeable); err != nil {
		t.Fatalf("read back PR row: %v", err)
	}
	if ciStatus != "failure" {
		t.Errorf("ci_status = %q, want %q (from GraphQL rollup)", ciStatus, "failure")
	}
	if mergeable != "CONFLICTING" {
		t.Errorf("mergeable = %q, want %q (from GraphQL, not the REST list's UNKNOWN)", mergeable, "CONFLICTING")
	}
}

func TestSyncProject_FallsBackToRESTWhenGraphQLUnavailable(t *testing.T) {
	if pr6Pool == nil {
		t.Skip("database not available")
	}
	pr6Wipe(t)
	ctx := context.Background()

	const prNumber = 4343
	ciCalls := 0
	fake := &fakeGithub{
		responses: []ghResponse{
			{prs: []gh.PullRequest{openPRForGraphQLTest(prNumber, "sha-rest")}},
			{prs: nil},
		},
		getCIStatusFn: func(context.Context, string, string, string) (string, error) {
			ciCalls++
			return "success", nil
		},
		// listEnrichedFn unset → fake default returns gh.ErrNotFound, so
		// SyncProject must fall back to the per-PR REST GetCIStatus path.
	}
	svc := &Service{Q: pr6Queries, Github: fake}

	if _, err := svc.SyncProject(ctx, pr6WorkspaceID, pr6ProjectID); err != nil {
		t.Fatalf("SyncProject: %v", err)
	}

	if ciCalls != 1 {
		t.Errorf("expected 1 per-PR GetCIStatus call on GraphQL fallback, got %d", ciCalls)
	}

	var ciStatus string
	if err := pr6Pool.QueryRow(ctx,
		`SELECT ci_status FROM pull_request WHERE workspace_id=$1 AND pr_number=$2`,
		pr6WorkspaceID, prNumber).Scan(&ciStatus); err != nil {
		t.Fatalf("read back PR row: %v", err)
	}
	if ciStatus != "success" {
		t.Errorf("ci_status = %q, want %q (from REST fallback)", ciStatus, "success")
	}
}
