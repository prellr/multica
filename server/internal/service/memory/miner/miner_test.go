package miner

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	pgxvec "github.com/pgvector/pgvector-go/pgx"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Integration tests for MineDecisions. Same TestMain shape as the
// channel-service tests: skip cleanly when no DB is reachable,
// otherwise spin up a workspace + member + issue, run the miner,
// assert on the created artifacts. Detector-level coverage lives in
// detect_test.go; this file is about the end-to-end wiring.

var (
	testPool        *pgxpool.Pool
	testQueries     *db.Queries
	testWorkspaceID pgtype.UUID
	testUserID      pgtype.UUID
)

const testWorkspaceSlug = "miner-svc-tests"

func TestMain(m *testing.M) {
	ctx := context.Background()
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://multica:multica@localhost:5432/multica?sslmode=disable"
	}
	// Register pgvector types on every new pool connection so the
	// memory_artifact.embedding column (vector(1536)) scans without
	// the "unsupported data type" error. Same pattern as the handler
	// test pool; miner-specific because miner tests use their own
	// TestMain.
	poolCfg, err := pgxpool.ParseConfig(dbURL)
	if err != nil {
		fmt.Printf("Skipping miner tests: could not parse database url: %v\n", err)
		os.Exit(0)
	}
	poolCfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_ = pgxvec.RegisterTypes(ctx, conn)
		return nil
	}
	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		fmt.Printf("Skipping miner tests: could not connect to database: %v\n", err)
		os.Exit(0)
	}
	if err := pool.Ping(ctx); err != nil {
		fmt.Printf("Skipping miner tests: database not reachable: %v\n", err)
		pool.Close()
		os.Exit(0)
	}
	testPool = pool
	testQueries = db.New(pool)

	if err := setupFixture(ctx); err != nil {
		fmt.Printf("miner fixture setup failed: %v\n", err)
		pool.Close()
		os.Exit(1)
	}

	code := m.Run()

	_ = teardownFixture(context.Background())
	pool.Close()
	os.Exit(code)
}

func setupFixture(ctx context.Context) error {
	_ = teardownFixture(ctx)
	if err := testPool.QueryRow(ctx, `
		INSERT INTO "user" (name, email) VALUES ($1, $2) RETURNING id
	`, "Miner Tests", "miner-tests@multica.local").Scan(&testUserID); err != nil {
		return err
	}
	if err := testPool.QueryRow(ctx, `
		INSERT INTO workspace (name, slug, description, issue_prefix)
		VALUES ($1, $2, $3, $4) RETURNING id
	`, "Miner Tests", testWorkspaceSlug, "miner fixture", "MIN").Scan(&testWorkspaceID); err != nil {
		return err
	}
	if _, err := testPool.Exec(ctx, `
		INSERT INTO member (workspace_id, user_id, role) VALUES ($1, $2, 'owner')
	`, testWorkspaceID, testUserID); err != nil {
		return err
	}
	return nil
}

func teardownFixture(ctx context.Context) error {
	_, _ = testPool.Exec(ctx, `DELETE FROM workspace WHERE slug = $1`, testWorkspaceSlug)
	_, _ = testPool.Exec(ctx, `DELETE FROM "user" WHERE email = $1`, "miner-tests@multica.local")
	return nil
}

// createIssueWithComment seeds the fixture for one test case: one
// issue with one comment containing the given text. Returns the
// issue ID so the test can clean up. Uses raw SQL because the
// service layer for issue/comment creation pulls in handlers we
// don't need here; the data shape is what the miner sees.
func createIssueWithComment(t *testing.T, title, commentText string) pgtype.UUID {
	t.Helper()
	ctx := context.Background()
	var issueID pgtype.UUID
	err := testPool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, title, status, priority, creator_type, creator_id, kind)
		VALUES ($1, $2, 'todo', 'medium', 'member', $3, 'issue')
		RETURNING id
	`, testWorkspaceID, title, testUserID).Scan(&issueID)
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}
	if _, err := testPool.Exec(ctx, `
		INSERT INTO comment (workspace_id, issue_id, author_type, author_id, content, type)
		VALUES ($1, $2, 'member', $3, $4, 'comment')
	`, testWorkspaceID, issueID, testUserID, commentText); err != nil {
		t.Fatalf("create comment: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, issueID)
	})
	return issueID
}

// cleanupMinedArtifacts removes anything the miner wrote in the
// workspace, so tests don't bleed dedup state into each other.
func cleanupMinedArtifacts(t *testing.T) {
	t.Helper()
	if _, err := testPool.Exec(context.Background(),
		`DELETE FROM memory_artifact WHERE workspace_id = $1 AND 'mined' = ANY(tags)`,
		testWorkspaceID); err != nil {
		t.Fatalf("cleanup mined: %v", err)
	}
}

func TestMineDecisions_DryRunDoesNotWrite(t *testing.T) {
	t.Cleanup(func() { cleanupMinedArtifacts(t) })
	createIssueWithComment(t, "Auth migration",
		"Talked through Postgres vs Mongo for this. Going with Postgres — schema discipline matters here.")

	res, err := MineDecisions(context.Background(), testQueries, Options{
		WorkspaceID: testWorkspaceID,
		AuthorType:  "member",
		AuthorID:    testUserID,
		DryRun:      true,
	})
	if err != nil {
		t.Fatalf("MineDecisions: %v", err)
	}
	if len(res.Matches) != 1 {
		t.Fatalf("matches: want 1, got %d", len(res.Matches))
	}
	if res.Matches[0].Reason != "going-with" {
		t.Fatalf("reason: want going-with, got %q", res.Matches[0].Reason)
	}
	if len(res.Created) != 0 {
		t.Fatalf("dry-run wrote artifacts: %v", res.Created)
	}
}

func TestMineDecisions_AppliedRunCreatesArtifact(t *testing.T) {
	t.Cleanup(func() { cleanupMinedArtifacts(t) })
	issueID := createIssueWithComment(t, "Caching layer",
		"After scoping it out we decided against a full rebuild — too risky this cycle.")

	res, err := MineDecisions(context.Background(), testQueries, Options{
		WorkspaceID: testWorkspaceID,
		AuthorType:  "member",
		AuthorID:    testUserID,
		DryRun:      false,
	})
	if err != nil {
		t.Fatalf("MineDecisions: %v", err)
	}
	if len(res.Created) != 1 {
		t.Fatalf("created: want 1, got %d (errors: %v)", len(res.Created), res.Errors)
	}

	// Validate the created artifact's shape: kind, tags, anchor, metadata.
	rows, err := testQueries.ListMemoryArtifactsByAnchor(context.Background(),
		db.ListMemoryArtifactsByAnchorParams{
			WorkspaceID: testWorkspaceID,
			AnchorType:  pgtype.Text{String: "issue", Valid: true},
			AnchorID:    issueID,
			Limit:       10,
		})
	if err != nil {
		t.Fatalf("list by anchor: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("anchored artifacts: want 1, got %d", len(rows))
	}
	got := rows[0]
	if got.Kind != "decision" {
		t.Errorf("kind: want decision, got %q", got.Kind)
	}
	hasMined, hasCandidate := false, false
	for _, tg := range got.Tags {
		if tg == "mined" {
			hasMined = true
		}
		if tg == "decision-candidate" {
			hasCandidate = true
		}
	}
	if !hasMined || !hasCandidate {
		t.Errorf("tags: want both 'mined' and 'decision-candidate', got %v", got.Tags)
	}
	if !strings.Contains(got.Title, "Caching layer") {
		t.Errorf("title should include source issue title; got %q", got.Title)
	}
	var meta map[string]any
	if err := json.Unmarshal(got.Metadata, &meta); err != nil {
		t.Fatalf("unmarshal metadata: %v", err)
	}
	if meta["reason"] != "decided-to" {
		t.Errorf("metadata.reason: want decided-to, got %v", meta["reason"])
	}
	if meta["detector_version"] != detectorVersion {
		t.Errorf("metadata.detector_version: want %q, got %v", detectorVersion, meta["detector_version"])
	}
	if got.VerifiedAt.Valid {
		t.Errorf("verified_at should be NULL for a freshly-mined proposal")
	}
}

func TestMineDecisions_Idempotent(t *testing.T) {
	t.Cleanup(func() { cleanupMinedArtifacts(t) })
	createIssueWithComment(t, "Deploy pipeline",
		"Decision: bundle the release into 0.3 instead of cutting 0.2.x. Easier rollback story.")

	ctx := context.Background()
	opts := Options{
		WorkspaceID: testWorkspaceID,
		AuthorType:  "member",
		AuthorID:    testUserID,
	}
	first, err := MineDecisions(ctx, testQueries, opts)
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if len(first.Created) != 1 {
		t.Fatalf("first run created: want 1, got %d", len(first.Created))
	}

	// Second run — same workspace, same fixture. Dedup via the
	// metadata.source_comment_id pre-load should produce ZERO new
	// artifacts even though the detector still matches.
	second, err := MineDecisions(ctx, testQueries, opts)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if len(second.Created) != 0 {
		t.Fatalf("second run created: want 0 (dedup), got %d (matches=%d)",
			len(second.Created), len(second.Matches))
	}
	if len(second.Matches) != 0 {
		t.Errorf("second run matches: want 0 after dedup, got %d", len(second.Matches))
	}
}

// TestMineDecisions_DescriptionSource — the 2026-05-28 prod preview
// against RoastConsole Cloud showed ~36% of high-quality candidates
// live in issue descriptions (the canonical "**Decision:**" label in
// design-doc-style issues), not comments. This pins that path.
func TestMineDecisions_DescriptionSource(t *testing.T) {
	t.Cleanup(func() { cleanupMinedArtifacts(t) })
	ctx := context.Background()
	// Issue with a decision in the DESCRIPTION and no comments at all
	// — the case the comments-only v1 miner missed.
	var issueID pgtype.UUID
	if err := testPool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, title, description, status, priority, creator_type, creator_id, kind)
		VALUES ($1, $2, $3, 'todo', 'medium', 'member', $4, 'issue')
		RETURNING id
	`, testWorkspaceID,
		"Auth migration architecture",
		"## Architecture decision: rename internal identifiers too\n\nNormally don't recommend renaming, but in this case the rebrand is shallow enough that we should.",
		testUserID).Scan(&issueID); err != nil {
		t.Fatalf("create issue: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, issueID)
	})

	res, err := MineDecisions(ctx, testQueries, Options{
		WorkspaceID: testWorkspaceID,
		AuthorType:  "member",
		AuthorID:    testUserID,
	})
	if err != nil {
		t.Fatalf("MineDecisions: %v", err)
	}
	if len(res.Created) != 1 {
		t.Fatalf("created: want 1 (from description), got %d (matches=%d errors=%v)",
			len(res.Created), len(res.Matches), res.Errors)
	}
	if res.DescriptionsScanned != 1 {
		t.Errorf("descriptions_scanned: want 1, got %d", res.DescriptionsScanned)
	}
	if res.Matches[0].Source != SourceDescription {
		t.Errorf("source: want description, got %q", res.Matches[0].Source)
	}

	// Idempotency: re-run, expect no new artifacts (description dedup
	// uses the synthetic "issue-desc:<uuid>" key).
	res2, err := MineDecisions(ctx, testQueries, Options{
		WorkspaceID: testWorkspaceID,
		AuthorType:  "member",
		AuthorID:    testUserID,
	})
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if len(res2.Created) != 0 {
		t.Fatalf("re-run created: want 0 (description dedup), got %d", len(res2.Created))
	}
}

func TestMineDecisions_SkipsSystemComments(t *testing.T) {
	t.Cleanup(func() { cleanupMinedArtifacts(t) })
	// System-typed comments (auto-emitted state changes) should be
	// skipped regardless of content — they're not human decisions
	// even when the templated text matches.
	ctx := context.Background()
	var issueID pgtype.UUID
	if err := testPool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, title, status, priority, creator_type, creator_id, kind)
		VALUES ($1, $2, 'todo', 'medium', 'member', $3, 'issue')
		RETURNING id
	`, testWorkspaceID, "System comment test", testUserID).Scan(&issueID); err != nil {
		t.Fatalf("create issue: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, issueID)
	})
	if _, err := testPool.Exec(ctx, `
		INSERT INTO comment (workspace_id, issue_id, author_type, author_id, content, type)
		VALUES ($1, $2, 'member', $3, $4, 'system')
	`, testWorkspaceID, issueID, testUserID,
		"Decision: bot-emitted text that should NOT be mined."); err != nil {
		t.Fatalf("create system comment: %v", err)
	}

	res, err := MineDecisions(ctx, testQueries, Options{
		WorkspaceID: testWorkspaceID,
		AuthorType:  "member",
		AuthorID:    testUserID,
		DryRun:      true,
	})
	if err != nil {
		t.Fatalf("MineDecisions: %v", err)
	}
	if len(res.Matches) != 0 {
		t.Errorf("system comments must be skipped; got %d matches", len(res.Matches))
	}
}

// TestMineDecisions_AuthorAsAgent — the production pattern is for the
// miner to author proposed artifacts AS AN AGENT (e.g. workspace's
// "Hermes" or a dedicated memory-miner agent), not as the member who
// triggered the run. Verify the engine honors that author identity.
func TestMineDecisions_AuthorAsAgent(t *testing.T) {
	t.Cleanup(func() { cleanupMinedArtifacts(t) })
	ctx := context.Background()

	// Seed a runtime + agent in the test workspace. Mirrors the
	// fixture pattern used by the channel-service tests.
	var runtimeID pgtype.UUID
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_runtime (workspace_id, name, runtime_mode, provider, status, device_info, metadata, last_seen_at)
		VALUES ($1, 'Miner Test Runtime', 'cloud', 'miner_tests', 'online', $2, '{}'::jsonb, now())
		RETURNING id
	`, testWorkspaceID, "miner test runtime").Scan(&runtimeID); err != nil {
		t.Fatalf("create runtime: %v", err)
	}
	var agentID pgtype.UUID
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent (workspace_id, runtime_id, name, description, runtime_mode, runtime_config)
		VALUES ($1, $2, 'miner-test-agent', 'miner author identity', 'cloud', '{}'::jsonb)
		RETURNING id
	`, testWorkspaceID, runtimeID).Scan(&agentID); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent WHERE id = $1`, agentID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_runtime WHERE id = $1`, runtimeID)
	})

	issueID := createIssueWithComment(t, "Auth migration",
		"Decision: we're going with Postgres for schema discipline.")

	res, err := MineDecisions(ctx, testQueries, Options{
		WorkspaceID: testWorkspaceID,
		AuthorType:  "agent",
		AuthorID:    agentID,
	})
	if err != nil {
		t.Fatalf("MineDecisions: %v", err)
	}
	if len(res.Created) != 1 {
		t.Fatalf("created: want 1, got %d", len(res.Created))
	}

	// Read it back, verify the author fields landed correctly.
	rows, err := testQueries.ListMemoryArtifactsByAnchor(ctx, db.ListMemoryArtifactsByAnchorParams{
		WorkspaceID: testWorkspaceID,
		AnchorType:  pgtype.Text{String: "issue", Valid: true},
		AnchorID:    issueID,
		Limit:       5,
	})
	if err != nil {
		t.Fatalf("list by anchor: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("anchored artifacts: want 1, got %d", len(rows))
	}
	if rows[0].AuthorType != "agent" {
		t.Errorf("author_type: want agent, got %q", rows[0].AuthorType)
	}
	gotAuthor := rows[0].AuthorID
	if !gotAuthor.Valid || gotAuthor != agentID {
		t.Errorf("author_id: want %v, got %v", agentID, gotAuthor)
	}
}
