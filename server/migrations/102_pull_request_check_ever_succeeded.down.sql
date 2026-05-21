-- Reverse of 102_pull_request_check_ever_succeeded.up.sql.
--
-- Safe to drop unconditionally: no FK references the column and the
-- best-ever CI rollup is the only reader. Dropping it reverts
-- recomputeCIStatus to the pre-PR7 "latest conclusion wins" behavior.

ALTER TABLE pull_request_check
    DROP COLUMN IF EXISTS ever_succeeded;
