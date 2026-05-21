-- Reverse of 106_ship_release_is_direct_merge.up.sql.
--
-- Only the schema change is cleanly reversible. The backfill UPDATEs
-- (stage -> 'done', ladder timestamps, is_active flips) are NOT reverted:
-- there is no record of which releases were `assembling` before the
-- migration ran, and reopening a release that has since legitimately
-- shipped would be worse than leaving it done. Dropping the column is
-- enough to undo the schema; the data stays reconciled.
ALTER TABLE ship_release
    DROP COLUMN IF EXISTS is_direct_merge;
