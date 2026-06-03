-- Phase 2 of the memory_artifact substrate (foreshadowed in migration
-- 068's comments): add a pgvector embedding column + HNSW index so the
-- search endpoint can blend full-text rank with semantic similarity.
--
-- Dimensionality: 1536 matches OpenAI text-embedding-3-small, the
-- model we ship with. The number is intentional, not arbitrary — the
-- embedding column's width is BAKED IN; swapping to a different
-- dimensionality model requires a migration. We picked the small
-- model for cost (~$0.02/M tokens, 6× cheaper than -large) at the
-- expense of some quality; that tradeoff is reversible by re-embedding
-- the corpus, but the schema width isn't.
--
-- Async fill: `embedded_at` tracks when we last embedded each row.
-- The background worker selects rows where `embedding IS NULL OR
-- updated_at > embedded_at` (stale or never-embedded) and batches
-- them through the OpenAI embeddings API. We deliberately don't
-- block writes on the embedding call — sync embed adds ~100ms to
-- every create/update which the MCP/CLI throughput can't absorb.
--
-- Index: HNSW + cosine ops. HNSW (vs IVFFlat) costs more to build
-- but gives better recall at query time, and at our corpus size
-- (~hundreds of memory artifacts) the build cost is fine. Cosine
-- distance matches what OpenAI's embeddings are calibrated for.

CREATE EXTENSION IF NOT EXISTS vector;

ALTER TABLE memory_artifact
    ADD COLUMN embedding   vector(1536),
    ADD COLUMN embedded_at TIMESTAMPTZ;

-- HNSW index for cosine-distance kNN search. The m + ef_construction
-- defaults (m=16, ef_construction=64) are pgvector's general-purpose
-- recommendation; tune later if recall is unsatisfying on a real
-- corpus.
--
-- Partial: only index rows with non-null embeddings. The worker
-- populates them lazily, and a NULL row has nothing for HNSW to
-- ANN-search against. A partial index also keeps the structure
-- small while embeddings are catching up after a fresh deploy.
CREATE INDEX memory_artifact_embedding_hnsw_idx
    ON memory_artifact USING hnsw (embedding vector_cosine_ops)
    WHERE embedding IS NOT NULL;
