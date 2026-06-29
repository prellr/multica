package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Draft annotation layer (Drafts slice 1). These tests cover:
//   - TestDraftAnnotationCreateAndList: create an annotation with an initial
//     message, list it back with its thread assembled (no N+1 gaps)
//   - TestDraftAnnotationThreading: a bare highlight has zero messages; adding
//     a message appends to the thread in order
//   - TestDraftAnnotationStateTransition: resolve/dismiss patches state and
//     preserves the anchor (COALESCE/narg)
//   - TestDraftAnnotationReanchorPersist: a re-anchor PATCH moves the anchor
//     without touching state
//   - TestDraftAnnotationDeleteCascade: deleting an annotation cascades its
//     messages
//   - TestDraftAnnotationRejectsBadUUID: a malformed annotation id never
//     reaches a write query (400/404, never a silent no-op)
//   - TestDraftAnnotationCrossOwnerDenied: an annotation on another owner's
//     draft is invisible (the parent draft 404s before the annotation is read)
//   - TestDraftAnnotationTypeEnumDrift: an unknown type downgrades, not 400s

// seedDraft inserts a draft owned by the default test user and registers
// cleanup. Returns its id.
func seedDraft(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	var draftID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO draft (workspace_id, owner_user_id, title, body)
		VALUES ($1, $2, $3, $4) RETURNING id::text
	`, testWorkspaceID, testUserID, "annotation host", "The quick brown fox jumps over the lazy dog.").Scan(&draftID); err != nil {
		t.Fatalf("seed draft: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM draft WHERE id = $1`, draftID)
	})
	return draftID
}

func TestDraftAnnotationCreateAndList(t *testing.T) {
	draftID := seedDraft(t)

	// Create an annotation with an initial comment message.
	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/drafts/"+draftID+"/annotations", map[string]any{
		"type":           "comment",
		"quote":          "quick brown fox",
		"context_before": "The ",
		"context_after":  " jumps",
		"pos_hint":       4,
		"message":        "is this the right animal?",
	})
	req = withURLParam(req, "id", draftID)
	testHandler.CreateDraftAnnotation(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateDraftAnnotation: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var created DraftAnnotationResponse
	if err := json.NewDecoder(w.Body).Decode(&created); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	if created.Type != "comment" {
		t.Fatalf("expected type 'comment', got %q", created.Type)
	}
	if created.State != "open" {
		t.Fatalf("expected default state 'open', got %q", created.State)
	}
	if created.Quote != "quick brown fox" || created.PosHint != 4 {
		t.Fatalf("anchor not round-tripped: quote=%q pos=%d", created.Quote, created.PosHint)
	}
	if created.AuthorType != "user" || created.AuthorUserID != testUserID {
		t.Fatalf("expected author user %s, got type=%q id=%q", testUserID, created.AuthorType, created.AuthorUserID)
	}
	if len(created.Messages) != 1 || created.Messages[0].Body != "is this the right animal?" {
		t.Fatalf("expected 1 initial message, got %+v", created.Messages)
	}

	// List should return the annotation with its thread assembled.
	w = httptest.NewRecorder()
	req = newRequest("GET", "/api/drafts/"+draftID+"/annotations", nil)
	req = withURLParam(req, "id", draftID)
	testHandler.ListDraftAnnotations(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("ListDraftAnnotations: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var listResp struct {
		Annotations []DraftAnnotationResponse `json:"annotations"`
		Total       int                       `json:"total"`
	}
	if err := json.NewDecoder(w.Body).Decode(&listResp); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if listResp.Total != 1 || len(listResp.Annotations) != 1 {
		t.Fatalf("expected 1 annotation, got total=%d len=%d", listResp.Total, len(listResp.Annotations))
	}
	if len(listResp.Annotations[0].Messages) != 1 {
		t.Fatalf("expected thread assembled in list, got %d messages", len(listResp.Annotations[0].Messages))
	}
}

func TestDraftAnnotationThreading(t *testing.T) {
	draftID := seedDraft(t)

	// A bare highlight: no initial message.
	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/drafts/"+draftID+"/annotations", map[string]any{
		"type":  "highlight",
		"quote": "lazy dog",
	})
	req = withURLParam(req, "id", draftID)
	testHandler.CreateDraftAnnotation(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateDraftAnnotation highlight: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var ann DraftAnnotationResponse
	json.NewDecoder(w.Body).Decode(&ann)
	if ann.Type != "highlight" {
		t.Fatalf("expected type 'highlight', got %q", ann.Type)
	}
	// Messages must be an empty slice, not null — the JSON contract.
	if ann.Messages == nil {
		t.Fatalf("expected non-nil empty messages slice for a bare highlight")
	}
	if len(ann.Messages) != 0 {
		t.Fatalf("expected 0 messages for a bare highlight, got %d", len(ann.Messages))
	}

	// Add two replies; they must come back in order.
	for _, body := range []string{"first reply", "second reply"} {
		w = httptest.NewRecorder()
		req = newRequest("POST", "/api/drafts/"+draftID+"/annotations/"+ann.ID+"/messages", map[string]any{
			"body": body,
		})
		req = withURLParam(req, "id", draftID)
		req = withURLParam(req, "aid", ann.ID)
		testHandler.AddDraftAnnotationMessage(w, req)
		if w.Code != http.StatusCreated {
			t.Fatalf("AddDraftAnnotationMessage %q: expected 201, got %d: %s", body, w.Code, w.Body.String())
		}
	}

	// Re-fetch via list; thread should hold both messages, oldest first.
	w = httptest.NewRecorder()
	req = newRequest("GET", "/api/drafts/"+draftID+"/annotations", nil)
	req = withURLParam(req, "id", draftID)
	testHandler.ListDraftAnnotations(w, req)
	var listResp struct {
		Annotations []DraftAnnotationResponse `json:"annotations"`
	}
	json.NewDecoder(w.Body).Decode(&listResp)
	if len(listResp.Annotations) != 1 {
		t.Fatalf("expected 1 annotation, got %d", len(listResp.Annotations))
	}
	msgs := listResp.Annotations[0].Messages
	if len(msgs) != 2 || msgs[0].Body != "first reply" || msgs[1].Body != "second reply" {
		t.Fatalf("thread not assembled in order: %+v", msgs)
	}
}

func TestDraftAnnotationStateTransition(t *testing.T) {
	draftID := seedDraft(t)

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/drafts/"+draftID+"/annotations", map[string]any{
		"type":     "block",
		"quote":    "brown fox",
		"pos_hint": 10,
	})
	req = withURLParam(req, "id", draftID)
	testHandler.CreateDraftAnnotation(w, req)
	var ann DraftAnnotationResponse
	json.NewDecoder(w.Body).Decode(&ann)

	// Patch state only — the anchor must be preserved (COALESCE/narg).
	w = httptest.NewRecorder()
	req = newRequest("PATCH", "/api/drafts/"+draftID+"/annotations/"+ann.ID, map[string]any{
		"state": "resolved",
	})
	req = withURLParam(req, "id", draftID)
	req = withURLParam(req, "aid", ann.ID)
	testHandler.UpdateDraftAnnotation(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("UpdateDraftAnnotation state: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var updated DraftAnnotationResponse
	json.NewDecoder(w.Body).Decode(&updated)
	if updated.State != "resolved" {
		t.Fatalf("expected state 'resolved', got %q", updated.State)
	}
	if updated.Quote != "brown fox" || updated.PosHint != 10 {
		t.Fatalf("state patch clobbered the anchor: quote=%q pos=%d", updated.Quote, updated.PosHint)
	}
}

func TestDraftAnnotationReanchorPersist(t *testing.T) {
	draftID := seedDraft(t)

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/drafts/"+draftID+"/annotations", map[string]any{
		"type":     "comment",
		"quote":    "quick brown",
		"pos_hint": 4,
		"message":  "anchor me",
	})
	req = withURLParam(req, "id", draftID)
	testHandler.CreateDraftAnnotation(w, req)
	var ann DraftAnnotationResponse
	json.NewDecoder(w.Body).Decode(&ann)

	// Re-anchor PATCH: move the anchor without touching state.
	w = httptest.NewRecorder()
	req = newRequest("PATCH", "/api/drafts/"+draftID+"/annotations/"+ann.ID, map[string]any{
		"quote":          "quick brown",
		"context_before": "A ",
		"context_after":  " fox",
		"pos_hint":       42,
	})
	req = withURLParam(req, "id", draftID)
	req = withURLParam(req, "aid", ann.ID)
	testHandler.UpdateDraftAnnotation(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("UpdateDraftAnnotation reanchor: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var updated DraftAnnotationResponse
	json.NewDecoder(w.Body).Decode(&updated)
	if updated.PosHint != 42 || updated.ContextBefore != "A " || updated.ContextAfter != " fox" {
		t.Fatalf("re-anchor did not persist: pos=%d before=%q after=%q", updated.PosHint, updated.ContextBefore, updated.ContextAfter)
	}
	// State untouched, and the thread is preserved across an anchor patch.
	if updated.State != "open" {
		t.Fatalf("re-anchor clobbered state: %q", updated.State)
	}
	if len(updated.Messages) != 1 {
		t.Fatalf("re-anchor dropped the thread, got %d messages", len(updated.Messages))
	}
}

func TestDraftAnnotationDeleteCascade(t *testing.T) {
	ctx := context.Background()
	draftID := seedDraft(t)

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/drafts/"+draftID+"/annotations", map[string]any{
		"type":    "comment",
		"quote":   "fox",
		"message": "to be deleted",
	})
	req = withURLParam(req, "id", draftID)
	testHandler.CreateDraftAnnotation(w, req)
	var ann DraftAnnotationResponse
	json.NewDecoder(w.Body).Decode(&ann)

	// Sanity: a message exists.
	var msgCount int
	testPool.QueryRow(ctx, `SELECT count(*) FROM draft_annotation_message WHERE annotation_id = $1`, ann.ID).Scan(&msgCount)
	if msgCount != 1 {
		t.Fatalf("expected 1 message before delete, got %d", msgCount)
	}

	// Delete the annotation.
	w = httptest.NewRecorder()
	req = newRequest("DELETE", "/api/drafts/"+draftID+"/annotations/"+ann.ID, nil)
	req = withURLParam(req, "id", draftID)
	req = withURLParam(req, "aid", ann.ID)
	testHandler.DeleteDraftAnnotation(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("DeleteDraftAnnotation: expected 204, got %d: %s", w.Code, w.Body.String())
	}

	// Both the annotation and its message must be gone (FK cascade).
	var annCount int
	testPool.QueryRow(ctx, `SELECT count(*) FROM draft_annotation WHERE id = $1`, ann.ID).Scan(&annCount)
	if annCount != 0 {
		t.Fatalf("annotation not deleted")
	}
	testPool.QueryRow(ctx, `SELECT count(*) FROM draft_annotation_message WHERE annotation_id = $1`, ann.ID).Scan(&msgCount)
	if msgCount != 0 {
		t.Fatalf("messages not cascaded on annotation delete, got %d", msgCount)
	}
}

// TestDraftAnnotationRejectsBadUUID guards the UUID-parsing convention: a
// malformed annotation id must never reach a write query. PATCH/DELETE on a
// non-UUID aid return 400 (and critically, DELETE must NOT 204).
func TestDraftAnnotationRejectsBadUUID(t *testing.T) {
	draftID := seedDraft(t)

	w := httptest.NewRecorder()
	req := newRequest("PATCH", "/api/drafts/"+draftID+"/annotations/not-a-uuid", map[string]any{"state": "resolved"})
	req = withURLParam(req, "id", draftID)
	req = withURLParam(req, "aid", "not-a-uuid")
	testHandler.UpdateDraftAnnotation(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("UpdateDraftAnnotation bad uuid: expected 400, got %d: %s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	req = newRequest("DELETE", "/api/drafts/"+draftID+"/annotations/not-a-uuid", nil)
	req = withURLParam(req, "id", draftID)
	req = withURLParam(req, "aid", "not-a-uuid")
	testHandler.DeleteDraftAnnotation(w, req)
	if w.Code == http.StatusNoContent {
		t.Fatalf("DeleteDraftAnnotation bad uuid: must not return 204; got %d", w.Code)
	}
	if w.Code != http.StatusBadRequest {
		t.Fatalf("DeleteDraftAnnotation bad uuid: expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

// TestDraftAnnotationCrossOwnerDenied confirms the parent draft is the
// authorization boundary: the requester cannot list or create annotations on a
// draft owned by another user (the draft 404s before any annotation work).
func TestDraftAnnotationCrossOwnerDenied(t *testing.T) {
	ctx := context.Background()

	var otherUserID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO "user" (name, email) VALUES ($1, $2) RETURNING id
	`, "Annotation Other Owner", "annotation-other-owner@multica.ai").Scan(&otherUserID); err != nil {
		t.Fatalf("seed other user: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(ctx, `DELETE FROM "user" WHERE id = $1`, otherUserID) })

	var otherDraftID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO draft (workspace_id, owner_user_id, title, body)
		VALUES ($1, $2, $3, $4) RETURNING id::text
	`, testWorkspaceID, otherUserID, "not yours", "secret body").Scan(&otherDraftID); err != nil {
		t.Fatalf("seed other draft: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(ctx, `DELETE FROM draft WHERE id = $1`, otherDraftID) })

	// List annotations on another owner's draft → 404 (draft invisible).
	w := httptest.NewRecorder()
	req := newRequest("GET", "/api/drafts/"+otherDraftID+"/annotations", nil)
	req = withURLParam(req, "id", otherDraftID)
	testHandler.ListDraftAnnotations(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("ListDraftAnnotations cross-owner: expected 404, got %d: %s", w.Code, w.Body.String())
	}

	// Create on another owner's draft → 404.
	w = httptest.NewRecorder()
	req = newRequest("POST", "/api/drafts/"+otherDraftID+"/annotations", map[string]any{"type": "comment", "quote": "secret"})
	req = withURLParam(req, "id", otherDraftID)
	testHandler.CreateDraftAnnotation(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("CreateDraftAnnotation cross-owner: expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

// TestDraftAnnotationTypeEnumDrift confirms the open-enum rule: an unknown type
// downgrades to 'comment' rather than 400ing.
func TestDraftAnnotationTypeEnumDrift(t *testing.T) {
	draftID := seedDraft(t)

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/drafts/"+draftID+"/annotations", map[string]any{
		"type":  "some_future_type",
		"quote": "fox",
	})
	req = withURLParam(req, "id", draftID)
	testHandler.CreateDraftAnnotation(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateDraftAnnotation unknown type: expected 201 (downgrade), got %d: %s", w.Code, w.Body.String())
	}
	var created DraftAnnotationResponse
	json.NewDecoder(w.Body).Decode(&created)
	if created.Type != "comment" {
		t.Fatalf("expected unknown type to downgrade to 'comment', got %q", created.Type)
	}
}

// 'question' is a first-class type (slice 2) — Aye's primary inquisitive verb.
// It must PERSIST as 'question', not collapse to 'comment' via the open-enum
// default. This asserts both the response AND the row in the DB (the CLI/MCP
// tests only check the outbound body against a mock, which can't catch a
// server-side downgrade).
func TestDraftAnnotationQuestionTypePersists(t *testing.T) {
	draftID := seedDraft(t)

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/drafts/"+draftID+"/annotations", map[string]any{
		"type":    "question",
		"quote":   "quick brown fox",
		"message": "Should this cover rollback?",
	})
	req = withURLParam(req, "id", draftID)
	testHandler.CreateDraftAnnotation(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateDraftAnnotation question: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var created DraftAnnotationResponse
	if err := json.NewDecoder(w.Body).Decode(&created); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	if created.Type != "question" {
		t.Fatalf("expected response type 'question', got %q", created.Type)
	}

	// The persisted row must be 'question', not a 'comment' downgrade.
	var persisted string
	if err := testPool.QueryRow(context.Background(),
		`SELECT type FROM draft_annotation WHERE id = $1`, created.ID,
	).Scan(&persisted); err != nil {
		t.Fatalf("load annotation row: %v", err)
	}
	if persisted != "question" {
		t.Fatalf("expected DB type 'question', got %q (silent downgrade to comment?)", persisted)
	}
}
