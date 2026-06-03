// Package embed wraps the OpenAI embeddings API + a background worker
// that fills the memory_artifact.embedding column as content arrives.
//
// Hand-rolled net/http rather than the openai-go SDK: the embeddings
// endpoint is a single POST, the worker only needs batch send + parse,
// and pulling in the SDK would add a meaningful dep for one call.
//
// Model: text-embedding-3-small (1536 dims) by default, configurable
// via MEMORY_EMBED_MODEL env. Cost: ~$0.02 per 1M input tokens — at
// our corpus size this is hundredths of a cent per full re-embed.
package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	defaultModel  = "text-embedding-3-small"
	defaultDim    = 1536
	openaiBaseURL = "https://api.openai.com/v1/embeddings"

	// requestTimeout — OpenAI's embeddings endpoint typically returns
	// in well under a second; 30s is a generous ceiling that still
	// won't pin the worker if the API hangs.
	requestTimeout = 30 * time.Second
)

// Client embeds text via the OpenAI embeddings API. Safe for concurrent
// use; the embedded http.Client is the zero value with its own pooling.
type Client struct {
	APIKey  string
	Model   string
	BaseURL string

	HTTPClient *http.Client
}

// NewClient — apiKey is required. model + baseURL fall back to
// production defaults so callers can construct with just the key.
func NewClient(apiKey, model string) *Client {
	if model == "" {
		model = defaultModel
	}
	return &Client{
		APIKey:     apiKey,
		Model:      model,
		BaseURL:    openaiBaseURL,
		HTTPClient: &http.Client{Timeout: requestTimeout},
	}
}

// Embed returns embeddings for the supplied inputs in the same order
// as they were given. OpenAI accepts up to 2048 inputs per request +
// 8191 tokens per input; the caller should batch within those bounds.
//
// Errors are wrapped with enough context to identify the call site
// (model name + first 80 chars of the first input) without dumping
// any embedding values.
func (c *Client) Embed(ctx context.Context, inputs []string) ([][]float32, error) {
	if c.APIKey == "" {
		return nil, errors.New("embed: APIKey is required")
	}
	if len(inputs) == 0 {
		return nil, nil
	}

	reqBody, err := json.Marshal(map[string]any{
		"model": c.Model,
		"input": inputs,
	})
	if err != nil {
		return nil, fmt.Errorf("embed: marshal: %w", err)
	}

	url := c.BaseURL
	if url == "" {
		url = openaiBaseURL
	}
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("embed: new request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	req.Header.Set("Content-Type", "application/json")

	httpClient := c.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: requestTimeout}
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embed: post: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// OpenAI returns a JSON error body — surface it verbatim so
		// rate-limit and quota messages aren't lost in transit. The
		// preview is bounded so a giant 5xx body can't blow up the log.
		const previewMax = 400
		preview := string(body)
		if len(preview) > previewMax {
			preview = preview[:previewMax] + "…"
		}
		return nil, fmt.Errorf("embed: %s returned %d: %s",
			c.Model, resp.StatusCode, preview)
	}

	var parsed struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
			Index     int       `json:"index"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("embed: parse response: %w", err)
	}
	if len(parsed.Data) != len(inputs) {
		return nil, fmt.Errorf("embed: expected %d embeddings, got %d",
			len(inputs), len(parsed.Data))
	}

	// OpenAI returns objects with their own `index` — order is usually
	// stable but we sort by index anyway to be safe before unrolling.
	out := make([][]float32, len(inputs))
	for _, d := range parsed.Data {
		if d.Index < 0 || d.Index >= len(out) {
			return nil, fmt.Errorf("embed: response index %d out of range", d.Index)
		}
		if len(d.Embedding) != defaultDim {
			// Only enforce when using the default model. A different
			// model legitimately returns a different dimension; the
			// caller's column width is what would constrain that.
			if c.Model == defaultModel {
				return nil, fmt.Errorf("embed: expected %d-dim vector, got %d (model %s)",
					defaultDim, len(d.Embedding), c.Model)
			}
		}
		out[d.Index] = d.Embedding
	}
	return out, nil
}
