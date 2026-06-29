package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Drafts are a standalone, workspace- + owner-scoped entity. These tests
// cover:
//   - TestDraftCRUD: round-trip through create/get/list/update/delete, plus
//     the partial-update invariant (body-only PATCH preserves title/status)
//   - TestDraftUpdateRejectsBadUUID / TestDraftDeleteRejectsBadUUID: a
//     malformed id never reaches a write query — it 404s (the loader maps an
//     unparseable UUID to not-found)
//   - TestDraftCreateAllowsEmptyBody: clicking "New draft" with no payload is
//     a valid blank draft, not a 400
//   - TestDraftStatusEnumDrift: an unknown status downgrades to 'draft'
//     rather than 400ing (open-enum / enum-drift rule)

func TestDraftCRUD(t *testing.T) {
	// Create
	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/drafts?workspace_id="+testWorkspaceID, map[string]any{
		"title": "draft crud",
		"body":  "# hello\n\nsome markdown",
	})
	testHandler.CreateDraft(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateDraft: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var created DraftResponse
	if err := json.NewDecoder(w.Body).Decode(&created); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	if created.Title != "draft crud" {
		t.Fatalf("CreateDraft: expected title 'draft crud', got %q", created.Title)
	}
	if created.Status != "draft" {
		t.Fatalf("CreateDraft: expected default status 'draft', got %q", created.Status)
	}
	if created.OwnerUserID != testUserID {
		t.Fatalf("CreateDraft: expected owner %s, got %s", testUserID, created.OwnerUserID)
	}
	draftID := created.ID
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM draft WHERE id = $1`, draftID)
	})

	// Get
	w = httptest.NewRecorder()
	req = newRequest("GET", "/api/drafts/"+draftID, nil)
	req = withURLParam(req, "id", draftID)
	testHandler.GetDraft(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GetDraft: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var fetched DraftResponse
	if err := json.NewDecoder(w.Body).Decode(&fetched); err != nil {
		t.Fatalf("decode get: %v", err)
	}
	if fetched.ID != draftID {
		t.Fatalf("GetDraft: expected id %s, got %s", draftID, fetched.ID)
	}
	if fetched.Body != "# hello\n\nsome markdown" {
		t.Fatalf("GetDraft: body not round-tripped, got %q", fetched.Body)
	}

	// Update — partial (body only). Title and status must be preserved.
	w = httptest.NewRecorder()
	req = newRequest("PATCH", "/api/drafts/"+draftID, map[string]any{
		"body": "## edited",
	})
	req = withURLParam(req, "id", draftID)
	testHandler.UpdateDraft(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("UpdateDraft: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var updated DraftResponse
	if err := json.NewDecoder(w.Body).Decode(&updated); err != nil {
		t.Fatalf("decode update: %v", err)
	}
	if updated.Body != "## edited" {
		t.Fatalf("UpdateDraft: expected body '## edited', got %q", updated.Body)
	}
	if updated.Title != "draft crud" {
		t.Fatalf("UpdateDraft: title should be preserved across partial patch, got %q", updated.Title)
	}
	if updated.Status != "draft" {
		t.Fatalf("UpdateDraft: status should be preserved across partial patch, got %q", updated.Status)
	}

	// List — should include our draft.
	w = httptest.NewRecorder()
	req = newRequest("GET", "/api/drafts?workspace_id="+testWorkspaceID, nil)
	testHandler.ListDrafts(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("ListDrafts: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var listResp map[string]any
	json.NewDecoder(w.Body).Decode(&listResp)
	drafts, _ := listResp["drafts"].([]any)
	found := false
	for _, item := range drafts {
		m, _ := item.(map[string]any)
		if m["id"] == draftID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("ListDrafts: created draft %s not present in response", draftID)
	}

	// Delete
	w = httptest.NewRecorder()
	req = newRequest("DELETE", "/api/drafts/"+draftID, nil)
	req = withURLParam(req, "id", draftID)
	testHandler.DeleteDraft(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("DeleteDraft: expected 204, got %d: %s", w.Code, w.Body.String())
	}

	// Verify deleted.
	w = httptest.NewRecorder()
	req = newRequest("GET", "/api/drafts/"+draftID, nil)
	req = withURLParam(req, "id", draftID)
	testHandler.GetDraft(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("GetDraft after delete: expected 404, got %d", w.Code)
	}
}

// TestDraftCreateAllowsEmptyBody confirms a blank "New draft" (no JSON
// payload) is a valid create, not a 400.
func TestDraftCreateAllowsEmptyBody(t *testing.T) {
	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/drafts?workspace_id="+testWorkspaceID, nil)
	testHandler.CreateDraft(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateDraft empty body: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var created DraftResponse
	json.NewDecoder(w.Body).Decode(&created)
	if created.Title != "" || created.Body != "" {
		t.Fatalf("CreateDraft empty body: expected blank title/body, got title=%q body=%q", created.Title, created.Body)
	}
	if created.Status != "draft" {
		t.Fatalf("CreateDraft empty body: expected status 'draft', got %q", created.Status)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM draft WHERE id = $1`, created.ID)
	})
}

// TestDraftStatusEnumDrift confirms the open-enum rule: an unknown status
// value downgrades to the canonical 'draft' rather than 400ing, so an older
// server stays forward-compatible with a newer client.
func TestDraftStatusEnumDrift(t *testing.T) {
	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/drafts?workspace_id="+testWorkspaceID, map[string]any{
		"title":  "enum drift probe",
		"status": "some_future_status_we_dont_know",
	})
	testHandler.CreateDraft(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateDraft unknown status: expected 201 (downgrade, not reject), got %d: %s", w.Code, w.Body.String())
	}
	var created DraftResponse
	json.NewDecoder(w.Body).Decode(&created)
	if created.Status != "draft" {
		t.Fatalf("CreateDraft unknown status: expected downgrade to 'draft', got %q", created.Status)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM draft WHERE id = $1`, created.ID)
	})
}

// TestDraftUpdateRejectsBadUUID guards the UUID-parsing convention: a path id
// that is not a valid UUID must never reach the UPDATE query. The loader maps
// it to 404 — and critically must NOT 204/200 (silent no-op data loss class,
// see #1661).
func TestDraftUpdateRejectsBadUUID(t *testing.T) {
	w := httptest.NewRecorder()
	req := newRequest("PATCH", "/api/drafts/not-a-uuid", map[string]any{"title": "x"})
	req = withURLParam(req, "id", "not-a-uuid")
	testHandler.UpdateDraft(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("UpdateDraft bad uuid: expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

// TestDraftDeleteRejectsBadUUID is the symmetric delete-side guard.
func TestDraftDeleteRejectsBadUUID(t *testing.T) {
	w := httptest.NewRecorder()
	req := newRequest("DELETE", "/api/drafts/not-a-uuid", nil)
	req = withURLParam(req, "id", "not-a-uuid")
	testHandler.DeleteDraft(w, req)
	if w.Code == http.StatusNoContent {
		t.Fatalf("DeleteDraft bad uuid: must not return 204; got %d", w.Code)
	}
	if w.Code != http.StatusNotFound {
		t.Fatalf("DeleteDraft bad uuid: expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

// TestDraftGetDeniesOtherOwner confirms owner scoping: a draft owned by a
// different user in the same workspace is invisible (404) to the requester.
// Slice-0 drafts are single-author; this is the guard that keeps them so.
func TestDraftGetDeniesOtherOwner(t *testing.T) {
	ctx := context.Background()

	// Seed a second user in the same workspace and a draft they own.
	var otherUserID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO "user" (name, email) VALUES ($1, $2) RETURNING id
	`, "Draft Other Owner", "draft-other-owner@multica.ai").Scan(&otherUserID); err != nil {
		t.Fatalf("seed other user: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM "user" WHERE id = $1`, otherUserID)
	})

	var otherDraftID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO draft (workspace_id, owner_user_id, title, body)
		VALUES ($1, $2, $3, $4) RETURNING id::text
	`, testWorkspaceID, otherUserID, "not yours", "secret body").Scan(&otherDraftID); err != nil {
		t.Fatalf("seed other draft: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM draft WHERE id = $1`, otherDraftID)
	})

	// The default test request authenticates as testUserID — that user must
	// not be able to read another owner's draft.
	w := httptest.NewRecorder()
	req := newRequest("GET", "/api/drafts/"+otherDraftID, nil)
	req = withURLParam(req, "id", otherDraftID)
	testHandler.GetDraft(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("GetDraft other owner's draft: expected 404, got %d: %s", w.Code, w.Body.String())
	}

	// And it must not show up in the requester's list.
	w = httptest.NewRecorder()
	req = newRequest("GET", "/api/drafts?workspace_id="+testWorkspaceID, nil)
	testHandler.ListDrafts(w, req)
	var listResp map[string]any
	json.NewDecoder(w.Body).Decode(&listResp)
	drafts, _ := listResp["drafts"].([]any)
	for _, item := range drafts {
		m, _ := item.(map[string]any)
		if m["id"] == otherDraftID {
			t.Fatalf("ListDrafts leaked another owner's draft %s", otherDraftID)
		}
	}
}
