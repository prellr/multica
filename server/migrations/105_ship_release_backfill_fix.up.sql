-- Re-attempt the backfill from migration 104, this time without the
-- PR-state guard. Migration 104's NOT EXISTS check required both
-- pr.state = 'merged' and rpr.merge_state = 'merged' in the DB -- but
-- Ship never recorded those merges for these releases, which is
-- exactly why they're stuck. Drop the guard; issue.status = 'done'
-- is sufficient evidence for these hardcoded, already-verified releases.

UPDATE ship_release r
SET stage = 'done',
    done_at = COALESCE(done_at, NOW()),
    updated_at = NOW()
FROM issue i
JOIN workspace w ON w.id = i.workspace_id
WHERE r.issue_id = i.id
  AND i.number IN (303, 166, 163, 162, 161, 160, 159, 158, 146)
  AND w.issue_prefix = 'ROA'
  AND i.status = 'done'
  AND r.stage NOT IN ('done', 'rolled_back', 'cancelled');

UPDATE ship_release_pull_request rpr
SET is_active = FALSE
FROM ship_release r
JOIN issue i ON i.id = r.issue_id
JOIN workspace w ON w.id = i.workspace_id
WHERE rpr.release_id = r.id
  AND r.stage = 'done'
  AND w.issue_prefix = 'ROA'
  AND i.number IN (303, 166, 163, 162, 161, 160, 159, 158, 146);
