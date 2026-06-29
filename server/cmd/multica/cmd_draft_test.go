package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Draft MCP tools (Drafts slice 2 — the agent-capability surface). These
// exercise each multica_draft_* registration end-to-end against an httptest
// server, reusing invokeShipTool / resultText from cmd_mcp_test.go (both are
// generic — they register the full toolset and dispatch by name). We assert on
// the URL+method+body the client actually hit and the body round-tripped to
// the model.
// ---------------------------------------------------------------------------

func TestDraftMCP_Get(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.Write([]byte(`{"id":"d1","title":"Plan","body":"# Hi","status":"draft"}`))
	}))
	defer srv.Close()

	res := invokeShipTool(t, srv, "multica_draft_get", map[string]any{"id": "d1"})
	if res.IsError {
		t.Fatalf("unexpected tool error: %s", resultText(t, res))
	}
	if gotMethod != http.MethodGet || gotPath != "/api/drafts/d1" {
		t.Errorf("expected GET /api/drafts/d1, got %s %s", gotMethod, gotPath)
	}
	if got := resultText(t, res); !strings.Contains(got, `"title":"Plan"`) {
		t.Errorf("expected verbatim body, got %q", got)
	}

	// Missing required id short-circuits before any HTTP call.
	gotPath = ""
	res2 := invokeShipTool(t, srv, "multica_draft_get", map[string]any{})
	if gotPath != "" {
		t.Errorf("server should not have been hit; got %s", gotPath)
	}
	if !strings.Contains(resultText(t, res2), "id") {
		t.Errorf("expected error to name the missing arg, got %q", resultText(t, res2))
	}
}

func TestDraftMCP_Annotations_FiltersStateClientSide(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/drafts/d1/annotations" {
			t.Errorf("expected /api/drafts/d1/annotations, got %s", r.URL.Path)
		}
		// Two annotations: one open, one resolved.
		w.Write([]byte(`{"annotations":[` +
			`{"id":"a1","state":"open","type":"question"},` +
			`{"id":"a2","state":"resolved","type":"comment"}` +
			`],"total":2}`))
	}))
	defer srv.Close()

	res := invokeShipTool(t, srv, "multica_draft_annotations", map[string]any{
		"id":    "d1",
		"state": "open",
	})
	if res.IsError {
		t.Fatalf("unexpected tool error: %s", resultText(t, res))
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(resultText(t, res)), &out); err != nil {
		t.Fatalf("result not JSON: %v", err)
	}
	list, _ := out["annotations"].([]any)
	if len(list) != 1 {
		t.Fatalf("expected 1 open annotation after client-side filter, got %d", len(list))
	}
	if total, _ := out["total"].(float64); total != 1 {
		t.Errorf("expected total recomputed to 1, got %v", out["total"])
	}
	first, _ := list[0].(map[string]any)
	if first["id"] != "a1" {
		t.Errorf("expected the open annotation a1, got %v", first["id"])
	}
}

func TestDraftMCP_Reply(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		json.Unmarshal(raw, &gotBody)
		w.Write([]byte(`{"id":"m1","body":"sounds right","author_type":"agent"}`))
	}))
	defer srv.Close()

	res := invokeShipTool(t, srv, "multica_draft_reply", map[string]any{
		"id":            "d1",
		"annotation_id": "a1",
		"body":          "sounds right",
	})
	if res.IsError {
		t.Fatalf("unexpected tool error: %s", resultText(t, res))
	}
	if gotMethod != http.MethodPost || gotPath != "/api/drafts/d1/annotations/a1/messages" {
		t.Errorf("expected POST /api/drafts/d1/annotations/a1/messages, got %s %s", gotMethod, gotPath)
	}
	if gotBody["body"] != "sounds right" {
		t.Errorf("expected body forwarded, got %v", gotBody["body"])
	}

	// Missing body short-circuits.
	gotPath = ""
	res2 := invokeShipTool(t, srv, "multica_draft_reply", map[string]any{"id": "d1", "annotation_id": "a1"})
	if gotPath != "" {
		t.Errorf("server should not have been hit; got %s", gotPath)
	}
	if !strings.Contains(resultText(t, res2), "body") {
		t.Errorf("expected error to name the missing arg, got %q", resultText(t, res2))
	}
}

func TestDraftMCP_SetState(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		json.Unmarshal(raw, &gotBody)
		w.Write([]byte(`{"id":"a1","state":"resolved"}`))
	}))
	defer srv.Close()

	res := invokeShipTool(t, srv, "multica_draft_set_state", map[string]any{
		"id":            "d1",
		"annotation_id": "a1",
		"state":         "resolved",
	})
	if res.IsError {
		t.Fatalf("unexpected tool error: %s", resultText(t, res))
	}
	if gotMethod != http.MethodPatch || gotPath != "/api/drafts/d1/annotations/a1" {
		t.Errorf("expected PATCH /api/drafts/d1/annotations/a1, got %s %s", gotMethod, gotPath)
	}
	if gotBody["state"] != "resolved" {
		t.Errorf("expected state forwarded, got %v", gotBody["state"])
	}
}

func TestDraftMCP_Annotate_Question(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		json.Unmarshal(raw, &gotBody)
		w.Write([]byte(`{"id":"a3","type":"question","state":"open"}`))
	}))
	defer srv.Close()

	res := invokeShipTool(t, srv, "multica_draft_annotate", map[string]any{
		"id":    "d1",
		"quote": "§4 rollback",
		"type":  "question",
		"body":  "Should this cover rollback?",
	})
	if res.IsError {
		t.Fatalf("unexpected tool error: %s", resultText(t, res))
	}
	if gotMethod != http.MethodPost || gotPath != "/api/drafts/d1/annotations" {
		t.Errorf("expected POST /api/drafts/d1/annotations, got %s %s", gotMethod, gotPath)
	}
	if gotBody["type"] != "question" || gotBody["quote"] != "§4 rollback" {
		t.Errorf("expected type+quote forwarded, got %v", gotBody)
	}
	if gotBody["message"] != "Should this cover rollback?" {
		t.Errorf("expected --body mapped to message, got %v", gotBody["message"])
	}
	// A non-suggestion must not carry suggestion fields.
	if _, has := gotBody["suggestion_after"]; has {
		t.Errorf("question annotation should not include suggestion_after")
	}
}

func TestDraftMCP_Annotate_Suggestion_RequiresAfter(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		json.Unmarshal(raw, &body)
		w.Write([]byte(`{"id":"a4","type":"suggestion"}`))
	}))
	defer srv.Close()

	// suggestion without suggestion_after → short-circuit error, no HTTP.
	res := invokeShipTool(t, srv, "multica_draft_annotate", map[string]any{
		"id":    "d1",
		"quote": "old text",
		"type":  "suggestion",
	})
	if gotPath != "" {
		t.Errorf("server should not have been hit; got %s", gotPath)
	}
	// The validation short-circuit returns an error CallToolResult from inside
	// the handler closure; toolHandler marshals it to a text block (same
	// convention as the missing-required-arg path), so we assert on the text.
	if !strings.Contains(resultText(t, res), "suggestion_after") {
		t.Errorf("expected error naming suggestion_after, got %q", resultText(t, res))
	}

	// With suggestion_after, suggestion_before defaults to quote.
	var gotBody map[string]any
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		json.Unmarshal(raw, &gotBody)
		w.Write([]byte(`{"id":"a4","type":"suggestion"}`))
	}))
	defer srv2.Close()

	res2 := invokeShipTool(t, srv2, "multica_draft_annotate", map[string]any{
		"id":               "d1",
		"quote":            "old text",
		"type":             "suggestion",
		"suggestion_after": "new text",
	})
	if res2.IsError {
		t.Fatalf("unexpected tool error: %s", resultText(t, res2))
	}
	if gotBody["suggestion_before"] != "old text" {
		t.Errorf("expected suggestion_before to default to quote, got %v", gotBody["suggestion_before"])
	}
	if gotBody["suggestion_after"] != "new text" {
		t.Errorf("expected suggestion_after forwarded, got %v", gotBody["suggestion_after"])
	}
}
