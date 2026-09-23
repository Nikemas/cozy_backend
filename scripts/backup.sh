#!/usr/bin/env bash
# Nightly backup of the production stack (docker/docker-compose.prod.yml).
#
# Runs ON THE VPS, from cron (see README.md "Бэкапы" for the cron line and
# the restore procedure):
#   1. pg_dump -Fc of the `cozy` database from the compose `postgres`
#      container -> $BACKUP_DIR/postgres/cozy_YYYYMMDD_HHMMSS.dump
#      (written to a .partial file, verified with pg_restore --list, then
#      renamed — a failed/truncated dump never looks like a good backup)
#   2. deletes dumps older than $KEEP_DAYS days (only after step 1
#      succeeded, so a broken backup job never eats the last good dumps)
#   3. optionally (MINIO_MIRROR=1) mirrors the media bucket into
#      $BACKUP_DIR/minio/<bucket>/ with `mc mirror` — incremental, and
#      objects deleted in MinIO are kept in the mirror (no --remove), so a
#      mistaken delete in the admin panel is recoverable
#
# Settings (env vars, all optional):
#   BACKUP_DIR    where backups go             (default /var/backups/cozy)
#   KEEP_DAYS     dump retention in days       (default 14)
#   MINIO_MIRROR  1 = also mirror MinIO        (default 0)
#   MINIO_BUCKET  bucket to mirror             (default: from .env, else cozy-media)
#   MC_IMAGE      mc image when no host `mc`   (default quay.io/minio/mc:latest)
#
# Exit code is non-zero on any failure, so cron's MAILTO / a monitoring
# wrapper can alert on it. Off-site copies (rsync/rclone of $BACKUP_DIR to
# another machine) are NOT done here — a backup on the same disk as the
# database does not survive losing the VPS.
set -euo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DOCKER="${DOCKER:-docker}" # overridable only so the script can be dry-run tested
COMPOSE=("$DOCKER" compose -f "$REPO_DIR/docker/docker-compose.prod.yml" --env-file "$REPO_DIR/.env")

BACKUP_DIR="${BACKUP_DIR:-/var/backups/cozy}"
KEEP_DAYS="${KEEP_DAYS:-14}"
MINIO_MIRROR="${MINIO_MIRROR:-0}"
MC_IMAGE="${MC_IMAGE:-quay.io/minio/mc:latest}"

log() { echo "backup: $(date '+%F %T') $*"; }
die() { echo "backup: $(date '+%F %T') ERROR: $*" >&2; exit 1; }

case "$KEEP_DAYS" in
  ''|*[!0-9]*) die "KEEP_DAYS must be a whole number of days, got '$KEEP_DAYS'" ;;
esac
[ -f "$REPO_DIR/.env" ] || die "$REPO_DIR/.env is missing"

# envval KEY: value of KEY from the server's .env (first match, surrounding
# quotes stripped). Read with grep instead of `source` so a stray shell
# metacharacter in some unrelated secret can't execute anything.
envval() {
  grep -E "^$1=" "$REPO_DIR/.env" | head -n1 | cut -d= -f2- | sed -e 's/^["'\'']//' -e 's/["'\'']$//'
}

mkdir -p "$BACKUP_DIR/postgres"
chmod 700 "$BACKUP_DIR"

# One backup at a time (a slow dump overlapping the next cron run would
# double the load and race on rotation). flock ships with util-linux.
if command -v flock >/dev/null 2>&1; then
  exec 9>"$BACKUP_DIR/.lock"
  flock -n 9 || die "another backup is still running"
fi

# --- 1. Postgres ----------------------------------------------------------
stamp="$(date +%Y%m%d_%H%M%S)"
final="$BACKUP_DIR/postgres/cozy_${stamp}.dump"
partial="$final.partial"
trap 'rm -f "$partial"' EXIT

log "dumping postgres -> $final"
# -Fc: compressed custom format, restorable selectively with pg_restore.
# --no-owner/--no-acl: restore works into any role/database name.
"${COMPOSE[@]}" exec -T postgres pg_dump -U cozy -d cozy -Fc --no-owner --no-acl >"$partial" \
  || die "pg_dump failed"
[ -s "$partial" ] || die "pg_dump produced an empty file"
"${COMPOSE[@]}" exec -T postgres pg_restore --list <"$partial" >/dev/null \
  || die "dump failed verification (pg_restore --list)"
mv "$partial" "$final"
chmod 600 "$final"
log "postgres dump ok ($(du -h "$final" | cut -f1))"

# --- 2. Rotation ----------------------------------------------------------
# -mtime +N matches files strictly older than N*24h.
removed="$(find "$BACKUP_DIR/postgres" -maxdepth 1 -type f -name 'cozy_*.dump' -mtime +"$KEEP_DAYS" -print -delete | wc -l | tr -d ' ')"
log "rotation: removed $removed dump(s) older than $KEEP_DAYS days"

# --- 3. MinIO (optional) --------------------------------------------------
if [ "$MINIO_MIRROR" = "1" ]; then
  bucket="${MINIO_BUCKET:-$(envval MINIO_BUCKET)}"
  bucket="${bucket:-cozy-media}"
  access="$(envval MINIO_ACCESS_KEY)"
  secret="$(envval MINIO_SECRET_KEY)"
  [ -n "$access" ] && [ -n "$secret" ] || die "MINIO_ACCESS_KEY/MINIO_SECRET_KEY not found in .env"
  mkdir -p "$BACKUP_DIR/minio/$bucket"

  minio_id="$("${COMPOSE[@]}" ps -q minio)"
  [ -n "$minio_id" ] || die "minio container is not running"

  log "mirroring minio bucket $bucket -> $BACKUP_DIR/minio/$bucket"
  # MinIO's port isn't published on the host, so mc runs in a throwaway
  # container that shares the minio container's network namespace and
  # reaches it on localhost:9000. Credentials go in via MC_HOST_<alias>,
  # passed by name (`-e MC_HOST_cozy` with the value in the environment)
  # so they never appear on a command line visible in `ps`. The secret is
  # embedded in a URL: it must not contain '/', '@' or ':'.
  MC_HOST_cozy="http://${access}:${secret}@localhost:9000" "$DOCKER" run --rm \
    --network "container:$minio_id" \
    -e MC_HOST_cozy \
    -v "$BACKUP_DIR/minio:/backup" \
    "$MC_IMAGE" mirror --overwrite --quiet "cozy/$bucket" "/backup/$bucket" \
    || die "mc mirror failed"
  log "minio mirror ok"
fi

log "done"
