package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Draft conversation rail (Rail-1). These tests cover:
//   - TestDraftMessageCreateAndList: post a message, list it back oldest-first
//   - TestDraftMessageEmptyBodyRejected: an empty body is a 400, never a write
//   - TestDraftMessageRejectsBadDraftUUID: a malformed draft id never reaches a
//     write query (404 — the draft is invisible)
//   - TestDraftMessageCrossOwnerDenied: messages on another owner's draft are
//     invisible (the parent draft 404s before any message work)
//
// The conversation rail is un-anchored: a flat per-draft log with no message-id
// path param, so there is no PATCH/DELETE bad-UUID surface like the annotation
// thread has. The bad-UUID guard here is on the draft id (the only request
// boundary).

func TestDraftMessageCreateAndList(t *testing.T) {
	draftID := seedDraft(t)

	// Post two messages; they must come back oldest-first.
	for _, body := range []string{"first thought", "second thought"} {
		w := httptest.NewRecorder()
		req := newRequest("POST", "/api/drafts/"+draftID+"/messages", map[string]any{
			"body": body,
		})
		req = withURLParam(req, "id", draftID)
		testHandler.CreateDraftMessage(w, req)
		if w.Code != http.StatusCreated {
			t.Fatalf("CreateDraftMessage %q: expected 201, got %d: %s", body, w.Code, w.Body.String())
		}
		var created DraftMessageResponse
		if err := json.NewDecoder(w.Body).Decode(&created); err != nil {
			t.Fatalf("decode create: %v", err)
		}
		if created.Body != body {
			t.Fatalf("body not round-tripped: got %q", created.Body)
		}
		if created.DraftID != draftID {
			t.Fatalf("expected draft_id %s, got %q", draftID, created.DraftID)
		}
		if created.AuthorType != "user" || created.AuthorUserID != testUserID {
			t.Fatalf("expected author user %s, got type=%q id=%q", testUserID, created.AuthorType, created.AuthorUserID)
		}
	}

	// List returns both, oldest-first.
	w := httptest.NewRecorder()
	req := newRequest("GET", "/api/drafts/"+draftID+"/messages", nil)
	req = withURLParam(req, "id", draftID)
	testHandler.ListDraftMessages(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("ListDraftMessages: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var listResp struct {
		Messages []DraftMessageResponse `json:"messages"`
		Total    int                    `json:"total"`
	}
	if err := json.NewDecoder(w.Body).Decode(&listResp); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if listResp.Total != 2 || len(listResp.Messages) != 2 {
		t.Fatalf("expected 2 messages, got total=%d len=%d", listResp.Total, len(listResp.Messages))
	}
	if listResp.Messages[0].Body != "first thought" || listResp.Messages[1].Body != "second thought" {
		t.Fatalf("conversation not assembled oldest-first: %+v", listResp.Messages)
	}
}

// TestDraftMessageEmptyBodyRejected confirms an empty body is a 400 and never a
// write (the rail has no "bare" message — every message carries content).
func TestDraftMessageEmptyBodyRejected(t *testing.T) {
	draftID := seedDraft(t)

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/drafts/"+draftID+"/messages", map[string]any{
		"body": "",
	})
	req = withURLParam(req, "id", draftID)
	testHandler.CreateDraftMessage(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("CreateDraftMessage empty body: expected 400, got %d: %s", w.Code, w.Body.String())
	}

	// Nothing was written.
	var count int
	testPool.QueryRow(context.Background(),
		`SELECT count(*) FROM draft_message WHERE draft_id = $1`, draftID,
	).Scan(&count)
	if count != 0 {
		t.Fatalf("empty-body create wrote a row, got %d", count)
	}
}

// TestDraftMessageRejectsBadDraftUUID guards the UUID-parsing convention: a
// malformed draft id never reaches a write query (loadDraftForUser 404s on an
// unparseable id — the draft is invisible).
func TestDraftMessageRejectsBadDraftUUID(t *testing.T) {
	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/drafts/not-a-uuid/messages", map[string]any{"body": "hi"})
	req = withURLParam(req, "id", "not-a-uuid")
	testHandler.CreateDraftMessage(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("CreateDraftMessage bad draft uuid: expected 404, got %d: %s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	req = newRequest("GET", "/api/drafts/not-a-uuid/messages", nil)
	req = withURLParam(req, "id", "not-a-uuid")
	testHandler.ListDraftMessages(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("ListDraftMessages bad draft uuid: expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

// TestDraftMessageCrossOwnerDenied confirms the parent draft is the
// authorization boundary: the requester cannot list or post messages on a draft
// owned by another user (the draft 404s before any message work).
func TestDraftMessageCrossOwnerDenied(t *testing.T) {
	ctx := context.Background()

	var otherUserID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO "user" (name, email) VALUES ($1, $2) RETURNING id
	`, "Message Other Owner", "message-other-owner@multica.ai").Scan(&otherUserID); err != nil {
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

	// List messages on another owner's draft → 404 (draft invisible).
	w := httptest.NewRecorder()
	req := newRequest("GET", "/api/drafts/"+otherDraftID+"/messages", nil)
	req = withURLParam(req, "id", otherDraftID)
	testHandler.ListDraftMessages(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("ListDraftMessages cross-owner: expected 404, got %d: %s", w.Code, w.Body.String())
	}

	// Post on another owner's draft → 404.
	w = httptest.NewRecorder()
	req = newRequest("POST", "/api/drafts/"+otherDraftID+"/messages", map[string]any{"body": "secret"})
	req = withURLParam(req, "id", otherDraftID)
	testHandler.CreateDraftMessage(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("CreateDraftMessage cross-owner: expected 404, got %d: %s", w.Code, w.Body.String())
	}
}
