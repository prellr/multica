-- Add a `kind` discriminator to issue so the same table can carry both formal
-- issues and lighter human-centric tasks. Default 'issue' preserves all existing
-- behavior; tasks are introduced in a subsequent migration along with their own
-- creation paths. Broad SELECT queries gain an explicit kind filter at the
-- application layer so a task row can never leak into an issue list.
ALTER TABLE issue
    ADD COLUMN kind TEXT NOT NULL DEFAULT 'issue'
    CHECK (kind IN ('issue', 'task'));

-- Compound index supports the (workspace_id, kind) filter applied by every
-- broad list/count query. Postgres can use idx_issue_workspace alone for the
-- single-column case, but the kind predicate is now ubiquitous and worth
-- indexing directly.
CREATE INDEX idx_issue_workspace_kind ON issue(workspace_id, kind);
