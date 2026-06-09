package main

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/handler"
	"github.com/multica-ai/multica/server/internal/service/ship"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// shipHubReconcileInterval is how often the reconciler scans every
// Ship-Hub-enabled workspace and refreshes its PR cache.
//
// 15 minutes is the post-2026-06-08 default. The original 5-minute spec
// assumed a single workspace with a handful of repos under an active
// GitHub App installation token (15k/hr quota). On 2026-06-08 we
// disconnected the workspace's App (broken `account_login=unknown`
// install path) and the system fell through to PAT-only auth, which
// shares a 5k/hr quota with every other tool the operator runs against
// the same account. A 5-project workspace pinging check-runs + status
// for every open PR every 5 minutes alone was exhausting the budget
// before any human interaction. Bumping to 15 minutes cuts the steady-
// state pull rate by 3x while keeping the "missed-webhook backstop"
// guarantee — the latency on a dropped webhook becomes "up to 15 min"
// instead of "up to 5 min", which is acceptable given webhooks
// themselves deliver in seconds when they work.
const shipHubReconcileInterval = 15 * time.Minute

// runShipHubReconciler periodically iterates every workspace with
// ship_hub_enabled = TRUE and calls Service.SyncWorkspace on each. Errors
// are logged per-workspace so one broken token doesn't stall the rest.
//
// The first iteration runs after the first tick — not immediately on
// startup. This matches the daemon-sweeper pattern and gives the API time
// to fully boot before we start hammering GitHub.
func runShipHubReconciler(ctx context.Context, queries *db.Queries) {
	slog.Info("ship hub reconciler started", "interval", shipHubReconcileInterval.String())
	t := time.NewTicker(shipHubReconcileInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			slog.Info("ship hub reconciler stopped")
			return
		case <-t.C:
			runShipHubOnce(ctx, queries)
		}
	}
}

// runShipHubOnce is the per-tick body. Extracted so a future test can
// drive it without spinning up the goroutine.
func runShipHubOnce(ctx context.Context, queries *db.Queries) {
	workspaces, err := queries.ListWorkspacesWithShipHubEnabled(ctx)
	if err != nil {
		slog.Warn("ship hub reconciler: list workspaces failed", "error", err)
		return
	}
	if len(workspaces) == 0 {
		return
	}
	for _, ws := range workspaces {
		token := handler.ReadShipHubGitHubTokenForReconciler(ws.Settings)
		if token == "" {
			// Phase 2: check the encrypted store before giving up.
			row, err := queries.GetWorkspaceSecret(ctx, db.GetWorkspaceSecretParams{
				WorkspaceID: ws.ID,
				Name:        "github_token",
			})
			if err == nil {
				token = handler.ReadShipHubGitHubTokenFromEncrypted(row.ValueEncrypted)
			} else if !errors.Is(err, pgx.ErrNoRows) {
				slog.Warn("ship hub reconciler: load encrypted token failed",
					"workspace_id", ws.ID, "error", err)
			}
		}
		client, hasAuth := handler.ShipHubGitHubClientForBackground(ctx, queries, ws.ID, token)
		if !hasAuth {
			// Workspace enabled the feature but never installed the App
			// AND never configured a PAT. We could still call the public
			// GitHub API for public repos, but the unauthenticated rate
			// limit (60/hr) is so low it would blow up the reconciler
			// immediately. Skip and log once.
			slog.Debug("ship hub reconciler: skipping workspace without GitHub auth",
				"workspace_id", ws.ID)
			continue
		}
		svc := &ship.Service{
			Q:      queries,
			Github: client,
		}
		if err := svc.SyncWorkspace(ctx, ws.ID); err != nil {
			slog.Warn("ship hub reconciler: sync workspace failed",
				"workspace_id", ws.ID, "error", err)
			continue
		}
	}
}
