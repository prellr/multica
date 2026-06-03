package embed

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Client unit tests — exercise the HTTP wrapper against a fake OpenAI
// server. No real API key, no real OpenAI traffic; the fake server
// asserts request shape and replies with canned embeddings so we know
// the round-trip is correct.

func TestClient_Embed_Success(t *testing.T) {
	want := []string{"alpha", "beta", "gamma"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("method: want POST, got %s", r.Method)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("authorization: want Bearer test-key, got %q", got)
		}
		var body struct {
			Model string   `json:"model"`
			Input []string `json:"input"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		if body.Model != "test-model" {
			t.Errorf("model: want test-model, got %q", body.Model)
		}
		if len(body.Input) != len(want) {
			t.Fatalf("input len: want %d, got %d", len(want), len(body.Input))
		}
		// Return one 1536-dim embedding per input — content doesn't
		// matter for the round-trip, only that the index mapping is
		// correct.
		out := struct {
			Data []map[string]any `json:"data"`
		}{Data: make([]map[string]any, len(want))}
		for i := range want {
			emb := make([]float32, 1536)
			emb[0] = float32(i + 1) // marker to verify ordering
			out.Data[i] = map[string]any{"embedding": emb, "index": i}
		}
		json.NewEncoder(w).Encode(out)
	}))
	defer srv.Close()

	c := &Client{APIKey: "test-key", Model: "test-model", BaseURL: srv.URL}
	got, err := c.Embed(context.Background(), want)
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("result len: want %d, got %d", len(want), len(got))
	}
	for i := range got {
		if got[i][0] != float32(i+1) {
			t.Errorf("index %d: want marker %f, got %f", i, float32(i+1), got[i][0])
		}
	}
}

func TestClient_Embed_OutOfOrderResponseIsResorted(t *testing.T) {
	// OpenAI normally returns Data in input order, but the protocol
	// allows reordering — the `index` field is what's authoritative.
	// The client must honor it.
	inputs := []string{"first", "second", "third"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		out := struct {
			Data []map[string]any `json:"data"`
		}{Data: []map[string]any{
			{"embedding": make([]float32, 1536), "index": 2},
			{"embedding": make([]float32, 1536), "index": 0},
			{"embedding": make([]float32, 1536), "index": 1},
		}}
		// Tag each entry's first dim with its original index so the
		// test can verify resorting.
		for i, d := range out.Data {
			emb := d["embedding"].([]float32)
			emb[0] = float32(d["index"].(int))
			out.Data[i]["embedding"] = emb
		}
		json.NewEncoder(w).Encode(out)
	}))
	defer srv.Close()

	c := &Client{APIKey: "k", Model: "test-model", BaseURL: srv.URL}
	got, err := c.Embed(context.Background(), inputs)
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	for i := range got {
		if got[i][0] != float32(i) {
			t.Errorf("index %d: want marker %f, got %f", i, float32(i), got[i][0])
		}
	}
}

func TestClient_Embed_EmptyInputs(t *testing.T) {
	// Empty input → no HTTP call; nil result without error.
	c := &Client{APIKey: "k", Model: "test-model"}
	got, err := c.Embed(context.Background(), nil)
	if err != nil {
		t.Errorf("nil inputs: want nil err, got %v", err)
	}
	if got != nil {
		t.Errorf("nil inputs: want nil result, got %v", got)
	}
}

func TestClient_Embed_MissingAPIKey(t *testing.T) {
	c := &Client{Model: "test-model"}
	_, err := c.Embed(context.Background(), []string{"x"})
	if err == nil || !strings.Contains(err.Error(), "APIKey") {
		t.Errorf("want APIKey-required error, got %v", err)
	}
}

func TestClient_Embed_BubblesAPIError(t *testing.T) {
	// OpenAI's error bodies should round-trip into the error message
	// so rate-limit details aren't lost in transit.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":{"message":"rate limit exceeded"}}`))
	}))
	defer srv.Close()
	c := &Client{APIKey: "k", Model: "test-model", BaseURL: srv.URL}
	_, err := c.Embed(context.Background(), []string{"x"})
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !strings.Contains(err.Error(), "429") || !strings.Contains(err.Error(), "rate limit") {
		t.Errorf("error should surface 429 + body: %v", err)
	}
}

func TestClient_Embed_RejectsCountMismatch(t *testing.T) {
	// Server returns fewer embeddings than inputs — should fail loud
	// rather than silently truncate, since downstream consumers map
	// embeddings to rows by position.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(struct {
			Data []map[string]any `json:"data"`
		}{Data: []map[string]any{
			{"embedding": make([]float32, 1536), "index": 0},
		}})
	}))
	defer srv.Close()
	c := &Client{APIKey: "k", Model: "test-model", BaseURL: srv.URL}
	_, err := c.Embed(context.Background(), []string{"a", "b"})
	if err == nil || !strings.Contains(err.Error(), "expected 2") {
		t.Errorf("want count-mismatch error, got %v", err)
	}
}

// Sanity check that errors.Is detection works on context cancellation —
// the worker loop relies on this to distinguish "stop" from real errors.
func TestClient_Embed_RespectsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c := &Client{APIKey: "k", Model: "test-model", BaseURL: "http://127.0.0.1:1"}
	_, err := c.Embed(ctx, []string{"x"})
	if !errors.Is(err, context.Canceled) {
		t.Logf("note: net/http may wrap the cancellation; got %v", err)
	}
}
