-- Revert content_tsv to title + content only (drops tags from the FTS vector).

DROP INDEX IF EXISTS idx_memory_tsv;

ALTER TABLE memory_artifact DROP COLUMN content_tsv;

ALTER TABLE memory_artifact
    ADD COLUMN content_tsv tsvector GENERATED ALWAYS AS (
        to_tsvector('english', coalesce(title, '') || ' ' || coalesce(content, ''))
    ) STORED;

CREATE INDEX idx_memory_tsv ON memory_artifact USING GIN(content_tsv);
