package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type ReminderResponse struct {
	ID            string  `json:"id"`
	WorkspaceID   string  `json:"workspace_id"`
	CreatorType   string  `json:"creator_type"`
	CreatorID     string  `json:"creator_id"`
	RecipientType string  `json:"recipient_type"`
	RecipientID   string  `json:"recipient_id"`
	Kind          string  `json:"kind"`
	Title         string  `json:"title"`
	Body          *string `json:"body"`
	IssueID       *string `json:"issue_id"`
	RemindAt      *string `json:"remind_at"`
	Status        string  `json:"status"`
	DeliveredAt   *string `json:"delivered_at"`
	CreatedAt     string  `json:"created_at"`
	UpdatedAt     string  `json:"updated_at"`
}

func reminderToResponse(r db.Reminder) ReminderResponse {
	return ReminderResponse{
		ID:            uuidToString(r.ID),
		WorkspaceID:   uuidToString(r.WorkspaceID),
		CreatorType:   r.CreatorType,
		CreatorID:     uuidToString(r.CreatorID),
		RecipientType: r.RecipientType,
		RecipientID:   uuidToString(r.RecipientID),
		Kind:          r.Kind,
		Title:         r.Title,
		Body:          textToPtr(r.Body),
		IssueID:       uuidToPtr(r.IssueID),
		RemindAt:      timestampToPtr(r.RemindAt),
		Status:        r.Status,
		DeliveredAt:   timestampToPtr(r.DeliveredAt),
		CreatedAt:     timestampToString(r.CreatedAt),
		UpdatedAt:     timestampToString(r.UpdatedAt),
	}
}

func optionalUUID(id string) pgtype.UUID {
	if id == "" {
		return pgtype.UUID{}
	}
	return parseUUID(id)
}

func validReminderRecipientType(t string) bool {
	return t == "member" || t == "agent"
}

func validReminderKind(k string) bool {
	return k == "system" || k == "task" || k == "check_in"
}

func (h *Handler) ListReminders(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := ctxWorkspaceID(r.Context())
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}

	actorType, actorID := h.resolveActor(r, userID, workspaceID)

	recipientType := actorType
	recipientID := actorID
	if rt := r.URL.Query().Get("recipient_type"); rt != "" {
		if !validReminderRecipientType(rt) {
			writeError(w, http.StatusBadRequest, "recipient_type must be member or agent")
			return
		}
		recipientType = rt
	}
	if rid := r.URL.Query().Get("recipient_id"); rid != "" {
		if _, ok := parseUUIDOrBadRequest(w, rid, "recipient_id"); !ok {
			return
		}
		recipientID = rid
	}

	limitVal := 50
	if ls := r.URL.Query().Get("limit"); ls != "" {
		if n, err := strconv.Atoi(ls); err == nil && n > 0 && n <= 200 {
			limitVal = n
		}
	}

	status := r.URL.Query().Get("status")
	if status != "" && status != "pending" && status != "delivered" && status != "cancelled" {
		writeError(w, http.StatusBadRequest, "status must be pending, delivered, or cancelled")
		return
	}
	kind := r.URL.Query().Get("kind")
	if kind != "" && !validReminderKind(kind) {
		writeError(w, http.StatusBadRequest, "kind must be system, task, or check_in")
		return
	}

	params := db.ListRemindersParams{
		WorkspaceID:   wsUUID,
		RecipientType: pgtype.Text{String: recipientType, Valid: recipientType != ""},
		RecipientID:   optionalUUID(recipientID),
		Status:        pgtype.Text{String: status, Valid: status != ""},
		Kind:          pgtype.Text{String: kind, Valid: kind != ""},
		LimitVal:      pgtype.Int4{Int32: int32(limitVal), Valid: true},
	}
	reminders, err := h.Queries.ListReminders(r.Context(), params)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list reminders")
		return
	}
	resp := make([]ReminderResponse, len(reminders))
	for i, rem := range reminders {
		resp[i] = reminderToResponse(rem)
	}
	writeJSON(w, http.StatusOK, map[string]any{"reminders": resp, "total": len(resp)})
}

func (h *Handler) CreateReminder(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := ctxWorkspaceID(r.Context())
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	actorType, actorID := h.resolveActor(r, userID, workspaceID)

	var body struct {
		Title         string  `json:"title"`
		Kind          string  `json:"kind"`
		Body          *string `json:"body"`
		IssueID       *string `json:"issue_id"`
		RemindAt      *string `json:"remind_at"`
		RecipientType *string `json:"recipient_type"`
		RecipientID   *string `json:"recipient_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if body.Title == "" {
		writeError(w, http.StatusBadRequest, "title is required")
		return
	}
	if !validReminderKind(body.Kind) {
		writeError(w, http.StatusBadRequest, "kind must be system, task, or check_in")
		return
	}
	if body.Kind == "task" && (body.IssueID == nil || *body.IssueID == "") {
		writeError(w, http.StatusBadRequest, "issue_id is required for kind=task")
		return
	}

	recipientType := actorType
	recipientID := actorID
	if body.RecipientType != nil && *body.RecipientType != "" {
		if !validReminderRecipientType(*body.RecipientType) {
			writeError(w, http.StatusBadRequest, "recipient_type must be member or agent")
			return
		}
		recipientType = *body.RecipientType
	}
	if body.RecipientID != nil && *body.RecipientID != "" {
		if _, ok := parseUUIDOrBadRequest(w, *body.RecipientID, "recipient_id"); !ok {
			return
		}
		recipientID = *body.RecipientID
	}

	params := db.CreateReminderParams{
		WorkspaceID:   wsUUID,
		CreatorType:   actorType,
		CreatorID:     parseUUID(actorID),
		RecipientType: recipientType,
		RecipientID:   parseUUID(recipientID),
		Kind:          body.Kind,
		Title:         body.Title,
	}
	if body.Body != nil {
		params.Body = pgtype.Text{String: *body.Body, Valid: true}
	}
	if body.IssueID != nil && *body.IssueID != "" {
		issueUUID, ok := parseUUIDOrBadRequest(w, *body.IssueID, "issue_id")
		if !ok {
			return
		}
		params.IssueID = issueUUID
	}

	var remindAt *time.Time
	if body.RemindAt != nil && *body.RemindAt != "" {
		t, err := time.Parse(time.RFC3339, *body.RemindAt)
		if err != nil {
			writeError(w, http.StatusBadRequest, "remind_at must be RFC3339")
			return
		}
		if t.Before(time.Now()) {
			writeError(w, http.StatusBadRequest, "remind_at must be in the future")
			return
		}
		params.RemindAt = pgtype.Timestamptz{Time: t, Valid: true}
		remindAt = &t
	}

	reminder, err := h.Queries.CreateReminder(r.Context(), params)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create reminder")
		return
	}

	if remindAt == nil {
		h.ReminderService.DeliverReminder(r.Context(), reminder)
		reminder.Status = "delivered"
	}

	writeJSON(w, http.StatusCreated, reminderToResponse(reminder))
}

func (h *Handler) GetReminder(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireUserID(w, r); !ok {
		return
	}
	workspaceID := ctxWorkspaceID(r.Context())
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	id := chi.URLParam(r, "id")
	idUUID, ok := parseUUIDOrBadRequest(w, id, "id")
	if !ok {
		return
	}
	reminder, err := h.Queries.GetReminderInWorkspace(r.Context(), db.GetReminderInWorkspaceParams{
		ID:          idUUID,
		WorkspaceID: wsUUID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "reminder not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to get reminder")
		return
	}
	writeJSON(w, http.StatusOK, reminderToResponse(reminder))
}

func (h *Handler) CancelReminder(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireUserID(w, r); !ok {
		return
	}
	id := chi.URLParam(r, "id")
	idUUID, ok := parseUUIDOrBadRequest(w, id, "id")
	if !ok {
		return
	}
	reminder, err := h.Queries.CancelReminder(r.Context(), idUUID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "reminder not found or already delivered/cancelled")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to cancel reminder")
		return
	}
	writeJSON(w, http.StatusOK, reminderToResponse(reminder))
}
