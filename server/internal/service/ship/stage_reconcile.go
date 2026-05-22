// Package ship — stage reconciler.
//
// DeriveReleaseStage (stage_derive.go) computes the stage a release
// SHOULD be in from observable facts. The read endpoints already use it,
// but every write-side release ACTION still gates on the stored
// `ship_release.stage` column — e.g. MarkReleaseDone no-ops unless stored
// stage == in_production. That column is written by scattered one-shot
// writers (merge train, promotion handlers) and frequently drifts stale:
// a release whose PR merged outside the merge train keeps stored
// 'assembling' forever even though it derives in_production, so it can
// never be marked done.
//
// ReconcileReleaseStages is the backstop: it recomputes the derived stage
// for every non-terminal release and writes it back into the stored
// column when they differ. This keeps the stored column honest so the
// stored-stage-gated actions work again. It is the "Option A" reconciler
// from the Ship Hub rebuild audit.
package ship

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// ReconcileReleaseStages recomputes the derived stage for every
// non-terminal release in the workspace and writes it back into the
// stored `stage` column when it has drifted from the derived value.
//
// Returns the number of releases whose stored stage was rewritten. A
// per-release failure (PR-state fetch, project lookup, update) is logged
// and skipped — one bad release must not abort the sweep. An error is
// returned only for a workspace-level failure, i.e. the active-release
// list query itself failing.
//
// The reconciler is pure DB work — no GitHub client needed — so it can
// run from a lightweight background poller built with just
// &ship.Service{Q: queries}.
//
// TODO(stage-reconciler): when a release transitions to `done` via this
// reconciler, the tracking issue is NOT auto-closed. finalizeReleaseDone
// performs the issue close, but it requires a *StagingDeps (publisher,
// channel ops) that a pure-DB background poller does not have. v1 keeps
// the reconciler scoped to syncing the stored `stage` column (plus
// stamping done_at on a reconciler-driven terminal so the row is a proper
// terminal). The tracking-issue close on a reconciler-driven done is left
// to the existing finalize path when an operator or the finalizer
// goroutine next acts on the release.
func (s *Service) ReconcileReleaseStages(ctx context.Context, workspaceID pgtype.UUID) (reconciled int, err error) {
	// Reuse the exact query the Active-releases rail uses — "active" =
	// any release not in a terminal stage.
	releases, err := s.Q.ListActiveReleasesByWorkspace(ctx, workspaceID)
	if err != nil {
		return 0, err
	}

	now := time.Now()

	// Memoize the resolved pipeline shape per project_id. Several
	// releases routinely share a project; without this we'd issue a
	// GetProject per release (N+1).
	shapeByProject := map[string]string{}
	shapeFor := func(projectID pgtype.UUID) string {
		key := uuidString(projectID)
		if shape, ok := shapeByProject[key]; ok {
			return shape
		}
		shape := ""
		project, perr := s.Q.GetProject(ctx, projectID)
		if perr != nil {
			slog.Warn("stage reconciler: get project failed",
				"workspace_id", uuidString(workspaceID),
				"project_id", key, "error", perr)
		} else {
			cfg, _ := EffectivePipelineConfig(project)
			shape = cfg.Shape
		}
		shapeByProject[key] = shape
		return shape
	}

	for _, release := range releases {
		prStates, psErr := s.Q.GetReleasePRStates(ctx, release.ID)
		if psErr != nil {
			slog.Warn("stage reconciler: get release PR states failed",
				"workspace_id", uuidString(workspaceID),
				"release_id", uuidString(release.ID), "error", psErr)
			continue
		}

		shape := shapeFor(release.ProjectID)
		derived := DeriveReleaseStage(release, prStates, shape, now)
		if derived == release.Stage {
			continue
		}

		params := db.UpdateReleaseStageParams{
			ID:    release.ID,
			Stage: derived,
		}
		// A reconciler-driven done must be a proper terminal: stamp
		// done_at when the derivation says done and the row doesn't
		// already carry one. DeriveReleaseStage treats a valid done_at
		// as sticky, so this also pins the result.
		if derived == db.ReleaseStageDone && !release.DoneAt.Valid {
			params.DoneAt = pgtype.Timestamptz{Time: now, Valid: true}
		}

		if _, uErr := s.Q.UpdateReleaseStage(ctx, params); uErr != nil {
			slog.Warn("stage reconciler: update release stage failed",
				"workspace_id", uuidString(workspaceID),
				"release_id", uuidString(release.ID),
				"from", release.Stage, "to", derived, "error", uErr)
			continue
		}
		slog.Info("stage reconciler: release stage reconciled",
			"workspace_id", uuidString(workspaceID),
			"release_id", uuidString(release.ID),
			"from", release.Stage, "to", derived)
		reconciled++
	}

	return reconciled, nil
}
