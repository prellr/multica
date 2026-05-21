-- PR6 of the Ship Hub rebuild — direct-merge release synthesis.
--
-- When a developer merges a PR straight to the default branch (not via
-- Ship Hub's merge train), `processPush` only triggers a SyncProject —
-- no `ship_release` row is ever created. Direct merges (which still fire
-- production CD) therefore never appear in Ship's release history.
--
-- PR6 fixes that by synthesizing a release for direct merges. The push
-- webhook only carries the merge commit SHA (`after`), so to discover
-- WHICH PR produced that commit we need the PR's merge_commit_sha
-- recorded locally. GitHub's pull_request payload already exposes it
-- (gh.PullRequest.MergeCommitSHA); this column persists it.
--
-- NOT NULL DEFAULT '' so the column behaves like the other PR string
-- columns (head_sha, html_url) — un-merged PRs and rows synced before
-- this migration simply carry an empty string, and the synthesis lookup
-- skips empty SHAs explicitly.

ALTER TABLE pull_request
    ADD COLUMN merge_commit_sha TEXT NOT NULL DEFAULT '';

-- Reverse lookup for FindOrphanMergedPRsByMergeCommitSHA: given a
-- pushed merge commit on a project's default branch, find the merged
-- PR that produced it. Scoped by project_id to keep the index narrow.
CREATE INDEX idx_pull_request_project_merge_commit
    ON pull_request(project_id, merge_commit_sha);
