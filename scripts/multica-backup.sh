#!/usr/bin/env bash
# multica-backup.sh — nightly ops backup for a self-hosted Multica install.
#
# Backs up everything required to fully restore an install:
#   1. PostgreSQL  → postgres.dump (pg_dump --format=custom, takes ACCESS
#      SHARE locks; does NOT block writes)
#   2. Uploads     → uploads.tar (live snapshot of the multica_backend_uploads
#      Docker volume; in-flight uploads may be partial — acceptable for
#      daily ops backup since the next day picks them up cleanly)
#   3. Config      → env.tar (.env + docker-compose.selfhost*.yml,
#      contains JWT_SECRET, DB password, CloudFront keys — handle on disk
#      with the same care as production secrets)
#   4. Manifest    → manifest.json (image tag, schema_version, sizes,
#      timing, script version — used by multica-restore.sh sanity checks)
#
# What's NOT backed up:
#   - Workspace_secret values are inside postgres.dump and remain
#     encrypted there; decryption keys live in JWT_SECRET inside env.tar.
#     Restoring the DB without the matching env is useless. Keep them
#     together.
#   - Daemon connection state — regenerated when daemons reconnect.
#
# ─── Schedule (cron) ─────────────────────────────────────────────────
#
# Add to crontab (`crontab -e`), runs nightly at 3am:
#   0 3 * * * /path/to/multica-local/scripts/multica-backup.sh >> "$HOME/multica-backups/backup.log" 2>&1
#
# On the Mac mini, use absolute paths in cron — cron PATH is minimal.
# Verify with: `crontab -l` and `tail -f ~/multica-backups/backup.log`
#
# ─── Configuration (env vars) ────────────────────────────────────────
#
#   MULTICA_COMPOSE_PROJECT  default: multica-prod
#   MULTICA_COMPOSE_FILE     default: <repo>/docker-compose.selfhost.yml
#   MULTICA_BACKUP_DIR       default: $HOME/multica-backups
#   MULTICA_RETENTION_DAYS   default: 30
#   POSTGRES_USER            default: multica
#   POSTGRES_DB              default: multica
#
# Backups land under:
#   $MULTICA_BACKUP_DIR/<YYYY-MM-DD_HHMMSS>/
#                       postgres.dump
#                       uploads.tar
#                       env.tar
#                       manifest.json
#   $MULTICA_BACKUP_DIR/latest -> most recent successful backup

set -euo pipefail

# ─── PATH ────────────────────────────────────────────────────────────
#
# Cron runs with a minimal PATH (`/usr/bin:/bin:/usr/sbin:/sbin`) and does
# NOT source ~/.zprofile or ~/.zshrc, so the `docker` binary installed by
# OrbStack (~/.orbstack/bin/docker), Docker Desktop (/usr/local/bin/docker),
# or Homebrew on Apple Silicon (/opt/homebrew/bin) won't be found unless
# we extend PATH explicitly here. Setting it in the script — not the
# crontab — makes the script work from cron, an SSH non-interactive
# shell, or a launchd job without further config.
export PATH="$HOME/.orbstack/bin:/usr/local/bin:/opt/homebrew/bin:$PATH"

# ─── Config ──────────────────────────────────────────────────────────

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

PROJECT="${MULTICA_COMPOSE_PROJECT:-multica-prod}"
COMPOSE_FILE="${MULTICA_COMPOSE_FILE:-$REPO_ROOT/docker-compose.selfhost.yml}"
BACKUP_ROOT="${MULTICA_BACKUP_DIR:-$HOME/multica-backups}"
RETENTION_DAYS="${MULTICA_RETENTION_DAYS:-30}"
PG_USER="${POSTGRES_USER:-multica}"
PG_DB="${POSTGRES_DB:-multica}"

SCRIPT_VERSION=1
TIMESTAMP="$(date -u +%Y-%m-%d_%H%M%S)"
STARTED_AT="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

# ─── Helpers ─────────────────────────────────────────────────────────

log()  { printf '[%s] %s\n' "$(date -u +%H:%M:%S)" "$*"; }
fail() { printf '[%s] ERROR: %s\n' "$(date -u +%H:%M:%S)" "$*" >&2; exit 1; }

compose() {
  docker compose -f "$COMPOSE_FILE" -p "$PROJECT" "$@"
}

bytes_of() {
  # Cross-platform `stat -c %s` (GNU) / `stat -f %z` (BSD). Returns 0 if missing.
  [ -f "$1" ] && wc -c < "$1" | tr -d ' ' || echo 0
}

# ─── Preflight ───────────────────────────────────────────────────────

[ -f "$COMPOSE_FILE" ] || fail "compose file not found: $COMPOSE_FILE"
command -v docker >/dev/null || fail "docker not on PATH"

# Confirm the stack is actually up — backing up nothing is worse than failing loud.
if ! compose ps --format json 2>/dev/null | grep -q '"State":"running"'; then
  fail "stack not running for project '$PROJECT' (compose file: $COMPOSE_FILE)"
fi

mkdir -p "$BACKUP_ROOT"
WORK_DIR="$BACKUP_ROOT/.in-progress.$$"
FINAL_DIR="$BACKUP_ROOT/$TIMESTAMP"
mkdir -p "$WORK_DIR"
trap 'rm -rf "$WORK_DIR"' EXIT

log "backup started"
log "  project        = $PROJECT"
log "  compose file   = $COMPOSE_FILE"
log "  destination    = $FINAL_DIR"
log "  retention days = $RETENTION_DAYS"

# ─── 1. PostgreSQL dump ──────────────────────────────────────────────

log "dumping postgres ($PG_DB as $PG_USER)..."
compose exec -T postgres \
  pg_dump -U "$PG_USER" -d "$PG_DB" \
    --format=custom \
    --no-owner --no-privileges \
    --compress=6 \
  > "$WORK_DIR/postgres.dump"

PG_SIZE="$(bytes_of "$WORK_DIR/postgres.dump")"
[ "$PG_SIZE" -gt 0 ] || fail "postgres dump is empty"
log "  postgres.dump  = $PG_SIZE bytes"

# Capture schema version from the dump's source DB — used by restore as a guard.
SCHEMA_VERSION="$(compose exec -T postgres \
  psql -U "$PG_USER" -d "$PG_DB" -tA \
    -c 'SELECT MAX(version) FROM schema_migrations' 2>/dev/null | tr -d '[:space:]')"
[ -n "$SCHEMA_VERSION" ] || SCHEMA_VERSION="unknown"
log "  schema_version = $SCHEMA_VERSION"

# ─── 2. Uploads volume snapshot ──────────────────────────────────────
#
# Tar the volume by spinning up a one-shot helper container that mounts
# it read-only. This is the canonical Docker pattern for volume backup —
# avoids needing host-side knowledge of where Docker stores volumes
# (varies between Docker Desktop, OrbStack, colima, etc.).

log "snapshotting backend_uploads volume..."
docker run --rm \
  -v multica_backend_uploads:/data:ro \
  busybox:stable \
  tar -cf - -C /data . \
  > "$WORK_DIR/uploads.tar"

UPLOADS_SIZE="$(bytes_of "$WORK_DIR/uploads.tar")"
log "  uploads.tar    = $UPLOADS_SIZE bytes"

# ─── 3. Config tarball ───────────────────────────────────────────────
#
# .env contains JWT_SECRET (encrypts workspace_secret rows in the DB),
# CloudFront private key, OAuth client secrets. Without these, postgres.dump
# is unusable for any encrypted column.

log "archiving config..."
CONFIG_FILES=()
for f in "$REPO_ROOT/.env" \
         "$REPO_ROOT/docker-compose.selfhost.yml" \
         "$REPO_ROOT/docker-compose.selfhost.build.yml"; do
  [ -f "$f" ] && CONFIG_FILES+=("$(basename "$f")")
done

if [ ${#CONFIG_FILES[@]} -gt 0 ]; then
  tar -cf "$WORK_DIR/env.tar" -C "$REPO_ROOT" "${CONFIG_FILES[@]}"
  ENV_SIZE="$(bytes_of "$WORK_DIR/env.tar")"
  log "  env.tar        = $ENV_SIZE bytes (${#CONFIG_FILES[@]} files)"
else
  log "  env.tar        = SKIPPED (no .env or compose files found)"
  ENV_SIZE=0
fi

# ─── 4. Manifest ─────────────────────────────────────────────────────

BACKEND_IMAGE="$(compose images --format json backend 2>/dev/null \
  | head -1 \
  | sed -E 's/.*"Repository":"([^"]*)".*"Tag":"([^"]*)".*/\1:\2/')"
[ -z "$BACKEND_IMAGE" ] && BACKEND_IMAGE="unknown"

COMPLETED_AT="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

cat > "$WORK_DIR/manifest.json" <<EOF
{
  "script_version": $SCRIPT_VERSION,
  "format": "multica-ops-backup-v1",
  "started_at": "$STARTED_AT",
  "completed_at": "$COMPLETED_AT",
  "project": "$PROJECT",
  "backend_image": "$BACKEND_IMAGE",
  "schema_version": "$SCHEMA_VERSION",
  "files": {
    "postgres.dump": $PG_SIZE,
    "uploads.tar":   $UPLOADS_SIZE,
    "env.tar":       $ENV_SIZE
  }
}
EOF

# ─── 5. Atomic commit + symlink update ───────────────────────────────

mv "$WORK_DIR" "$FINAL_DIR"
trap - EXIT  # WORK_DIR is gone; nothing to clean
ln -sfn "$FINAL_DIR" "$BACKUP_ROOT/latest"

log "backup committed: $FINAL_DIR"
log "  latest -> $TIMESTAMP"

# ─── 6. Retention ────────────────────────────────────────────────────
#
# Match the timestamp pattern explicitly so we never touch `latest`,
# `backup.log`, or any operator-placed file.

log "pruning backups older than $RETENTION_DAYS days..."
PRUNED=0
while IFS= read -r dir; do
  log "  removing $(basename "$dir")"
  rm -rf "$dir"
  PRUNED=$((PRUNED + 1))
done < <(find "$BACKUP_ROOT" -maxdepth 1 -type d \
           -name '20[0-9][0-9]-[0-9][0-9]-[0-9][0-9]_[0-9][0-9][0-9][0-9][0-9][0-9]' \
           -mtime "+$RETENTION_DAYS" \
           2>/dev/null)
log "  pruned $PRUNED old backup(s)"

log "done. total size: $(du -sh "$FINAL_DIR" | cut -f1)"
