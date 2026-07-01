package handler

import (
	"context"
	"errors"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// BackfillAyeAgents keeps Aye (the Drafts lead agent) current across every
// workspace on boot. Two jobs:
//
//   - SEED: workspaces created BEFORE Drafts shipped have no Aye row (she's
//     normally seeded inside the workspace-create transaction, seedAyeAgent).
//     Without an Aye row StartDraftTurn can't resolve AyeAgentID and the
//     Send-turn 409s — so we seed her where she's missing.
//   - UPDATE: workspaces that already have Aye may carry a STALE identity — an
//     older soul in agent.instructions or an older Drafts skill body — because
//     those are baked from constants at seed time and the constants evolve
//     (e.g. Rail-2 adds the conversation rail). We refresh the existing row's
//     instructions and its attached skill's content in place so the new soul +
//     rail-aware skill reach already-seeded prod agents, not just new
//     workspaces.
//
// Modeled on migrateShipHubSecrets (server/cmd/server): a one-shot task run in a
// goroutine on boot. It is idempotent and cheap to re-run — a workspace whose
// Aye already matches the constants is a no-op (the update queries only write on
// a real diff), so it's safe on EVERY boot. It never blocks startup.
//
// Scope safety: both the seed and the update key on Aye's DETERMINISTIC id
// (AyeAgentID), never a name or role match, so a user-created agent is never
// touched. The skill update additionally pins by Aye's skill name via the
// agent_skill join.
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
	updated := 0
	for _, row := range rows {
		if !row.WorkspaceID.Valid || !row.OwnerID.Valid {
			continue
		}

		// Probe for Aye at her deterministic id. Present → refresh her identity
		// in place; absent → seed her. This keeps a re-run on every boot a no-op
		// once she both exists and matches the current constants.
		ayeID := AyeAgentID(row.WorkspaceID)
		if _, err := queries.GetAgent(ctx, ayeID); err == nil {
			if refreshAyeIdentity(ctx, queries, ayeID, row.WorkspaceID) {
				updated++
			}
			continue
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
	if updated > 0 {
		slog.Info("refreshed Aye identity in workspaces", "count", updated)
	}
}

// refreshAyeIdentity updates an already-seeded Aye's instructions (Layer 1 soul)
// and her attached Drafts-surface skill's content (Layer 2) to match the current
// constants, writing only what actually drifted. Returns true if anything was
// written. Both writes are scoped to Aye's deterministic id (and the skill by
// name via the agent_skill join), so a user-created agent is never touched.
//
// No transaction: the two updates are independent idempotent refreshes, not an
// atomic invariant — if only one lands this boot, the next boot finishes the
// other. Best-effort like the rest of the backfill: a failure is logged, not
// surfaced, so it never blocks startup.
func refreshAyeIdentity(ctx context.Context, queries *db.Queries, ayeID, workspaceID pgtype.UUID) bool {
	changed := false

	n, err := queries.UpdateAgentInstructions(ctx, db.UpdateAgentInstructionsParams{
		ID:           ayeID,
		WorkspaceID:  workspaceID,
		Instructions: ayeInstructions,
	})
	if err != nil {
		slog.Warn("aye backfill: update instructions failed",
			"workspace_id", uuidToString(workspaceID), "error", err)
	} else if n > 0 {
		changed = true
	}

	n, err = queries.UpdateAgentSkillContent(ctx, db.UpdateAgentSkillContentParams{
		AgentID: ayeID,
		Name:    ayeSkillName,
		Content: ayeSkillContent,
	})
	if err != nil {
		slog.Warn("aye backfill: update skill content failed",
			"workspace_id", uuidToString(workspaceID), "error", err)
	} else if n > 0 {
		changed = true
	}

	if changed {
		slog.Info("aye backfill: refreshed Aye identity",
			"workspace_id", uuidToString(workspaceID))
	}
	return changed
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
