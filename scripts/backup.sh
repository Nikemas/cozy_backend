#!/usr/bin/env bash
# Nightly backup of the production stack (docker/docker-compose.prod.yml).
#
# Runs ON THE VPS, from cron (see README.md "Бэкапы" for the cron line and
# the restore procedure):
#   1. pg_dump -Fc of the `cozy` database from the compose `postgres`
#      container -> $BACKUP_DIR/postgres/cozy_YYYYMMDD_HHMMSS.dump
#      (written to a .partial file, verified with pg_restore --list, then
#      renamed — a failed/truncated dump never looks like a good backup)
#   2. deletes local dumps older than $KEEP_DAYS days (only after step 1
#      succeeded, so a broken backup job never eats the last good dumps)
#   3. mirrors the MinIO media bucket into $BACKUP_DIR/minio/<bucket>/ with
#      `mc mirror` — incremental, and objects deleted in MinIO are kept in
#      the mirror (no --remove), so a mistaken delete in the admin panel is
#      recoverable. On by default; MINIO_MIRROR=0 skips it
#   4. optionally copies both off the server with rclone
#      (BACKUP_RCLONE_REMOTE) — a backup on the same disk as the database
#      does not survive losing the VPS
#   5. optionally reports to a dead-man's-switch monitor
#      (BACKUP_HEALTHCHECK_URL, e.g. healthchecks.io): /start when it
#      begins, the plain URL on success, /fail + the log tail on any error.
#      The monitor also alerts when no ping arrives at all (cron broken,
#      server down), which an exit code alone can never do
#
# Settings (env vars, all optional):
#   BACKUP_DIR              where backups go             (default /var/backups/cozy)
#   BACKUP_LOG              log file (everything is also
#                           written here)                (default $BACKUP_DIR/backup.log)
#   KEEP_DAYS               local dump retention, days   (default 14)
#   MINIO_MIRROR            1 = also mirror MinIO        (default 1)
#   MINIO_BUCKET            bucket to mirror             (default: from .env, else cozy-media)
#   MC_IMAGE                mc image when no host `mc`   (default pinned quay.io/minio/mc release)
#   BACKUP_RCLONE_REMOTE    rclone destination, e.g. "b2:cozy-backups/staging"
#                           (a remote configured with `rclone config`); empty = no off-site copy
#   BACKUP_RCLONE_KEEP_DAYS off-site dump retention      (default 60; 0 = keep forever)
#   RCLONE_CONFIG           rclone config file           (default ~/.config/rclone/rclone.conf)
#   RCLONE_IMAGE            rclone image when no host `rclone` (default rclone/rclone:1.75.1)
#   BACKUP_HEALTHCHECK_URL  ping URL, e.g. https://hc-ping.com/<uuid>; empty = no pings
#
# Exit code is non-zero on any failure, and errors go to stderr as well as
# the log, so cron mails them (MAILTO) when stdout is discarded.
set -euo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DOCKER="${DOCKER:-docker}" # overridable only so the script can be dry-run tested
COMPOSE=("$DOCKER" compose -f "$REPO_DIR/docker/docker-compose.prod.yml" --env-file "$REPO_DIR/.env")

BACKUP_DIR="${BACKUP_DIR:-/var/backups/cozy}"
BACKUP_LOG="${BACKUP_LOG:-$BACKUP_DIR/backup.log}"
KEEP_DAYS="${KEEP_DAYS:-14}"
MINIO_MIRROR="${MINIO_MIRROR:-1}"
MC_IMAGE="${MC_IMAGE:-quay.io/minio/mc:RELEASE.2025-08-13T08-35-41Z}"
BACKUP_RCLONE_REMOTE="${BACKUP_RCLONE_REMOTE:-}"
BACKUP_RCLONE_KEEP_DAYS="${BACKUP_RCLONE_KEEP_DAYS:-60}"
RCLONE_CONFIG="${RCLONE_CONFIG:-${HOME:-/root}/.config/rclone/rclone.conf}"
RCLONE_IMAGE="${RCLONE_IMAGE:-rclone/rclone:1.75.1}"
BACKUP_HEALTHCHECK_URL="${BACKUP_HEALTHCHECK_URL:-}"

log() { echo "backup: $(date '+%F %T') $*"; }
die() {
  echo "backup: $(date '+%F %T') ERROR: $*" >&2
  exit 1
}

# hc_ping SUFFIX [BODY]: best effort — a monitoring outage must never fail
# the backup itself.
hc_ping() {
  [ -n "$BACKUP_HEALTHCHECK_URL" ] || return 0
  command -v curl >/dev/null 2>&1 || {
    echo "backup: curl not installed, cannot ping $BACKUP_HEALTHCHECK_URL" >&2
    return 0
  }
  curl -fsS -m 10 --retry 3 -o /dev/null --data-binary "${2:-}" "${BACKUP_HEALTHCHECK_URL%/}$1" \
    || echo "backup: healthcheck ping ${1:-success} failed" >&2
}

partial=""
on_exit() {
  rc=$?
  [ -n "$partial" ] && rm -f "$partial"
  if [ "$rc" -eq 0 ]; then
    hc_ping ""
  else
    sleep 1 # let the tee processes flush the last error into the log
    hc_ping /fail "$(tail -n 40 "$BACKUP_LOG" 2>/dev/null || echo "exit code $rc")"
  fi
}
trap on_exit EXIT

hc_ping /start

for v in KEEP_DAYS BACKUP_RCLONE_KEEP_DAYS; do
  case "${!v}" in
    ''|*[!0-9]*) die "$v must be a whole number of days, got '${!v}'" ;;
  esac
done
[ -f "$REPO_DIR/.env" ] || die "$REPO_DIR/.env is missing"

# envval KEY: value of KEY from the server's .env (first match, surrounding
# quotes stripped). Read with grep instead of `source` so a stray shell
# metacharacter in some unrelated secret can't execute anything.
envval() {
  grep -E "^$1=" "$REPO_DIR/.env" | head -n1 | cut -d= -f2- | sed -e 's/^["'\'']//' -e 's/["'\'']$//'
}

mkdir -p "$BACKUP_DIR/postgres" "$(dirname "$BACKUP_LOG")"
chmod 700 "$BACKUP_DIR"

# Everything from here on also goes to $BACKUP_LOG; stderr stays stderr, so
# cron still mails errors when the crontab line discards stdout.
exec > >(tee -a "$BACKUP_LOG") 2> >(tee -a "$BACKUP_LOG" >&2)

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

# --- 3. MinIO -------------------------------------------------------------
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

# --- 4. Off-site copy (optional) --------------------------------------------
if [ -n "$BACKUP_RCLONE_REMOTE" ]; then
  remote="${BACKUP_RCLONE_REMOTE%/}"
  if command -v rclone >/dev/null 2>&1; then
    rclone_cmd=(rclone --config "$RCLONE_CONFIG")
    local_root="$BACKUP_DIR"
  else
    # No host rclone: run the pinned image with the config dir (read-write,
    # OAuth-based remotes refresh their token in it) and backups (read-only)
    # mounted.
    [ -f "$RCLONE_CONFIG" ] || die "rclone config $RCLONE_CONFIG not found (run \`rclone config\` or set RCLONE_CONFIG)"
    rclone_cmd=("$DOCKER" run --rm
      -v "$(dirname "$RCLONE_CONFIG"):/config/rclone"
      -v "$BACKUP_DIR:/backup:ro"
      "$RCLONE_IMAGE" --config "/config/rclone/$(basename "$RCLONE_CONFIG")")
    local_root=/backup
  fi

  log "off-site: copying dumps -> $remote/postgres"
  # copy, not sync: an off-site copy must not lose files just because they
  # were rotated (or deleted by mistake) locally.
  "${rclone_cmd[@]}" copy --include 'cozy_*.dump' "$local_root/postgres" "$remote/postgres" \
    || die "rclone copy of dumps failed"
  if [ "$BACKUP_RCLONE_KEEP_DAYS" -gt 0 ]; then
    "${rclone_cmd[@]}" delete --min-age "${BACKUP_RCLONE_KEEP_DAYS}d" --include 'cozy_*.dump' "$remote/postgres" \
      || die "rclone off-site rotation failed"
  fi
  if [ -d "$BACKUP_DIR/minio" ]; then
    log "off-site: copying media mirror -> $remote/minio"
    "${rclone_cmd[@]}" copy "$local_root/minio" "$remote/minio" \
      || die "rclone copy of media failed"
  fi
  log "off-site copy ok"
fi

log "done"
