-- PR7 of the Ship Hub rebuild — flaky CI repair.
--
-- recomputeCIStatus folds every check row on a PR's current head_sha and
-- lets a failure conclusion dominate the rollup. That breaks the
-- retry-passed case: a check that ran, passed, and then had a late-
-- arriving rerun row mutate its `conclusion` back to a failure variant
-- makes the rollup report failure even though CI did, in fact, pass.
--
-- This column records whether a check (per pr, head_sha, name) has EVER
-- had a `success` conclusion. UpsertPullRequestCheck keeps it sticky:
-- once true it never goes false again. recomputeCIStatus reads it under
-- a "best-ever" flag so a currently-failing-but-once-succeeded check
-- counts as success.
--
-- NOT NULL DEFAULT false so rows synced before this migration behave as
-- "never observed a success" — the worst case is the pre-PR7 behavior,
-- which is what those rows already exhibited.

ALTER TABLE pull_request_check
    ADD COLUMN ever_succeeded BOOLEAN NOT NULL DEFAULT false;
