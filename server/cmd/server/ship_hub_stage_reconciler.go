package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/multica-ai/multica/server/internal/service/ship"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// shipHubStageReconcileInterval is how often the stage reconciler
// recomputes every active release's derived stage and writes it back
// into the stored `ship_release.stage` column.
//
// The stored column is written by scattered one-shot writers (merge
// train, promotion handlers) and drifts stale — a release whose PR
// merged outside the merge train keeps stored 'assembling' forever even
// though it derives in_production, which silently blocks every
// stored-stage-gated action (MarkReleaseDone, PromoteRelease, etc.).
// 5 minutes keeps the stored column honest without hammering the DB.
const shipHubStageReconcileInterval = 5 * time.Minute

// runShipHubStageReconciler periodically iterates every Ship-Hub-enabled
// workspace and runs Service.ReconcileReleaseStages, which rewrites any
// stored release stage that has drifted from the value DeriveReleaseStage
// computes from observable facts.
//
// The first sweep runs after the first tick — not immediately on startup
// — matching the other Ship Hub pollers and giving the API time to fully
// boot.
//
// This is pure DB work (no GitHub client), so the per-workspace
// ship.Service is built with just the queries handle. Errors are logged
// per-workspace so one bad workspace can't starve the rest of the fleet.
func runShipHubStageReconciler(ctx context.Context, queries *db.Queries) {
	slog.Info("ship hub stage reconciler started",
		"interval", shipHubStageReconcileInterval.String())
	t := time.NewTicker(shipHubStageReconcileInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			slog.Info("ship hub stage reconciler stopped")
			return
		case <-t.C:
			runShipHubStageReconcileOnce(ctx, queries)
		}
	}
}

// runShipHubStageReconcileOnce is the per-tick body. Extracted so a test
// can drive it without spinning up the goroutine.
func runShipHubStageReconcileOnce(ctx context.Context, queries *db.Queries) {
	workspaces, err := queries.ListWorkspacesWithShipHubEnabled(ctx)
	if err != nil {
		slog.Warn("ship hub stage reconciler: list workspaces failed", "error", err)
		return
	}
	for _, ws := range workspaces {
		svc := &ship.Service{Q: queries}
		reconciled, err := svc.ReconcileReleaseStages(ctx, ws.ID)
		if err != nil {
			slog.Warn("ship hub stage reconciler: reconcile workspace failed",
				"workspace_id", ws.ID, "error", err)
			continue
		}
		if reconciled > 0 {
			slog.Info("ship hub stage reconciler: reconciled releases",
				"workspace_id", ws.ID, "count", reconciled)
		}
	}
}
