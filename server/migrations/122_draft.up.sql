-- Drafts: a living markdown document a user co-authors with an agent. Slice 0
-- ships only the standalone entity + single-user CRUD; later slices layer on
-- annotations, the agent, Yjs co-editing, and graduation. The owner column is
-- named `owner_user_id` (not a single-user flag) on purpose — forward-compat
-- for the future multi-author surface; slice 0 simply scopes every query to
-- the requesting user.
CREATE TABLE draft (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    owner_user_id UUID NOT NULL REFERENCES "user"(id) ON DELETE CASCADE,
    title TEXT NOT NULL DEFAULT '',
    body TEXT NOT NULL DEFAULT '',
    -- Open enum. Slice 0 only writes 'draft'; future values (e.g. 'accepted')
    -- arrive without a migration. Intentionally NOT a CHECK constraint so a
    -- newer server can introduce a value an older one never saw — the Go side
    -- switches on it with a `default` branch, the TS side treats unknown values
    -- as a generic fallback (per the repo's enum-drift rules).
    status TEXT NOT NULL DEFAULT 'draft',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Drives the default list query: drafts for one owner in one workspace, newest
-- edit first. The compound shape lets Postgres satisfy the WHERE + ORDER BY in
-- a single index scan.
CREATE INDEX idx_draft_workspace_owner_updated
    ON draft(workspace_id, owner_user_id, updated_at DESC);
