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
// Phase 1 (this file): derivation reads from ship_release's own timestamp
// ladder (merged_at, staged_at, promoted_at, done_at, rollback_reason)
// plus the stored stage for sticky-terminal classification. This is enough
// to fix Bug 3 from the audit (Active Releases doesn't clear) without
// needing to thread PR-membership and deploy-environment data through
// every read path.
//
// Phase 2 (follow-up): extend the derivation to consult PR merge state +
// deploy environments + deploy_preflight.qa_verified_at so we can detect
// edge cases the timestamp ladder alone misses (e.g. a promoted_at stamp
// without a matching successful production deploy means "promoting", not
// "in_production").
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

// DeriveReleaseStage returns the stage a release SHOULD be in given the
// observable facts on the release row. Terminal stages and the explicit
// rollback signal are sticky; everything else is derived from the
// timestamp ladder.
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
func DeriveReleaseStage(r db.ShipRelease, now time.Time) db.ReleaseStage {
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
	// Defensive: we only auto-graduate when production_deploy_id is
	// non-NULL. Without that link there's no evidence the deploy
	// actually fired, so we leave the release at in_production and
	// require a manual Mark Done.
	if r.PromotedAt.Valid && r.ProductionDeployID.Valid {
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

	// (6) staged_at stamped => verifying or in_staging. We don't have
	// qa_verified_at on the release row (it's on deploy_preflight,
	// keyed by (env, sha)). Without joining that table here, fall back
	// to the stored stage to distinguish: if the writer last set
	// "verifying" we honor it, otherwise default to "in_staging".
	// This is the one branch where Phase 1 still consults the stored
	// stage as a tiebreaker. Phase 2 will replace this with a real
	// deploy_preflight lookup.
	if r.StagedAt.Valid {
		if r.Stage == db.ReleaseStageVerifying {
			return db.ReleaseStageVerifying
		}
		return db.ReleaseStageInStaging
	}

	// (7) merged_at stamped, no staging yet => in_staging (awaiting
	// the staging deploy that will stamp staged_at). The previous
	// behavior went to "in_staging" via the writer in
	// completeMergeTrain — derivation now does it directly.
	if r.MergedAt.Valid {
		return db.ReleaseStageInStaging
	}

	// (8) Honor the merging-state signal from the stored column when
	// no timestamps have moved yet. The merge train uses "merging" as
	// a soft state during dispatch; the next writer (completeMergeTrain
	// on success or a failure handler on abort) flips it.
	if r.Stage == db.ReleaseStageMerging {
		return db.ReleaseStageMerging
	}

	// (9) Default: still assembling. The release exists, no PR has
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
