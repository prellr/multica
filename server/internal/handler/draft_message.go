package handler

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/logger"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// Draft conversation rail (Drafts "global germination rail", Rail-1). A
// draft-level, UN-anchored conversation surface — a flat per-draft message log,
// distinct from the anchored annotation threads (draft_annotation.go). Where an
// annotation message hangs off a span via a parent annotation, a draft_message
// hangs directly off the draft: the document-wide back-and-forth, not a margin
// note.
//
// Rail-1 is HUMAN-ONLY (every message is author_type='user'), but author_type +
// author_user_id exist now so Rail-2 (Aye authoring via `multica draft say`) is
// a drop-in with no schema or handler-shape change.
//
// Authorization: every route resolves the parent draft via loadDraftForUser
// first (workspace membership + owner scoping), then operates on draft.ID. The
// draft is the only request-boundary identifier here (no message id in any
// path), so there is no extra UUID to validate beyond what loadDraftForUser
// already does.

// DraftMessageResponse is the JSON shape for one conversation-rail message.
type DraftMessageResponse struct {
	ID           string `json:"id"`
	DraftID      string `json:"draft_id"`
	WorkspaceID  string `json:"workspace_id"`
	AuthorType   string `json:"author_type"`
	AuthorUserID string `json:"author_user_id"`
	Body         string `json:"body"`
	CreatedAt    string `json:"created_at"`
}

func draftMessageToResponse(m db.DraftMessage) DraftMessageResponse {
	return DraftMessageResponse{
		ID:           uuidToString(m.ID),
		DraftID:      uuidToString(m.DraftID),
		WorkspaceID:  uuidToString(m.WorkspaceID),
		AuthorType:   m.AuthorType,
		AuthorUserID: uuidToString(m.AuthorUserID),
		Body:         m.Body,
		CreatedAt:    timestampToString(m.CreatedAt),
	}
}

// AddDraftMessageRequest is the body for POST /drafts/{id}/messages — a new
// message on the conversation rail.
type AddDraftMessageRequest struct {
	Body string `json:"body"`
}

// ListDraftMessages returns every message on a draft's conversation rail,
// oldest-first.
func (h *Handler) ListDraftMessages(w http.ResponseWriter, r *http.Request) {
	draft, ok := h.loadDraftForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}

	messages, err := h.Queries.ListDraftMessages(r.Context(), draft.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list messages")
		return
	}

	resp := make([]DraftMessageResponse, len(messages))
	for i, m := range messages {
		resp[i] = draftMessageToResponse(m)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"messages": resp,
		"total":    len(resp),
	})
}

// CreateDraftMessage appends a message to a draft's conversation rail. Rail-1
// always authors as the requesting user; the agent attribution path
// (author_type='agent') is shared with the annotation surface and reached in
// Rail-2.
func (h *Handler) CreateDraftMessage(w http.ResponseWriter, r *http.Request) {
	draft, ok := h.loadDraftForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	var req AddDraftMessageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Body == "" {
		writeError(w, http.StatusBadRequest, "message body is required")
		return
	}

	ownerUUID, ok := parseUUIDOrBadRequest(w, userID, "user id")
	if !ok {
		return
	}

	// Attribution: agent callers (Aye, Rail-2) write author_type='agent' with a
	// NULL author_user_id; human callers write 'user' with their own id. The
	// resolver is shared with the annotation surface (annotation-agnostic).
	workspaceID := uuidToString(draft.WorkspaceID)
	authorType, authorUserID, actorType, actorID := h.resolveDraftAuthor(r, userID, workspaceID, ownerUUID)

	msg, err := h.Queries.CreateDraftMessage(r.Context(), db.CreateDraftMessageParams{
		DraftID:      draft.ID,
		WorkspaceID:  draft.WorkspaceID,
		AuthorType:   authorType,
		AuthorUserID: authorUserID,
		Body:         req.Body,
	})
	if err != nil {
		slog.Warn("create draft message failed", append(logger.RequestAttrs(r), "error", err, "draft_id", uuidToString(draft.ID))...)
		writeError(w, http.StatusInternalServerError, "failed to create message")
		return
	}

	resp := draftMessageToResponse(msg)
	h.publish(protocol.EventDraftMessageCreated, workspaceID, actorType, actorID, map[string]any{
		"draft_id": uuidToString(draft.ID),
		"message":  resp,
	})
	writeJSON(w, http.StatusCreated, resp)
}
