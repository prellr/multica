-- Backfill Ship releases that were stranded in an active stage after
-- their Multica tracking issue was manually/externally marked done.
WITH affected AS (
    SELECT r.id
    FROM ship_release r
    JOIN issue i ON i.id = r.issue_id
    JOIN workspace w ON w.id = i.workspace_id
    WHERE i.number IN (
        303,
        166,
        163,
        162,
        161,
        160,
        159,
        158,
        146
    )
      AND w.issue_prefix = 'ROA'
      AND i.status = 'done'
      AND r.stage NOT IN ('done', 'rolled_back', 'cancelled')
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
            AND rpr.merge_state <> 'merged'
      )
)
UPDATE ship_release r
SET stage = 'done',
    done_at = COALESCE(done_at, NOW()),
    updated_at = NOW()
FROM affected
WHERE r.id = affected.id;

UPDATE ship_release_pull_request rpr
SET is_active = FALSE
FROM ship_release r
JOIN issue i ON i.id = r.issue_id
JOIN workspace w ON w.id = i.workspace_id
WHERE rpr.release_id = r.id
  AND r.stage = 'done'
  AND w.issue_prefix = 'ROA'
  AND i.number IN (
      303,
      166,
      163,
      162,
      161,
      160,
      159,
      158,
      146
  );
