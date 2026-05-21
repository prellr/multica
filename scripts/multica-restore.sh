#!/usr/bin/env bash
# multica-restore.sh — restore a Multica install from a backup directory
# produced by multica-backup.sh.
#
# WARNING: destructive. Drops and recreates the production database and
# overwrites the uploads volume. Run only when you are sure you want to
# revert to the chosen snapshot.
#
# ─── Usage ───────────────────────────────────────────────────────────
#
#   ./multica-restore.sh                       # restores `latest`
#   ./multica-restore.sh 2026-05-12_030000     # restores a specific snapshot
#   ./multica-restore.sh /full/path/to/backup  # any backup dir
#   ./multica-restore.sh --yes ...             # skip confirmation prompt
#
# ─── Configuration (env vars) ────────────────────────────────────────
#
#   MULTICA_COMPOSE_PROJECT  default: multica-prod
#   MULTICA_COMPOSE_FILE     default: <repo>/docker-compose.selfhost.yml
#   MULTICA_BACKUP_DIR       default: $HOME/multica-backups
#   POSTGRES_USER            default: multica
#   POSTGRES_DB              default: multica

set -euo pipefail

# ─── PATH ────────────────────────────────────────────────────────────
#
# Match the backup script — see scripts/multica-backup.sh for full
# rationale. Briefly: cron and non-interactive shells get a minimal
# PATH that excludes every common `docker` install location.
export PATH="$HOME/.orbstack/bin:/usr/local/bin:/opt/homebrew/bin:$PATH"

# ─── Args ────────────────────────────────────────────────────────────

ASSUME_YES=false
if [ "${1:-}" = "--yes" ] || [ "${1:-}" = "-y" ]; then
  ASSUME_YES=true
  shift
fi

TARGET_ARG="${1:-latest}"

# ─── Config ──────────────────────────────────────────────────────────

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

PROJECT="${MULTICA_COMPOSE_PROJECT:-multica-prod}"
COMPOSE_FILE="${MULTICA_COMPOSE_FILE:-$REPO_ROOT/docker-compose.selfhost.yml}"
BACKUP_ROOT="${MULTICA_BACKUP_DIR:-$HOME/multica-backups}"
PG_USER="${POSTGRES_USER:-multica}"
PG_DB="${POSTGRES_DB:-multica}"

# ─── Helpers ─────────────────────────────────────────────────────────

log()  { printf '[%s] %s\n' "$(date -u +%H:%M:%S)" "$*"; }
fail() { printf '[%s] ERROR: %s\n' "$(date -u +%H:%M:%S)" "$*" >&2; exit 1; }

compose() {
  docker compose -f "$COMPOSE_FILE" -p "$PROJECT" "$@"
}

# ─── Resolve backup directory ────────────────────────────────────────

if [ -d "$TARGET_ARG" ]; then
  BACKUP="$(cd "$TARGET_ARG" && pwd)"
elif [ -L "$BACKUP_ROOT/$TARGET_ARG" ] || [ -d "$BACKUP_ROOT/$TARGET_ARG" ]; then
  BACKUP="$(cd "$BACKUP_ROOT/$TARGET_ARG" && pwd)"
else
  fail "backup not found: $TARGET_ARG (tried as path and as $BACKUP_ROOT/$TARGET_ARG)"
fi

[ -f "$BACKUP/postgres.dump" ] || fail "missing postgres.dump in $BACKUP"
[ -f "$BACKUP/manifest.json"  ] || fail "missing manifest.json in $BACKUP (not a valid backup directory)"

log "restore source: $BACKUP"
log "manifest:"
sed 's/^/  /' "$BACKUP/manifest.json"

# ─── Confirm ─────────────────────────────────────────────────────────

if ! $ASSUME_YES; then
  echo
  echo "This will:"
  echo "  1. STOP backend + frontend containers"
  echo "  2. DROP and recreate database '$PG_DB' (current data destroyed)"
  echo "  3. OVERWRITE the multica_backend_uploads volume"
  echo "  4. Restart backend + frontend"
  echo
  printf "Type 'restore' to proceed: "
  read -r answer
  [ "$answer" = "restore" ] || fail "aborted"
fi

# ─── Preflight ───────────────────────────────────────────────────────

[ -f "$COMPOSE_FILE" ] || fail "compose file not found: $COMPOSE_FILE"
command -v docker >/dev/null || fail "docker not on PATH"

# Postgres must be reachable; backend/frontend will be stopped below.
if ! compose ps postgres --format json 2>/dev/null | grep -q '"State":"running"'; then
  log "postgres not running — bringing it up..."
  compose up -d postgres
  # Wait for healthcheck.
  for i in $(seq 1 30); do
    if compose exec -T postgres pg_isready -U "$PG_USER" -d postgres >/dev/null 2>&1; then
      break
    fi
    sleep 1
  done
fi

# ─── Stop application containers ─────────────────────────────────────

log "stopping backend + frontend..."
compose stop backend frontend 2>/dev/null || true

# ─── Restore postgres ────────────────────────────────────────────────
#
# Drop+recreate via the maintenance DB `postgres`, then pg_restore into
# the freshly created target. --no-owner / --no-privileges so the dump's
# original ownership doesn't fight the restore target's role setup.

log "dropping and recreating database '$PG_DB'..."
compose exec -T postgres psql -U "$PG_USER" -d postgres -v ON_ERROR_STOP=1 <<SQL
  SELECT pg_terminate_backend(pid)
    FROM pg_stat_activity
    WHERE datname = '$PG_DB' AND pid <> pg_backend_pid();
  DROP DATABASE IF EXISTS "$PG_DB";
  CREATE DATABASE "$PG_DB" OWNER "$PG_USER";
SQL

log "restoring postgres dump..."
compose exec -T postgres \
  pg_restore -U "$PG_USER" -d "$PG_DB" \
    --no-owner --no-privileges \
    --exit-on-error \
  < "$BACKUP/postgres.dump"

# ─── Restore uploads volume ──────────────────────────────────────────
#
# Wipe the volume, then extract the tarball into it. We use the same
# busybox helper pattern as the backup side for symmetry.

if [ -f "$BACKUP/uploads.tar" ] && [ -s "$BACKUP/uploads.tar" ]; then
  log "restoring uploads volume..."
  docker run --rm \
    -v multica_backend_uploads:/data \
    busybox:stable \
    sh -c 'rm -rf /data/* /data/..?* /data/.[!.]* 2>/dev/null; true'

  docker run --rm -i \
    -v multica_backend_uploads:/data \
    busybox:stable \
    tar -xf - -C /data \
    < "$BACKUP/uploads.tar"
else
  log "uploads.tar missing or empty — skipping uploads restore"
fi

# ─── Note: env.tar is NOT auto-restored ──────────────────────────────
#
# .env contains JWT_SECRET and other live secrets. Restoring it
# blindly would clobber any rotation done after the backup. If you
# need the captured config, extract manually:
#   tar -xf $BACKUP/env.tar -C /tmp/restore-config
# and reconcile by hand.

if [ -f "$BACKUP/env.tar" ]; then
  log "env.tar present but NOT auto-restored (see script comment)"
  log "  to inspect: tar -tvf $BACKUP/env.tar"
fi

# ─── Restart stack ───────────────────────────────────────────────────

log "starting backend + frontend..."
compose up -d backend frontend

# Health check — give backend ~60s to come up.
log "waiting for backend health..."
HEALTHY=false
for i in $(seq 1 30); do
  if compose ps backend --format json 2>/dev/null | grep -q '"State":"running"'; then
    HEALTHY=true
    break
  fi
  sleep 2
done

if $HEALTHY; then
  log "restore complete. backend is running."
else
  fail "backend did not reach running state — check 'docker compose logs backend'"
fi
