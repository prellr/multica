-- Reverse 117. If any task rows exist (number IS NULL), this will fail at the
-- NOT NULL step — that is intentional. Down-migrating after tasks have been
-- created requires explicit cleanup of those rows by the operator.
DROP INDEX IF EXISTS uq_issue_workspace_number;
ALTER TABLE issue ALTER COLUMN number SET DEFAULT 0;
ALTER TABLE issue ALTER COLUMN number SET NOT NULL;
ALTER TABLE issue ADD CONSTRAINT uq_issue_workspace_number UNIQUE (workspace_id, number);
