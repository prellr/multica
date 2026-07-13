-- Fold `tags` into the memory_artifact full-text vector.
--
-- The original content_tsv (migration 068) covered only title + content, so the
-- SearchMemoryArtifacts query (the MCP `memory_search` tool) could never match a
-- tag — even though the tool advertises "titles, content, and tags". A search
-- for e.g. `planning-doc-migration` returned nothing despite rows carrying that
-- tag. This makes the tool's promise true.
--
-- A generated column's expression can't be ALTERed in place, so we drop its GIN
-- index, drop and re-add the column with tags folded in, and recreate the index.
-- Re-adding a STORED generated column recomputes it for every existing row
-- (a table rewrite), so all current artifacts become tag-searchable immediately.
-- memory_artifact is small; the rewrite is quick.

DROP INDEX IF EXISTS idx_memory_tsv;

ALTER TABLE memory_artifact DROP COLUMN content_tsv;

ALTER TABLE memory_artifact
    ADD COLUMN content_tsv tsvector GENERATED ALWAYS AS (
        to_tsvector(
            'english',
            coalesce(title, '') || ' ' ||
            coalesce(content, '') || ' ' ||
            coalesce(array_to_string(tags, ' '), '')
        )
    ) STORED;

CREATE INDEX idx_memory_tsv ON memory_artifact USING GIN(content_tsv);
