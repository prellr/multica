-- name: CreateReminder :one
INSERT INTO reminder (
    workspace_id, creator_type, creator_id,
    recipient_type, recipient_id, kind, title, body, issue_id, remind_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7,
    sqlc.narg('body'), sqlc.narg('issue_id'), sqlc.narg('remind_at')
) RETURNING *;

-- name: GetReminder :one
SELECT * FROM reminder WHERE id = $1;

-- name: GetReminderInWorkspace :one
SELECT * FROM reminder WHERE id = $1 AND workspace_id = $2;

-- name: ListReminders :many
SELECT * FROM reminder
WHERE workspace_id = $1
  AND (sqlc.narg('recipient_type')::text IS NULL OR recipient_type = sqlc.narg('recipient_type'))
  AND (sqlc.narg('recipient_id')::uuid IS NULL OR recipient_id = sqlc.narg('recipient_id'))
  AND (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status'))
  AND (sqlc.narg('kind')::text IS NULL OR kind = sqlc.narg('kind'))
ORDER BY created_at DESC
LIMIT COALESCE(sqlc.narg('limit_val')::int, 50);

-- name: CancelReminder :one
UPDATE reminder SET status = 'cancelled', updated_at = now()
WHERE id = $1 AND status = 'pending'
RETURNING *;

-- name: ClaimDueReminders :many
-- Atomically claim all due reminders to prevent double-delivery.
UPDATE reminder SET status = 'delivered', delivered_at = now(), updated_at = now()
WHERE status = 'pending'
  AND remind_at IS NOT NULL
  AND remind_at <= now()
RETURNING *;

-- name: MarkReminderDelivered :exec
UPDATE reminder SET status = 'delivered', delivered_at = now(), updated_at = now()
WHERE id = $1;
