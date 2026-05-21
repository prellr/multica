-- ROA-311 — direct-merge releases (and older stranded releases) stuck on
-- the Active Releases rail showing "assembling" forever.
--
-- Root cause: PR6's Service.synthesizeDirectMergeRelease writes a
-- ship_release for a PR merged directly to the default branch, but only
-- stamps merged_main_sha + PR membership — it never stamps the timestamp
-- ladder (merged_at / promoted_at). DeriveReleaseStage (Phase 1) derives
-- stage purely from that ladder, so with every column NULL it falls
-- through to step (9): `assembling`. Multica auto-deploys on every merge,
-- so the rail floods with synthesized direct-merge releases that can
-- never advance. A separate set of older non-synthetic releases stranded
-- the same way (their PRs all merged, tracking issues already `done`,
-- but Ship never stamped merged_at).
--
-- Migrations 104 + 105 were earlier ROA-311 attempts that backfilled a
-- hardcoded set of 9 stranded non-synthetic releases. They did NOT add
-- the column below and did NOT touch the synthesized direct-merge flood;
-- this migration is the durable, predicate-based follow-up. Kept as a
-- separate migration (not a rewrite of 104) because 104/105 are already
-- applied in production — the runner tracks by name and would never
-- re-run a rewritten file.
--
-- This migration:
--   1. Adds ship_release.is_direct_merge so the derivation can apply the
--      24h auto-done rule to direct-merge releases that legitimately
--      never get a production_deploy_id linked.
--   2. Backfills the two stuck cohorts to `done` so they drop off the
--      Active rail immediately.

-- (1) Column. A release synthesized from a direct merge to the default
-- branch represents an already-completed ship; the flag lets
-- DeriveReleaseStage treat the merge itself as evidence the deploy fired.
ALTER TABLE ship_release
    ADD COLUMN is_direct_merge BOOLEAN NOT NULL DEFAULT false;

-- (2a) Backfill — synthesized direct-merge releases (the flood).
-- Identified by the exact description PR6 writes plus stage='assembling'.
-- These are old completed merges: stamp the ladder from created_at, mark
-- them done so they drop off the rail. done_at makes DeriveReleaseStage
-- step (3) return `done` stickily.
UPDATE ship_release
SET is_direct_merge = true,
    merged_at       = COALESCE(merged_at, created_at),
    promoted_at     = COALESCE(promoted_at, created_at),
    done_at         = COALESCE(done_at, NOW()),
    stage           = 'done',
    updated_at      = NOW()
WHERE stage = 'assembling'
  AND description = 'Auto-synthesized by Ship Hub for a direct merge to the default branch.';

-- (2b) Backfill — non-synthetic stranded releases. Any release still in
-- `assembling` that has >=1 member PR AND every member PR has merged.
-- The EXISTS guards against zero-member releases (genuinely still
-- assembling — must NOT be matched); the NOT EXISTS guards against any
-- unmerged member PR. Idempotent with 104/105 — already-done releases
-- are excluded by the stage='assembling' filter.
UPDATE ship_release r
SET stage      = 'done',
    done_at    = COALESCE(r.done_at, NOW()),
    merged_at  = COALESCE(r.merged_at, r.created_at),
    updated_at = NOW()
WHERE r.stage = 'assembling'
  AND EXISTS (
      SELECT 1
      FROM ship_release_pull_request rpr
      WHERE rpr.release_id = r.id
  )
  AND NOT EXISTS (
      SELECT 1
      FROM ship_release_pull_request rpr
      JOIN pull_request pr ON pr.id = rpr.pull_request_id
      WHERE rpr.release_id = r.id
        AND pr.state <> 'merged'
  );

-- Free the now-done releases' PRs from the partial unique index
-- `(pull_request_id) WHERE is_active=TRUE` so they can join a future
-- release (mirrors DeactivateReleasePullRequests). Scoped to releases
-- this migration just transitioned to done: a direct-merge release, or
-- a non-synthetic release with every member PR merged.
UPDATE ship_release_pull_request rpr
SET is_active = FALSE
FROM ship_release r
WHERE rpr.release_id = r.id
  AND rpr.is_active = TRUE
  AND r.stage = 'done'
  AND (
      r.is_direct_merge = true
      OR EXISTS (
          SELECT 1
          FROM ship_release_pull_request rpr2
          JOIN pull_request pr ON pr.id = rpr2.pull_request_id
          WHERE rpr2.release_id = r.id
          GROUP BY rpr2.release_id
          HAVING bool_and(pr.state = 'merged')
      )
  );
