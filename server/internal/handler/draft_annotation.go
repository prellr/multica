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
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// Draft annotation layer (Drafts slice 1). A non-destructive overlay on a
// draft's markdown body: a user selects a span of text and attaches a typed,
// anchored, threaded annotation. The body stays clean markdown — the
// highlight/pin renders from the persisted anchor, never inlined. Anchors
// re-anchor to their text as the body is edited (frontend engine); when the
// range moves the new quote/context/pos_hint is PATCHed back here.
//
// Slice 1 is HUMAN-ONLY (every annotation/message is author_type='user'), but
// author_type + author_user_id exist now so slice 2 (agent authoring/replies)
// is a drop-in with no schema or handler-shape change.
//
// Authorization: every route resolves the parent draft via loadDraftForUser
// first (workspace membership + owner scoping), then operates on draft.ID. Per
// the repo's UUID-parsing convention the annotation id is a pure-UUID request
// boundary, validated with parseUUIDOrBadRequest; all writes use the resolved
// draft.ID and the validated annotation UUID, never a raw URL string.

const (
	// draftAnnotationDefaultType is the application-layer fallback the type
	// switch downgrades unknown values to (open-enum / enum-drift rule).
	draftAnnotationDefaultType = "comment"
	// draftAnnotationDefaultState is the canonical create-time state.
	draftAnnotationDefaultState = "open"
	// draftAnnotationAuthorUser is the human author kind.
	draftAnnotationAuthorUser = "user"
	// draftAnnotationAuthorAgent is the agent author kind (Drafts slice 2).
	// Aye writes annotation replies + anchored suggestions as 'agent'; the
	// author_user_id column stays NULL for these rows.
	draftAnnotationAuthorAgent = "agent"
)

// resolveDraftAuthor decides how a draft-scoped write (an annotation / thread
// message, or a conversation-rail message) is attributed. It is
// annotation-agnostic: it mirrors comment.go in that an agent caller (validated
// X-Agent-ID + X-Task-ID via resolveActor) writes author_type='agent' with a
// NULL author_user_id; everyone else writes author_type='user' with their own
// id. The returned (actorType, actorID) pair is what the WS publish uses so the
// frontend can render agent-authored rows with agent styling.
//
// Slice 2 / Rail-2 keep Aye's surface to replies + anchored suggestions (and,
// for the rail, `multica draft say`); the body endpoint stays human-only
// (full-replace, no CRDT yet).
func (h *Handler) resolveDraftAuthor(r *http.Request, userID, workspaceID string, fallbackUserID pgtype.UUID) (authorType string, authorUserID pgtype.UUID, actorType, actorID string) {
	actorType, actorID = h.resolveActor(r, userID, workspaceID)
	if actorType == "agent" {
		// author_user_id is NULL for agent-authored rows (the column is
		// nullable for exactly this case); the agent identity travels via
		// author_type + the WS actor fields, not the user column.
		return draftAnnotationAuthorAgent, pgtype.UUID{}, actorType, actorID
	}
	return draftAnnotationAuthorUser, fallbackUserID, "member", userID
}

// normalizeDraftAnnotationType maps a caller-supplied annotation type onto a
// value safe to write. `type` is an open enum; the switch has a `default`
// branch so a genuinely unknown value downgrades to the canonical 'comment'
// rather than 400ing — an older server stays forward-compatible with a value a
// newer client legitimately sends.
//
// 'question' is a first-class type (Drafts slice 2): it's Aye's primary
// inquisitive verb — an anchored "I'm asking you something" thread, distinct
// from a 'comment' so it can be filtered/styled separately. It must persist as
// 'question', not silently collapse to 'comment'.
func normalizeDraftAnnotationType(s string) string {
	switch s {
	case "comment", "question", "suggestion", "approve", "block", "highlight":
		return s
	default:
		return draftAnnotationDefaultType
	}
}

// normalizeDraftAnnotationState maps a caller-supplied state onto a safe value.
// Open enum with a `default` branch (enum-drift rule): an unknown state
// downgrades to 'open'.
func normalizeDraftAnnotationState(s string) string {
	switch s {
	case "open", "acknowledged", "resolved", "dismissed":
		return s
	default:
		return draftAnnotationDefaultState
	}
}

// DraftAnnotationMessageResponse is the JSON shape for one thread message.
type DraftAnnotationMessageResponse struct {
	ID           string `json:"id"`
	AnnotationID string `json:"annotation_id"`
	AuthorType   string `json:"author_type"`
	AuthorUserID string `json:"author_user_id"`
	Body         string `json:"body"`
	CreatedAt    string `json:"created_at"`
}

// DraftAnnotationResponse is the JSON shape for an annotation plus its thread.
// The anchor fields (quote/context_before/context_after/pos_hint) are what the
// frontend re-anchoring engine consumes.
type DraftAnnotationResponse struct {
	ID            string `json:"id"`
	DraftID       string `json:"draft_id"`
	WorkspaceID   string `json:"workspace_id"`
	AuthorType    string `json:"author_type"`
	AuthorUserID  string `json:"author_user_id"`
	Type          string `json:"type"`
	Quote         string `json:"quote"`
	ContextBefore string `json:"context_before"`
	ContextAfter  string `json:"context_after"`
	PosHint       int    `json:"pos_hint"`
	State         string `json:"state"`
	// Suggestion payload (type='suggestion'); null otherwise.
	SuggestionBefore *string `json:"suggestion_before"`
	SuggestionAfter  *string `json:"suggestion_after"`
	CreatedAt        string  `json:"created_at"`
	UpdatedAt        string  `json:"updated_at"`
	// The thread. message[0] is the initial comment; a bare highlight has an
	// empty slice. Always non-nil so the JSON is `[]` rather than `null`.
	Messages []DraftAnnotationMessageResponse `json:"messages"`
}

func draftAnnotationMessageToResponse(m db.DraftAnnotationMessage) DraftAnnotationMessageResponse {
	return DraftAnnotationMessageResponse{
		ID:           uuidToString(m.ID),
		AnnotationID: uuidToString(m.AnnotationID),
		AuthorType:   m.AuthorType,
		AuthorUserID: uuidToString(m.AuthorUserID),
		Body:         m.Body,
		CreatedAt:    timestampToString(m.CreatedAt),
	}
}

func draftAnnotationToResponse(a db.DraftAnnotation, messages []DraftAnnotationMessageResponse) DraftAnnotationResponse {
	if messages == nil {
		messages = []DraftAnnotationMessageResponse{}
	}
	return DraftAnnotationResponse{
		ID:               uuidToString(a.ID),
		DraftID:          uuidToString(a.DraftID),
		WorkspaceID:      uuidToString(a.WorkspaceID),
		AuthorType:       a.AuthorType,
		AuthorUserID:     uuidToString(a.AuthorUserID),
		Type:             a.Type,
		Quote:            a.Quote,
		ContextBefore:    a.ContextBefore,
		ContextAfter:     a.ContextAfter,
		PosHint:          int(a.PosHint),
		State:            a.State,
		SuggestionBefore: textToPtr(a.SuggestionBefore),
		SuggestionAfter:  textToPtr(a.SuggestionAfter),
		CreatedAt:        timestampToString(a.CreatedAt),
		UpdatedAt:        timestampToString(a.UpdatedAt),
		Messages:         messages,
	}
}

// loadAnnotationForDraft resolves an annotation by its (validated) UUID, scoped
// to a draft the caller has already resolved+authorized via loadDraftForUser.
// The draftID predicate is the authorization tie-back: an annotation belonging
// to a different draft returns no rows → 404. Per the UUID-parsing convention
// the annotation id is validated with parseUUIDOrBadRequest before reaching any
// query.
func (h *Handler) loadAnnotationForDraft(w http.ResponseWriter, r *http.Request, draftID pgtype.UUID, annotationIDStr string) (db.DraftAnnotation, bool) {
	annotationUUID, ok := parseUUIDOrBadRequest(w, annotationIDStr, "annotation id")
	if !ok {
		return db.DraftAnnotation{}, false
	}
	annotation, err := h.Queries.GetDraftAnnotation(r.Context(), db.GetDraftAnnotationParams{
		ID:      annotationUUID,
		DraftID: draftID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "annotation not found")
		return db.DraftAnnotation{}, false
	}
	return annotation, true
}

// CreateDraftAnnotationRequest is the body for POST /drafts/{id}/annotations.
// `type` and the anchor are required for a meaningful annotation; `message` is
// the optional initial comment (omitted/empty for a bare highlight). state
// defaults to 'open' at create time and is not caller-settable here.
type CreateDraftAnnotationRequest struct {
	Type             *string `json:"type"`
	Quote            *string `json:"quote"`
	ContextBefore    *string `json:"context_before"`
	ContextAfter     *string `json:"context_after"`
	PosHint          *int    `json:"pos_hint"`
	SuggestionBefore *string `json:"suggestion_before"`
	SuggestionAfter  *string `json:"suggestion_after"`
	// Initial thread message. Omitted/empty for a bare highlight.
	Message *string `json:"message"`
}

// UpdateDraftAnnotationRequest carries optional fields. A missing field is "no
// change" (SQL uses COALESCE/narg). Two distinct callers:
//   - state transition: { state }
//   - re-anchor persistence: { quote, context_before, context_after, pos_hint }
type UpdateDraftAnnotationRequest struct {
	State         *string `json:"state"`
	Quote         *string `json:"quote"`
	ContextBefore *string `json:"context_before"`
	ContextAfter  *string `json:"context_after"`
	PosHint       *int    `json:"pos_hint"`
}

// AddDraftAnnotationMessageRequest is the body for POST
// /drafts/{id}/annotations/{aid}/messages — a reply on the thread.
type AddDraftAnnotationMessageRequest struct {
	Body string `json:"body"`
}

// ListDraftAnnotations returns every annotation on a draft, each with its full
// thread. Two queries (annotations + a bulk message fetch) assembled in a
// single pass to avoid N+1.
func (h *Handler) ListDraftAnnotations(w http.ResponseWriter, r *http.Request) {
	draft, ok := h.loadDraftForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}

	annotations, err := h.Queries.ListDraftAnnotations(r.Context(), draft.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list annotations")
		return
	}
	messages, err := h.Queries.ListDraftAnnotationMessagesForDraft(r.Context(), draft.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list annotation messages")
		return
	}

	// Group messages by annotation id (the bulk query is ordered by
	// annotation_id, created_at, so each annotation's messages are contiguous
	// and already time-ordered).
	byAnnotation := make(map[string][]DraftAnnotationMessageResponse, len(annotations))
	for _, m := range messages {
		aid := uuidToString(m.AnnotationID)
		byAnnotation[aid] = append(byAnnotation[aid], draftAnnotationMessageToResponse(m))
	}

	resp := make([]DraftAnnotationResponse, len(annotations))
	for i, a := range annotations {
		resp[i] = draftAnnotationToResponse(a, byAnnotation[uuidToString(a.ID)])
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"annotations": resp,
		"total":       len(resp),
	})
}

// CreateDraftAnnotation creates an annotation (and, if a message is supplied,
// the initial thread comment). Slice 1 always authors as the requesting user.
func (h *Handler) CreateDraftAnnotation(w http.ResponseWriter, r *http.Request) {
	draft, ok := h.loadDraftForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	var req CreateDraftAnnotationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	ownerUUID, ok := parseUUIDOrBadRequest(w, userID, "user id")
	if !ok {
		return
	}

	// Attribution: agent callers (Aye, slice 2) write author_type='agent' with
	// a NULL author_user_id; human callers write 'user' with their own id.
	workspaceID := uuidToString(draft.WorkspaceID)
	authorType, authorUserID, actorType, actorID := h.resolveDraftAuthor(r, userID, workspaceID, ownerUUID)

	annType := draftAnnotationDefaultType
	if req.Type != nil {
		annType = normalizeDraftAnnotationType(*req.Type)
	}
	quote := ""
	if req.Quote != nil {
		quote = *req.Quote
	}
	contextBefore := ""
	if req.ContextBefore != nil {
		contextBefore = *req.ContextBefore
	}
	contextAfter := ""
	if req.ContextAfter != nil {
		contextAfter = *req.ContextAfter
	}
	posHint := 0
	if req.PosHint != nil {
		posHint = *req.PosHint
	}

	annotation, err := h.Queries.CreateDraftAnnotation(r.Context(), db.CreateDraftAnnotationParams{
		DraftID:          draft.ID,
		WorkspaceID:      draft.WorkspaceID,
		AuthorType:       authorType,
		AuthorUserID:     authorUserID,
		Type:             annType,
		Quote:            quote,
		ContextBefore:    contextBefore,
		ContextAfter:     contextAfter,
		PosHint:          int32(posHint),
		State:            draftAnnotationDefaultState,
		SuggestionBefore: ptrToText(req.SuggestionBefore),
		SuggestionAfter:  ptrToText(req.SuggestionAfter),
	})
	if err != nil {
		slog.Warn("create draft annotation failed", append(logger.RequestAttrs(r), "error", err, "draft_id", uuidToString(draft.ID))...)
		writeError(w, http.StatusInternalServerError, "failed to create annotation")
		return
	}

	// Optional initial thread message. A bare highlight has none. Authored by
	// the same actor as the annotation itself.
	var messages []DraftAnnotationMessageResponse
	if req.Message != nil && *req.Message != "" {
		msg, err := h.Queries.CreateDraftAnnotationMessage(r.Context(), db.CreateDraftAnnotationMessageParams{
			AnnotationID: annotation.ID,
			AuthorType:   authorType,
			AuthorUserID: authorUserID,
			Body:         *req.Message,
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to create annotation message")
			return
		}
		messages = append(messages, draftAnnotationMessageToResponse(msg))
	}

	resp := draftAnnotationToResponse(annotation, messages)
	h.publish(protocol.EventDraftAnnotationCreated, workspaceID, actorType, actorID, map[string]any{"annotation": resp})
	writeJSON(w, http.StatusCreated, resp)
}

// UpdateDraftAnnotation patches state and/or the anchor (re-anchor
// persistence). Loads the thread so the response carries the full annotation.
func (h *Handler) UpdateDraftAnnotation(w http.ResponseWriter, r *http.Request) {
	draft, ok := h.loadDraftForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	annotation, ok := h.loadAnnotationForDraft(w, r, draft.ID, chi.URLParam(r, "aid"))
	if !ok {
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	var req UpdateDraftAnnotationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// COALESCE/narg semantics: a non-Valid field means "leave unchanged", so a
	// state-only patch never clobbers the anchor and a re-anchor patch never
	// clobbers the state.
	var state pgtype.Text
	if req.State != nil {
		state = pgtype.Text{String: normalizeDraftAnnotationState(*req.State), Valid: true}
	}
	var quote pgtype.Text
	if req.Quote != nil {
		quote = pgtype.Text{String: *req.Quote, Valid: true}
	}
	var contextBefore pgtype.Text
	if req.ContextBefore != nil {
		contextBefore = pgtype.Text{String: *req.ContextBefore, Valid: true}
	}
	var contextAfter pgtype.Text
	if req.ContextAfter != nil {
		contextAfter = pgtype.Text{String: *req.ContextAfter, Valid: true}
	}
	var posHint pgtype.Int4
	if req.PosHint != nil {
		posHint = pgtype.Int4{Int32: int32(*req.PosHint), Valid: true}
	}

	updated, err := h.Queries.UpdateDraftAnnotation(r.Context(), db.UpdateDraftAnnotationParams{
		ID:            annotation.ID,
		DraftID:       draft.ID,
		State:         state,
		Quote:         quote,
		ContextBefore: contextBefore,
		ContextAfter:  contextAfter,
		PosHint:       posHint,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update annotation")
		return
	}

	messages, err := h.Queries.ListDraftAnnotationMessages(r.Context(), updated.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load annotation thread")
		return
	}
	msgResp := make([]DraftAnnotationMessageResponse, len(messages))
	for i, m := range messages {
		msgResp[i] = draftAnnotationMessageToResponse(m)
	}

	workspaceID := uuidToString(draft.WorkspaceID)
	// State transitions (resolve/ack/dismiss) and re-anchors can come from Aye
	// too; reflect the real actor on the WS event so clients attribute the
	// change correctly.
	actorType, actorID := h.resolveActor(r, userID, workspaceID)
	resp := draftAnnotationToResponse(updated, msgResp)
	h.publish(protocol.EventDraftAnnotationUpdated, workspaceID, actorType, actorID, map[string]any{"annotation": resp})
	writeJSON(w, http.StatusOK, resp)
}

// DeleteDraftAnnotation deletes an annotation; its thread messages cascade.
func (h *Handler) DeleteDraftAnnotation(w http.ResponseWriter, r *http.Request) {
	draft, ok := h.loadDraftForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	annotation, ok := h.loadAnnotationForDraft(w, r, draft.ID, chi.URLParam(r, "aid"))
	if !ok {
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	if err := h.Queries.DeleteDraftAnnotation(r.Context(), db.DeleteDraftAnnotationParams{
		ID:      annotation.ID,
		DraftID: draft.ID,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete annotation")
		return
	}

	workspaceID := uuidToString(draft.WorkspaceID)
	h.publish(protocol.EventDraftAnnotationDeleted, workspaceID, "member", userID, map[string]any{
		"draft_id":      uuidToString(draft.ID),
		"annotation_id": uuidToString(annotation.ID),
	})
	w.WriteHeader(http.StatusNoContent)
}

// AddDraftAnnotationMessage appends a reply to an annotation thread.
func (h *Handler) AddDraftAnnotationMessage(w http.ResponseWriter, r *http.Request) {
	draft, ok := h.loadDraftForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	annotation, ok := h.loadAnnotationForDraft(w, r, draft.ID, chi.URLParam(r, "aid"))
	if !ok {
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	var req AddDraftAnnotationMessageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
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

	// Attribution: agent replies (Aye draining a thread, slice 2) carry
	// author_type='agent'; human replies carry 'user'.
	workspaceID := uuidToString(draft.WorkspaceID)
	authorType, authorUserID, actorType, actorID := h.resolveDraftAuthor(r, userID, workspaceID, ownerUUID)

	msg, err := h.Queries.CreateDraftAnnotationMessage(r.Context(), db.CreateDraftAnnotationMessageParams{
		AnnotationID: annotation.ID,
		AuthorType:   authorType,
		AuthorUserID: authorUserID,
		Body:         req.Body,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to add annotation message")
		return
	}

	resp := draftAnnotationMessageToResponse(msg)
	h.publish(protocol.EventDraftAnnotationMessageCreated, workspaceID, actorType, actorID, map[string]any{
		"draft_id":      uuidToString(draft.ID),
		"annotation_id": uuidToString(annotation.ID),
		"message":       resp,
	})
	writeJSON(w, http.StatusCreated, resp)
}
