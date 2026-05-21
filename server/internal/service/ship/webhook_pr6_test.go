package ship

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	gh "github.com/multica-ai/multica/server/pkg/github"
)

// PR6 — direct-merge release synthesis.
//
// These tests run against a real Postgres because the synthesis path's
// correctness depends on the anti-join in FindOrphanMergedPRsByMergeCommitSHA
// and on the ship_release / ship_release_pull_request constraints — mocking
// sqlc would only re-assert the calls we wrote. The harness mirrors the
// channel package's TestMain: skip cleanly when no database is reachable.
//
// Pure (non-DB) mapping tests for this package live in webhook_test.go and
// service_test.go; they don't need TestMain, so this file owns the package's
// single TestMain.

var (
	pr6Pool        *pgxpool.Pool
	pr6Queries     *db.Queries
	pr6WorkspaceID pgtype.UUID
	pr6ProjectID   pgtype.UUID
)

const (
	pr6WorkspaceSlug = "ship-pr6-tests"
	pr6RepoURL       = "https://github.com/multica-test/ship-pr6"
	pr6DefaultBranch = "main"
)

func TestMain(m *testing.M) {
	ctx := context.Background()
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://multica:multica@localhost:5432/multica?sslmode=disable"
	}

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		fmt.Printf("Skipping ship PR6 DB tests: could not connect to database: %v\n", err)
		os.Exit(0)
	}
	if err := pool.Ping(ctx); err != nil {
		fmt.Printf("Skipping ship PR6 DB tests: database not reachable: %v\n", err)
		pool.Close()
		os.Exit(0)
	}
	pr6Pool = pool
	pr6Queries = db.New(pool)

	if err := pr6SetupFixture(ctx); err != nil {
		fmt.Printf("ship PR6 test fixture setup failed: %v\n", err)
		pool.Close()
		os.Exit(1)
	}

	code := m.Run()

	if err := pr6TeardownFixture(context.Background()); err != nil {
		fmt.Printf("ship PR6 test teardown failed: %v\n", err)
		if code == 0 {
			code = 1
		}
	}
	pool.Close()
	os.Exit(code)
}

func pr6SetupFixture(ctx context.Context) error {
	if err := pr6TeardownFixture(ctx); err != nil {
		return err
	}
	if err := pr6Pool.QueryRow(ctx, `
		INSERT INTO workspace (name, slug, description, issue_prefix)
		VALUES ($1, $2, $3, $4)
		RETURNING id
	`, "Ship PR6 Tests", pr6WorkspaceSlug, "ship pr6 test fixture", "SP6").Scan(&pr6WorkspaceID); err != nil {
		return err
	}
	if err := pr6Pool.QueryRow(ctx, `
		INSERT INTO project (workspace_id, title) VALUES ($1, $2) RETURNING id
	`, pr6WorkspaceID, "Ship PR6 Project").Scan(&pr6ProjectID); err != nil {
		return err
	}
	// A github_repo resource so resolveProject can map the push webhook's
	// repo URL back to this project.
	if _, err := pr6Pool.Exec(ctx, `
		INSERT INTO project_resource (project_id, workspace_id, resource_type, resource_ref)
		VALUES ($1, $2, 'github_repo', jsonb_build_object('url', $3::text))
	`, pr6ProjectID, pr6WorkspaceID, pr6RepoURL); err != nil {
		return err
	}
	return nil
}

func pr6TeardownFixture(ctx context.Context) error {
	_, _ = pr6Pool.Exec(ctx, `DELETE FROM workspace WHERE slug = $1`, pr6WorkspaceSlug)
	return nil
}

// pr6Wipe clears all PR-derived rows between tests so one test's release
// can't satisfy another's "no release expected" assertion.
func pr6Wipe(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	if _, err := pr6Pool.Exec(ctx, `DELETE FROM ship_release WHERE workspace_id = $1`, pr6WorkspaceID); err != nil {
		t.Fatalf("pr6Wipe ship_release: %v", err)
	}
	if _, err := pr6Pool.Exec(ctx, `DELETE FROM pull_request WHERE workspace_id = $1`, pr6WorkspaceID); err != nil {
		t.Fatalf("pr6Wipe pull_request: %v", err)
	}
}

// pr6Service builds a Service wired to the test DB. The fake GitHub client
// returns no PRs so the SyncProject call inside processPush is a clean
// no-op — the synthesis path under test is exercised purely from DB state.
func pr6Service() *Service {
	return &Service{
		Q:      pr6Queries,
		Github: &fakeGithub{},
	}
}

// pr6InsertMergedPR inserts a merged pull_request row carrying the given
// merge_commit_sha and returns its id.
func pr6InsertMergedPR(t *testing.T, prNumber int, title, mergeCommitSHA string) pgtype.UUID {
	t.Helper()
	now := time.Now()
	row, err := pr6Queries.UpsertPullRequest(context.Background(), db.UpsertPullRequestParams{
		WorkspaceID:    pr6WorkspaceID,
		ProjectID:      pr6ProjectID,
		RepoUrl:        pr6RepoURL,
		PrNumber:       int32(prNumber),
		Title:          title,
		State:          db.PullRequestStateMerged,
		AuthorLogin:    "octocat",
		BaseRef:        pr6DefaultBranch,
		HeadRef:        fmt.Sprintf("feature-%d", prNumber),
		HeadSha:        fmt.Sprintf("head%d", prNumber),
		HtmlUrl:        fmt.Sprintf("%s/pull/%d", pr6RepoURL, prNumber),
		Labels:         []byte(`[]`),
		PrCreatedAt:    pgtype.Timestamptz{Time: now.Add(-time.Hour), Valid: true},
		PrUpdatedAt:    pgtype.Timestamptz{Time: now, Valid: true},
		PrMergedAt:     pgtype.Timestamptz{Time: now, Valid: true},
		MergeCommitSha: mergeCommitSHA,
	})
	if err != nil {
		t.Fatalf("insert merged PR #%d: %v", prNumber, err)
	}
	return row.ID
}

// pr6PushBody builds a marshalled push webhook body for a push to the
// default branch landing at afterSHA, with the given commit subject.
func pr6PushBody(t *testing.T, afterSHA, subject string) []byte {
	t.Helper()
	ev := gh.PushEvent{
		Ref:    "refs/heads/" + pr6DefaultBranch,
		Before: "0000000000000000000000000000000000000000",
		After:  afterSHA,
		Repository: gh.Repository{
			HTMLURL:       pr6RepoURL,
			DefaultBranch: pr6DefaultBranch,
		},
	}
	if subject != "" {
		ev.HeadCommit = &gh.PushCommit{ID: afterSHA, Message: subject}
	}
	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal push body: %v", err)
	}
	return b
}

func pr6ProcessPush(t *testing.T, body []byte) {
	t.Helper()
	svc := pr6Service()
	if _, err := svc.ProcessWebhook(context.Background(), WebhookEvent{
		WorkspaceID: pr6WorkspaceID,
		EventType:   "push",
		Body:        body,
	}); err != nil {
		t.Fatalf("ProcessWebhook(push): %v", err)
	}
}

func pr6ReleasesForProject(t *testing.T) []db.ShipRelease {
	t.Helper()
	rows, err := pr6Queries.ListReleasesByProject(context.Background(), db.ListReleasesByProjectParams{
		ProjectID:       pr6ProjectID,
		IncludeTerminal: true,
	})
	if err != nil {
		t.Fatalf("ListReleasesByProject: %v", err)
	}
	return rows
}

// (a) A push whose merge commit SHA matches an orphan merged PR
//
//	synthesizes a release with that PR attached and merged_main_sha set.
func TestSynthesizeDirectMergeRelease_CreatesReleaseForOrphan(t *testing.T) {
	pr6Wipe(t)
	const mergeSHA = "abc123directmergeorphan"
	prID := pr6InsertMergedPR(t, 101, "Add direct-merge handling", mergeSHA)

	pr6ProcessPush(t, pr6PushBody(t, mergeSHA, "Add direct-merge handling (#101)"))

	releases := pr6ReleasesForProject(t)
	if len(releases) != 1 {
		t.Fatalf("expected exactly 1 synthesized release, got %d", len(releases))
	}
	rel := releases[0]
	if rel.MergedMainSha.String != mergeSHA {
		t.Errorf("merged_main_sha = %q, want %q", rel.MergedMainSha.String, mergeSHA)
	}
	if rel.CreatedBy.Valid {
		t.Errorf("created_by should be NULL for a synthesized release, got %v", rel.CreatedBy)
	}
	if want := "Direct merge: Add direct-merge handling (#101)"; rel.Title != want {
		t.Errorf("title = %q, want %q", rel.Title, want)
	}

	prs, err := pr6Queries.ListReleasePullRequests(context.Background(), rel.ID)
	if err != nil {
		t.Fatalf("ListReleasePullRequests: %v", err)
	}
	if len(prs) != 1 || prs[0].ID != prID {
		t.Fatalf("expected PR %v attached, got %+v", prID, prs)
	}

	events, err := pr6Queries.ListReleaseEvents(context.Background(), db.ListReleaseEventsParams{
		ReleaseID: rel.ID,
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("ListReleaseEvents: %v", err)
	}
	found := false
	for _, e := range events {
		if e.EventType == "synthesized_direct_merge" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a synthesized_direct_merge event, got %+v", events)
	}
}

// (b) A push whose PR is already tracked by a release synthesizes nothing.
func TestSynthesizeDirectMergeRelease_SkipsAlreadyTrackedPR(t *testing.T) {
	pr6Wipe(t)
	const mergeSHA = "def456alreadytracked"
	prID := pr6InsertMergedPR(t, 102, "Tracked by an existing release", mergeSHA)

	// Pre-existing release that already tracks this PR.
	existing, err := pr6Queries.CreateRelease(context.Background(), db.CreateReleaseParams{
		WorkspaceID: pr6WorkspaceID,
		ProjectID:   pr6ProjectID,
		Title:       "Pre-existing release",
		RiskLevel:   db.RiskLevelMedium,
	})
	if err != nil {
		t.Fatalf("CreateRelease (pre-existing): %v", err)
	}
	if _, err := pr6Queries.AddPullRequestToRelease(context.Background(), db.AddPullRequestToReleaseParams{
		ReleaseID:     existing.ID,
		PullRequestID: prID,
		Position:      0,
	}); err != nil {
		t.Fatalf("AddPullRequestToRelease (pre-existing): %v", err)
	}

	pr6ProcessPush(t, pr6PushBody(t, mergeSHA, "Tracked by an existing release (#102)"))

	releases := pr6ReleasesForProject(t)
	if len(releases) != 1 {
		t.Fatalf("expected only the pre-existing release, got %d releases", len(releases))
	}
	if releases[0].ID != existing.ID {
		t.Errorf("unexpected extra release synthesized: %v", releases[0].ID)
	}
}

// (c) A push with no PR matching the merge commit SHA synthesizes nothing.
func TestSynthesizeDirectMergeRelease_NoMatchingPR(t *testing.T) {
	pr6Wipe(t)
	// A merged PR exists but with a DIFFERENT merge_commit_sha.
	pr6InsertMergedPR(t, 103, "Unrelated merge", "unrelatedsha999")

	pr6ProcessPush(t, pr6PushBody(t, "pushedshawithnopr000", "Some commit with no tracked PR"))

	if releases := pr6ReleasesForProject(t); len(releases) != 0 {
		t.Fatalf("expected no synthesized release, got %d", len(releases))
	}
}

// (d) A branch-delete push (After all-zeros) synthesizes nothing even if
//
//	a PR with an empty merge_commit_sha exists.
func TestSynthesizeDirectMergeRelease_BranchDeleteIsNoop(t *testing.T) {
	pr6Wipe(t)
	// An orphan merged PR with an EMPTY merge_commit_sha — the all-zeros
	// guard must short-circuit before the lookup so this is never matched.
	pr6InsertMergedPR(t, 104, "PR with empty merge sha", "")

	pr6ProcessPush(t, pr6PushBody(t, "0000000000000000000000000000000000000000", ""))

	if releases := pr6ReleasesForProject(t); len(releases) != 0 {
		t.Fatalf("expected no synthesized release for branch delete, got %d", len(releases))
	}
}

// TestDirectMergeReleaseTitle is a pure-function check for the title
// builder — no DB needed, but it lives here next to the synthesis code.
func TestDirectMergeReleaseTitle(t *testing.T) {
	cases := []struct {
		name   string
		commit *gh.PushCommit
		after  string
		want   string
	}{
		{"simple subject", &gh.PushCommit{Message: "Fix the thing"}, "sha1", "Direct merge: Fix the thing"},
		{"multiline takes first line", &gh.PushCommit{Message: "Fix the thing\n\nLong body here"}, "sha1", "Direct merge: Fix the thing"},
		{"nil head commit", nil, "sha1", "Direct merge to main"},
		{"empty message", &gh.PushCommit{Message: "   "}, "sha1", "Direct merge to main"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ev := gh.PushEvent{After: tc.after, HeadCommit: tc.commit}
			if got := directMergeReleaseTitle(ev); got != tc.want {
				t.Errorf("directMergeReleaseTitle = %q, want %q", got, tc.want)
			}
		})
	}

	// Long subject is capped with an ellipsis.
	long := ""
	for i := 0; i < 200; i++ {
		long += "x"
	}
	got := directMergeReleaseTitle(gh.PushEvent{HeadCommit: &gh.PushCommit{Message: long}})
	if len([]rune(got)) > directMergeReleaseTitleCap {
		t.Errorf("capped title length = %d runes, want <= %d", len([]rune(got)), directMergeReleaseTitleCap)
	}
}

// TestIsZeroSHA covers the branch-delete sentinel detection.
func TestIsZeroSHA(t *testing.T) {
	cases := map[string]bool{
		"":    true,
		"   ": true,
		"0000000000000000000000000000000000000000": true,
		"abc123": false,
		"0000000000000000000000000000000000000001": false,
	}
	for in, want := range cases {
		if got := isZeroSHA(in); got != want {
			t.Errorf("isZeroSHA(%q) = %v, want %v", in, got, want)
		}
	}
}
