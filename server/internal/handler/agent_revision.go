package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Agent revision recording.
//
// Lives next to agent.go but in its own file because three different
// handler paths feed it: UpdateAgent, SetAgentSkills, and CreateAgent.
// Keeping the diff logic in one place makes "what counts as a behavioral
// change" the single source of truth — see the migration 120 header for
// the contract.
//
// The trigger field list is the authoritative answer to "would a cached
// provider session_id be invalid after this change?" Each field below
// has a one-line reason for being on the list:
//
//   instructions    — different system prompt = different agent persona
//   model           — can't resume a Sonnet 4 session as Sonnet 5
//   thinking_level  — changes how the model approaches the task
//   custom_env      — API keys/account changes invalidate the session
//   custom_args     — CLI args change runtime behavior
//   mcp_config      — different tools = different agent capability
//   runtime_id      — session_id is tied to the runtime that produced it
//   runtime_config  — runtime-side config (model overrides, etc.)
//   skills (set)    — effective system prompt + tool calling change
//
// Deliberately NOT triggers (would generate audit noise):
//   name, description, avatar_url — cosmetic
//   visibility, status            — access / lifecycle
//   archived_at, archived_by      — lifecycle (separate audit path)
//   max_concurrent_tasks          — throughput, not behavior

// agentRevisionTriggerSnapshot captures every trigger field at a single
// moment. Used both as the "before" reference for the diff and as the
// "after" payload that gets persisted into the agent_revision row's
// change_summary.snapshot.
//
// Marshaled as JSON keys that match the column names so the persisted
// snapshot reads naturally when an operator queries
// agent_revision.change_summary directly.
type agentRevisionTriggerSnapshot struct {
	Instructions   string          `json:"instructions"`
	Model          string          `json:"model"`
	ThinkingLevel  string          `json:"thinking_level"`
	CustomEnv      json.RawMessage `json:"custom_env"`
	CustomArgs     json.RawMessage `json:"custom_args"`
	McpConfig      json.RawMessage `json:"mcp_config"`
	RuntimeID      string          `json:"runtime_id"`
	RuntimeConfig  json.RawMessage `json:"runtime_config"`
	SkillIDs       []string        `json:"skill_ids"`
}

// snapshotFromAgent extracts the trigger-field snapshot from a fully-
// loaded agent row + its current skill set. Skill ids are sorted to make
// the diff order-independent (two agents with the same skills in
// different attach order should NOT count as "different").
func snapshotFromAgent(a db.Agent, skillIDs []pgtype.UUID) agentRevisionTriggerSnapshot {
	sortedSkills := make([]string, 0, len(skillIDs))
	for _, sid := range skillIDs {
		sortedSkills = append(sortedSkills, uuidToString(sid))
	}
	sort.Strings(sortedSkills)

	return agentRevisionTriggerSnapshot{
		Instructions:  a.Instructions, // db.Agent.Instructions is `string`, not pgtype.Text
		Model:         textOrEmpty(a.Model),
		ThinkingLevel: textOrEmpty(a.ThinkingLevel),
		CustomEnv:     jsonbOrEmptyObject(a.CustomEnv),
		CustomArgs:    jsonbOrEmptyArray(a.CustomArgs),
		McpConfig:     jsonbOrNull(a.McpConfig),
		RuntimeID:     uuidToString(a.RuntimeID),
		RuntimeConfig: jsonbOrEmptyObject(a.RuntimeConfig),
		SkillIDs:      sortedSkills,
	}
}

// changedFields returns the list of trigger field names whose values
// differ between before and after. Empty slice means "no behavioral
// change" — the caller short-circuits and doesn't write a revision row.
//
// Skill set comparison treats {a, b} and {b, a} as equal (set semantics,
// not list semantics) — both snapshots have already been sorted in
// snapshotFromAgent, so a slice equality check is correct.
//
// JSONB fields compare byte-for-byte after the snapshot extraction. That
// means a re-serialization of the same logical JSON with different
// whitespace WOULD be reported as a change. We accept that small
// false-positive risk in exchange for not needing a semantic JSON
// comparator here; in practice the JSONB columns round-trip through the
// same sqlc-generated codec and produce byte-identical output for
// unchanged values.
func changedFields(before, after agentRevisionTriggerSnapshot) []string {
	var changed []string
	if before.Instructions != after.Instructions {
		changed = append(changed, "instructions")
	}
	if before.Model != after.Model {
		changed = append(changed, "model")
	}
	if before.ThinkingLevel != after.ThinkingLevel {
		changed = append(changed, "thinking_level")
	}
	if !bytes.Equal(before.CustomEnv, after.CustomEnv) {
		changed = append(changed, "custom_env")
	}
	if !bytes.Equal(before.CustomArgs, after.CustomArgs) {
		changed = append(changed, "custom_args")
	}
	if !bytes.Equal(before.McpConfig, after.McpConfig) {
		changed = append(changed, "mcp_config")
	}
	if before.RuntimeID != after.RuntimeID {
		changed = append(changed, "runtime_id")
	}
	if !bytes.Equal(before.RuntimeConfig, after.RuntimeConfig) {
		changed = append(changed, "runtime_config")
	}
	if !stringSlicesEqual(before.SkillIDs, after.SkillIDs) {
		changed = append(changed, "skills")
	}
	return changed
}

// recordAgentRevisionIfChanged inserts a new agent_revision row and
// updates the agent's current_revision_* pointers IF the new state
// differs from before on any trigger field. Returns (revisionNumber,
// true, nil) when a row was written, or (0, false, nil) when no
// behavioral change was detected. Errors propagate so the caller can
// roll back the surrounding transaction.
//
// MUST be called inside a transaction (the `q` parameter should be a
// `qtx := h.Queries.WithTx(tx)`). The revision row insert and the
// agent pointer update are two SQL statements; if the surrounding work
// commits but this helper's writes don't, the agent's persisted state
// and revision history fall out of sync.
//
// `before` and `after` are the agent row's full state pre- and post-
// update. For paths that only change skills, `before` and `after` are
// the same agent row and only the skill_ids differ. For paths that
// only change agent fields, `beforeSkills` and `afterSkills` are the
// same slice.
//
// `actorID` is the user_id of the human (or service) triggering the
// change. Pass an invalid pgtype.UUID for backend-initiated changes —
// the column is nullable specifically to accommodate that.
func (h *Handler) recordAgentRevisionIfChanged(
	ctx context.Context,
	q *db.Queries,
	before, after db.Agent,
	beforeSkills, afterSkills []pgtype.UUID,
	actorID pgtype.UUID,
) (int32, bool, error) {
	beforeSnap := snapshotFromAgent(before, beforeSkills)
	afterSnap := snapshotFromAgent(after, afterSkills)
	changed := changedFields(beforeSnap, afterSnap)
	if len(changed) == 0 {
		return 0, false, nil
	}

	summaryBytes, err := json.Marshal(map[string]any{
		"changed_fields": changed,
		"snapshot":       afterSnap,
	})
	if err != nil {
		return 0, false, fmt.Errorf("marshal change summary: %w", err)
	}

	nextNum := after.CurrentRevisionNumber + 1
	rev, err := q.CreateAgentRevision(ctx, db.CreateAgentRevisionParams{
		WorkspaceID:    after.WorkspaceID,
		AgentID:        after.ID,
		RevisionNumber: nextNum,
		CreatedBy:      actorID,
		ChangeSummary:  summaryBytes,
	})
	if err != nil {
		return 0, false, fmt.Errorf("create agent revision: %w", err)
	}

	if err := q.SetAgentCurrentRevision(ctx, db.SetAgentCurrentRevisionParams{
		ID:                    after.ID,
		CurrentRevisionID:     pgtype.UUID{Bytes: rev.ID.Bytes, Valid: true},
		CurrentRevisionNumber: nextNum,
	}); err != nil {
		return 0, false, fmt.Errorf("update agent current revision pointer: %w", err)
	}

	return nextNum, true, nil
}

// recordInitialAgentRevision writes revision 1 for a brand-new agent,
// called by CreateAgent right after the agent row is inserted. The
// migration 120 backfill only catches agents that exist at migration
// time; this is the handler-side counterpart for agents created later.
//
// The "snapshot" carries the new agent's full trigger-field state so a
// revision-1 row is symmetric with any subsequent revision row in
// terms of how its content is queried. changed_fields is the sentinel
// ["initial"] to match the backfill marker.
func (h *Handler) recordInitialAgentRevision(
	ctx context.Context,
	q *db.Queries,
	agent db.Agent,
	skillIDs []pgtype.UUID,
	actorID pgtype.UUID,
) error {
	snap := snapshotFromAgent(agent, skillIDs)
	summaryBytes, err := json.Marshal(map[string]any{
		"changed_fields": []string{"initial"},
		"snapshot":       snap,
	})
	if err != nil {
		return fmt.Errorf("marshal initial change summary: %w", err)
	}

	rev, err := q.CreateAgentRevision(ctx, db.CreateAgentRevisionParams{
		WorkspaceID:    agent.WorkspaceID,
		AgentID:        agent.ID,
		RevisionNumber: 1,
		CreatedBy:      actorID,
		ChangeSummary:  summaryBytes,
	})
	if err != nil {
		return fmt.Errorf("create initial agent revision: %w", err)
	}

	if err := q.SetAgentCurrentRevision(ctx, db.SetAgentCurrentRevisionParams{
		ID:                    agent.ID,
		CurrentRevisionID:     pgtype.UUID{Bytes: rev.ID.Bytes, Valid: true},
		CurrentRevisionNumber: 1,
	}); err != nil {
		return fmt.Errorf("update initial agent revision pointer: %w", err)
	}
	return nil
}

// ─── small helpers — value extraction + comparison ──────────────────────

func textOrEmpty(t pgtype.Text) string {
	if !t.Valid {
		return ""
	}
	return t.String
}

// jsonbOrNull returns the JSONB bytes verbatim or a literal `null` token
// so downstream byte-comparison is deterministic for a never-set column.
func jsonbOrNull(b []byte) json.RawMessage {
	if len(b) == 0 {
		return json.RawMessage("null")
	}
	return json.RawMessage(b)
}

// jsonbOrEmptyObject normalizes an absent JSONB to `{}` so two agents
// that differ only on "never had custom_env" vs "explicitly set to {}"
// don't look like a behavioral change. The agent table defaults the
// column to '{}' anyway, but historical rows might predate the default.
func jsonbOrEmptyObject(b []byte) json.RawMessage {
	if len(b) == 0 {
		return json.RawMessage("{}")
	}
	return json.RawMessage(b)
}

func jsonbOrEmptyArray(b []byte) json.RawMessage {
	if len(b) == 0 {
		return json.RawMessage("[]")
	}
	return json.RawMessage(b)
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
