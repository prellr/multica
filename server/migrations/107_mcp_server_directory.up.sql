CREATE TABLE mcp_server_directory (
    id              TEXT        PRIMARY KEY,
    name            TEXT        NOT NULL,
    slug            TEXT        NOT NULL UNIQUE,
    description     TEXT,
    transport_types TEXT[]      NOT NULL DEFAULT '{}',
    publisher_name  TEXT,
    homepage        TEXT,
    stars           INT         NOT NULL DEFAULT 0,
    search_vector   TSVECTOR    GENERATED ALWAYS AS (
        setweight(to_tsvector('english', name), 'A') ||
        setweight(to_tsvector('english', coalesce(description, '')), 'B')
    ) STORED,
    last_fetched_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX mcp_server_directory_fts ON mcp_server_directory USING gin(search_vector);
CREATE INDEX mcp_server_directory_transport ON mcp_server_directory USING gin(transport_types);
