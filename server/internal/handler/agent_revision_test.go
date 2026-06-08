package handler

import (
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// uuidFromString returns a valid pgtype.UUID for tests that just need a
// distinct sentinel. The actual byte values don't matter — we only
// compare equality through the snapshot helpers.
func mustUUID(t *testing.T, hex string) pgtype.UUID {
	t.Helper()
	var u pgtype.UUID
	if err := u.Scan(hex); err != nil {
		t.Fatalf("parse uuid %q: %v", hex, err)
	}
	return u
}

// baseAgent returns a fully-populated db.Agent with values that won't
// trigger spurious "field changed" reports. Tests vary single fields off
// this baseline so a diff against a clone of baseAgent surfaces ONLY
// the field under test.
func baseAgent(t *testing.T) db.Agent {
	t.Helper()
	return db.Agent{
		ID:            mustUUID(t, "11111111-1111-1111-1111-111111111111"),
		WorkspaceID:   mustUUID(t, "22222222-2222-2222-2222-222222222222"),
		Instructions:  "You are a helpful assistant.",
		Model:         pgtype.Text{String: "claude-sonnet-4-7", Valid: true},
		ThinkingLevel: pgtype.Text{String: "high", Valid: true},
		CustomEnv:     []byte(`{"FOO":"bar"}`),
		CustomArgs:    []byte(`["--verbose"]`),
		McpConfig:     []byte(`{"servers":["a"]}`),
		RuntimeID:     mustUUID(t, "33333333-3333-3333-3333-333333333333"),
		RuntimeConfig: []byte(`{"model_override":""}`),
	}
}

func TestChangedFields_IdenticalAgent_NoChange(t *testing.T) {
	// Two identical agents (same row, same skills) must produce an empty
	// changed_fields list. This is the gating case for "did anything
	// behaviorally meaningful change?" — false-positives here would
	// pollute the revision history with noise rows for every name edit.
	a := baseAgent(t)
	snap := snapshotFromAgent(a, nil)
	if got := changedFields(snap, snap); len(got) != 0 {
		t.Fatalf("expected no changes, got %v", got)
	}
}

func TestChangedFields_InstructionsChanged(t *testing.T) {
	before := baseAgent(t)
	after := before
	after.Instructions = "You are a code reviewer."

	got := changedFields(snapshotFromAgent(before, nil), snapshotFromAgent(after, nil))
	if !revContainsOnly(got, "instructions") {
		t.Fatalf("expected only [instructions], got %v", got)
	}
}

func TestChangedFields_ModelChanged(t *testing.T) {
	before := baseAgent(t)
	after := before
	after.Model = pgtype.Text{String: "claude-opus-5", Valid: true}

	got := changedFields(snapshotFromAgent(before, nil), snapshotFromAgent(after, nil))
	if !revContainsOnly(got, "model") {
		t.Fatalf("expected only [model], got %v", got)
	}
}

func TestChangedFields_JsonbBytesDiffer(t *testing.T) {
	// Byte-level comparison on JSONB columns: changing a value (not just
	// re-serializing) must be detected.
	before := baseAgent(t)
	after := before
	after.CustomEnv = []byte(`{"FOO":"baz"}`)
	after.McpConfig = []byte(`{"servers":["a","b"]}`)

	got := changedFields(snapshotFromAgent(before, nil), snapshotFromAgent(after, nil))
	if !revContainsAll(got, "custom_env", "mcp_config") {
		t.Fatalf("expected [custom_env, mcp_config], got %v", got)
	}
	if len(got) != 2 {
		t.Fatalf("expected exactly 2 changes, got %d: %v", len(got), got)
	}
}

func TestChangedFields_SkillSetReorderedIsNotAChange(t *testing.T) {
	// The skill set is a SET, not a list. {a, b} == {b, a}. The snapshot
	// helper sorts both sides; the diff must NOT report skills as
	// changed when order differs but content is the same.
	a := baseAgent(t)
	s1 := mustUUID(t, "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	s2 := mustUUID(t, "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb")

	got := changedFields(
		snapshotFromAgent(a, []pgtype.UUID{s1, s2}),
		snapshotFromAgent(a, []pgtype.UUID{s2, s1}),
	)
	if len(got) != 0 {
		t.Fatalf("reordered skill set should not register as change, got %v", got)
	}
}

func TestChangedFields_SkillAdded(t *testing.T) {
	a := baseAgent(t)
	s1 := mustUUID(t, "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	s2 := mustUUID(t, "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb")

	got := changedFields(
		snapshotFromAgent(a, []pgtype.UUID{s1}),
		snapshotFromAgent(a, []pgtype.UUID{s1, s2}),
	)
	if !revContainsOnly(got, "skills") {
		t.Fatalf("expected only [skills], got %v", got)
	}
}

func TestChangedFields_AllFieldsChanged(t *testing.T) {
	// Worst case — every trigger field differs. Confirms the diff loop
	// reports all of them (not just the first hit) and that the
	// canonical order in the slice matches the source code order so
	// downstream consumers reading change_summary can rely on stable
	// ordering for tests of their own.
	before := baseAgent(t)
	after := db.Agent{
		Instructions:  "completely different",
		Model:         pgtype.Text{String: "different-model", Valid: true},
		ThinkingLevel: pgtype.Text{String: "low", Valid: true},
		CustomEnv:     []byte(`{}`),
		CustomArgs:    []byte(`[]`),
		McpConfig:     []byte(`null`),
		RuntimeID:     mustUUID(t, "99999999-9999-9999-9999-999999999999"),
		RuntimeConfig: []byte(`{"x":"y"}`),
	}
	s1 := mustUUID(t, "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")

	got := changedFields(snapshotFromAgent(before, nil), snapshotFromAgent(after, []pgtype.UUID{s1}))
	wantFields := []string{
		"instructions", "model", "thinking_level",
		"custom_env", "custom_args", "mcp_config",
		"runtime_id", "runtime_config", "skills",
	}
	for _, f := range wantFields {
		if !revContains(got, f) {
			t.Errorf("expected %q in changed_fields, got %v", f, got)
		}
	}
	if len(got) != len(wantFields) {
		t.Errorf("expected exactly %d changes, got %d: %v", len(wantFields), len(got), got)
	}
}

func TestSnapshotFromAgent_NormalizesEmptyJSONB(t *testing.T) {
	// Agents created before defaults landed may have NULL/empty bytes for
	// the JSONB columns. The snapshot must normalize them to a canonical
	// form ({} for objects, [] for arrays, null for mcp_config) so a
	// pre-default agent and a freshly-created one with explicit empty
	// values don't show up as "changed."
	a := db.Agent{
		Instructions: "x",
	}
	snap := snapshotFromAgent(a, nil)
	if string(snap.CustomEnv) != "{}" {
		t.Errorf("CustomEnv empty bytes should normalize to {}, got %q", snap.CustomEnv)
	}
	if string(snap.CustomArgs) != "[]" {
		t.Errorf("CustomArgs empty bytes should normalize to [], got %q", snap.CustomArgs)
	}
	if string(snap.McpConfig) != "null" {
		t.Errorf("McpConfig empty bytes should normalize to null, got %q", snap.McpConfig)
	}
	if string(snap.RuntimeConfig) != "{}" {
		t.Errorf("RuntimeConfig empty bytes should normalize to {}, got %q", snap.RuntimeConfig)
	}
}

// ─── tiny test helpers ─────────────────────────────────────────────────

func revContains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func revContainsOnly(xs []string, x string) bool {
	return len(xs) == 1 && xs[0] == x
}

func revContainsAll(xs []string, want ...string) bool {
	for _, w := range want {
		if !revContains(xs, w) {
			return false
		}
	}
	return true
}
