package handler

import (
	"context"
	"errors"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// BackfillAyeAgents seeds Aye (the Drafts lead agent) into every workspace that
// predates the Drafts deploy and therefore has no Aye row. Aye is normally
// seeded inside the workspace-create transaction (seedAyeAgent), so only
// workspaces created BEFORE that shipped are missing her — without an Aye row
// StartDraftTurn can't resolve AyeAgentID and the Send-turn 409s.
//
// Modeled on migrateShipHubSecrets (server/cmd/server): a one-shot task run in a
// goroutine on boot. It is idempotent and cheap to re-run — a workspace that
// already has Aye is skipped after a single GetAgent probe, so it's safe on
// EVERY boot. It never blocks startup.
//
// txStarter is the same Begin-only interface the Handler holds (a *pgxpool.Pool
// satisfies it); each missing workspace gets its own transaction so the
// agent+skill+agent_skill seed is atomic and one bad workspace can't poison the
// rest.
func BackfillAyeAgents(ctx context.Context, queries *db.Queries, txStarter txStarter) {
	rows, err := queries.ListWorkspacesWithOwner(ctx)
	if err != nil {
		slog.Warn("aye backfill: list workspaces failed", "error", err)
		return
	}

	seeded := 0
	for _, row := range rows {
		if !row.WorkspaceID.Valid || !row.OwnerID.Valid {
			continue
		}

		// Skip workspaces that already have Aye at her deterministic id. This is
		// the idempotency check that makes a re-run on every boot a no-op.
		ayeID := AyeAgentID(row.WorkspaceID)
		if _, err := queries.GetAgent(ctx, ayeID); err == nil {
			continue // already seeded
		} else if !errors.Is(err, pgx.ErrNoRows) {
			slog.Warn("aye backfill: probe failed",
				"workspace_id", uuidToString(row.WorkspaceID), "error", err)
			continue
		}

		if err := seedAyeInTx(ctx, queries, txStarter, row.WorkspaceID, row.OwnerID); err != nil {
			slog.Warn("aye backfill: seed failed",
				"workspace_id", uuidToString(row.WorkspaceID), "error", err)
			continue
		}
		seeded++
		slog.Info("aye backfill: seeded Aye into workspace",
			"workspace_id", uuidToString(row.WorkspaceID))
	}

	if seeded > 0 {
		slog.Info("backfilled Aye into workspaces", "count", seeded)
	}
}

// seedAyeInTx runs the existing seedAyeAgent inside its own transaction so the
// three writes (agent, skill, agent_skill) commit atomically — the same shape
// the workspace-create path uses, just one workspace at a time.
func seedAyeInTx(ctx context.Context, queries *db.Queries, txStarter txStarter, workspaceID, ownerID pgtype.UUID) error {
	tx, err := txStarter.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if err := seedAyeAgent(ctx, queries.WithTx(tx), workspaceID, ownerID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
