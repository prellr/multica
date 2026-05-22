package service

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type ReminderService struct {
	Queries *db.Queries
	Bus     *events.Bus
}

func NewReminderService(queries *db.Queries, bus *events.Bus) *ReminderService {
	return &ReminderService{Queries: queries, Bus: bus}
}

// DeliverReminder creates an inbox_item for the reminder's recipient and publishes inbox:new.
func (s *ReminderService) DeliverReminder(ctx context.Context, r db.Reminder) {
	wsID := util.UUIDToString(r.WorkspaceID)
	details, _ := json.Marshal(map[string]any{
		"reminder_id": util.UUIDToString(r.ID),
		"kind":        r.Kind,
	})

	var issueID pgtype.UUID
	if r.IssueID.Valid {
		issueID = r.IssueID
	}

	item, err := s.Queries.CreateInboxItem(ctx, db.CreateInboxItemParams{
		WorkspaceID:   r.WorkspaceID,
		RecipientType: r.RecipientType,
		RecipientID:   r.RecipientID,
		Type:          "reminder",
		Severity:      "info",
		IssueID:       issueID,
		Title:         r.Title,
		Body:          r.Body,
		ActorType:     pgtype.Text{String: r.CreatorType, Valid: true},
		ActorID:       r.CreatorID,
		Details:       details,
	})
	if err != nil {
		slog.Error("reminder: failed to create inbox item",
			"reminder_id", util.UUIDToString(r.ID), "error", err)
		return
	}

	s.Bus.Publish(events.Event{
		Type:        protocol.EventInboxNew,
		WorkspaceID: wsID,
		ActorType:   r.CreatorType,
		ActorID:     util.UUIDToString(r.CreatorID),
		Payload: map[string]any{
			"item": map[string]any{
				"id":             util.UUIDToString(item.ID),
				"workspace_id":   wsID,
				"recipient_type": item.RecipientType,
				"recipient_id":   util.UUIDToString(item.RecipientID),
				"type":           item.Type,
				"severity":       item.Severity,
				"title":          item.Title,
				"read":           item.Read,
				"archived":       item.Archived,
				"created_at":     item.CreatedAt.Time.Format("2006-01-02T15:04:05Z07:00"),
			},
		},
	})

	if err := s.Queries.MarkReminderDelivered(ctx, r.ID); err != nil {
		slog.Error("reminder: failed to mark delivered",
			"reminder_id", util.UUIDToString(r.ID), "error", err)
	}
}

// ProcessDueReminders claims all due reminders and delivers each one.
func (s *ReminderService) ProcessDueReminders(ctx context.Context) {
	reminders, err := s.Queries.ClaimDueReminders(ctx)
	if err != nil {
		slog.Warn("reminder scheduler: failed to claim due reminders", "error", err)
		return
	}
	for _, r := range reminders {
		s.DeliverReminder(ctx, r)
	}
}
