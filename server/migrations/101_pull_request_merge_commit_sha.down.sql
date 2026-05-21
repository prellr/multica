-- Reverse of 101_pull_request_merge_commit_sha.up.sql.
--
-- Safe to drop unconditionally: no FK references the column, and the
-- synthesis path is the only reader. Dropping the column removes the
-- index with it; the explicit DROP INDEX keeps the down migration
-- readable and order-independent.

DROP INDEX IF EXISTS idx_pull_request_project_merge_commit;

ALTER TABLE pull_request
    DROP COLUMN IF EXISTS merge_commit_sha;
