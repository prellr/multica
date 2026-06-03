package embed

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/pgvector/pgvector-go"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Worker polls memory_artifact for rows whose embedding is missing or
// stale (updated since the last embed), batches them, and writes the
// embeddings back. Runs as a long-lived goroutine, started alongside
// the rest of the server's background tasks.
//
// Why not sync-on-create? Embedding via OpenAI adds ~100-300ms per
// call. The MCP and CLI write paths can't absorb that — the polisher
// alone makes dozens of writes per session. Async + eventual
// consistency is the right tradeoff for our scale: the first search
// after a write may miss the new row in the vector ranking but the
// FTS ranking catches it, and the worker fills the embedding within
// seconds.

type Worker struct {
	Queries *db.Queries
	Client  *Client

	// PollInterval — how often to wake up and check for unembedded
	// rows when there's nothing to do. 30s is responsive enough for
	// a substrate where most writes come from interactive UI / MCP.
	PollInterval time.Duration

	// BatchSize — number of rows to embed per OpenAI request. The
	// API caps at 2048 but at 50 we get a reasonable cost/latency
	// balance and bounded backpressure if the worker falls behind.
	BatchSize int

	// Logger — falls back to slog.Default() if nil.
	Logger *slog.Logger
}

// Run drives the worker loop until ctx is canceled. Errors from a single
// pass are logged and swallowed; a sustained failure (e.g. invalid API
// key, persistent rate limit) becomes a long string of warnings rather
// than crashing the server. Callers are expected to launch this with
// `go worker.Run(ctx)` from the main startup sequence.
func (w *Worker) Run(ctx context.Context) {
	log := w.logger()
	if w.Client == nil || w.Client.APIKey == "" {
		log.Info("memory-embed worker: no API key configured, idle")
		return
	}
	if w.BatchSize <= 0 {
		w.BatchSize = 50
	}
	if w.PollInterval <= 0 {
		w.PollInterval = 30 * time.Second
	}
	log.Info("memory-embed worker started",
		"model", w.Client.Model,
		"batch_size", w.BatchSize,
		"poll_interval", w.PollInterval.String())

	for {
		// Loop: process all available work in tight succession; when
		// a pass finds nothing to do, sleep for PollInterval before
		// trying again. This way a flood of writes drains quickly,
		// and an idle workspace doesn't pin the OpenAI budget.
		processed, err := w.tick(ctx)
		if errors.Is(err, context.Canceled) {
			log.Info("memory-embed worker stopped")
			return
		}
		if err != nil {
			log.Warn("memory-embed tick failed", "error", err)
		}
		if processed == 0 {
			select {
			case <-ctx.Done():
				log.Info("memory-embed worker stopped")
				return
			case <-time.After(w.PollInterval):
			}
		}
	}
}

// tick processes up to BatchSize rows. Returns the count processed; 0
// means "nothing pending, sleep before next poll."
func (w *Worker) tick(ctx context.Context) (int, error) {
	rows, err := w.Queries.ListMemoryArtifactsNeedingEmbedding(ctx, int32(w.BatchSize))
	if err != nil {
		return 0, fmt.Errorf("list pending: %w", err)
	}
	if len(rows) == 0 {
		return 0, nil
	}

	// Materialize the inputs in the same order as rows; later we
	// index back through the same slice to map embeddings → row IDs.
	inputs := make([]string, len(rows))
	for i, r := range rows {
		inputs[i] = r.Content
	}

	vecs, err := w.Client.Embed(ctx, inputs)
	if err != nil {
		return 0, fmt.Errorf("openai embed (batch=%d): %w", len(rows), err)
	}
	if len(vecs) != len(rows) {
		return 0, fmt.Errorf("embed count mismatch: rows=%d vecs=%d", len(rows), len(vecs))
	}

	// Per-row UPDATE. A bulk UPDATE via a single round-trip would be
	// faster but pgx's bulk path is awkward for vector — the per-row
	// path is fine at our throughput and keeps the failure mode local
	// (one bad row doesn't roll back the batch).
	for i, r := range rows {
		v := pgvector.NewVector(vecs[i])
		if err := w.Queries.UpdateMemoryArtifactEmbedding(ctx,
			db.UpdateMemoryArtifactEmbeddingParams{
				ID:        r.ID,
				Embedding: &v,
			}); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				// Row was deleted between SELECT and UPDATE — fine,
				// skip silently.
				continue
			}
			w.logger().Warn("memory-embed: update failed",
				"row_id", r.ID, "error", err)
		}
	}
	return len(rows), nil
}

func (w *Worker) logger() *slog.Logger {
	if w.Logger != nil {
		return w.Logger
	}
	return slog.Default()
}
