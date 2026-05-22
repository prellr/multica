// Package ship — PR4 of the Ship Hub rebuild.
//
// DeriveReleaseStage closes the audit's central architectural finding for
// releases: Ship Hub's stored release.stage column is written at a point in
// time by multiple writers (merge train, staging promotion, production
// promotion, rollback, cancel, etc.) and never reconciled against the facts
// that actually move a release through stages — PR merge state, deploy
// timestamps, env.current_sha. A failed mid-train write or a missed
// webhook strands the release in a stage that no longer matches reality,
// and the "Active releases" rail keeps showing it forever.
//
// This file holds the pure read-time derivation function. The writers stay
// in place (they keep updating the stored column for compatibility), but
// the read endpoints — ListWorkspaceActiveReleases and GetRelease — now
// run DeriveReleaseStage and return the derived value. A release whose
// stored stage says "in_staging" but whose observable facts say "done"
// will display as "done" and drop off the Active rail on the next page
// load, with no operator intervention.
//
// Phase 1: derivation reads from ship_release's own timestamp ladder
// (merged_at, staged_at, promoted_at, done_at, rollback_reason) plus the
// stored stage for sticky-terminal classification.
//
// Phase 2 (ROA-311): the derivation now also consults member-PR merge
// state. A release whose PRs all merged outside Ship Hub's merge train
// (a direct GitHub merge, or a merge-train goroutine that died after the
// merge landed) never gets merged_at stamped, so the timestamp ladder
// alone strands it in `assembling` forever and floods the Active
// Releases rail. The PR-membership step derives such a release to its
// post-merge stage instead. Direct-merge releases additionally satisfy
// the 24h auto-done rule without a linked production deploy row (the
// merge is the evidence the deploy fired). Both fixes drop stuck
// releases off the Active rail without operator intervention.
//
// Phase 2b (ROA-333): the post-merge derivation is pipeline-shape-aware.
// A direct_to_prod project has no staging environment — its pipeline is
// assembling → merging → promoting → in_production → done — so a merged
// release that previously derived to the hardcoded `in_staging` landed
// on a stage that isn't in its kanban and got stuck (every advance
// action has a stage precondition the phantom `in_staging` fails). The
// caller now passes the project's pipeline shape; a direct_to_prod
// release's post-merge stage is in_production, every other shape keeps
// in_staging.
package ship

import (
	"time"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// stageDoneAfterPromote is how long a release stays in `in_production`
// before auto-graduating to `done`. Mirrors the 24-hour monitoring window
// the audit doc proposes (Section 3.2). Operators can still mark a release
// done explicitly before this window elapses — that path stamps done_at,
// which short-circuits this auto-rule.
const stageDoneAfterPromote = 24 * time.Hour

// postMergeStage is the stage a release sits at once its PRs are
// merged but no deploy/promote timestamp has been stamped. A
// direct_to_prod project has no staging environment — a merge to its
// default branch auto-deploys to production — so its post-merge
// stage is in_production, not in_staging.
func postMergeStage(shape string) db.ReleaseStage {
	if shape == "direct_to_prod" {
		return db.ReleaseStageInProduction
	}
	return db.ReleaseStageInStaging
}

// DeriveReleaseStage returns the stage a release SHOULD be in given the
// observable facts on the release row. Terminal stages and the explicit
// rollback signal are sticky; everything else is derived from the
// timestamp ladder, with PR merge state consulted as a final fallback.
//
// prMergeStates carries the pull_request.state value of every member PR
// of the release (the caller fetches them — see GetReleasePRStates).
// It is used only by the Phase 2 step that rescues a release whose PRs
// merged outside the merge train: such a release never gets merged_at
// stamped and would otherwise strand in `assembling`. Pass an empty
// slice when the release has no member PRs.
//
// shape is the project's pipeline shape — one of direct_to_prod,
// staged_strict, manual_only, library, manual_compose (the values
// produced by EffectivePipelineConfig(project).Shape). It makes the
// post-merge derivation shape-aware: a direct_to_prod project has no
// staging environment, so a merge to its default branch auto-deploys
// to production and its post-merge stage is in_production, not the
// phantom in_staging that isn't even in its kanban. Every other shape
// keeps in_staging as the post-merge stage.
//
// The function is pure: same input → same output, no I/O, no clock skew
// risk beyond the time.Now() call for the 24-hour auto-done rule (which
// the test injects via the now parameter).
//
// Order of evaluation matters: rollback wins over done wins over
// production stages wins over staging stages wins over pre-merge stages.
// The structure mirrors a strict "what's the most-advanced state this
// release has reached" check rather than a "what was the last writer
// trying to do" check. The latter is what the stored column does and is
// exactly what we're moving away from.
func DeriveReleaseStage(r db.ShipRelease, prMergeStates []db.PullRequestState, shape string, now time.Time) db.ReleaseStage {
	// (1) Sticky terminals from the stored column. These represent
	// explicit operator intent (Cancel button, Rollback button, Mark
	// Done button) that no observable fact can override. A user who
	// cancelled a release doesn't want the derivation to "resurrect"
	// it because they happened to merge one of its PRs anyway.
	switch r.Stage {
	case db.ReleaseStageCancelled,
		db.ReleaseStageRolledBack,
		db.ReleaseStageDone:
		return r.Stage
	}

	// (2) rollback_reason set => rolled_back. Belt-and-suspenders for
	// the case where the stored stage drifted but the rollback record
	// is intact. The rollback path always writes both, so this branch
	// is rare in practice — but it makes derivation robust to a write
	// that landed the timestamp but lost the stage update.
	if r.RollbackReason.Valid && r.RollbackReason.String != "" {
		return db.ReleaseStageRolledBack
	}

	// (3) done_at explicitly stamped => done. Operator clicked "Mark
	// Done" before the 24-hour auto-rule fired.
	if r.DoneAt.Valid {
		return db.ReleaseStageDone
	}

	// (4) Promoted long enough ago => done. The audit doc's 24-hour
	// "monitoring window" rule (Section 3.2 of the rebuild doc). A
	// release sitting at in_production for >24h with no rollback has
	// effectively shipped — keeping it on the Active rail clutters the
	// UI and confuses operators reviewing "what's still in flight".
	//
	// Defensive: we auto-graduate when production_deploy_id is non-NULL
	// (a deploy webhook linked a real deploy row). For a direct-merge
	// release (ROA-311) the deploy webhook may never link a deploy row —
	// but the merge itself is the evidence the deploy fired, since
	// Multica auto-deploys on every merge to the default branch. So a
	// direct-merge release also satisfies the rule: before 24h it
	// derives in_production, after it derives done and drops off the
	// Active rail. A non-direct-merge release with no production deploy
	// link still requires a manual Mark Done (falls through to step 5).
	if r.PromotedAt.Valid && (r.ProductionDeployID.Valid || r.IsDirectMerge) {
		if now.Sub(r.PromotedAt.Time) > stageDoneAfterPromote {
			return db.ReleaseStageDone
		}
		return db.ReleaseStageInProduction
	}

	// (5) promoted_at stamped but no production_deploy_id => promoting.
	// The promote endpoint stamps promoted_at first, then creates the
	// deploy row. A read sees promoted_at without production_deploy_id
	// during that gap; the right display is "promoting".
	if r.PromotedAt.Valid {
		return db.ReleaseStagePromoting
	}

	// (6) staged_at stamped => the release reached staging; the exact
	// stage is a tiebreaker on the stored column. staged_at alone can't
	// distinguish in_staging / verifying / promoting / in_production, so
	// we honor an advanced stored stage and otherwise default to the
	// shape-aware post-merge stage.
	//
	// Honoring promoting / in_production here matters for direct_to_prod
	// projects: they have no staging environment, and a release that
	// reached `promoting` must not be pinned back to `in_staging` just
	// because a staged_at timestamp is present (a legacy synthetic
	// staged_at from an older completeMergeTrain, or any later writer).
	// The default fall-through is shape-aware too: a direct_to_prod
	// release carrying a legacy synthetic staged_at derives to
	// in_production, not the phantom in_staging that isn't in its
	// kanban. Showing "IN STAGING" for a repo with no staging is the
	// ROA-31x report this branch fixes.
	if r.StagedAt.Valid {
		switch r.Stage {
		case db.ReleaseStageVerifying,
			db.ReleaseStagePromoting,
			db.ReleaseStageInProduction:
			return r.Stage
		}
		return postMergeStage(shape)
	}

	// (7) merged_at stamped, no staging yet => the shape-aware
	// post-merge stage. For a staged shape that's in_staging (awaiting
	// the staging deploy that will stamp staged_at); for a
	// direct_to_prod shape — which has no staging environment — the
	// merge auto-deploys to production, so it's in_production. The
	// previous behavior went to "in_staging" via the writer in
	// completeMergeTrain — derivation now does it directly, and
	// shape-aware.
	if r.MergedAt.Valid {
		return postMergeStage(shape)
	}

	// (8) Honor the merging-state signal from the stored column when
	// no timestamps have moved yet. The merge train uses "merging" as
	// a soft state during dispatch; the next writer (completeMergeTrain
	// on success or a failure handler on abort) flips it.
	if r.Stage == db.ReleaseStageMerging {
		return db.ReleaseStageMerging
	}

	// (9) PR-membership consultation (Phase 2). The timestamp ladder
	// missed this release: nothing stamped merged_at, so steps (4)-(8)
	// all fell through. But if every member PR has actually merged, the
	// release IS past assembling — its PRs merged outside Ship Hub's
	// merge train (a direct GitHub merge, a server restart that killed
	// the merge-train goroutine after the merge landed, etc.) so no
	// writer ever stamped the ladder.
	//
	// release_stage has no `merged` value; the post-merge pre-deploy
	// stage is what an all-PRs-merged release derives to, and it is
	// shape-aware: a staged shape derives to in_staging, a
	// direct_to_prod shape — which has no staging environment, so its
	// merge auto-deploys to production — derives to in_production.
	// This is the durable fix for ROA-311 (and the ROA-333 follow-up
	// that made it shape-aware): a release whose PRs merged outside the
	// merge train can never strand in `assembling`, nor in a phantom
	// `in_staging` that isn't in a direct_to_prod project's kanban. A
	// release with NO member PRs, or with any unmerged member PR, keeps
	// the existing behavior and falls through to step (10).
	if len(prMergeStates) > 0 {
		allMerged := true
		for _, st := range prMergeStates {
			if st != db.PullRequestStateMerged {
				allMerged = false
				break
			}
		}
		if allMerged {
			return postMergeStage(shape)
		}
	}

	// (10) Default: still assembling. The release exists, no PR has
	// merged yet, no merge train started.
	return db.ReleaseStageAssembling
}

// IsTerminalDerivedStage returns true when the derived stage means the
// release no longer belongs on the "Active releases" rail. Mirrors the
// terminal-set the existing SQL filter uses, but applied to the derived
// value so a release whose stored stage drifted still drops off the
// Active list once derivation catches up.
func IsTerminalDerivedStage(s db.ReleaseStage) bool {
	switch s {
	case db.ReleaseStageDone,
		db.ReleaseStageRolledBack,
		db.ReleaseStageCancelled:
		return true
	}
	return false
}
