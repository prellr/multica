DROP INDEX IF EXISTS idx_issue_workspace_kind;
ALTER TABLE issue DROP COLUMN IF EXISTS kind;
