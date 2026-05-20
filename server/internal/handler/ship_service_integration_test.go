package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service/ship"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	gh "github.com/multica-ai/multica/server/pkg/github"
)

// TestShipService_SyncProject_HappyPath drives the service end-to-end
// against the real database using an httptest-backed GitHub mock. Verifies
// the upsert lands a PR row and that re-running the sync is idempotent.
//
// Lives in the handler package (not internal/service/ship) because the
// existing testPool / testWorkspaceID fixtures live here.
func TestShipService_SyncProject_HappyPath(t *testing.T) {
	enableShipHub(t, false)
	projectID := createShipProject(t, "https://github.com/multica-ai/multica")

	body := `[{
        "number": 7, "title": "Hello PRs", "state": "open", "draft": false,
        "html_url": "https://github.com/multica-ai/multica/pull/7",
        "body": "summary",
        "user": {"login": "alice", "avatar_url": "https://example.com/a.png"},
        "base": {"ref": "main"},
        "head": {"ref": "feat/x", "sha": "abc"},
        "labels": [{"name": "feat", "color": "00ff00"}],
        "additions": 10, "deletions": 5, "changed_files": 2,
        "created_at": "2026-04-30T00:00:00Z", "updated_at": "2026-05-01T00:00:00Z"
    }]`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		// Both /pulls?state=open and /pulls?state=closed get the same body in
		// this mock — the closed result will simply re-upsert the same row.
		// Idempotency check below proves that re-running the sync is safe.
		w.Write([]byte(body))
	}))
	defer srv.Close()

	client := gh.NewClient("test-token")
	client.BaseURL = srv.URL
	svc := &ship.Service{Q: testHandler.Queries, Github: client}

	wsUUID := parseUUID(testWorkspaceID)
	projUUID := parseUUID(projectID)
	res, err := svc.SyncProject(context.Background(), wsUUID, projUUID)
	if err != nil {
		t.Fatalf("SyncProject: %v", err)
	}
	if res.Upserted == 0 {
		t.Fatalf("expected upserts, got %+v", res)
	}

	// Verify exactly one row landed in the DB despite two API calls.
	var count int
	if err := testPool.QueryRow(context.Background(),
		`SELECT count(*) FROM pull_request WHERE workspace_id = $1 AND pr_number = 7`,
		testWorkspaceID).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 PR row after sync, got %d", count)
	}

	// Idempotency: a second sync produces no duplicates.
	if _, err := svc.SyncProject(context.Background(), wsUUID, projUUID); err != nil {
		t.Fatalf("second SyncProject: %v", err)
	}
	if err := testPool.QueryRow(context.Background(),
		`SELECT count(*) FROM pull_request WHERE workspace_id = $1 AND pr_number = 7`,
		testWorkspaceID).Scan(&count); err != nil {
		t.Fatalf("count after second sync: %v", err)
	}
	if count != 1 {
		t.Fatalf("idempotency violated: %d rows", count)
	}
}

// TestShipService_SyncProject_PreservesWebhookCIStatus pins PR1 of the Ship
// Hub rebuild (audit doc Section 2.1, Bug 1).
//
// Before the fix, `Service.upsertPR` hard-coded ci_status="" / review_decision=""
// on every call and the UpsertPullRequest SQL wrote those columns to EXCLUDED
// on conflict. That meant: a check_run webhook would correctly write
// ci_status='success' via UpdatePullRequestCIStatus, then the very next
// 5-minute reconciler tick (or any "Sync Now" click) would call upsertPR and
// blank ci_status back to "". The UI then showed "CI pending" for merged PRs
// indefinitely.
//
// The fix dropped ci_status + review_decision from the upsert. This test
// proves the regression is closed: webhook writes survive a subsequent sync.
func TestShipService_SyncProject_PreservesWebhookCIStatus(t *testing.T) {
	enableShipHub(t, false)
	projectID := createShipProject(t, "https://github.com/multica-ai/multica")

	// Mock GH: returns a single merged PR. Sync will route this through
	// upsertPR with state=merged. Pre-fix, the upsert hard-blanked ci_status
	// + review_decision for any non-open PR.
	body := `[{
        "number": 42, "title": "merged PR", "state": "closed", "draft": false,
        "merged_at": "2026-05-01T01:00:00Z",
        "html_url": "https://github.com/multica-ai/multica/pull/42",
        "body": "merged",
        "user": {"login": "alice", "avatar_url": "https://example.com/a.png"},
        "base": {"ref": "main"},
        "head": {"ref": "feat/y", "sha": "def"},
        "labels": [],
        "additions": 1, "deletions": 0, "changed_files": 1,
        "created_at": "2026-04-30T00:00:00Z", "updated_at": "2026-05-01T01:00:00Z",
        "closed_at": "2026-05-01T01:00:00Z"
    }]`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(body))
	}))
	defer srv.Close()

	client := gh.NewClient("test-token")
	client.BaseURL = srv.URL
	svc := &ship.Service{Q: testHandler.Queries, Github: client}

	wsUUID := parseUUID(testWorkspaceID)
	projUUID := parseUUID(projectID)

	// First sync seeds the PR row.
	if _, err := svc.SyncProject(context.Background(), wsUUID, projUUID); err != nil {
		t.Fatalf("first SyncProject: %v", err)
	}

	// Locate the seeded row.
	var prID pgtype.UUID
	if err := testPool.QueryRow(context.Background(),
		`SELECT id FROM pull_request WHERE workspace_id = $1 AND pr_number = 42`,
		testWorkspaceID).Scan(&prID); err != nil {
		t.Fatalf("locate PR row: %v", err)
	}

	// Simulate the webhook path writing both fields. These are the
	// authoritative writers the sync MUST NOT clobber.
	if _, err := testHandler.Queries.UpdatePullRequestCIStatus(context.Background(), db.UpdatePullRequestCIStatusParams{
		ID:       prID,
		CiStatus: pgtype.Text{String: "success", Valid: true},
	}); err != nil {
		t.Fatalf("UpdatePullRequestCIStatus: %v", err)
	}
	if _, err := testHandler.Queries.UpdatePullRequestReviewDecision(context.Background(), db.UpdatePullRequestReviewDecisionParams{
		ID:             prID,
		ReviewDecision: pgtype.Text{String: "APPROVED", Valid: true},
	}); err != nil {
		t.Fatalf("UpdatePullRequestReviewDecision: %v", err)
	}

	// Second sync — pre-fix, this would have wiped the columns.
	if _, err := svc.SyncProject(context.Background(), wsUUID, projUUID); err != nil {
		t.Fatalf("second SyncProject: %v", err)
	}

	// Assert webhook-supplied state survived the sync.
	var ciStatus, reviewDecision pgtype.Text
	if err := testPool.QueryRow(context.Background(),
		`SELECT ci_status, review_decision FROM pull_request WHERE id = $1`,
		prID).Scan(&ciStatus, &reviewDecision); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !ciStatus.Valid || ciStatus.String != "success" {
		t.Errorf("ci_status was clobbered by sync: got %+v, want 'success'", ciStatus)
	}
	if !reviewDecision.Valid || reviewDecision.String != "APPROVED" {
		t.Errorf("review_decision was clobbered by sync: got %+v, want 'APPROVED'", reviewDecision)
	}
}
