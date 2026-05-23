package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/multica-ai/multica/server/internal/service"
)

const reminderSchedulerInterval = 30 * time.Second

func runReminderScheduler(ctx context.Context, svc *service.ReminderService) {
	ticker := time.NewTicker(reminderSchedulerInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			slog.Debug("reminder scheduler: checking for due reminders")
			svc.ProcessDueReminders(ctx)
		}
	}
}
