package ship

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Stage reconciler DB tests.
//
// ReconcileReleaseStages recomputes every active release's derived stage
// and rewrites the stored `ship_release.stage` column when it drifts.
// These tests run against the real Postgres harness owned by
// webhook_pr6_test.go's TestMain (skips cleanly when no DB is reachable).
//
// They reuse pr6's workspace fixture for a staged-shape project and
// create a dedicated direct_to_prod project for the shape-aware case.

// recProjectDirectToProd lazily creates (once) a direct_to_prod project
// in the pr6 workspace so the shape-aware derivation can be exercised.
var recProjectDirectToProd pgtype.UUID

func recEnsureDirectToProdProject(t *testing.T) pgtype.UUID {
	t.Helper()
	if recProjectDirectToProd.Valid {
		return recProjectDirectToProd
	}
	ctx := context.Background()
	var id pgtype.UUID
	if err := pr6Pool.QueryRow(ctx, `
		INSERT INTO project (workspace_id, title, pipeline_kind)
		VALUES ($1, $2, 'direct_to_prod')
		RETURNING id
	`, pr6WorkspaceID, "Ship Stage Reconciler Direct").Scan(&id); err != nil {
		t.Fatalf("create direct_to_prod project: %v", err)
	}
	recProjectDirectToProd = id
	return id
}

// recInsertRelease inserts a release row with an explicit stored stage,
// optionally stamping merged_at / promoted_at.
func recInsertRelease(t *testing.T, projectID pgtype.UUID, stage db.ReleaseStage, mergedAt, promotedAt pgtype.Timestamptz) db.ShipRelease {
	t.Helper()
	ctx := context.Background()
	var id pgtype.UUID
	if err := pr6Pool.QueryRow(ctx, `
		INSERT INTO ship_release (workspace_id, project_id, title, stage, merged_at, promoted_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id
	`, pr6WorkspaceID, projectID, "reconciler test release", stage, mergedAt, promotedAt).Scan(&id); err != nil {
		t.Fatalf("insert release: %v", err)
	}
	rel, err := pr6Queries.GetRelease(ctx, id)
	if err != nil {
		t.Fatalf("get inserted release: %v", err)
	}
	return rel
}

func recValidTime(ts time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: ts, Valid: true}
}

func TestReconcileReleaseStages_StaleStoredStageIsRewritten(t *testing.T) {
	if pr6Pool == nil {
		t.Skip("no database")
	}
	pr6Wipe(t)
	ctx := context.Background()

	// Staged-shape project (pr6 project defaults to pipeline_kind 'staged').
	// Release has merged_at stamped but stored stage is the stale
	// 'assembling' — DeriveReleaseStage step (7) derives the post-merge
	// stage, which for a staged shape is in_staging.
	rel := recInsertRelease(t, pr6ProjectID, db.ReleaseStageAssembling,
		recValidTime(time.Now().Add(-2*time.Hour)), pgtype.Timestamptz{})

	svc := &Service{Q: pr6Queries}
	reconciled, err := svc.ReconcileReleaseStages(ctx, pr6WorkspaceID)
	if err != nil {
		t.Fatalf("ReconcileReleaseStages: %v", err)
	}
	if reconciled != 1 {
		t.Fatalf("reconciled = %d, want 1", reconciled)
	}

	got, err := pr6Queries.GetRelease(ctx, rel.ID)
	if err != nil {
		t.Fatalf("get release: %v", err)
	}
	if got.Stage != db.ReleaseStageInStaging {
		t.Fatalf("stored stage = %q, want %q", got.Stage, db.ReleaseStageInStaging)
	}
}

func TestReconcileReleaseStages_MatchingStageNotRewritten(t *testing.T) {
	if pr6Pool == nil {
		t.Skip("no database")
	}
	pr6Wipe(t)
	ctx := context.Background()

	// Stored stage already matches the derived value: a release with no
	// timestamps and no PRs derives 'assembling' (step 10), and that is
	// also what's stored. The reconciler must not count or rewrite it.
	rel := recInsertRelease(t, pr6ProjectID, db.ReleaseStageAssembling,
		pgtype.Timestamptz{}, pgtype.Timestamptz{})
	before, err := pr6Queries.GetRelease(ctx, rel.ID)
	if err != nil {
		t.Fatalf("get release before: %v", err)
	}

	svc := &Service{Q: pr6Queries}
	reconciled, err := svc.ReconcileReleaseStages(ctx, pr6WorkspaceID)
	if err != nil {
		t.Fatalf("ReconcileReleaseStages: %v", err)
	}
	if reconciled != 0 {
		t.Fatalf("reconciled = %d, want 0 (no drift)", reconciled)
	}

	after, err := pr6Queries.GetRelease(ctx, rel.ID)
	if err != nil {
		t.Fatalf("get release after: %v", err)
	}
	if after.Stage != db.ReleaseStageAssembling {
		t.Fatalf("stored stage = %q, want unchanged %q", after.Stage, db.ReleaseStageAssembling)
	}
	// No write happened — updated_at must be untouched.
	if !after.UpdatedAt.Time.Equal(before.UpdatedAt.Time) {
		t.Fatalf("updated_at moved (%v -> %v); reconciler wrote a no-op",
			before.UpdatedAt.Time, after.UpdatedAt.Time)
	}
}

func TestReconcileReleaseStages_DirectToProdMergedDerivesInProduction(t *testing.T) {
	if pr6Pool == nil {
		t.Skip("no database")
	}
	pr6Wipe(t)
	ctx := context.Background()

	projectID := recEnsureDirectToProdProject(t)

	// direct_to_prod project: a release with merged_at stamped but stale
	// stored 'assembling' derives in_production (postMergeStage for a
	// direct_to_prod shape has no staging environment).
	rel := recInsertRelease(t, projectID, db.ReleaseStageAssembling,
		recValidTime(time.Now().Add(-1*time.Hour)), pgtype.Timestamptz{})

	svc := &Service{Q: pr6Queries}
	reconciled, err := svc.ReconcileReleaseStages(ctx, pr6WorkspaceID)
	if err != nil {
		t.Fatalf("ReconcileReleaseStages: %v", err)
	}
	if reconciled != 1 {
		t.Fatalf("reconciled = %d, want 1", reconciled)
	}

	got, err := pr6Queries.GetRelease(ctx, rel.ID)
	if err != nil {
		t.Fatalf("get release: %v", err)
	}
	if got.Stage != db.ReleaseStageInProduction {
		t.Fatalf("stored stage = %q, want %q", got.Stage, db.ReleaseStageInProduction)
	}
}

func TestReconcileReleaseStages_TerminalDoneStampsDoneAt(t *testing.T) {
	if pr6Pool == nil {
		t.Skip("no database")
	}
	pr6Wipe(t)
	ctx := context.Background()

	projectID := recEnsureDirectToProdProject(t)

	// A direct_to_prod release promoted >24h ago with no production
	// deploy link. Step (4) of DeriveReleaseStage auto-graduates a
	// direct-merge-style release to `done` once the 24h window elapses.
	// Here we drive the same branch by setting is_direct_merge so the
	// ProductionDeployID-absent case still satisfies the rule.
	promoted := recValidTime(time.Now().Add(-48 * time.Hour))
	var id pgtype.UUID
	if err := pr6Pool.QueryRow(ctx, `
		INSERT INTO ship_release
			(workspace_id, project_id, title, stage, merged_at, promoted_at, is_direct_merge)
		VALUES ($1, $2, $3, 'in_production', $4, $5, TRUE)
		RETURNING id
	`, pr6WorkspaceID, projectID, "reconciler terminal release",
		promoted, promoted).Scan(&id); err != nil {
		t.Fatalf("insert terminal release: %v", err)
	}

	svc := &Service{Q: pr6Queries}
	reconciled, err := svc.ReconcileReleaseStages(ctx, pr6WorkspaceID)
	if err != nil {
		t.Fatalf("ReconcileReleaseStages: %v", err)
	}
	if reconciled != 1 {
		t.Fatalf("reconciled = %d, want 1", reconciled)
	}

	got, err := pr6Queries.GetRelease(ctx, id)
	if err != nil {
		t.Fatalf("get release: %v", err)
	}
	if got.Stage != db.ReleaseStageDone {
		t.Fatalf("stored stage = %q, want %q", got.Stage, db.ReleaseStageDone)
	}
	if !got.DoneAt.Valid {
		t.Fatalf("done_at not stamped on reconciler-driven terminal")
	}
}
