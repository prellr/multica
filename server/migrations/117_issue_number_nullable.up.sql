-- Make issue.number nullable so task rows (kind='task') can exist without a
-- human-readable identifier. Issues continue to carry sequential per-workspace
-- numbers; tasks are NULL. The unique constraint is replaced with a partial
-- unique index that only enforces uniqueness on rows that actually have a
-- number — NULL numbers do not collide with each other.
--
-- Notes for future readers:
--   * The constraint name uq_issue_workspace_number is preserved (now as an
--     index name) so docs / dashboards / error messages stay consistent.
--   * The handler-side CreateTask path MUST pass NULL for number — not 0.
--     `0` would compare equal across rows and the partial index would treat
--     them as a uniqueness collision.
--   * idx_issue_workspace_number from migration 020 remains for lookup by
--     (workspace_id, number) and is unaffected by this change.

ALTER TABLE issue ALTER COLUMN number DROP NOT NULL;
ALTER TABLE issue ALTER COLUMN number DROP DEFAULT;
ALTER TABLE issue DROP CONSTRAINT uq_issue_workspace_number;
CREATE UNIQUE INDEX uq_issue_workspace_number
    ON issue(workspace_id, number)
    WHERE number IS NOT NULL;
