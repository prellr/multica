package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/multica-ai/multica/server/internal/util"
)

// AyeAgentID must be deterministic (same workspace → same agent id) and unique
// across workspaces (different workspace → different id). That's the contract
// that lets dev/test/E2E address Aye without a lookup while keeping the agent
// PK unique per workspace.
func TestAyeAgentID_DeterministicAndUnique(t *testing.T) {
	ws1 := util.MustParseUUID("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
	ws2 := util.MustParseUUID("bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb")

	id1a := AyeAgentID(ws1)
	id1b := AyeAgentID(ws1)
	id2 := AyeAgentID(ws2)

	if !id1a.Valid {
		t.Fatal("AyeAgentID returned an invalid UUID")
	}
	if util.UUIDToString(id1a) != util.UUIDToString(id1b) {
		t.Errorf("AyeAgentID not deterministic for the same workspace: %s vs %s",
			util.UUIDToString(id1a), util.UUIDToString(id1b))
	}
	if util.UUIDToString(id1a) == util.UUIDToString(id2) {
		t.Errorf("AyeAgentID collided across workspaces: %s", util.UUIDToString(id1a))
	}
}

// CreateWorkspace must seed Aye (agent row + attached skill) atomically. A
// brand-new workspace should always have an addressable Aye at her derived id,
// styled as an agent (visibility=workspace), with Layer 1 in instructions and
// the Layer 2 skill attached.
func TestCreateWorkspace_SeedsAye(t *testing.T) {
	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/workspaces", map[string]any{
		"name": "Aye Seed Test",
		"slug": "aye-seed-test",
	})
	testHandler.CreateWorkspace(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var ws struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(w.Body).Decode(&ws); err != nil {
		t.Fatalf("decode workspace: %v", err)
	}
	t.Cleanup(func() {
		// agent + skill + agent_skill cascade on workspace delete.
		testPool.Exec(context.Background(), `DELETE FROM workspace WHERE id = $1`, ws.ID)
	})

	wsUUID := util.MustParseUUID(ws.ID)
	ayeID := util.UUIDToString(AyeAgentID(wsUUID))

	var name, visibility, instructions string
	if err := testPool.QueryRow(context.Background(),
		`SELECT name, visibility, instructions FROM agent WHERE id = $1 AND workspace_id = $2`,
		ayeID, ws.ID,
	).Scan(&name, &visibility, &instructions); err != nil {
		t.Fatalf("Aye not seeded at derived id %s: %v", ayeID, err)
	}
	if name != "Aye" {
		t.Errorf("expected agent name Aye, got %q", name)
	}
	if visibility != "workspace" {
		t.Errorf("expected workspace visibility, got %q", visibility)
	}
	if len(instructions) == 0 {
		t.Error("expected Layer 1 instructions to be seeded, got empty")
	}

	// The Drafts surface skill is attached via agent_skill, and its content is
	// the Layer 2 prose.
	var skillContent string
	if err := testPool.QueryRow(context.Background(), `
		SELECT s.content
		FROM agent_skill ask
		JOIN skill s ON s.id = ask.skill_id
		WHERE ask.agent_id = $1
	`, ayeID).Scan(&skillContent); err != nil {
		t.Fatalf("Aye skill not attached: %v", err)
	}
	if len(skillContent) == 0 {
		t.Error("expected Layer 2 skill content, got empty")
	}
}
