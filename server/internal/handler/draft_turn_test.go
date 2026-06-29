package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
)

// Drafts slice 2 — the Send-turn + the agent-authoring path. These cover:
//   - StartDraftTurn bad-UUID / cross-owner → 404 at the loader (no enqueue)
//   - StartDraftTurn with no Aye row → 409 (not a 500)
//   - StartDraftTurn happy path → 202 + a draft_turn task with the right context
//   - agent-authored annotation reply (X-Agent-ID + X-Task-ID) → author_type='agent'

// seedAyeForTest creates Aye's deterministic row for the test workspace with a
// runtime attached (EnqueueDraftTurn requires a runtime), and returns her id.
func seedAyeForTest(t *testing.T) string {
	t.Helper()
	wsUUID := util.MustParseUUID(testWorkspaceID)
	ayeID := AyeAgentID(wsUUID)
	ayeIDStr := util.UUIDToString(ayeID)
	// Idempotent: a prior test in the same run may have seeded her.
	if _, err := testPool.Exec(context.Background(), `
		INSERT INTO agent (
			id, workspace_id, name, description, runtime_mode, runtime_config,
			runtime_id, visibility, max_concurrent_tasks, owner_id, instructions
		)
		VALUES ($1, $2, 'Aye', '', 'local', '{}'::jsonb, $3, 'workspace', 1, $4, '')
		ON CONFLICT (id) DO UPDATE SET runtime_id = EXCLUDED.runtime_id
	`, ayeIDStr, testWorkspaceID, handlerTestRuntimeID(t), testUserID); err != nil {
		t.Fatalf("seed Aye: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE agent_id = $1`, ayeIDStr)
		testPool.Exec(context.Background(), `DELETE FROM agent WHERE id = $1`, ayeIDStr)
	})
	return ayeIDStr
}

func createDraftForTest(t *testing.T, title, body string) string {
	t.Helper()
	var draftID string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO draft (workspace_id, owner_user_id, title, body, status)
		VALUES ($1, $2, $3, $4, 'draft')
		RETURNING id
	`, testWorkspaceID, testUserID, title, body).Scan(&draftID); err != nil {
		t.Fatalf("create draft: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM draft WHERE id = $1`, draftID)
	})
	return draftID
}

func TestStartDraftTurn_RejectsBadUUID(t *testing.T) {
	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/drafts/not-a-uuid/turn", nil)
	req = withURLParam(req, "id", "not-a-uuid")
	testHandler.StartDraftTurn(w, req)
	// loadDraftForUser maps an unparseable id to 404 before any enqueue.
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for bad UUID, got %d: %s", w.Code, w.Body.String())
	}
}

func TestStartDraftTurn_RejectsCrossOwner(t *testing.T) {
	// A draft owned by a different user (random UUID) must not be turn-able by
	// the test user — the owner-scoped loader returns 404.
	otherUser := util.UUIDToString(util.MustParseUUID("11111111-1111-4111-8111-111111111111"))
	_, _ = testPool.Exec(context.Background(), `INSERT INTO "user" (id, name, email) VALUES ($1, 'Other', 'other-draftturn@test.local') ON CONFLICT (id) DO NOTHING`, otherUser)
	var draftID string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO draft (workspace_id, owner_user_id, title, body, status)
		VALUES ($1, $2, 'theirs', '', 'draft') RETURNING id
	`, testWorkspaceID, otherUser).Scan(&draftID); err != nil {
		t.Fatalf("seed foreign draft: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM draft WHERE id = $1`, draftID)
		testPool.Exec(context.Background(), `DELETE FROM "user" WHERE id = $1`, otherUser)
	})

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/drafts/"+draftID+"/turn", nil)
	req = withURLParam(req, "id", draftID)
	testHandler.StartDraftTurn(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for cross-owner draft, got %d: %s", w.Code, w.Body.String())
	}
}

func TestStartDraftTurn_NoAyeReturns409(t *testing.T) {
	// No Aye seeded for this workspace → 409, not 500.
	draftID := createDraftForTest(t, "needs aye", "# body")
	wsUUID := util.MustParseUUID(testWorkspaceID)
	// Ensure no Aye row exists for this workspace id.
	testPool.Exec(context.Background(), `DELETE FROM agent WHERE id = $1`, util.UUIDToString(AyeAgentID(wsUUID)))

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/drafts/"+draftID+"/turn", nil)
	req = withURLParam(req, "id", draftID)
	testHandler.StartDraftTurn(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409 when Aye is absent, got %d: %s", w.Code, w.Body.String())
	}
}

func TestStartDraftTurn_EnqueuesDraftTurn(t *testing.T) {
	ayeID := seedAyeForTest(t)
	draftID := createDraftForTest(t, "plan", "# Plan\n\nsection one")

	// Seed one open annotation so the snapshot is non-empty.
	var annID string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO draft_annotation (draft_id, workspace_id, author_type, author_user_id, type, quote, state)
		VALUES ($1, $2, 'user', $3, 'question', 'section one', 'open')
		RETURNING id
	`, draftID, testWorkspaceID, testUserID).Scan(&annID); err != nil {
		t.Fatalf("seed annotation: %v", err)
	}

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/drafts/"+draftID+"/turn", nil)
	req = withURLParam(req, "id", draftID)
	testHandler.StartDraftTurn(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", w.Code, w.Body.String())
	}
	var resp DraftTurnResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode turn resp: %v", err)
	}
	if resp.DraftID != draftID {
		t.Errorf("expected draft id %s, got %s", draftID, resp.DraftID)
	}
	if resp.AgentID != ayeID {
		t.Errorf("expected Aye %s, got %s", ayeID, resp.AgentID)
	}
	if resp.Status != "queued" {
		t.Errorf("expected status queued, got %s", resp.Status)
	}

	// The task row carries a draft_turn context with the draft id + the open
	// annotation snapshot.
	var ctxBytes []byte
	if err := testPool.QueryRow(context.Background(),
		`SELECT context FROM agent_task_queue WHERE id = $1`, resp.TaskID,
	).Scan(&ctxBytes); err != nil {
		t.Fatalf("load task context: %v", err)
	}
	var dtc service.DraftTurnContext
	if err := json.Unmarshal(ctxBytes, &dtc); err != nil {
		t.Fatalf("unmarshal draft_turn context: %v", err)
	}
	if dtc.Type != service.DraftTurnContextType {
		t.Errorf("expected context.type=%q, got %q", service.DraftTurnContextType, dtc.Type)
	}
	if dtc.DraftID != draftID {
		t.Errorf("context draft_id mismatch: %s", dtc.DraftID)
	}
	if dtc.WorkspaceID != testWorkspaceID {
		t.Errorf("context workspace_id mismatch: %s", dtc.WorkspaceID)
	}
	if len(dtc.OpenAnnotationIDs) != 1 || dtc.OpenAnnotationIDs[0] != annID {
		t.Errorf("expected open annotation snapshot [%s], got %v", annID, dtc.OpenAnnotationIDs)
	}
}

func TestCreateDraftAnnotation_AgentAuthorsAsAgent(t *testing.T) {
	// An agent caller (X-Agent-ID + X-Task-ID) authoring an annotation must
	// write author_type='agent' with a NULL author_user_id.
	ayeID := seedAyeForTest(t)
	draftID := createDraftForTest(t, "agent authoring", "# body\n\nthe load-bearing line")

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/drafts/"+draftID+"/annotations", map[string]any{
		"type":    "question",
		"quote":   "the load-bearing line",
		"message": "Should this cover rollback?",
	})
	req = withURLParam(req, "id", draftID)
	req = setAgentActor(t, req, ayeID)
	testHandler.CreateDraftAnnotation(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var ann DraftAnnotationResponse
	if err := json.NewDecoder(w.Body).Decode(&ann); err != nil {
		t.Fatalf("decode annotation: %v", err)
	}
	if ann.AuthorType != "agent" {
		t.Errorf("expected author_type=agent, got %q", ann.AuthorType)
	}
	if ann.AuthorUserID != "" {
		t.Errorf("expected NULL author_user_id for agent author, got %q", ann.AuthorUserID)
	}
	if len(ann.Messages) != 1 || ann.Messages[0].AuthorType != "agent" {
		t.Errorf("expected initial message authored by agent, got %+v", ann.Messages)
	}

	// Verify the DB row directly (defense against response-shape masking).
	var authorType string
	var ownerNull *string
	if err := testPool.QueryRow(context.Background(),
		`SELECT author_type, author_user_id::text FROM draft_annotation WHERE id = $1`, ann.ID,
	).Scan(&authorType, &ownerNull); err != nil {
		t.Fatalf("load annotation row: %v", err)
	}
	if authorType != "agent" {
		t.Errorf("DB author_type expected agent, got %q", authorType)
	}
	if ownerNull != nil {
		t.Errorf("DB author_user_id expected NULL for agent author, got %v", *ownerNull)
	}
}
