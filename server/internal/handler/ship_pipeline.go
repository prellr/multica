package handler

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/multica-ai/multica/server/internal/service/ship"
	gh "github.com/multica-ai/multica/server/pkg/github"
)

// ship_pipeline.go — PR8 of the Ship Hub rebuild: the on-demand
// pipeline-refresh + proposal accept/reject endpoints.
//
// PR8's three trigger paths (on-demand button, push webhook, daily
// poll) all converge on ship.Service.RefreshProjectPipeline. This file
// is the operator-facing on-demand path:
//
//   POST /api/projects/{id}/pipeline/refresh           — re-introspect now
//   POST /api/projects/{id}/pipeline/proposal/accept   — apply a parked proposal
//   POST /api/projects/{id}/pipeline/proposal/reject   — discard a parked proposal
//
// All three use loadShipProject for auth (requireShipHubEnabled +
// workspace scoping + UUID validation) and shipServiceFromWorkspace for
// the GH-credentialed service, mirroring SyncProjectPullRequests.

// RefreshProjectPipeline re-runs the pipeline introspector against the
// project's repo synchronously and returns the diff outcome so the UI
// can render "what changed". Additive changes are auto-applied;
// destructive changes are parked as a pending proposal.
//
// POST /api/projects/{id}/pipeline/refresh
func (h *Handler) RefreshProjectPipeline(w http.ResponseWriter, r *http.Request) {
	project, _, ws, ok := h.loadShipProject(w, r)
	if !ok {
		return
	}
	svc, ok := h.shipServiceFromWorkspace(w, r, ws, true)
	if !ok {
		return
	}
	outcome, err := svc.RefreshProjectPipeline(r.Context(), project)
	if err != nil {
		// Translate the typed GH errors so the UI can render something
		// actionable, matching SyncProjectPullRequests.
		if errors.Is(err, gh.ErrUnauthorized) {
			writeError(w, http.StatusUnauthorized, "github: invalid or revoked token")
			return
		}
		if errors.Is(err, gh.ErrRateLimited) {
			writeError(w, http.StatusTooManyRequests, "github: rate limit hit, retry shortly")
			return
		}
		writeError(w, http.StatusInternalServerError, "pipeline refresh failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, outcome)
}

// AcceptPipelineProposal applies a project's pending pipeline proposal
// to the live config. Blocked (409) when an in-flight release sits at a
// stage the proposal would drop or rename.
//
// POST /api/projects/{id}/pipeline/proposal/accept
func (h *Handler) AcceptPipelineProposal(w http.ResponseWriter, r *http.Request) {
	project, _, _, ok := h.loadShipProject(w, r)
	if !ok {
		return
	}
	updated, diff, err := h.shipServiceForProposal(w, r).AcceptPipelineProposal(r.Context(), project)
	if err != nil {
		if errors.Is(err, ship.ErrNoPipelineProposal) {
			writeError(w, http.StatusNotFound, "no pending pipeline proposal")
			return
		}
		var blocked *ship.ProposalBlockedError
		if errors.As(err, &blocked) {
			// 409 Conflict — the operator must resolve the listed in-flight
			// releases before this destructive change can apply.
			writeJSON(w, http.StatusConflict, map[string]any{
				"error":                "pipeline proposal blocked by in-flight releases",
				"affected_release_ids": blocked.AffectedReleaseIDs,
				"blocking_stage_ids":   blocked.BlockingStageIDs,
			})
			return
		}
		writeError(w, http.StatusInternalServerError, "accept pipeline proposal failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"project_id":      uuidToString(updated.ID),
		"applied":         true,
		"pipeline_config": jsonRawOrNull(updated.PipelineConfig),
		"diff":            diff,
	})
}

// RejectPipelineProposal discards a project's pending pipeline proposal
// and leaves the live config untouched.
//
// POST /api/projects/{id}/pipeline/proposal/reject
func (h *Handler) RejectPipelineProposal(w http.ResponseWriter, r *http.Request) {
	project, _, _, ok := h.loadShipProject(w, r)
	if !ok {
		return
	}
	updated, err := h.shipServiceForProposal(w, r).RejectPipelineProposal(r.Context(), project)
	if err != nil {
		if errors.Is(err, ship.ErrNoPipelineProposal) {
			writeError(w, http.StatusNotFound, "no pending pipeline proposal")
			return
		}
		writeError(w, http.StatusInternalServerError, "reject pipeline proposal failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"project_id": uuidToString(updated.ID),
		"rejected":   true,
	})
}

// shipServiceForProposal builds a ship.Service for the accept/reject
// paths. Unlike refresh, accept/reject never call GitHub — they only
// move JSONB between project columns — so a GH client is not required.
// We still construct the service through the workspace's queries.
func (h *Handler) shipServiceForProposal(w http.ResponseWriter, r *http.Request) *ship.Service {
	return &ship.Service{Q: h.Queries}
}

// jsonRawOrNull returns the raw JSONB bytes as a json.RawMessage so the
// encoder emits the config verbatim, or nil so it emits `null` when the
// column is empty.
func jsonRawOrNull(raw []byte) any {
	if len(raw) == 0 {
		return nil
	}
	return json.RawMessage(raw)
}
