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

// shipHubPipelineRefreshInterval is how often the pipeline-refresh
// poller re-introspects every Ship-Hub project. PR8's primary refresh
// trigger is the push webhook (an operator editing `.github/workflows/`
// is picked up within seconds); this daily sweep is the backstop for
// repos whose webhook delivery is unreliable — cross-org repos, repos
// added before the webhook was installed, or any delivery GitHub drops.
// 24h is loose enough to stay far under the per-token rate limit even
// for a workspace with dozens of repos.
const shipHubPipelineRefreshInterval = 24 * time.Hour

// runShipHubPipelineRefreshPoller periodically iterates every
// Ship-Hub-enabled workspace, runs the pipeline introspector against
// each project's repo, and applies PR8's diff/apply classification via
// Service.RefreshProjectPipeline: additive changes auto-apply, a
// destructive change is parked as a pending proposal for the operator.
//
// The first sweep runs after the first tick — not immediately on
// startup — matching the other Ship Hub pollers and giving the API time
// to fully boot before we start hammering GitHub.
//
// Errors are logged per-workspace / per-project so one bad token or one
// unreachable repo can't starve the rest of the fleet.
func runShipHubPipelineRefreshPoller(ctx context.Context, queries *db.Queries) {
	slog.Info("ship hub pipeline refresh poller started",
		"interval", shipHubPipelineRefreshInterval.String())
	t := time.NewTicker(shipHubPipelineRefreshInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			slog.Info("ship hub pipeline refresh poller stopped")
			return
		case <-t.C:
			runShipHubPipelineRefreshOnce(ctx, queries)
		}
	}
}

// runShipHubPipelineRefreshOnce is the per-tick body. Extracted so a
// future test can drive it without spinning up the goroutine.
func runShipHubPipelineRefreshOnce(ctx context.Context, queries *db.Queries) {
	workspaces, err := queries.ListWorkspacesWithShipHubEnabled(ctx)
	if err != nil {
		slog.Warn("ship hub pipeline refresh poller: list workspaces failed", "error", err)
		return
	}
	for _, ws := range workspaces {
		token := handler.ReadShipHubGitHubTokenForReconciler(ws.Settings)
		if token == "" {
			row, err := queries.GetWorkspaceSecret(ctx, db.GetWorkspaceSecretParams{
				WorkspaceID: ws.ID,
				Name:        "github_token",
			})
			if err == nil {
				token = handler.ReadShipHubGitHubTokenFromEncrypted(row.ValueEncrypted)
			} else if !errors.Is(err, pgx.ErrNoRows) {
				slog.Warn("ship hub pipeline refresh poller: load encrypted token failed",
					"workspace_id", ws.ID, "error", err)
			}
		}
		client, hasAuth := handler.ShipHubGitHubClientForBackground(ctx, queries, ws.ID, token)
		if !hasAuth {
			// Feature enabled but neither a GitHub App installation nor
			// a personal token is configured — the introspector can't
			// authenticate. Skip quietly.
			slog.Debug("ship hub pipeline refresh poller: skipping workspace without GitHub auth",
				"workspace_id", ws.ID)
			continue
		}
		svc := &ship.Service{
			Q:      queries,
			Github: client,
		}
		projects, err := queries.ListProjects(ctx, db.ListProjectsParams{
			WorkspaceID:     ws.ID,
			IncludeArchived: false,
		})
		if err != nil {
			slog.Warn("ship hub pipeline refresh poller: list projects failed",
				"workspace_id", ws.ID, "error", err)
			continue
		}
		for _, p := range projects {
			outcome, err := svc.RefreshProjectPipeline(ctx, p)
			if err != nil {
				slog.Warn("ship hub pipeline refresh poller: refresh project failed",
					"workspace_id", ws.ID, "project_id", p.ID, "error", err)
				continue
			}
			if outcome.Kind != ship.RefreshUnchanged && outcome.Kind != ship.RefreshSkippedNoRepo {
				slog.Info("ship hub pipeline refresh poller: pipeline change detected",
					"workspace_id", ws.ID, "project_id", p.ID, "outcome", outcome.Kind)
			}
		}
	}
}
