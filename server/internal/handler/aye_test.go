package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
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

// seedUnboundAye inserts Aye's deterministic row for the test workspace with a
// NULL runtime_id (the freshly-seeded, no-daemon-yet state) and returns her id.
func seedUnboundAye(t *testing.T) string {
	t.Helper()
	wsUUID := util.MustParseUUID(testWorkspaceID)
	ayeID := util.UUIDToString(AyeAgentID(wsUUID))
	if _, err := testPool.Exec(context.Background(), `
		INSERT INTO agent (
			id, workspace_id, name, description, runtime_mode, runtime_config,
			runtime_id, visibility, max_concurrent_tasks, owner_id, instructions,
			custom_env, custom_args
		)
		VALUES ($1, $2, 'Aye', '', 'local', '{}'::jsonb, NULL, 'workspace', 1, $3, '', '{}'::jsonb, '[]'::jsonb)
		ON CONFLICT (id) DO UPDATE SET runtime_id = NULL
	`, ayeID, testWorkspaceID, testUserID); err != nil {
		t.Fatalf("seed unbound Aye: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM agent WHERE id = $1`, ayeID)
	})
	return ayeID
}

func ayeRuntimeID(t *testing.T, ayeID string) (string, bool) {
	t.Helper()
	var rt *string
	if err := testPool.QueryRow(context.Background(),
		`SELECT runtime_id::text FROM agent WHERE id = $1`, ayeID,
	).Scan(&rt); err != nil {
		t.Fatalf("load Aye runtime_id: %v", err)
	}
	if rt == nil {
		return "", false
	}
	return *rt, true
}

// Registering a daemon runtime must auto-bind a previously-unbound Aye, so the
// workspace's first `make daemon` makes her runnable with no manual step.
func TestDaemonRegister_AutoBindsUnboundAye(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ayeID := seedUnboundAye(t)
	if _, bound := ayeRuntimeID(t, ayeID); bound {
		t.Fatal("precondition: Aye should start unbound")
	}

	w := httptest.NewRecorder()
	req := newDaemonTokenRequest("POST", "/api/daemon/register", map[string]any{
		"workspace_id": testWorkspaceID,
		"daemon_id":    "test-daemon-autobind",
		"device_name":  "autobind-device",
		"runtimes": []map[string]any{
			{"name": "autobind-runtime", "type": "claude", "version": "1.0.0", "status": "online"},
		},
	}, testWorkspaceID, "test-daemon-autobind")
	testHandler.DaemonRegister(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("DaemonRegister: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	runtimes, _ := resp["runtimes"].([]any)
	if len(runtimes) == 0 {
		t.Fatal("expected a registered runtime in the response")
	}
	registeredRuntimeID := runtimes[0].(map[string]any)["id"].(string)
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM agent_runtime WHERE id = $1`, registeredRuntimeID)
	})

	bound, ok := ayeRuntimeID(t, ayeID)
	if !ok {
		t.Fatal("expected Aye to be auto-bound to a runtime after daemon register")
	}
	if bound != registeredRuntimeID {
		t.Errorf("expected Aye bound to the registered runtime %s, got %s", registeredRuntimeID, bound)
	}
}

// BindBuiltinAgentRuntime must NOT rebind an already-bound agent (it only binds
// when runtime_id IS NULL), and must never touch a user-created agent.
func TestBindBuiltinAgentRuntime_IdempotentAndScoped(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ayeID := seedUnboundAye(t)
	wsUUID := util.MustParseUUID(testWorkspaceID)

	// Two runtimes: bind to the first, then attempt a rebind to the second.
	rt1 := createClaimReclaimRuntime(t, context.Background(), "bind-rt-1")
	rt2 := createClaimReclaimRuntime(t, context.Background(), "bind-rt-2")

	n, err := testHandler.Queries.BindBuiltinAgentRuntime(context.Background(), bindParams(ayeID, rt1, testWorkspaceID))
	if err != nil {
		t.Fatalf("first bind: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected first bind to affect 1 row, got %d", n)
	}

	// Second bind must be a no-op — she's no longer NULL.
	n, err = testHandler.Queries.BindBuiltinAgentRuntime(context.Background(), bindParams(ayeID, rt2, testWorkspaceID))
	if err != nil {
		t.Fatalf("second bind: %v", err)
	}
	if n != 0 {
		t.Errorf("expected second bind to be a no-op (already bound), affected %d rows", n)
	}
	if bound, _ := ayeRuntimeID(t, ayeID); bound != rt1 {
		t.Errorf("expected Aye to stay bound to rt1 %s, got %s", rt1, bound)
	}

	// A user-created agent at a DIFFERENT id must never be touched by a bind
	// keyed on Aye's id, even if it's unbound.
	userAgentID := createHandlerTestAgent(t, "user-agent-bind", []byte("[]"))
	testPool.Exec(context.Background(), `UPDATE agent SET runtime_id = NULL WHERE id = $1`, userAgentID)
	n, err = testHandler.Queries.BindBuiltinAgentRuntime(context.Background(), bindParams(util.UUIDToString(AyeAgentID(wsUUID)), rt2, testWorkspaceID))
	if err != nil {
		t.Fatalf("bind keyed on Aye id: %v", err)
	}
	if n != 0 {
		t.Errorf("a bind keyed on Aye's id must not touch any other agent, affected %d rows", n)
	}
}

// A workspace created before Drafts shipped has no Aye row. BackfillAyeAgents
// must seed her correctly (agent at AyeAgentID + the Drafts skill + the
// agent_skill link), attributed to the workspace owner — and a second run must
// be a no-op (no duplicate, no error).
func TestBackfillAyeAgents_SeedsMissingAndIsIdempotent(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()

	// A standalone owner user + workspace WITHOUT Aye — the pre-Drafts state.
	var ownerID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO "user" (name, email) VALUES ($1, $2) RETURNING id
	`, "Backfill Owner", "aye-backfill-owner@multica.ai").Scan(&ownerID); err != nil {
		t.Fatalf("create owner: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM "user" WHERE id = $1`, ownerID)
	})

	var wsID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO workspace (name, slug, issue_prefix)
		VALUES ($1, $2, $3) RETURNING id
	`, "Aye Backfill WS", "aye-backfill-ws", "ABF").Scan(&wsID); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	t.Cleanup(func() {
		// agent + skill + agent_skill + member cascade on workspace delete.
		testPool.Exec(ctx, `DELETE FROM workspace WHERE id = $1`, wsID)
	})
	if _, err := testPool.Exec(ctx, `
		INSERT INTO member (workspace_id, user_id, role) VALUES ($1, $2, 'owner')
	`, wsID, ownerID); err != nil {
		t.Fatalf("add owner member: %v", err)
	}

	wsUUID := util.MustParseUUID(wsID)
	ayeID := util.UUIDToString(AyeAgentID(wsUUID))

	// Precondition: Aye must NOT exist yet.
	var preCount int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM agent WHERE id = $1`, ayeID).Scan(&preCount); err != nil {
		t.Fatalf("precondition probe: %v", err)
	}
	if preCount != 0 {
		t.Fatalf("precondition: expected no Aye before backfill, found %d", preCount)
	}

	// First backfill run — seeds Aye.
	BackfillAyeAgents(ctx, testHandler.Queries, testPool)

	var name, visibility, instructions string
	var seededOwner string
	if err := testPool.QueryRow(ctx,
		`SELECT name, visibility, instructions, owner_id::text FROM agent WHERE id = $1 AND workspace_id = $2`,
		ayeID, wsID,
	).Scan(&name, &visibility, &instructions, &seededOwner); err != nil {
		t.Fatalf("Aye not seeded by backfill at %s: %v", ayeID, err)
	}
	if name != "Aye" {
		t.Errorf("expected agent name Aye, got %q", name)
	}
	if visibility != "workspace" {
		t.Errorf("expected workspace visibility, got %q", visibility)
	}
	if len(instructions) == 0 {
		t.Error("expected Layer 1 instructions, got empty")
	}
	if seededOwner != ownerID {
		t.Errorf("expected skill/agent attributed to owner %s, got owner_id %s", ownerID, seededOwner)
	}

	// The Drafts surface skill is attached via agent_skill and created_by the owner.
	var skillContent, skillCreatedBy string
	if err := testPool.QueryRow(ctx, `
		SELECT s.content, s.created_by::text
		FROM agent_skill ask
		JOIN skill s ON s.id = ask.skill_id
		WHERE ask.agent_id = $1
	`, ayeID).Scan(&skillContent, &skillCreatedBy); err != nil {
		t.Fatalf("Aye skill not attached by backfill: %v", err)
	}
	if len(skillContent) == 0 {
		t.Error("expected Layer 2 skill content, got empty")
	}
	if skillCreatedBy != ownerID {
		t.Errorf("expected skill created_by owner %s, got %s", ownerID, skillCreatedBy)
	}

	// Second run must be a no-op: still exactly one Aye, one skill, one link.
	BackfillAyeAgents(ctx, testHandler.Queries, testPool)

	var agentCount, skillLinkCount int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM agent WHERE id = $1`, ayeID).Scan(&agentCount); err != nil {
		t.Fatalf("post-rerun agent count: %v", err)
	}
	if agentCount != 1 {
		t.Errorf("expected exactly 1 Aye after re-run, got %d", agentCount)
	}
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM agent_skill WHERE agent_id = $1`, ayeID).Scan(&skillLinkCount); err != nil {
		t.Fatalf("post-rerun skill-link count: %v", err)
	}
	if skillLinkCount != 1 {
		t.Errorf("expected exactly 1 agent_skill link after re-run, got %d", skillLinkCount)
	}
}

func bindParams(agentID, runtimeID, workspaceID string) db.BindBuiltinAgentRuntimeParams {
	return db.BindBuiltinAgentRuntimeParams{
		ID:          util.MustParseUUID(agentID),
		RuntimeID:   util.MustParseUUID(runtimeID),
		WorkspaceID: util.MustParseUUID(workspaceID),
	}
}
