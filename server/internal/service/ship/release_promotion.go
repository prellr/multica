// Phase 7d — Production promotion + rollback.
//
// This file owns the production half of a release: PromoteRelease
// flips the release into the "promoting" stage and waits for the
// production deploy to land; LinkProductionDeploy is the webhook-side
// counterpart that records the linkage when a deployment_status
// event matches; MarkReleaseRollback records the user's decision to
// roll back.
//
// Why no auto-revert orchestrator in v1: a "create revert PR" REST
// endpoint doesn't exist on GitHub — generating a true revert means
// constructing new tree objects via the Git Trees + Refs API, which
// has a substantial correctness/idempotency surface (cherry-pick
// conflicts, branch protection interactions, signed commits, etc.).
// Phase 7d's value is closing the release loop *now*: a user can
// click Promote, see the deploy, and roll back if needed. The "roll
// back" part is currently user-driven — the channel post lists the
// merged PRs in reverse merge order with deep links so the user can
// click GitHub's per-PR "Revert" button. v2 (Phase 7e or later) adds
// the auto-orchestrator on top of the same data model — the
// per-PR revert_state columns we added in migration 089 are already
// in place.
//
// All four user-facing methods take a `*StagingDeps` (re-used from
// Phase 7c) for the channel post + WS publisher. Phase 7d doesn't
// need a separate deps struct because the production-stage actions
// follow the same write-pattern as the staging ones.

package ship

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	gh "github.com/multica-ai/multica/server/pkg/github"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// Sentinel errors specific to the production-stage transitions. Mapped
// to clean HTTP statuses by the handler.
var (
	ErrReleaseNotInVerifying2  = errors.New("release: not in verifying stage")
	ErrReleaseNotInProduction  = errors.New("release: not in a production stage (verifying / promoting / in_production)")
	ErrReleaseAlreadyPromoted  = errors.New("release: already promoted")
	ErrReleaseAlreadyRolled    = errors.New("release: already rolled back")
	ErrReleaseRollbackNoTarget = errors.New("release: nothing to roll back (no merged PRs)")
)

// PromoteRelease initiates the production deploy. Records the stage
// transition (verifying → promoting) and the promoted_at / promoted_by
// pair. The actual deploy still has to land via the user's CI/CD; we
// record the intent and wait for the deployment_status webhook (or a
// manual MarkReleaseProductionDeployed) to confirm.
//
// Preconditions:
//   - release.stage == "verifying"
//   - All Phase 5 risk-tier rules satisfied (the verify gate already
//     enforced approver_id for medium+ and second_approver_id for
//     critical, so by the time we reach Promote those have been set).
//   - Caller passes the canVerifyRelease eligibility test (re-used
//     from Phase 7c — same approver-equality rule).
//
// Returns ErrReleaseStageMismatch / ErrApproverRequired so the handler
// can surface clean 4xx codes.
func (s *Service) PromoteRelease(
	ctx context.Context,
	releaseID, requestedBy pgtype.UUID,
	approval ApprovalContext,
	deps *StagingDeps,
) (db.ShipRelease, error) {
	release, err := s.Q.GetRelease(ctx, releaseID)
	if err != nil {
		return db.ShipRelease{}, fmt.Errorf("get release: %w", err)
	}
	if release.Stage != db.ReleaseStageVerifying {
		return db.ShipRelease{}, fmt.Errorf("%w: stage=%s, want verifying", ErrReleaseStageMismatch, release.Stage)
	}
	// Approver eligibility — same rule as MarkVerified. We re-check
	// here because the user who clicks Promote may not be the same
	// user who verified (and the configured rule demands the right
	// approver every transition).
	//
	// "two"-rule promotes don't need fresh signoffs: by the time we
	// reach Promote, both signoffs already exist on the release (or
	// the verify step would have returned ErrTwoApproverPending).
	// The rule still gates on slot-match so a non-approver can't
	// trigger Promote even if smoke + verify silently passed.
	rule := resolveApprovalRule(approval.Rule, release.RiskLevel)
	if _, ok := approverEligibility(rule, release, requestedBy, approval); !ok {
		return db.ShipRelease{}, ErrApproverRequired
	}

	now := pgtype.Timestamptz{Time: deps.now(), Valid: true}
	updated, err := s.Q.SetReleasePromoted(ctx, db.SetReleasePromotedParams{
		ID:                 releaseID,
		PromotedAt:         now,
		ProductionDeployID: pgtype.UUID{}, // filled by LinkProductionDeploy
		ProductionMainSha:  pgtype.Text{},
		PromotedBy:         requestedBy,
	})
	if err != nil {
		return db.ShipRelease{}, fmt.Errorf("set release promoted: %w", err)
	}

	// Stage flip: verifying → promoting.
	flipped, err := s.Q.UpdateReleaseStage(ctx, db.UpdateReleaseStageParams{
		ID:         releaseID,
		Stage:      db.ReleaseStagePromoting,
		PromotedAt: now,
	})
	if err == nil {
		updated = flipped
	}

	_, _ = s.insertReleaseEvent(ctx, releaseID, "release_promoted", requestedBy, map[string]any{
		"sha":        textValue(updated.MergedMainSha),
		"by_user_id": uuidString(requestedBy),
	})
	if deps != nil && deps.Publisher != nil {
		deps.Publisher.PublishMergeEvent(protocol.EventReleasePromoted, uuidString(updated.WorkspaceID), map[string]any{
			"release_id": uuidString(releaseID),
			"sha":        textValue(updated.MergedMainSha),
			"by_user_id": uuidString(requestedBy),
		})
		deps.Publisher.PublishMergeEvent(protocol.EventReleaseUpdated, uuidString(updated.WorkspaceID), map[string]any{
			"release_id": uuidString(releaseID),
			"stage":      string(updated.Stage),
		})
	}
	postReleaseChannelStaging(deps, ctx, updated.ChannelID, fmt.Sprintf(
		"🚀 Promoted to production · sha=%s · awaiting production deploy",
		shortSHA(textValue(updated.MergedMainSha)),
	))

	// Auto-deploy hook: if the production env opted in (auto_deploy=true
	// AND deploy_workflow_filename is set), fire workflow_dispatch on
	// the project's repo so the merge-and-promote flow ends in an
	// actual production deploy instead of "awaiting deploy" forever.
	// Best-effort: log warnings on failure but don't block the promote
	// — operators can still click Run Workflow on GH or Mark deployed.
	s.maybeAutoDispatchProductionDeploy(ctx, updated, requestedBy, deps)

	return updated, nil
}

// Sentinel error returned when an env-level promote is requested but
// the env has no deploy_workflow_filename to dispatch.
var ErrEnvNoDeployWorkflow = errors.New("deploy environment has no deploy_workflow_filename configured")

// dispatchEnvDeployWorkflow is the reusable dispatch core shared by
// maybeAutoDispatchProductionDeploy (release-driven, opt-in) and
// PromoteEnvironment (board-level, explicit). It resolves the project's
// github_repo resource, parses owner/repo, and fires the env's
// deploy_workflow_filename via workflow_dispatch on the env's
// target_branch (default "main").
//
// Returns:
//   - ErrEnvNoDeployWorkflow when the env has no workflow filename.
//   - a descriptive error when the repo can't be resolved/parsed or the
//     dispatch call fails.
//
// Callers decide whether the error is fatal (PromoteEnvironment surfaces
// it to the user) or best-effort (maybeAutoDispatchProductionDeploy logs
// and moves on).
func (s *Service) dispatchEnvDeployWorkflow(
	ctx context.Context,
	projectID pgtype.UUID,
	env db.DeployEnvironment,
) error {
	if s.Github == nil {
		return errors.New("github client unset")
	}
	filename := strings.TrimSpace(textValue(env.DeployWorkflowFilename))
	if filename == "" {
		return ErrEnvNoDeployWorkflow
	}
	// Resolve repo URL from the project's github_repo resource.
	resources, err := s.Q.ListProjectResources(ctx, projectID)
	if err != nil {
		return fmt.Errorf("list project resources: %w", err)
	}
	var repoURL string
	for _, r := range resources {
		if r.ResourceType != "github_repo" {
			continue
		}
		if u, err := repoURLFromResource(r.ResourceRef); err == nil {
			repoURL = u
			break
		}
	}
	if repoURL == "" {
		return errors.New("project has no github_repo resource")
	}
	owner, repo, err := gh.ParseRepoURL(repoURL)
	if err != nil {
		return fmt.Errorf("parse repo url %q: %w", repoURL, err)
	}
	branch := strings.TrimSpace(env.TargetBranch)
	if branch == "" {
		branch = "main"
	}
	inputs := parseDeployWorkflowInputs(env)
	if err := s.Github.DispatchWorkflow(ctx, owner, repo, filename, branch, inputs); err != nil {
		return fmt.Errorf("dispatch workflow %s on %s: %w", filename, branch, err)
	}
	return nil
}

// parseDeployWorkflowInputs unmarshals the env's stored deploy_workflow_inputs
// JSONB into the flat string→string map GitHub's workflow_dispatch API
// expects. A null/empty column yields nil (no inputs — the legacy
// behavior). Malformed stored JSON must NOT panic the promote path: we log
// a warning and treat it as no inputs, so a corrupt value degrades to the
// inputless dispatch rather than crashing.
func parseDeployWorkflowInputs(env db.DeployEnvironment) map[string]string {
	if len(env.DeployWorkflowInputs) == 0 {
		return nil
	}
	var inputs map[string]string
	if err := json.Unmarshal(env.DeployWorkflowInputs, &inputs); err != nil {
		slog.Warn("ship: ignoring malformed deploy_workflow_inputs",
			"env_id", uuidString(env.ID), "error", err)
		return nil
	}
	if len(inputs) == 0 {
		return nil
	}
	return inputs
}

// PromoteEnvironment dispatches the env's deploy workflow on demand —
// the board-level "Deploy to production" action. Unlike
// maybeAutoDispatchProductionDeploy (which is release-scoped and gated
// on auto_deploy), this is an explicit operator request: it ALWAYS
// dispatches (no auto_deploy gate) and a missing workflow filename is a
// hard error rather than a silent no-op.
//
// Works for any env kind; the UI exposes it on production. One prod
// deploy ships all of main, and the Phase B ancestry resolver advances
// every release that deploy provably shipped — so this single action
// replaces N per-release promotes.
//
// Returns ErrEnvNoDeployWorkflow when no workflow is configured so the
// handler can surface a clean 400.
func (s *Service) PromoteEnvironment(
	ctx context.Context,
	env db.DeployEnvironment,
	actor pgtype.UUID,
	deps *StagingDeps,
) error {
	if strings.TrimSpace(textValue(env.DeployWorkflowFilename)) == "" {
		return ErrEnvNoDeployWorkflow
	}
	if err := s.dispatchEnvDeployWorkflow(ctx, env.ProjectID, env); err != nil {
		slog.Warn("ship: env promote dispatch failed",
			"env_id", uuidString(env.ID), "project_id", uuidString(env.ProjectID), "error", err)
		return err
	}
	branch := strings.TrimSpace(env.TargetBranch)
	if branch == "" {
		branch = "main"
	}
	filename := strings.TrimSpace(textValue(env.DeployWorkflowFilename))
	slog.Info("ship: env promote dispatched",
		"env_id", uuidString(env.ID), "project_id", uuidString(env.ProjectID),
		"workflow", filename, "ref", branch)
	return nil
}

// maybeAutoDispatchProductionDeploy fires the production env's
// workflow_dispatch when auto_deploy is set. No-op (with a debug log)
// when prerequisites aren't met. Errors don't fail the surrounding
// PromoteRelease — the user already got the stage transition, and
// they can recover by clicking Run Workflow on GitHub directly.
func (s *Service) maybeAutoDispatchProductionDeploy(
	ctx context.Context,
	release db.ShipRelease,
	actor pgtype.UUID,
	deps *StagingDeps,
) {
	if s.Github == nil {
		slog.Debug("ship: auto-deploy skipped, github client unset",
			"release_id", uuidString(release.ID))
		return
	}
	envs, err := s.Q.ListDeployEnvironmentsByProject(ctx, release.ProjectID)
	if err != nil {
		slog.Warn("ship: auto-deploy lookup envs failed",
			"release_id", uuidString(release.ID), "error", err)
		return
	}
	var prodEnv *db.DeployEnvironment
	for i, e := range envs {
		if e.Kind == db.DeployEnvironmentKindProduction {
			prodEnv = &envs[i]
			break
		}
	}
	if prodEnv == nil || !prodEnv.AutoDeploy {
		return
	}
	filename := strings.TrimSpace(textValue(prodEnv.DeployWorkflowFilename))
	if filename == "" {
		slog.Info("ship: auto-deploy skipped, no deploy_workflow_filename on prod env",
			"release_id", uuidString(release.ID), "env_id", uuidString(prodEnv.ID))
		return
	}
	branch := strings.TrimSpace(prodEnv.TargetBranch)
	if branch == "" {
		branch = "main"
	}
	// Delegate to the shared dispatch core so the release-driven and the
	// board-level promote paths fire identical workflow_dispatch calls.
	if err := s.dispatchEnvDeployWorkflow(ctx, release.ProjectID, *prodEnv); err != nil {
		if errors.Is(err, ErrEnvNoDeployWorkflow) {
			// Already logged above by the empty-filename guard; defensive.
			return
		}
		slog.Warn("ship: auto-deploy workflow_dispatch failed",
			"release_id", uuidString(release.ID),
			"workflow", filename, "ref", branch, "error", err)
		_, _ = s.insertReleaseEvent(ctx, release.ID, "release_auto_dispatch_failed", actor, map[string]any{
			"workflow": filename, "ref": branch, "error": err.Error(),
		})
		return
	}
	slog.Info("ship: auto-deploy dispatched",
		"release_id", uuidString(release.ID),
		"workflow", filename, "ref", branch)
	_, _ = s.insertReleaseEvent(ctx, release.ID, "release_auto_dispatched", actor, map[string]any{
		"workflow": filename, "ref": branch,
	})
	postReleaseChannelStaging(deps, ctx, release.ChannelID, fmt.Sprintf(
		"⚡ Auto-dispatched %s on %s — production deploy starting",
		filename, branch,
	))
}

// LinkProductionDeploy is the webhook-side counterpart to PromoteRelease.
// When a successful production deploy lands whose sha matches the
// release's merged_main_sha (or production_main_sha when set), we
// record production_deploy_id / production_main_sha and advance to
// in_production.
//
// Tolerant of stage being "in_staging", "verifying", or "promoting" —
// the deploy can land before the user clicks Promote (a fast pipeline
// that auto-promotes from staging), and we don't want to drop the
// linkage in that case. in_staging is included for the Phase B
// environment-level resolver: when a production deploy provably ships a
// release's code (it merged before the deploy ran), the release IS in
// production even if Ship Hub's state machine still shows it parked in
// staging — that's exactly the stranded-release condition Phase C heals.
// When stage is in_staging/verifying we treat the deploy as a
// promote-and-link in a single step.
func (s *Service) LinkProductionDeploy(
	ctx context.Context,
	releaseID, deployID pgtype.UUID,
	deploySHA string,
	deps *StagingDeps,
) (db.ShipRelease, error) {
	now := pgtype.Timestamptz{Time: deps.now(), Valid: true}

	// Stamp production_deploy_id + production_main_sha. promoted_at is
	// COALESCE'd so a delayed webhook doesn't overwrite the click-time
	// timestamp (and an auto-promote-from-staging path stamps it now).
	updated, err := s.Q.SetReleasePromoted(ctx, db.SetReleasePromotedParams{
		ID:                 releaseID,
		PromotedAt:         now,
		ProductionDeployID: deployID,
		ProductionMainSha:  pgtype.Text{String: deploySHA, Valid: deploySHA != ""},
		PromotedBy:         pgtype.UUID{},
	})
	if err != nil {
		return db.ShipRelease{}, fmt.Errorf("set production deploy: %w", err)
	}

	// Stage flip to in_production. We allow the transition from
	// promoting, verifying, or in_staging. The latter two happen when
	// the user's pipeline auto-promotes (no explicit click) and the
	// webhook fires while the release is still upstream, and — for the
	// Phase B resolver — when a prod deploy provably shipped a release
	// still parked in staging.
	if updated.Stage == db.ReleaseStagePromoting ||
		updated.Stage == db.ReleaseStageVerifying ||
		updated.Stage == db.ReleaseStageInStaging {
		flipped, err := s.Q.UpdateReleaseStage(ctx, db.UpdateReleaseStageParams{
			ID:         releaseID,
			Stage:      db.ReleaseStageInProduction,
			PromotedAt: now,
		})
		if err == nil {
			updated = flipped
		}
	}

	_, _ = s.insertReleaseEvent(ctx, releaseID, "production_deploy_landed", pgtype.UUID{}, map[string]any{
		"deploy_id": uuidString(deployID),
		"sha":       deploySHA,
	})
	if deps != nil && deps.Publisher != nil {
		deps.Publisher.PublishMergeEvent(protocol.EventReleaseInProduction, uuidString(updated.WorkspaceID), map[string]any{
			"release_id": uuidString(releaseID),
			"deploy_id":  uuidString(deployID),
			"sha":        deploySHA,
		})
		deps.Publisher.PublishMergeEvent(protocol.EventReleaseUpdated, uuidString(updated.WorkspaceID), map[string]any{
			"release_id": uuidString(releaseID),
			"stage":      string(updated.Stage),
		})
	}
	if releasePRsAllMerged(ctx, s.Q, releaseID) {
		finalized, err := s.MarkReleaseDone(ctx, releaseID, deps)
		if err != nil {
			return updated, err
		}
		return finalized, nil
	}
	postReleaseChannelStaging(deps, ctx, updated.ChannelID, fmt.Sprintf(
		"Production deploy landed · sha=%s",
		shortSHA(deploySHA),
	))
	return updated, nil
}

// ResolveReleasesForProdSHA is the ancestry/ordering resolver shared by
// the prod-deploy-landed webhook path (Phase B) and the manual
// reconcile endpoint (Phase C). It advances EVERY non-terminal release
// for `projectID` that the production deploy at `sha` provably shipped —
// not just the one release whose merged_main_sha matches exactly.
//
// Ordering heuristic (NOT git ancestry):
//
//	A production deploy ships "main at SHA X", which contains every PR
//	merged to main BEFORE that deploy ran. We approximate git ancestry
//	with merge time: a release whose merged_at <= the deploy's cutoff is
//	contained in the deploy. This is correct for the common case
//	(releases merge, then a prod deploy ships all of main) and is the
//	same heuristic FindStuckPromotingReleaseForProject already relies on.
//
// Why it's safe:
//   - Only NON-terminal releases (in_staging / verifying / promoting)
//     are touched — never a done / rolled_back / cancelled release.
//   - Only releases whose merged_at <= cutoff are advanced. A release
//     merged AFTER the deploy ran is provably NOT in it and is skipped.
//   - Each release is advanced via LinkProductionDeploy, which already
//     enforces its own stage guard and finalizes to done only when all
//     PRs are merged.
//
// Known limitation (future refinement): out-of-order merges. If release
// B merged before release A but A's deploy landed first, merge-time
// ordering can mis-attribute. The exact fix is the GitHub compare API
// (does the deployed commit contain each release's merge_commit_sha?);
// that's deferred. Merge time is a strict, monotonic, locally-available
// signal that handles the overwhelmingly common in-order case.
//
// `cutoff` is the deploy's effective time (the anchor release's
// merged_at when an exact SHA match exists, else the deploy row's
// completed_at/triggered_at). Best-effort: a per-release failure logs
// and continues; the function never panics. Returns the count of
// releases it advanced.
func (s *Service) ResolveReleasesForProdSHA(
	ctx context.Context,
	projectID, deployID pgtype.UUID,
	sha string,
	cutoff time.Time,
	deps *StagingDeps,
) int {
	if !projectID.Valid || sha == "" {
		return 0
	}
	releases, err := s.Q.ListNonTerminalReleasesForProjectMergedAtOrBefore(ctx,
		db.ListNonTerminalReleasesForProjectMergedAtOrBeforeParams{
			ProjectID: projectID,
			Cutoff:    pgtype.Timestamptz{Time: cutoff, Valid: true},
		})
	if err != nil {
		slog.Warn("ship: resolve releases for prod sha — list failed",
			"project_id", uuidString(projectID), "sha", sha, "error", err)
		return 0
	}
	advanced := 0
	for _, rel := range releases {
		if _, err := s.LinkProductionDeploy(ctx, rel.ID, deployID, sha, deps); err != nil {
			slog.Warn("ship: resolve releases for prod sha — link failed",
				"release_id", uuidString(rel.ID), "sha", sha, "error", err)
			continue
		}
		advanced++
	}
	return advanced
}

// MarkReleaseRollback records the user's decision to roll back. Sets
// rolled_back_by + rolled_back_completed_at, posts the rollback
// instructions to the channel (linking each merged PR to its GitHub
// page so the user can click "Revert" on each), and transitions stage
// to rolled_back.
//
// v1 is user-driven: the actual revert PRs are created manually via
// GitHub's per-PR Revert button. v2 (Phase 7e+) will replace the body
// of this method with an orchestrator that creates and merges revert
// PRs automatically — the data model already supports it.
//
// Preconditions:
//   - release.stage in ("verifying", "promoting", "in_production")
//   - At least one merged PR to roll back (otherwise the rollback is
//     a no-op and should be a Cancel instead).
//   - Caller is workspace owner/admin OR the release's approver/second
//     approver (handler-level check; this method assumes the gate
//     already passed).
func (s *Service) MarkReleaseRollback(
	ctx context.Context,
	releaseID, requestedBy pgtype.UUID,
	reason string,
	deps *StagingDeps,
) (db.ShipRelease, error) {
	release, err := s.Q.GetRelease(ctx, releaseID)
	if err != nil {
		return db.ShipRelease{}, fmt.Errorf("get release: %w", err)
	}
	switch release.Stage {
	case db.ReleaseStageVerifying, db.ReleaseStagePromoting, db.ReleaseStageInProduction:
		// allowed
	case db.ReleaseStageRolledBack:
		return db.ShipRelease{}, ErrReleaseAlreadyRolled
	default:
		return db.ShipRelease{}, fmt.Errorf("%w: stage=%s", ErrReleaseNotInProduction, release.Stage)
	}

	mergedPRs, err := s.Q.ListReleasePRsByMergeOrderDesc(ctx, releaseID)
	if err != nil {
		return db.ShipRelease{}, fmt.Errorf("list merged prs: %w", err)
	}
	if len(mergedPRs) == 0 {
		return db.ShipRelease{}, ErrReleaseRollbackNoTarget
	}

	now := pgtype.Timestamptz{Time: deps.now(), Valid: true}
	cleanReason := strings.TrimSpace(reason)
	updated, err := s.Q.SetReleaseRolledBack(ctx, db.SetReleaseRolledBackParams{
		ID:                    releaseID,
		RolledBackCompletedAt: now,
		RolledBackBy:          requestedBy,
		RollbackReason:        pgtype.Text{String: cleanReason, Valid: cleanReason != ""},
	})
	if err != nil {
		return db.ShipRelease{}, fmt.Errorf("set rolled back: %w", err)
	}

	// Mark each still-merged PR as 'pending' revert so the UI can
	// surface a per-PR "revert needed" affordance. Best-effort — the
	// state is informational; failures don't block the rollback.
	for _, row := range mergedPRs {
		_, perErr := s.Q.UpdatePRRevertState(ctx, db.UpdatePRRevertStateParams{
			ReleaseID:      releaseID,
			PullRequestID:  row.PullRequestID,
			RevertState:    db.NullPrRevertState{PrRevertState: db.PrRevertStatePending, Valid: true},
			RevertPrNumber: pgtype.Int4{},
			RevertPrUrl:    pgtype.Text{},
			RevertError:    pgtype.Text{},
		})
		if perErr != nil {
			// Just log via insertReleaseEvent so the user has a trail
			// of which PRs failed to mark.
			_, _ = s.insertReleaseEvent(ctx, releaseID, "warning", requestedBy, map[string]any{
				"reason": "mark pr revert pending failed: " + perErr.Error(),
				"pr_id":  uuidString(row.PullRequestID),
			})
		}
	}

	// Stage flip → rolled_back. Caller pairs this with the audit event
	// + WS publication below.
	flipped, err := s.Q.UpdateReleaseStage(ctx, db.UpdateReleaseStageParams{
		ID:             releaseID,
		Stage:          db.ReleaseStageRolledBack,
		RollbackReason: pgtype.Text{String: cleanReason, Valid: cleanReason != ""},
	})
	if err == nil {
		updated = flipped
	}

	_, _ = s.insertReleaseEvent(ctx, releaseID, "release_rolled_back", requestedBy, map[string]any{
		"reason":     cleanReason,
		"pr_count":   len(mergedPRs),
		"by_user_id": uuidString(requestedBy),
	})
	if deps != nil && deps.Publisher != nil {
		deps.Publisher.PublishMergeEvent(protocol.EventReleaseRollbackInitiated, uuidString(updated.WorkspaceID), map[string]any{
			"release_id": uuidString(releaseID),
			"reason":     cleanReason,
			"by_user_id": uuidString(requestedBy),
		})
		deps.Publisher.PublishMergeEvent(protocol.EventReleaseUpdated, uuidString(updated.WorkspaceID), map[string]any{
			"release_id": uuidString(releaseID),
			"stage":      string(updated.Stage),
		})
	}

	// Channel post: the rollback instructions. Lists each merged PR
	// in reverse merge order with a deep link to the GitHub PR so the
	// user can click "Revert" on each. v2 will replace this with an
	// auto-orchestrator that creates the revert PRs itself.
	if updated.ChannelID.Valid {
		var b strings.Builder
		b.WriteString(fmt.Sprintf("🛑 Rollback initiated · %d PR%s to revert\n",
			len(mergedPRs), pluralS(len(mergedPRs))))
		if cleanReason != "" {
			b.WriteString(fmt.Sprintf("Reason: %s\n", cleanReason))
		}
		b.WriteString("\nClick GitHub's “Revert” button on each PR (newest first) to create the revert PRs:\n")
		for _, row := range mergedPRs {
			b.WriteString(fmt.Sprintf("• #%d %s — %s\n", row.PrNumber, row.Title, row.HtmlUrl))
		}
		postReleaseChannelStaging(deps, ctx, updated.ChannelID, b.String())
	}

	return updated, nil
}

// MarkReleaseDone is the explicit "fast-forward" path: when the
// 24h health-monitoring window has elapsed without a rollback, the
// finalizer goroutine OR a user click lands here to flip the release
// to its terminal "done" stage. Re-callable on rolled_back releases
// is a no-op — we only act on in_production.
func (s *Service) MarkReleaseDone(
	ctx context.Context,
	releaseID pgtype.UUID,
	deps *StagingDeps,
) (db.ShipRelease, error) {
	release, err := s.Q.GetRelease(ctx, releaseID)
	if err != nil {
		return db.ShipRelease{}, fmt.Errorf("get release: %w", err)
	}
	if release.Stage != db.ReleaseStageInProduction {
		return release, nil // idempotent no-op
	}

	return s.finalizeReleaseDone(ctx, release, releaseID, deps)
}

// MarkReleaseDoneIfTrackingIssueDone closes active releases that already
// have a done tracking issue and whose PR set is fully merged. This is a
// reconciliation path for issue-status updates that bypassed Ship's release
// stage writer.
func (s *Service) MarkReleaseDoneIfTrackingIssueDone(
	ctx context.Context,
	releaseID pgtype.UUID,
	deps *StagingDeps,
) (db.ShipRelease, bool, error) {
	release, err := s.Q.GetRelease(ctx, releaseID)
	if err != nil {
		return db.ShipRelease{}, false, fmt.Errorf("get release: %w", err)
	}
	if isTerminalReleaseStage(release.Stage) {
		return release, false, nil
	}
	if !release.IssueID.Valid {
		return release, false, nil
	}
	issue, err := s.Q.GetIssue(ctx, release.IssueID)
	if err != nil {
		return release, false, fmt.Errorf("get tracking issue: %w", err)
	}
	if issue.Status != "done" {
		return release, false, nil
	}
	if !releasePRsAllMerged(ctx, s.Q, releaseID) {
		return release, false, nil
	}
	flipped, err := s.finalizeReleaseDone(ctx, release, releaseID, deps)
	return flipped, err == nil, err
}

func (s *Service) finalizeReleaseDone(
	ctx context.Context,
	release db.ShipRelease,
	releaseID pgtype.UUID,
	deps *StagingDeps,
) (db.ShipRelease, error) {
	now := pgtype.Timestamptz{Time: deps.now(), Valid: true}
	flipped, err := s.Q.UpdateReleaseStage(ctx, db.UpdateReleaseStageParams{
		ID:     releaseID,
		Stage:  db.ReleaseStageDone,
		DoneAt: now,
	})
	if err != nil {
		return release, fmt.Errorf("update stage to done: %w", err)
	}
	if err := s.Q.DeactivateReleasePullRequests(ctx, releaseID); err != nil {
		_, _ = s.insertReleaseEvent(ctx, releaseID, "warning", pgtype.UUID{}, map[string]any{
			"reason": "release PR deactivation failed: " + err.Error(),
		})
	}
	if flipped.IssueID.Valid {
		if err := s.closeReleaseTrackingIssue(ctx, releaseID, flipped.IssueID); err != nil {
			_, _ = s.insertReleaseEvent(ctx, releaseID, "warning", pgtype.UUID{}, map[string]any{
				"reason": "issue close failed: " + err.Error(),
			})
		}
	}
	if flipped.ChannelID.Valid && deps != nil && deps.ChannelOps != nil {
		if err := deps.ChannelOps.ArchiveReleaseChannel(ctx, flipped.ChannelID); err != nil {
			_, _ = s.insertReleaseEvent(ctx, releaseID, "warning", pgtype.UUID{}, map[string]any{
				"reason": "channel archive failed: " + err.Error(),
			})
		}
	}
	_, _ = s.insertReleaseEvent(ctx, releaseID, "release_done", pgtype.UUID{}, map[string]any{
		"done_at": deps.now().Format(time.RFC3339),
	})
	if deps != nil && deps.Publisher != nil {
		deps.Publisher.PublishMergeEvent(protocol.EventReleaseUpdated, uuidString(flipped.WorkspaceID), map[string]any{
			"release_id": uuidString(releaseID),
			"stage":      string(flipped.Stage),
		})
	}
	postReleaseChannelStaging(deps, ctx, flipped.ChannelID,
		"Release closed · production deployed and all release PRs are merged")
	return flipped, nil
}

func isTerminalReleaseStage(stage db.ReleaseStage) bool {
	return stage == db.ReleaseStageDone ||
		stage == db.ReleaseStageRolledBack ||
		stage == db.ReleaseStageCancelled
}

// TryMarkReleaseDoneIfAllMerged closes an in-production release once
// every PR in the release is confirmed merged. It is safe to call from
// periodic reconciliation; MarkReleaseDone handles idempotency for
// terminal or non-in-production releases.
func (s *Service) TryMarkReleaseDoneIfAllMerged(
	ctx context.Context,
	releaseID pgtype.UUID,
	deps *StagingDeps,
) (db.ShipRelease, bool, error) {
	if !releasePRsAllMerged(ctx, s.Q, releaseID) {
		rel, err := s.Q.GetRelease(ctx, releaseID)
		return rel, false, err
	}
	rel, err := s.MarkReleaseDone(ctx, releaseID, deps)
	return rel, err == nil, err
}

// TryMarkReleaseDoneOnPRMerge is the event-driven variant of
// TryMarkReleaseDoneIfAllMerged. Unlike MarkReleaseDone (which guards
// on in_production), this accepts any non-terminal stage — covering
// repos without a staging/production CI pipeline whose releases stay
// in "merging" after the last PR lands.
func (s *Service) TryMarkReleaseDoneOnPRMerge(
	ctx context.Context,
	releaseID pgtype.UUID,
	deps *StagingDeps,
) (db.ShipRelease, bool, error) {
	release, err := s.Q.GetRelease(ctx, releaseID)
	if err != nil {
		return db.ShipRelease{}, false, fmt.Errorf("get release: %w", err)
	}
	if isTerminalReleaseStage(release.Stage) {
		return release, false, nil
	}
	if !releasePRsAllMerged(ctx, s.Q, releaseID) {
		return release, false, nil
	}
	flipped, err := s.finalizeReleaseDone(ctx, release, releaseID, deps)
	return flipped, err == nil, err
}

func releasePRsAllMerged(ctx context.Context, q *db.Queries, releaseID pgtype.UUID) bool {
	rows, err := q.ListReleasePullRequests(ctx, releaseID)
	if err != nil || len(rows) == 0 {
		return false
	}
	for _, row := range rows {
		if row.State != db.PullRequestStateMerged && row.MembershipMergeState != db.PrMergeStateMerged {
			return false
		}
	}
	return true
}

func (s *Service) closeReleaseTrackingIssue(ctx context.Context, releaseID, issueID pgtype.UUID) error {
	issue, err := s.Q.GetIssue(ctx, issueID)
	if err != nil {
		return err
	}
	if issue.Description.Valid && issue.Description.String != "" {
		checked := strings.ReplaceAll(issue.Description.String, "- [ ] #", "- [x] #")
		if checked != issue.Description.String {
			if _, err := s.Q.UpdateIssueDescription(ctx, db.UpdateIssueDescriptionParams{
				ID:          issueID,
				Description: pgtype.Text{String: checked, Valid: true},
			}); err != nil {
				return err
			}
		}
	}
	_, err = s.Q.UpdateIssueStatus(ctx, db.UpdateIssueStatusParams{
		ID:          issueID,
		Status:      "done",
		WorkspaceID: issue.WorkspaceID,
	})
	if err == nil {
		_, _ = s.insertReleaseEvent(ctx, releaseID, "issue_closed", pgtype.UUID{}, map[string]any{
			"issue_id": uuidString(issueID),
			"status":   "done",
		})
	}
	return err
}

// shortSHA truncates a SHA to its 7-char abbreviation. Returns the
// input as-is when it's already short or empty.
func shortSHA(s string) string {
	if len(s) > 7 {
		return s[:7]
	}
	return s
}
