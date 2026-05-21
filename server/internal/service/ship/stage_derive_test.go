package ship

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Helpers to build release rows in test-readable shorthand.

func ts(t time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: t, Valid: true}
}

func ptrText(s string) pgtype.Text {
	if s == "" {
		return pgtype.Text{Valid: false}
	}
	return pgtype.Text{String: s, Valid: true}
}

func ptrUUID(set bool) pgtype.UUID {
	if !set {
		return pgtype.UUID{Valid: false}
	}
	// Any non-zero value will do; the function only checks .Valid.
	var u pgtype.UUID
	u.Bytes = [16]byte{0xde, 0xad, 0xbe, 0xef}
	u.Valid = true
	return u
}

// rel constructs a minimal ship_release row with the given stored stage
// and timestamps. Test cases populate just the fields they exercise.
type relBuilder struct {
	r db.ShipRelease
}

func aRelease(storedStage db.ReleaseStage) *relBuilder {
	return &relBuilder{r: db.ShipRelease{Stage: storedStage}}
}

func (b *relBuilder) mergedAt(t time.Time) *relBuilder    { b.r.MergedAt = ts(t); return b }
func (b *relBuilder) stagedAt(t time.Time) *relBuilder    { b.r.StagedAt = ts(t); return b }
func (b *relBuilder) promotedAt(t time.Time) *relBuilder  { b.r.PromotedAt = ts(t); return b }
func (b *relBuilder) doneAt(t time.Time) *relBuilder      { b.r.DoneAt = ts(t); return b }
func (b *relBuilder) rollbackReason(s string) *relBuilder { b.r.RollbackReason = ptrText(s); return b }
func (b *relBuilder) withProductionDeploy() *relBuilder {
	b.r.ProductionDeployID = ptrUUID(true)
	return b
}
func (b *relBuilder) directMerge() *relBuilder { b.r.IsDirectMerge = true; return b }
func (b *relBuilder) build() db.ShipRelease    { return b.r }

// merged / unmerged build the prMergeStates slice DeriveReleaseStage's
// Phase 2 step consults — n member PRs all in the given state.
func merged(n int) []db.PullRequestState {
	out := make([]db.PullRequestState, n)
	for i := range out {
		out[i] = db.PullRequestStateMerged
	}
	return out
}

// TestDeriveReleaseStage_StickyTerminals — the stored "done" / "rolled_back"
// / "cancelled" values are operator intent and must not be re-derived.
// Even if the timestamp ladder suggests otherwise, derivation respects
// the operator's last word.
func TestDeriveReleaseStage_StickyTerminals(t *testing.T) {
	t.Parallel()
	now := time.Now()
	cases := []struct {
		name  string
		stage db.ReleaseStage
	}{
		{"done", db.ReleaseStageDone},
		{"rolled_back", db.ReleaseStageRolledBack},
		{"cancelled", db.ReleaseStageCancelled},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// Even with promoted_at and a production_deploy_id set,
			// terminal stages must not move.
			r := aRelease(c.stage).
				promotedAt(now.Add(-48 * time.Hour)).
				withProductionDeploy().
				build()
			if got := DeriveReleaseStage(r, nil, now); got != c.stage {
				t.Errorf("terminal stage %q must not be re-derived; got %q", c.stage, got)
			}
		})
	}
}

// TestDeriveReleaseStage_RollbackReasonOverridesDrift — if the rollback
// timestamp/reason landed but the stage column didn't move, derivation
// reports rolled_back from the reason field.
func TestDeriveReleaseStage_RollbackReasonOverridesDrift(t *testing.T) {
	t.Parallel()
	now := time.Now()
	r := aRelease(db.ReleaseStageInProduction). // stale stored stage
							rollbackReason("smoke tests failed").
							promotedAt(now.Add(-2 * time.Hour)).
							withProductionDeploy().
							build()
	if got := DeriveReleaseStage(r, nil, now); got != db.ReleaseStageRolledBack {
		t.Errorf("rollback_reason set must derive rolled_back; got %q", got)
	}
}

// TestDeriveReleaseStage_ExplicitDoneAtBeatsAutoWindow — if the operator
// clicked Mark Done explicitly, we return done immediately regardless of
// how recently promoted_at fired.
func TestDeriveReleaseStage_ExplicitDoneAtBeatsAutoWindow(t *testing.T) {
	t.Parallel()
	now := time.Now()
	r := aRelease(db.ReleaseStageInProduction).
		promotedAt(now.Add(-1 * time.Hour)). // within auto window
		doneAt(now.Add(-30 * time.Minute)).
		withProductionDeploy().
		build()
	if got := DeriveReleaseStage(r, nil, now); got != db.ReleaseStageDone {
		t.Errorf("explicit done_at must derive done; got %q", got)
	}
}

// TestDeriveReleaseStage_AutoDoneAfter24h — promoted_at + a production
// deploy + 24h elapsed = done. This is THE rule that finally drops a
// stuck "in_production" release off the Active rail without operator
// intervention.
func TestDeriveReleaseStage_AutoDoneAfter24h(t *testing.T) {
	t.Parallel()
	now := time.Now()
	r := aRelease(db.ReleaseStageInProduction).
		promotedAt(now.Add(-25 * time.Hour)).
		withProductionDeploy().
		build()
	if got := DeriveReleaseStage(r, nil, now); got != db.ReleaseStageDone {
		t.Errorf("promoted_at > 24h ago + production_deploy_id must derive done; got %q", got)
	}
}

// TestDeriveReleaseStage_AutoDoneRequiresDeploy — promoted_at alone is
// not enough. If production_deploy_id is NULL the deploy may never have
// fired (e.g. workflow_dispatch never got triggered manually). Keep it
// at in_production so the operator notices via the Active rail.
func TestDeriveReleaseStage_AutoDoneRequiresDeploy(t *testing.T) {
	t.Parallel()
	now := time.Now()
	r := aRelease(db.ReleaseStageInProduction).
		promotedAt(now.Add(-48 * time.Hour)).
		// no withProductionDeploy() — deploy never recorded
		build()
	if got := DeriveReleaseStage(r, nil, now); got != db.ReleaseStagePromoting {
		// promoted_at without production_deploy_id => promoting (Phase 1
		// branch 5). After phase 2 with full deploy lookup this might
		// stay at in_production or be more nuanced; for now we surface
		// "promoting" so the operator knows the deploy didn't land.
		t.Errorf("promoted_at without production_deploy_id must derive promoting (deploy not yet recorded); got %q", got)
	}
}

// TestDeriveReleaseStage_InProductionWithinWindow — fresh promotion, less
// than 24h old, deploy recorded => in_production.
func TestDeriveReleaseStage_InProductionWithinWindow(t *testing.T) {
	t.Parallel()
	now := time.Now()
	r := aRelease(db.ReleaseStagePromoting). // stored stage hasn't advanced yet
							promotedAt(now.Add(-2 * time.Hour)).
							withProductionDeploy().
							build()
	if got := DeriveReleaseStage(r, nil, now); got != db.ReleaseStageInProduction {
		t.Errorf("fresh promoted release with deploy must derive in_production; got %q", got)
	}
}

// TestDeriveReleaseStage_StagingWithoutVerifying — staged_at stamped,
// stored stage is something other than verifying => in_staging.
func TestDeriveReleaseStage_StagingWithoutVerifying(t *testing.T) {
	t.Parallel()
	now := time.Now()
	r := aRelease(db.ReleaseStageInStaging).
		stagedAt(now.Add(-30 * time.Minute)).
		mergedAt(now.Add(-1 * time.Hour)).
		build()
	if got := DeriveReleaseStage(r, nil, now); got != db.ReleaseStageInStaging {
		t.Errorf("staged_at without verifying intent must derive in_staging; got %q", got)
	}
}

// TestDeriveReleaseStage_VerifyingHonoredFromStored — if a writer set
// "verifying" (QA in progress) and staged_at is set, honor the writer's
// hint. Phase 2 replaces this with a deploy_preflight.qa_verified_at
// lookup.
func TestDeriveReleaseStage_VerifyingHonoredFromStored(t *testing.T) {
	t.Parallel()
	now := time.Now()
	r := aRelease(db.ReleaseStageVerifying).
		stagedAt(now.Add(-15 * time.Minute)).
		mergedAt(now.Add(-1 * time.Hour)).
		build()
	if got := DeriveReleaseStage(r, nil, now); got != db.ReleaseStageVerifying {
		t.Errorf("stored verifying with staged_at must derive verifying; got %q", got)
	}
}

// TestDeriveReleaseStage_StagedAtHonorsPromoting — a direct_to_prod
// release whose merge train stamped `promoting` must NOT be pinned back
// to in_staging by a (legacy synthetic) staged_at. ROA-31x: "IN STAGING"
// was showing on a repo with no staging environment.
func TestDeriveReleaseStage_StagedAtHonorsPromoting(t *testing.T) {
	t.Parallel()
	now := time.Now()
	r := aRelease(db.ReleaseStagePromoting).
		stagedAt(now.Add(-10 * time.Minute)).
		mergedAt(now.Add(-12 * time.Minute)).
		build()
	if got := DeriveReleaseStage(r, nil, now); got != db.ReleaseStagePromoting {
		t.Errorf("stored promoting with staged_at must derive promoting, not in_staging; got %q", got)
	}
}

// TestDeriveReleaseStage_StagedAtHonorsInProduction — same tiebreaker:
// a stored in_production survives a present staged_at.
func TestDeriveReleaseStage_StagedAtHonorsInProduction(t *testing.T) {
	t.Parallel()
	now := time.Now()
	r := aRelease(db.ReleaseStageInProduction).
		stagedAt(now.Add(-1 * time.Hour)).
		mergedAt(now.Add(-2 * time.Hour)).
		build()
	if got := DeriveReleaseStage(r, nil, now); got != db.ReleaseStageInProduction {
		t.Errorf("stored in_production with staged_at must derive in_production; got %q", got)
	}
}

// TestDeriveReleaseStage_MergedWaitingForStaging — merge train completed,
// no staging deploy yet => in_staging (awaiting the staging deploy event).
func TestDeriveReleaseStage_MergedWaitingForStaging(t *testing.T) {
	t.Parallel()
	now := time.Now()
	r := aRelease(db.ReleaseStageInStaging).
		mergedAt(now.Add(-3 * time.Minute)).
		build()
	if got := DeriveReleaseStage(r, nil, now); got != db.ReleaseStageInStaging {
		t.Errorf("merged_at without staged_at must derive in_staging; got %q", got)
	}
}

// TestDeriveReleaseStage_MergingFromStored — pre-merge state. Stored
// stage carries the merge train signal because no monotone timestamp
// captures "merge in flight" yet.
func TestDeriveReleaseStage_MergingFromStored(t *testing.T) {
	t.Parallel()
	now := time.Now()
	r := aRelease(db.ReleaseStageMerging).build()
	if got := DeriveReleaseStage(r, nil, now); got != db.ReleaseStageMerging {
		t.Errorf("stored merging with no timestamps must derive merging; got %q", got)
	}
}

// TestDeriveReleaseStage_DefaultAssembling — brand-new release row, no
// timestamps, default stored stage. Derivation returns assembling.
func TestDeriveReleaseStage_DefaultAssembling(t *testing.T) {
	t.Parallel()
	now := time.Now()
	r := aRelease(db.ReleaseStageAssembling).build()
	if got := DeriveReleaseStage(r, nil, now); got != db.ReleaseStageAssembling {
		t.Errorf("blank release must derive assembling; got %q", got)
	}
}

// TestDeriveReleaseStage_FixesActiveRailDriftFromBug3 — the canonical
// regression scenario from the audit doc (Bug 3): a release whose stored
// stage drifted (e.g. failed mid-train write) but whose timestamp ladder
// reached production hours ago.
//
// Before this fix the Active Releases rail would show the release at
// its drifted stage forever. With derivation, it correctly returns done
// once 24h has elapsed since promotion, and IsTerminalDerivedStage drops
// it off the rail at the same moment.
func TestDeriveReleaseStage_FixesActiveRailDriftFromBug3(t *testing.T) {
	t.Parallel()
	now := time.Now()
	r := aRelease(db.ReleaseStageInStaging). // drifted! actually shipped 30h ago
							mergedAt(now.Add(-30 * time.Hour)).
							stagedAt(now.Add(-30 * time.Hour)).
							promotedAt(now.Add(-30 * time.Hour)).
							withProductionDeploy().
							build()
	derived := DeriveReleaseStage(r, nil, now)
	if derived != db.ReleaseStageDone {
		t.Fatalf("drifted stage on shipped release must derive done; got %q", derived)
	}
	if !IsTerminalDerivedStage(derived) {
		t.Fatalf("derived done must be terminal so Active rail drops it")
	}
}

// TestDeriveReleaseStage_AllPRsMergedRescuesAssembling — ROA-311 Phase 2.
// A release whose timestamp ladder is entirely NULL (no merged_at) but
// whose every member PR has merged on GitHub must NOT strand in
// `assembling`. The PR-membership step derives in_staging — the
// post-merge pre-deploy stage — so the release leaves assembling and the
// rail reflects reality.
func TestDeriveReleaseStage_AllPRsMergedRescuesAssembling(t *testing.T) {
	t.Parallel()
	now := time.Now()
	r := aRelease(db.ReleaseStageAssembling).build() // ladder all NULL
	if got := DeriveReleaseStage(r, merged(2), now); got != db.ReleaseStageInStaging {
		t.Errorf("all-PRs-merged release must derive in_staging; got %q", got)
	}
}

// TestDeriveReleaseStage_UnmergedPRKeepsAssembling — the Phase 2 step
// only fires when EVERY member PR has merged. One still-open PR means
// the release genuinely is still assembling.
func TestDeriveReleaseStage_UnmergedPRKeepsAssembling(t *testing.T) {
	t.Parallel()
	now := time.Now()
	r := aRelease(db.ReleaseStageAssembling).build()
	states := []db.PullRequestState{db.PullRequestStateMerged, db.PullRequestStateOpen}
	if got := DeriveReleaseStage(r, states, now); got != db.ReleaseStageAssembling {
		t.Errorf("release with an unmerged PR must stay assembling; got %q", got)
	}
}

// TestDeriveReleaseStage_NoMemberPRsKeepsAssembling — a release with zero
// member PRs is genuinely still assembling; the empty-slice path must not
// trip the all-merged rule (vacuous truth would be wrong here).
func TestDeriveReleaseStage_NoMemberPRsKeepsAssembling(t *testing.T) {
	t.Parallel()
	now := time.Now()
	r := aRelease(db.ReleaseStageAssembling).build()
	if got := DeriveReleaseStage(r, nil, now); got != db.ReleaseStageAssembling {
		t.Errorf("release with no member PRs must stay assembling; got %q", got)
	}
}

// TestDeriveReleaseStage_DirectMergeAutoDoneAfter24h — ROA-311 Phase 2a.
// A synthesized direct-merge release is born with promoted_at stamped but
// never gets a production_deploy_id linked. After 24h it must still
// auto-graduate to done so it drops off the Active rail — the merge
// itself is the evidence the deploy fired.
func TestDeriveReleaseStage_DirectMergeAutoDoneAfter24h(t *testing.T) {
	t.Parallel()
	now := time.Now()
	r := aRelease(db.ReleaseStageInProduction).
		promotedAt(now.Add(-25 * time.Hour)).
		directMerge().
		// no withProductionDeploy() — direct merges never link one
		build()
	if got := DeriveReleaseStage(r, nil, now); got != db.ReleaseStageDone {
		t.Errorf("direct-merge release promoted >24h ago must derive done; got %q", got)
	}
}

// TestDeriveReleaseStage_DirectMergeInProductionWithinWindow — before the
// 24h window elapses, a direct-merge release derives in_production even
// without a linked production deploy.
func TestDeriveReleaseStage_DirectMergeInProductionWithinWindow(t *testing.T) {
	t.Parallel()
	now := time.Now()
	r := aRelease(db.ReleaseStageInProduction).
		promotedAt(now.Add(-2 * time.Hour)).
		directMerge().
		build()
	if got := DeriveReleaseStage(r, nil, now); got != db.ReleaseStageInProduction {
		t.Errorf("fresh direct-merge release must derive in_production; got %q", got)
	}
}

// TestIsTerminalDerivedStage covers the small helper that the handler
// uses to filter the Active rail.
func TestIsTerminalDerivedStage(t *testing.T) {
	t.Parallel()
	cases := []struct {
		stage    db.ReleaseStage
		terminal bool
	}{
		{db.ReleaseStageAssembling, false},
		{db.ReleaseStageMerging, false},
		{db.ReleaseStageInStaging, false},
		{db.ReleaseStageVerifying, false},
		{db.ReleaseStagePromoting, false},
		{db.ReleaseStageInProduction, false},
		{db.ReleaseStageDone, true},
		{db.ReleaseStageRolledBack, true},
		{db.ReleaseStageCancelled, true},
	}
	for _, c := range cases {
		if got := IsTerminalDerivedStage(c.stage); got != c.terminal {
			t.Errorf("IsTerminalDerivedStage(%q): got %v, want %v", c.stage, got, c.terminal)
		}
	}
}
