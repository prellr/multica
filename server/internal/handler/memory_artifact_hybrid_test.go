package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/multica-ai/multica/server/internal/service/memory/embed"
)

// Hybrid-search handler coverage. The actual RRF blend lives in
// SearchMemoryArtifactsHybrid; this test exercises the handler's
// wiring around it: mode=hybrid is required, an unconfigured client
// returns 400, the query is embedded once and threaded through, and
// the response carries mode=hybrid back to the caller.

// hasPgvector probes for the vector extension. The CI image
// (pgvector/pgvector:pg17) has it; local dev Postgres often doesn't.
// Tests that need the column gracefully skip when the extension is
// absent rather than failing on an unrelated environment quirk.
func hasPgvector(t *testing.T) bool {
	t.Helper()
	if testHandler == nil {
		return false
	}
	var found bool
	row := testHandler.DB.QueryRow(t.Context(),
		"SELECT EXISTS (SELECT 1 FROM pg_extension WHERE extname='vector')")
	if err := row.Scan(&found); err != nil {
		return false
	}
	return found
}

func TestSearchMemoryArtifactsHybrid_Returns400WithoutEmbedClient(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	// Save + restore so other tests still see whatever client was set.
	prev := testHandler.MemoryEmbedClient
	testHandler.MemoryEmbedClient = nil
	t.Cleanup(func() { testHandler.MemoryEmbedClient = prev })

	w := httptest.NewRecorder()
	req := newRequest("GET",
		"/api/memory/search?workspace_id="+testWorkspaceID+"&q=anything&mode=hybrid", nil)
	testHandler.SearchMemoryArtifacts(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 when embed client unconfigured, got %d %s",
			w.Code, w.Body.String())
	}
}

func TestSearchMemoryArtifactsHybrid_RoundTrip(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	if !hasPgvector(t) {
		t.Skip("pgvector extension not available locally (CI image has it)")
	}
	// Fake OpenAI — embeds whatever inputs the handler sends and
	// returns 1536-dim zero vectors. We're testing wire-up, not the
	// quality of the RRF blend (that's exercised by the SQL itself).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Input []string `json:"input"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		out := struct {
			Data []map[string]any `json:"data"`
		}{Data: make([]map[string]any, len(body.Input))}
		for i := range body.Input {
			out.Data[i] = map[string]any{
				"embedding": make([]float32, 1536),
				"index":     i,
			}
		}
		json.NewEncoder(w).Encode(out)
	}))
	defer srv.Close()

	prev := testHandler.MemoryEmbedClient
	testHandler.MemoryEmbedClient = &embed.Client{
		APIKey: "test-key", Model: "text-embedding-3-small", BaseURL: srv.URL,
	}
	t.Cleanup(func() { testHandler.MemoryEmbedClient = prev })

	// Seed one artifact whose content the query will hit on FTS, so
	// the response has something to surface. Its embedding is NULL
	// (the test doesn't run the worker) — the RRF still includes it
	// via the FTS leg.
	cw := httptest.NewRecorder()
	creq := newRequest("POST", "/api/memory?workspace_id="+testWorkspaceID, map[string]any{
		"kind": "decision", "title": "Hybrid test", "content": "Postgres beats Mongo for this portal.",
	})
	testHandler.CreateMemoryArtifact(cw, creq)
	if cw.Code != http.StatusCreated {
		t.Fatalf("seed: %d %s", cw.Code, cw.Body.String())
	}
	var seeded MemoryArtifactResponse
	json.NewDecoder(cw.Body).Decode(&seeded)
	t.Cleanup(func() {
		req := newRequest("DELETE", "/api/memory/"+seeded.ID, nil)
		req = withURLParam(req, "id", seeded.ID)
		testHandler.DeleteMemoryArtifact(httptest.NewRecorder(), req)
	})

	w := httptest.NewRecorder()
	req := newRequest("GET",
		"/api/memory/search?workspace_id="+testWorkspaceID+"&q=postgres&mode=hybrid", nil)
	testHandler.SearchMemoryArtifacts(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("hybrid search: %d %s", w.Code, w.Body.String())
	}
	var resp struct {
		Artifacts []MemoryArtifactResponse `json:"memory_artifacts"`
		Mode      string                   `json:"mode"`
	}
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.Mode != "hybrid" {
		t.Errorf("mode echo: want hybrid, got %q", resp.Mode)
	}
	found := false
	for _, a := range resp.Artifacts {
		if a.ID == seeded.ID {
			found = true
		}
	}
	if !found {
		t.Errorf("hybrid search did not return the seeded artifact: %+v", resp.Artifacts)
	}
}

func TestSearchMemoryArtifactsHybrid_DegradesOnEmbedFailure(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	if !hasPgvector(t) {
		t.Skip("pgvector extension not available locally (CI image has it)")
	}
	// Fake OpenAI returns 500 — handler should set the degraded
	// header and fall back to FTS-only rather than 500ing the search.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":{"message":"upstream down"}}`))
	}))
	defer srv.Close()
	prev := testHandler.MemoryEmbedClient
	testHandler.MemoryEmbedClient = &embed.Client{
		APIKey: "test-key", Model: "text-embedding-3-small", BaseURL: srv.URL,
	}
	t.Cleanup(func() { testHandler.MemoryEmbedClient = prev })

	w := httptest.NewRecorder()
	req := newRequest("GET",
		"/api/memory/search?workspace_id="+testWorkspaceID+"&q=anything&mode=hybrid", nil)
	testHandler.SearchMemoryArtifacts(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected graceful degrade to 200, got %d %s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("X-Memory-Search-Degraded"); got != "fts-only" {
		t.Errorf("X-Memory-Search-Degraded: want fts-only, got %q", got)
	}
}
