DROP INDEX IF EXISTS memory_artifact_embedding_hnsw_idx;
ALTER TABLE memory_artifact
    DROP COLUMN IF EXISTS embedding,
    DROP COLUMN IF EXISTS embedded_at;
-- Intentionally NOT dropping the vector extension; other tables may add
-- vector columns and the extension itself is safe to leave around.
