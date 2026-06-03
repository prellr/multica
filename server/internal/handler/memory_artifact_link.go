package handler

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/logger"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// memory_artifact_link handlers — the many-to-many graph layer over
// memory_artifact. See migrations/118_memory_artifact_link.up.sql for
// the schema rationale.
//
// Routing convention:
//   GET    /api/memory/:id/links                   ListLinks (outgoing)
//   POST   /api/memory/:id/links                   CreateLink
//   DELETE /api/memory/links/:linkId               DeleteLink
//   GET    /api/memory/backlinks/:type/:id         ListBacklinks (incoming)

// ---------------------------------------------------------------------------
// Validation
// ---------------------------------------------------------------------------

// allowedLinkTargetTypes is what the link's target can point AT. Same
// entity set as anchor types, plus memory_artifact (so an artifact can
// link to another artifact — the case the single-anchor model can't
// express, e.g. "supersedes").
var allowedLinkTargetTypes = map[string]bool{
	"issue":           true,
	"project":         true,
	"agent":           true,
	"channel":         true,
	"memory_artifact": true,
}

// allowedRelationTypes — the semantic relationships the substrate
// understands. Open-string column in SQL; allowlist enforced here so
// new relation types are a one-line change in the handler with no
// migration. The set deliberately starts small — we'll add more once
// the mining workflow surfaces a relation we wish we had.
var allowedRelationTypes = map[string]bool{
	"cites":        true, // references this thing (informational)
	"supersedes":   true, // replaces a prior decision
	"contradicts":  true, // conflicts with another decision
	"implements":   true, // concrete realization of an abstract decision
	"scope":        true, // applies to this entity (broader-than-anchor)
	"discussed-in": true, // relevant conversation lived here
	"informs":      true, // informed but did not commit this artifact
}

// ---------------------------------------------------------------------------
// Wire types
// ---------------------------------------------------------------------------

type MemoryArtifactLinkResponse struct {
	ID            string `json:"id"`
	WorkspaceID   string `json:"workspace_id"`
	ArtifactID    string `json:"artifact_id"`
	TargetType    string `json:"target_type"`
	TargetID      string `json:"target_id"`
	RelationType  string `json:"relation_type"`
	CreatedByType string `json:"created_by_type"`
	CreatedByID   string `json:"created_by_id"`
	CreatedAt     string `json:"created_at"`
}

func memoryArtifactLinkToResponse(l db.MemoryArtifactLink) MemoryArtifactLinkResponse {
	return MemoryArtifactLinkResponse{
		ID:            uuidString(l.ID),
		WorkspaceID:   uuidString(l.WorkspaceID),
		ArtifactID:    uuidString(l.ArtifactID),
		TargetType:    l.TargetType,
		TargetID:      uuidString(l.TargetID),
		RelationType:  l.RelationType,
		CreatedByType: l.CreatedByType,
		CreatedByID:   uuidString(l.CreatedByID),
		CreatedAt:     timestampString(l.CreatedAt),
	}
}

// Local helpers to keep this file's response shaping consistent with
// memoryArtifactToResponse. Mirrors uuidString / timestampString
// elsewhere in the package; redeclared here for file-local clarity.
func uuidString(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	b := u.Bytes
	return formatUUID(b)
}

func timestampString(t pgtype.Timestamptz) string {
	if !t.Valid {
		return ""
	}
	return t.Time.UTC().Format("2006-01-02T15:04:05Z07:00")
}

// formatUUID renders the canonical 8-4-4-4-12 hex form. pgtype's
// String() varies across versions; this is the stable serializer
// used by the wire layer.
func formatUUID(b [16]byte) string {
	const hex = "0123456789abcdef"
	out := make([]byte, 36)
	pos := 0
	for i, v := range b {
		out[pos] = hex[v>>4]
		out[pos+1] = hex[v&0x0f]
		pos += 2
		if i == 3 || i == 5 || i == 7 || i == 9 {
			out[pos] = '-'
			pos++
		}
	}
	return string(out)
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

type createMemoryArtifactLinkRequest struct {
	TargetType   string `json:"target_type"`
	TargetID     string `json:"target_id"`
	RelationType string `json:"relation_type"`
}

// CreateMemoryArtifactLink — POST /api/memory/{id}/links.
//
// Idempotent: re-creating an existing link returns the canonical row
// with 200 instead of 201, so callers (CLI, miner follow-ups, MCP
// tools) can safely upsert without first-checking existence.
func (h *Handler) CreateMemoryArtifactLink(w http.ResponseWriter, r *http.Request) {
	artifactIDRaw := chi.URLParam(r, "id")
	workspaceID := h.resolveWorkspaceID(r)
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	artifactUUID, ok := parseUUIDOrBadRequest(w, artifactIDRaw, "artifact id")
	if !ok {
		return
	}
	userIDStr, ok := requireUserID(w, r)
	if !ok {
		return
	}
	userUUID, ok := parseUUIDOrBadRequest(w, userIDStr, "user_id")
	if !ok {
		return
	}

	// Verify the artifact exists in this workspace before accepting a
	// link from it. Skipping this check would let a member create
	// links anchored to artifacts in other workspaces.
	if _, err := h.Queries.GetMemoryArtifact(r.Context(), db.GetMemoryArtifactParams{
		ID: artifactUUID, WorkspaceID: wsUUID,
	}); err != nil {
		writeError(w, http.StatusNotFound, "memory artifact not found")
		return
	}

	var req createMemoryArtifactLinkRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if !allowedLinkTargetTypes[req.TargetType] {
		writeError(w, http.StatusBadRequest, "target_type must be one of: issue, project, agent, channel, memory_artifact")
		return
	}
	if !allowedRelationTypes[req.RelationType] {
		writeError(w, http.StatusBadRequest, "relation_type must be one of: cites, supersedes, contradicts, implements, scope, discussed-in, informs")
		return
	}
	// Identifier-form resolution for issue targets mirrors what
	// memoryArtifact create + by-anchor already do for the primary
	// anchor — accept "ROA-427" alongside the raw UUID.
	var targetUUID pgtype.UUID
	resolved := false
	if req.TargetType == "issue" {
		if issue, ok := h.resolveIssueByIdentifier(r.Context(), req.TargetID, workspaceID); ok {
			targetUUID = issue.ID
			resolved = true
		}
	}
	if !resolved {
		t, ok := parseUUIDOrBadRequest(w, req.TargetID, "target_id")
		if !ok {
			return
		}
		targetUUID = t
	}
	// Self-link guard. An artifact linking to itself is always a
	// mistake — there's no useful "this decision supersedes itself."
	if req.TargetType == "memory_artifact" && artifactUUID == targetUUID {
		writeError(w, http.StatusBadRequest, "an artifact cannot link to itself")
		return
	}

	row, err := h.Queries.CreateMemoryArtifactLink(r.Context(), db.CreateMemoryArtifactLinkParams{
		WorkspaceID:   wsUUID,
		ArtifactID:    artifactUUID,
		TargetType:    req.TargetType,
		TargetID:      targetUUID,
		RelationType:  req.RelationType,
		CreatedByType: "member",
		CreatedByID:   userUUID,
	})
	status := http.StatusCreated
	if errors.Is(err, pgx.ErrNoRows) {
		// ON CONFLICT DO NOTHING returned zero rows = link already
		// existed. Fetch it and return 200 so the caller still gets
		// the canonical row.
		existing, getErr := h.Queries.GetMemoryArtifactLink(r.Context(), db.GetMemoryArtifactLinkParams{
			ArtifactID:   artifactUUID,
			TargetType:   req.TargetType,
			TargetID:     targetUUID,
			RelationType: req.RelationType,
		})
		if getErr != nil {
			slog.Warn("CreateMemoryArtifactLink follow-up lookup failed",
				append(logger.RequestAttrs(r), "error", getErr)...)
			writeError(w, http.StatusInternalServerError, "failed to fetch existing link")
			return
		}
		row = existing
		status = http.StatusOK
	} else if err != nil {
		slog.Warn("CreateMemoryArtifactLink failed",
			append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to create link")
		return
	}
	writeJSON(w, status, memoryArtifactLinkToResponse(row))
}

// ListMemoryArtifactLinks — GET /api/memory/{id}/links. Outgoing
// edges. Powers the detail page's Links section.
func (h *Handler) ListMemoryArtifactLinks(w http.ResponseWriter, r *http.Request) {
	artifactIDRaw := chi.URLParam(r, "id")
	workspaceID := h.resolveWorkspaceID(r)
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	artifactUUID, ok := parseUUIDOrBadRequest(w, artifactIDRaw, "artifact id")
	if !ok {
		return
	}
	rows, err := h.Queries.ListMemoryArtifactLinks(r.Context(), db.ListMemoryArtifactLinksParams{
		WorkspaceID: wsUUID,
		ArtifactID:  artifactUUID,
	})
	if err != nil {
		slog.Warn("ListMemoryArtifactLinks failed",
			append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to list links")
		return
	}
	out := make([]MemoryArtifactLinkResponse, len(rows))
	for i, r := range rows {
		out[i] = memoryArtifactLinkToResponse(r)
	}
	writeJSON(w, http.StatusOK, map[string]any{"links": out})
}

// ListMemoryArtifactBacklinks — GET /api/memory/backlinks/{type}/{id}.
// Incoming edges — "every artifact that links to this entity." Powers
// the future "memory for issue X" surface and the runtime-injection
// follow-up that traverses links one hop.
func (h *Handler) ListMemoryArtifactBacklinks(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	targetType := chi.URLParam(r, "targetType")
	if !allowedLinkTargetTypes[targetType] {
		writeError(w, http.StatusBadRequest, "target_type must be one of: issue, project, agent, channel, memory_artifact")
		return
	}
	rawTargetID := chi.URLParam(r, "targetId")
	var targetUUID pgtype.UUID
	resolved := false
	if targetType == "issue" {
		if issue, ok := h.resolveIssueByIdentifier(r.Context(), rawTargetID, workspaceID); ok {
			targetUUID = issue.ID
			resolved = true
		}
	}
	if !resolved {
		t, ok := parseUUIDOrBadRequest(w, rawTargetID, "target_id")
		if !ok {
			return
		}
		targetUUID = t
	}
	limit := int32(defaultMemoryLimit)
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			if n > maxMemoryListLimit {
				n = maxMemoryListLimit
			}
			limit = int32(n)
		}
	}
	rows, err := h.Queries.ListMemoryArtifactBacklinks(r.Context(), db.ListMemoryArtifactBacklinksParams{
		WorkspaceID: wsUUID,
		TargetType:  targetType,
		TargetID:    targetUUID,
		Limit:       limit,
	})
	if err != nil {
		slog.Warn("ListMemoryArtifactBacklinks failed",
			append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to list backlinks")
		return
	}
	out := make([]MemoryArtifactLinkResponse, len(rows))
	for i, r := range rows {
		out[i] = memoryArtifactLinkToResponse(r)
	}
	writeJSON(w, http.StatusOK, map[string]any{"links": out})
}

// DeleteMemoryArtifactLink — DELETE /api/memory/links/{linkId}.
// Hard delete. Workspace-scoping is enforced via the GetByID guard
// before the destructive op so a leaked link id from another
// workspace can't trigger a delete here.
func (h *Handler) DeleteMemoryArtifactLink(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	linkIDRaw := chi.URLParam(r, "linkId")
	linkUUID, ok := parseUUIDOrBadRequest(w, linkIDRaw, "link id")
	if !ok {
		return
	}
	if _, err := h.Queries.GetMemoryArtifactLinkByID(r.Context(), db.GetMemoryArtifactLinkByIDParams{
		ID: linkUUID, WorkspaceID: wsUUID,
	}); err != nil {
		writeError(w, http.StatusNotFound, "link not found")
		return
	}
	if err := h.Queries.DeleteMemoryArtifactLink(r.Context(), db.DeleteMemoryArtifactLinkParams{
		ID: linkUUID, WorkspaceID: wsUUID,
	}); err != nil {
		slog.Warn("DeleteMemoryArtifactLink failed",
			append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to delete link")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
