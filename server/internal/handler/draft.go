package handler

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/logger"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// Drafts are a standalone, workspace-scoped, owner-scoped entity: a living
// markdown document a user co-authors with an agent. Slice 0 ships only the
// single-user CRUD surface (create / list / open / edit title+body / delete
// with autosave). Later slices layer on the annotation surface, the agent,
// Yjs co-editing, and graduation — the owner_user_id column and the open
// `status` enum are the forward-compat seams for that work. See migration 122
// and draft.sql for the query-layer scoping.

// draftDefaultStatus is the canonical slice-0 status. The column default is
// 'draft' too; this constant is the application-layer fallback the status
// switch downgrades unknown values to (enum-drift rule).
const draftDefaultStatus = "draft"

// normalizeDraftStatus maps a caller-supplied status onto a value safe to
// write. `status` is an open enum: slice 0 only writes 'draft', but future
// slices add values like 'accepted'. The switch has a `default` branch per the
// repo's enum-drift rules — an empty / unknown value downgrades to the
// canonical 'draft' rather than 400ing, so an older server staying forward-
// compatible never rejects a value a newer client legitimately sends.
func normalizeDraftStatus(s string) string {
	switch s {
	case "draft", "accepted":
		return s
	default:
		return draftDefaultStatus
	}
}

// DraftResponse is the JSON shape returned for a draft.
type DraftResponse struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	OwnerUserID string `json:"owner_user_id"`
	Title       string `json:"title"`
	Body        string `json:"body"`
	Status      string `json:"status"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

func draftToResponse(d db.Draft) DraftResponse {
	return DraftResponse{
		ID:          uuidToString(d.ID),
		WorkspaceID: uuidToString(d.WorkspaceID),
		OwnerUserID: uuidToString(d.OwnerUserID),
		Title:       d.Title,
		Body:        d.Body,
		Status:      d.Status,
		CreatedAt:   timestampToString(d.CreatedAt),
		UpdatedAt:   timestampToString(d.UpdatedAt),
	}
}

// loadDraftForUser fetches a draft by UUID, enforcing workspace membership
// (the route is mounted behind RequireWorkspaceMember) and owner scoping.
// Per CLAUDE.md's "Backend Handler UUID Parsing Convention" this is the single
// resolution point: callers MUST use the returned db.Draft.ID for any
// subsequent write rather than re-parsing the raw URL string.
func (h *Handler) loadDraftForUser(w http.ResponseWriter, r *http.Request, draftID string) (db.Draft, bool) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return db.Draft{}, false
	}
	workspaceID := h.resolveWorkspaceID(r)
	if workspaceID == "" {
		writeError(w, http.StatusBadRequest, "workspace_id is required")
		return db.Draft{}, false
	}
	draftUUID, err := util.ParseUUID(draftID)
	if err != nil {
		writeError(w, http.StatusNotFound, "draft not found")
		return db.Draft{}, false
	}
	wsUUID, err := util.ParseUUID(workspaceID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid workspace_id")
		return db.Draft{}, false
	}
	ownerUUID, err := util.ParseUUID(userID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid user id")
		return db.Draft{}, false
	}
	draft, err := h.Queries.GetDraft(r.Context(), db.GetDraftParams{
		ID:          draftUUID,
		WorkspaceID: wsUUID,
		OwnerUserID: ownerUUID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "draft not found")
		return db.Draft{}, false
	}
	return draft, true
}

// CreateDraftRequest is the body for POST /drafts. All fields are optional —
// a "blank draft" create (just clicking New draft) is a valid empty body.
type CreateDraftRequest struct {
	Title  *string `json:"title"`
	Body   *string `json:"body"`
	Status *string `json:"status"`
}

// UpdateDraftRequest carries optional fields. A missing field is "no change"
// (the SQL uses COALESCE/narg), so a body-only autosave PATCH leaves title and
// status untouched.
type UpdateDraftRequest struct {
	Title  *string `json:"title"`
	Body   *string `json:"body"`
	Status *string `json:"status"`
}

func (h *Handler) ListDrafts(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := h.resolveWorkspaceID(r)
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	ownerUUID, ok := parseUUIDOrBadRequest(w, userID, "user id")
	if !ok {
		return
	}

	drafts, err := h.Queries.ListDraftsForUser(r.Context(), db.ListDraftsForUserParams{
		WorkspaceID: wsUUID,
		OwnerUserID: ownerUUID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list drafts")
		return
	}

	resp := make([]DraftResponse, len(drafts))
	for i, d := range drafts {
		resp[i] = draftToResponse(d)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"drafts": resp,
		"total":  len(resp),
	})
}

func (h *Handler) GetDraft(w http.ResponseWriter, r *http.Request) {
	draft, ok := h.loadDraftForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, draftToResponse(draft))
}

func (h *Handler) CreateDraft(w http.ResponseWriter, r *http.Request) {
	var req CreateDraftRequest
	// An empty body is valid (blank draft = just clicking "New draft"). An
	// empty body decodes to io.EOF, which we tolerate; only a malformed JSON
	// payload is a 400.
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := h.resolveWorkspaceID(r)
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	ownerUUID, ok := parseUUIDOrBadRequest(w, userID, "user id")
	if !ok {
		return
	}

	title := ""
	if req.Title != nil {
		title = *req.Title
	}
	body := ""
	if req.Body != nil {
		body = *req.Body
	}
	status := draftDefaultStatus
	if req.Status != nil {
		status = normalizeDraftStatus(*req.Status)
	}

	draft, err := h.Queries.CreateDraft(r.Context(), db.CreateDraftParams{
		WorkspaceID: wsUUID,
		OwnerUserID: ownerUUID,
		Title:       title,
		Body:        body,
		Status:      status,
	})
	if err != nil {
		slog.Warn("create draft failed", append(logger.RequestAttrs(r), "error", err, "workspace_id", workspaceID)...)
		writeError(w, http.StatusInternalServerError, "failed to create draft")
		return
	}

	resp := draftToResponse(draft)
	h.publish(protocol.EventDraftCreated, workspaceID, "member", userID, map[string]any{"draft": resp})
	writeJSON(w, http.StatusCreated, resp)
}

func (h *Handler) UpdateDraft(w http.ResponseWriter, r *http.Request) {
	draft, ok := h.loadDraftForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}

	var req UpdateDraftRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	workspaceID := uuidToString(draft.WorkspaceID)
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	// COALESCE/narg semantics: a non-Valid pgtype.Text means "leave this
	// column unchanged", so a body-only autosave PATCH never clobbers title or
	// status. Mirrors UpdateTask / UpdateProject.
	var title pgtype.Text
	if req.Title != nil {
		title = pgtype.Text{String: *req.Title, Valid: true}
	}
	var body pgtype.Text
	if req.Body != nil {
		body = pgtype.Text{String: *req.Body, Valid: true}
	}
	var status pgtype.Text
	if req.Status != nil {
		status = pgtype.Text{String: normalizeDraftStatus(*req.Status), Valid: true}
	}

	updated, err := h.Queries.UpdateDraft(r.Context(), db.UpdateDraftParams{
		ID:          draft.ID,
		WorkspaceID: draft.WorkspaceID,
		OwnerUserID: draft.OwnerUserID,
		Title:       title,
		Body:        body,
		Status:      status,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update draft")
		return
	}

	resp := draftToResponse(updated)
	h.publish(protocol.EventDraftUpdated, workspaceID, "member", userID, map[string]any{"draft": resp})
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) DeleteDraft(w http.ResponseWriter, r *http.Request) {
	draft, ok := h.loadDraftForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	workspaceID := uuidToString(draft.WorkspaceID)
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	if err := h.Queries.DeleteDraft(r.Context(), db.DeleteDraftParams{
		ID:          draft.ID,
		WorkspaceID: draft.WorkspaceID,
		OwnerUserID: draft.OwnerUserID,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete draft")
		return
	}

	h.publish(protocol.EventDraftDeleted, workspaceID, "member", userID, map[string]any{"draft_id": uuidToString(draft.ID)})
	w.WriteHeader(http.StatusNoContent)
}

// DraftTurnResponse is the JSON shape returned by the Send endpoint. It echoes
// the enqueued task so the Draft view can subscribe to its task:message /
// task:progress stream and show the "Aye is working" indicator.
type DraftTurnResponse struct {
	TaskID  string `json:"task_id"`
	DraftID string `json:"draft_id"`
	AgentID string `json:"agent_id"`
	Status  string `json:"status"`
}

// StartDraftTurn enqueues a draft-turn task for Aye (Drafts slice 2). The human
// clicked Send: snapshot the open-annotation queue, then queue one conservative
// turn. Aye reads the live draft + queue and responds via the `multica draft *`
// surface; her replies/suggestions stream back as draft_annotation:* events
// during the run, and a draft:turn_completed event fires when she's done.
//
// Per the UUID-parsing convention the draft is resolved via loadDraftForUser
// (workspace membership + owner scoping) and all subsequent work uses draft.ID;
// the request carries no pure-UUID body so there's nothing else to validate.
// Cross-owner / bad-UUID requests 404 at loadDraftForUser before any enqueue.
func (h *Handler) StartDraftTurn(w http.ResponseWriter, r *http.Request) {
	draft, ok := h.loadDraftForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	// Snapshot the currently-open annotation ids. These are provenance only —
	// Aye re-lists the live queue at run time — so a failure to load them is
	// non-fatal: the turn still runs, just without the Send-time snapshot.
	var openIDs []string
	annotations, err := h.Queries.ListDraftAnnotations(r.Context(), draft.ID)
	if err == nil {
		for _, a := range annotations {
			if a.State == draftAnnotationDefaultState { // "open"
				openIDs = append(openIDs, uuidToString(a.ID))
			}
		}
	} else {
		slog.Warn("start draft turn: failed to snapshot open annotations",
			append(logger.RequestAttrs(r), "error", err, "draft_id", uuidToString(draft.ID))...)
	}

	// Aye's id is deterministic from the workspace id (seeded at workspace
	// create; see aye.go). Verify the row exists before enqueueing so a
	// workspace that somehow predates the seed gets a clear 409 instead of a
	// task that never claims.
	ayeID := AyeAgentID(draft.WorkspaceID)
	if _, err := h.Queries.GetAgent(r.Context(), ayeID); err != nil {
		writeError(w, http.StatusConflict, "draft agent is not available in this workspace")
		return
	}

	task, err := h.TaskService.EnqueueDraftTurn(r.Context(), service.EnqueueDraftTurnParams{
		WorkspaceID:       draft.WorkspaceID,
		AgentID:           ayeID,
		DraftID:           draft.ID,
		RequesterID:       userID,
		OpenAnnotationIDs: openIDs,
		DocRev:            timestampToString(draft.UpdatedAt),
	})
	if err != nil {
		// The most common cause is "agent has no runtime" — the owner hasn't
		// connected a daemon yet. Surface it as a 409 so the UI can prompt
		// them to start one rather than reading it as a server fault.
		slog.Warn("start draft turn failed",
			append(logger.RequestAttrs(r), "error", err, "draft_id", uuidToString(draft.ID))...)
		writeError(w, http.StatusConflict, "could not start a turn: "+err.Error())
		return
	}

	writeJSON(w, http.StatusAccepted, DraftTurnResponse{
		TaskID:  uuidToString(task.ID),
		DraftID: uuidToString(draft.ID),
		AgentID: uuidToString(ayeID),
		Status:  task.Status,
	})
}
