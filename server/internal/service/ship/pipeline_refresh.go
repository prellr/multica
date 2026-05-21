// Package ship — PR8 of the Ship Hub rebuild: pipeline refresh + apply
// orchestration.
//
// pipeline_diff.go classifies a (current, proposed) config pair; this
// file drives the full refresh cycle that produces that pair and acts
// on the classification. It is the single converging point for all
// three PR8 trigger paths (on-demand button, push webhook on a
// workflow-file change, daily scheduled poll).
//
// The cycle:
//  1. Find the project's primary github_repo resource (shared with the
//     seed driver via Service.findProjectRepo).
//  2. Run IntrospectPipeline → the PROPOSED config.
//  3. Diff the proposed config against the project's CURRENT effective
//     config (EffectivePipelineConfig handles NULL-config projects).
//  4. Act on the classification:
//     - none        → stamp introspected_at, clear any stale proposal.
//     - additive    → auto-apply: write proposed to pipeline_config,
//     stamp introspected_at, clear any stale proposal.
//     - destructive → park proposed in pipeline_config_proposed; do
//     NOT touch pipeline_config. Operator must
//     Accept / Reject.
//
// Accept (AcceptPipelineProposal) swaps the proposal into the live
// config — but only after checking that no in-flight release sits at a
// stage the proposal would drop or rename. Reject
// (RejectPipelineProposal) clears the proposal and leaves the live
// config alone.
package ship

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// RefreshOutcomeKind is the closed enum of what a refresh did.
type RefreshOutcomeKind string

const (
	// RefreshUnchanged — introspection produced no diff. introspected_at
	// was stamped; nothing else changed.
	RefreshUnchanged RefreshOutcomeKind = "unchanged"
	// RefreshAppliedAdditive — an additive diff was auto-applied straight
	// to pipeline_config.
	RefreshAppliedAdditive RefreshOutcomeKind = "applied_additive"
	// RefreshProposedDestructive — a destructive diff was parked as a
	// pending proposal; the operator must Accept or Reject.
	RefreshProposedDestructive RefreshOutcomeKind = "proposed_destructive"
	// RefreshSkippedNoRepo — the project has no github_repo resource, so
	// there's nothing to introspect.
	RefreshSkippedNoRepo RefreshOutcomeKind = "skipped_no_repo"
)

// PipelineRefreshOutcome is the result of one RefreshProjectPipeline
// call. The handler returns it as the refresh endpoint's JSON body so
// the frontend can render the diff banner.
type PipelineRefreshOutcome struct {
	ProjectID string             `json:"project_id"`
	Kind      RefreshOutcomeKind `json:"kind"`
	// Diff is populated for applied_additive and proposed_destructive so
	// the UI can show "what changed". Nil for unchanged / skipped.
	Diff *PipelineDiff `json:"diff,omitempty"`
	// Shape is the proposed config's shape, populated whenever an
	// introspection actually ran (everything except skipped_no_repo).
	Shape string `json:"shape,omitempty"`
}

// ProposalBlockedError is returned by AcceptPipelineProposal when one or
// more in-flight (non-terminal) releases sit at a release stage whose
// name matches a pipeline stage id the proposal drops or renames.
// Accepting would leave those releases pointing at a stage that no
// longer exists, so the operator must resolve them first.
type ProposalBlockedError struct {
	// AffectedReleaseIDs are the in-flight releases blocking the accept.
	AffectedReleaseIDs []string
	// BlockingStageIDs are the dropped/renamed pipeline stage ids those
	// releases are currently sitting at.
	BlockingStageIDs []string
}

func (e *ProposalBlockedError) Error() string {
	return fmt.Sprintf(
		"pipeline proposal blocked: %d in-flight release(s) at to-be-removed stage(s) %v",
		len(e.AffectedReleaseIDs), e.BlockingStageIDs,
	)
}

// ErrNoPipelineProposal is returned by Accept/Reject when the project
// has no pending proposal to act on.
var ErrNoPipelineProposal = errors.New("pipeline: no pending proposal")

// RefreshProjectPipeline runs the introspector against the project's
// repo, diffs the result against the project's current effective
// config, and applies the PR8 classification rules. It is the shared
// entry point for the on-demand handler, the push webhook, and the
// daily scheduled poller.
//
// Returns an error only for a real failure the caller should surface
// (GH call failed, DB write failed). A project with no repo is NOT an
// error — it returns a skipped_no_repo outcome.
func (s *Service) RefreshProjectPipeline(ctx context.Context, project db.Project) (PipelineRefreshOutcome, error) {
	outcome := PipelineRefreshOutcome{ProjectID: uuidString(project.ID)}

	if s.Github == nil {
		return outcome, fmt.Errorf("ship: github client not configured")
	}

	owner, repo, _, foundRepo, err := s.findProjectRepo(ctx, project)
	if err != nil {
		return outcome, fmt.Errorf("resolve project repo: %w", err)
	}
	if !foundRepo {
		outcome.Kind = RefreshSkippedNoRepo
		return outcome, nil
	}

	proposed, err := IntrospectPipeline(ctx, s.Github, owner, repo)
	if err != nil {
		return outcome, fmt.Errorf("introspect %s/%s: %w", owner, repo, err)
	}
	outcome.Shape = proposed.Shape

	// Current effective config — EffectivePipelineConfig synthesizes from
	// the legacy pipeline_kind enum for projects whose pipeline_config is
	// still NULL, so the very first refresh diffs against the same shape
	// the kanban is currently rendering.
	current, err := EffectivePipelineConfig(project)
	if err != nil {
		// Corrupt JSONB — EffectivePipelineConfig already fell back to a
		// legacy-enum default and returned it alongside the error; log and
		// continue diffing against that default.
		slog.Warn("ship: pipeline refresh — current config unreadable, using fallback",
			"project_id", uuidString(project.ID), "error", err)
	}

	diff := DiffPipelineConfig(current, proposed)

	switch diff.Kind {
	case DiffNone:
		// Stamp introspected_at and drop any previously-parked proposal:
		// the repo now matches the live config, so a stale proposal would
		// be misleading.
		if _, err := s.Q.StampProjectPipelineIntrospectedAt(ctx, project.ID); err != nil {
			return outcome, fmt.Errorf("stamp introspected_at: %w", err)
		}
		if project.PipelineConfigProposedAt.Valid {
			if _, err := s.Q.ClearProjectPipelineProposal(ctx, project.ID); err != nil {
				return outcome, fmt.Errorf("clear stale proposal: %w", err)
			}
		}
		outcome.Kind = RefreshUnchanged
		return outcome, nil

	case DiffAdditive:
		// Auto-apply: the operator's existing kanban columns all keep
		// their meaning, so writing the new config straight in is safe.
		raw, err := proposed.ToJSON()
		if err != nil {
			return outcome, fmt.Errorf("serialize proposed config: %w", err)
		}
		if _, err := s.Q.UpdateProjectPipelineConfig(ctx, db.UpdateProjectPipelineConfigParams{
			ID:             project.ID,
			PipelineConfig: raw,
		}); err != nil {
			return outcome, fmt.Errorf("apply additive config: %w", err)
		}
		if project.PipelineConfigProposedAt.Valid {
			if _, err := s.Q.ClearProjectPipelineProposal(ctx, project.ID); err != nil {
				return outcome, fmt.Errorf("clear stale proposal: %w", err)
			}
		}
		outcome.Kind = RefreshAppliedAdditive
		outcome.Diff = &diff
		slog.Info("ship: pipeline refresh — additive change auto-applied",
			"project_id", uuidString(project.ID), "shape", proposed.Shape)
		return outcome, nil

	default: // DiffDestructive
		raw, err := proposed.ToJSON()
		if err != nil {
			return outcome, fmt.Errorf("serialize proposed config: %w", err)
		}
		if _, err := s.Q.SetProjectPipelineProposal(ctx, db.SetProjectPipelineProposalParams{
			ID:                     project.ID,
			PipelineConfigProposed: raw,
		}); err != nil {
			return outcome, fmt.Errorf("park destructive proposal: %w", err)
		}
		outcome.Kind = RefreshProposedDestructive
		outcome.Diff = &diff
		slog.Info("ship: pipeline refresh — destructive change parked for review",
			"project_id", uuidString(project.ID), "shape", proposed.Shape)
		return outcome, nil
	}
}

// AcceptPipelineProposal swaps a project's pending proposal into the
// live pipeline_config — the operator's "yes, apply this destructive
// change" decision.
//
// Before swapping it runs the in-flight-release safety check: if any
// non-terminal release for the project sits at a release stage whose
// name matches a pipeline stage id the proposal drops or renames,
// acceptance is blocked with a ProposalBlockedError listing the
// affected releases. The operator must finish or roll back those
// releases first.
//
// Returns ErrNoPipelineProposal if there's nothing to accept.
func (s *Service) AcceptPipelineProposal(ctx context.Context, project db.Project) (db.Project, *PipelineDiff, error) {
	if !project.PipelineConfigProposedAt.Valid || len(project.PipelineConfigProposed) == 0 {
		return db.Project{}, nil, ErrNoPipelineProposal
	}

	proposed, err := FromJSON(project.PipelineConfigProposed)
	if err != nil {
		return db.Project{}, nil, fmt.Errorf("parse parked proposal: %w", err)
	}
	current, _ := EffectivePipelineConfig(project)
	diff := DiffPipelineConfig(current, proposed)

	// Stage ids the proposal removes outright OR renames — releases
	// sitting at either are at risk, because the stage they reference
	// either vanishes or changes meaning.
	atRisk := map[string]bool{}
	for _, s := range diff.RemovedStages {
		atRisk[s.ID] = true
	}
	for _, r := range diff.RenamedStages {
		atRisk[r.ID] = true
	}

	if len(atRisk) > 0 {
		// In-flight (non-terminal) releases for this project. Their
		// `stage` is the release-lifecycle enum (assembling, merging,
		// in_staging, …) — we match those strings against the at-risk
		// pipeline stage ids. Overlapping names (in_staging, in_production,
		// done) are exactly the stages the destructive-change safety check
		// cares about.
		releases, err := s.Q.ListReleasesByProject(ctx, db.ListReleasesByProjectParams{
			ProjectID:       project.ID,
			IncludeTerminal: false,
		})
		if err != nil {
			return db.Project{}, nil, fmt.Errorf("list in-flight releases: %w", err)
		}
		blocked := &ProposalBlockedError{}
		blockingStages := map[string]bool{}
		for _, rel := range releases {
			stageName := string(rel.Stage)
			if atRisk[stageName] {
				blocked.AffectedReleaseIDs = append(blocked.AffectedReleaseIDs, uuidString(rel.ID))
				blockingStages[stageName] = true
			}
		}
		if len(blocked.AffectedReleaseIDs) > 0 {
			for stage := range blockingStages {
				blocked.BlockingStageIDs = append(blocked.BlockingStageIDs, stage)
			}
			return db.Project{}, nil, blocked
		}
	}

	updated, err := s.Q.ApplyProjectPipelineProposal(ctx, project.ID)
	if err != nil {
		return db.Project{}, nil, fmt.Errorf("apply pipeline proposal: %w", err)
	}
	slog.Info("ship: pipeline proposal accepted",
		"project_id", uuidString(project.ID), "shape", proposed.Shape)
	return updated, &diff, nil
}

// RejectPipelineProposal clears a project's pending proposal without
// touching pipeline_config — the operator's "no, keep the current
// shape" decision. The next introspection will re-diff and (because the
// repo still differs) re-propose, so a reject is not permanent; it's a
// "not now".
//
// Returns ErrNoPipelineProposal if there's nothing to reject.
func (s *Service) RejectPipelineProposal(ctx context.Context, project db.Project) (db.Project, error) {
	if !project.PipelineConfigProposedAt.Valid {
		return db.Project{}, ErrNoPipelineProposal
	}
	updated, err := s.Q.ClearProjectPipelineProposal(ctx, project.ID)
	if err != nil {
		return db.Project{}, fmt.Errorf("clear pipeline proposal: %w", err)
	}
	slog.Info("ship: pipeline proposal rejected", "project_id", uuidString(project.ID))
	return updated, nil
}
