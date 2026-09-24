#!/usr/bin/env bash
# Deploy a commit of main to this server.
#
# Runs ON THE VPS (invoked over SSH by .github/workflows/deploy.yml, or by
# hand: `bash /opt/cozy/scripts/deploy.sh`). The image is built here from
# source — there is no registry in the loop, so the server needs nothing
# but git, docker (with the compose plugin), curl and the .env file that
# already lives next to this repo.
#
# Steps (the old backend keeps serving until step 6):
#   0. preflight: .env, git, docker, curl present; one deploy at a time
#   1. check out DEPLOY_REF (default origin/main; CI passes the exact SHA it
#      tested). Hard reset: the server is a deploy target, never a place to
#      edit code; .env is untracked and survives
#   2. if docker/Caddyfile changed, validate it with the pinned caddy image
#   3. build the backend image as cozy-backend:<sha>; remember the image the
#      running backend uses as cozy-backend:previous (the rollback target)
#   4. start postgres, wait until healthy, refuse to continue on a dirty or
#      untracked migration state
#   5. apply migrations (scripts/migrate.sh: migrate/migrate, same image CI uses)
#   6. switch services to the new image (`up -d`), restart caddy if its
#      bind-mounted config changed
#   7. wait for /readyz through Caddy. Not ready -> print logs, put
#      cozy-backend:previous back, exit 1
#   8. on success: tag the image :latest, drop old images beyond KEEP_IMAGES
#
# Rollback never reverts migrations (down migrations can drop data), so a
# migration must stay compatible with the previous release's code
# (expand/contract: add columns/tables first, remove them a release later).
#
# Settings (env vars, all optional):
#   DEPLOY_REF     commit/ref to deploy        (default origin/main)
#   HEALTH_URL     readiness URL through Caddy (default https://cozy.erpsystemsales.com/readyz)
#   HEALTH_TIMEOUT seconds to wait for it      (default 90)
#   KEEP_IMAGES    cozy-backend:<sha> images to keep (default 5)
#   MIGRATE_IMAGE  default migrate/migrate:v4.18.3
set -euo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
COMPOSE_FILE=docker/docker-compose.prod.yml
COMPOSE=(docker compose -f "$COMPOSE_FILE" --env-file .env)
IMAGE_REPO=cozy-backend
DEPLOY_REF="${DEPLOY_REF:-origin/main}"
HEALTH_URL="${HEALTH_URL:-https://cozy.erpsystemsales.com/readyz}"
HEALTH_TIMEOUT="${HEALTH_TIMEOUT:-90}"
KEEP_IMAGES="${KEEP_IMAGES:-5}"
MIGRATE_IMAGE="${MIGRATE_IMAGE:-migrate/migrate:v4.18.3}"

log() { echo "deploy: $*"; }
die() {
  echo "deploy: ERROR: $*" >&2
  exit 1
}

cd "$REPO_DIR"

# --- 0. preflight ------------------------------------------------------------
[ -f .env ] || die "$REPO_DIR/.env is missing — copy .env.example and fill it in first"
for bin in git docker curl; do
  command -v "$bin" >/dev/null 2>&1 || die "'$bin' is not installed on this server (apt install $bin)"
done
docker compose version >/dev/null 2>&1 || die "the docker compose plugin is not installed"
case "$HEALTH_TIMEOUT$KEEP_IMAGES" in
  *[!0-9]*) die "HEALTH_TIMEOUT and KEEP_IMAGES must be whole numbers" ;;
esac

# Two deploys at once (CI + someone by hand) would race on the checkout and
# the containers. flock ships with util-linux.
if command -v flock >/dev/null 2>&1; then
  exec 9>"${TMPDIR:-/tmp}/cozy-deploy.lock"
  flock -n 9 || die "another deploy is running"
fi

# --- 1. checkout ---------------------------------------------------------------
old="$(git rev-parse HEAD)"
git fetch -q origin main
git reset -q --hard "$DEPLOY_REF"
new="$(git rev-parse HEAD)"
tag="${new:0:12}"
log "$old -> $new"
git --no-pager log --oneline "$old..$new" 2>/dev/null || true

changed() { [ "$old" != "$new" ] && git diff --name-only "$old" "$new" -- "$1" | grep -q .; }

# --- 2. Caddyfile --------------------------------------------------------------
caddy_changed=0
if changed docker/Caddyfile; then
  caddy_changed=1
  caddy_image="$(sed -n 's/^[[:space:]]*image:[[:space:]]*\(caddy:[^[:space:]]*\).*/\1/p' "$COMPOSE_FILE" | head -n1)"
  [ -n "$caddy_image" ] || die "could not find the caddy image in $COMPOSE_FILE"
  log "Caddyfile changed, validating with $caddy_image"
  docker run --rm -v "$REPO_DIR/docker/Caddyfile:/etc/caddy/Caddyfile:ro" "$caddy_image" \
    caddy validate --config /etc/caddy/Caddyfile --adapter caddyfile >/dev/null \
    || die "docker/Caddyfile is invalid — no container was touched"
fi

# --- 3. build ------------------------------------------------------------------
running_image=""
backend_id="$("${COMPOSE[@]}" ps -q backend 2>/dev/null || true)"
if [ -n "$backend_id" ]; then
  running_image="$(docker inspect -f '{{.Image}}' "$backend_id")"
fi

log "building $IMAGE_REPO:$tag"
BACKEND_TAG="$tag" "${COMPOSE[@]}" build backend \
  || die "image build failed — the running backend was not touched"
new_image="$(docker image inspect -f '{{.Id}}' "$IMAGE_REPO:$tag")"

have_rollback=0
if [ -n "$running_image" ] && [ "$running_image" != "$new_image" ]; then
  docker tag "$running_image" "$IMAGE_REPO:previous"
  have_rollback=1
  log "rollback target: $IMAGE_REPO:previous (${running_image:7:12})"
fi

# --- 4. postgres + migration state ---------------------------------------------
"${COMPOSE[@]}" up -d --no-build postgres
pg_container="$("${COMPOSE[@]}" ps -q postgres)"
for _ in $(seq 1 60); do
  [ "$(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{end}}' "$pg_container")" = healthy ] && break
  # Healthcheck not defined (older compose file) — fall back to pg_isready.
  "${COMPOSE[@]}" exec -T postgres pg_isready -U cozy -d cozy >/dev/null 2>&1 && break
  sleep 2
done
"${COMPOSE[@]}" exec -T postgres pg_isready -U cozy -d cozy >/dev/null 2>&1 \
  || die "postgres did not become ready"

# psql over the container's local socket (trust auth — no password needed).
psql_q() { "${COMPOSE[@]}" exec -T postgres psql -U cozy -d cozy -XAtq -v ON_ERROR_STOP=1 -c "$1"; }

migrations_help() {
  cat >&2 <<EOF
deploy: How to recover (README "Миграции: dirty и восстановление"):
deploy:   1. Look at the migration file for version N and the error above.
deploy:      A file runs as one transaction, so if it failed nothing of it
deploy:      was applied: the schema is still at version N-1.
deploy:   2. Mark that:  $MIGRATE_CMD_HINT force <N-1>
deploy:   3. Fix the migration in a new commit and deploy again.
deploy:   If the file was NOT transactional (e.g. it had its own COMMIT) check
deploy:   by hand which statements took effect before choosing the version.
EOF
}
MIGRATE_CMD_HINT="bash scripts/migrate.sh"

if [ "$(psql_q "SELECT to_regclass('public.schema_migrations') IS NOT NULL")" = t ]; then
  state="$(psql_q "SELECT version || ' ' || dirty FROM schema_migrations LIMIT 1")"
  if [ "${state#* }" = true ]; then
    echo "deploy: ERROR: migration state is DIRTY at version ${state%% *}: a previous migration failed half-way." >&2
    migrations_help
    exit 1
  fi
  log "schema at version ${state%% *}"
elif [ "$(psql_q "SELECT to_regclass('public.customers') IS NOT NULL")" = t ]; then
  # 2026-09 incident: the schema existed but schema_migrations did not, so
  # `migrate up` would try 000001 again and fail on "already exists".
  cat >&2 <<EOF
deploy: ERROR: tables exist but schema_migrations does not — migrate does not
deploy: know which migrations are applied. Find the newest applied migration
deploy: (compare the schema with migrations/*.up.sql), then record it:
deploy:   $MIGRATE_CMD_HINT force <version>
deploy: and deploy again.
EOF
  exit 1
fi

# --- 5. migrations -------------------------------------------------------------
# scripts/migrate.sh reads the password from the postgres container and
# hands it to migrate via PGPASSWORD, so it never appears in `ps`.
log "applying migrations"
if ! MIGRATE_IMAGE="$MIGRATE_IMAGE" bash "$REPO_DIR/scripts/migrate.sh" up </dev/null; then
  echo "deploy: ERROR: migration failed. The previous backend is still running." >&2
  state="$(psql_q "SELECT version || ' dirty=' || dirty FROM schema_migrations LIMIT 1" 2>/dev/null || true)"
  [ -n "$state" ] && echo "deploy: migration state now: version $state" >&2
  migrations_help
  exit 1
fi

# --- 6. switch -----------------------------------------------------------------
wait_ready() {
  local deadline=$((SECONDS + HEALTH_TIMEOUT))
  while [ "$SECONDS" -lt "$deadline" ]; do
    curl -fsS -m 5 "$HEALTH_URL" >/dev/null 2>&1 && return 0
    sleep 3
  done
  return 1
}

# rollback_and_fail MESSAGE: put the image the backend ran before this
# deploy back and exit 1. Migrations are not reverted (see header).
rollback_and_fail() {
  echo "deploy: ERROR: $1" >&2
  "${COMPOSE[@]}" logs --tail=80 backend >&2 || true
  if [ "$have_rollback" != 1 ]; then
    echo "deploy: no previous image to roll back to (first deploy, or same image redeployed)." >&2
    exit 1
  fi
  echo "deploy: rolling back to $IMAGE_REPO:previous (migrations are NOT reverted)" >&2
  if BACKEND_TAG=previous "${COMPOSE[@]}" up -d --no-build --no-deps backend && wait_ready; then
    echo "deploy: rollback done, the previous backend is serving. Deploy of $tag FAILED." >&2
  else
    echo "deploy: rollback did not become ready either — the site is DOWN, investigate now." >&2
  fi
  exit 1
}

log "starting $IMAGE_REPO:$tag"
BACKEND_TAG="$tag" "${COMPOSE[@]}" up -d --no-build \
  || rollback_and_fail "docker compose up failed"
if [ "$caddy_changed" = 1 ]; then
  # Bind-mounted single file: git replaced it with a new inode, which the
  # running container doesn't see until it restarts.
  log "Caddyfile changed, restarting caddy"
  "${COMPOSE[@]}" restart caddy
fi

# --- 7. health -----------------------------------------------------------------
log "waiting for $HEALTH_URL (up to ${HEALTH_TIMEOUT}s)"
wait_ready || rollback_and_fail "$IMAGE_REPO:$tag did not become ready"
log "healthy"

# --- 8. housekeeping -----------------------------------------------------------
# :latest = what is deployed, so a hand-run `docker compose up -d` (no
# BACKEND_TAG) keeps running this version instead of a stale one.
docker tag "$IMAGE_REPO:$tag" "$IMAGE_REPO:latest"

# `docker image ls` lists newest first. Keep the newest KEEP_IMAGES SHA tags
# (plus whatever :previous/:latest point at — `rmi` of a tag that shares
# its image with another tag only removes the tag); an image a container
# still uses is refused by `rmi` without -f, which is what we want.
docker image ls "$IMAGE_REPO" --format '{{.Tag}}' \
  | grep -Ev '^(latest|previous|<none>)$' \
  | tail -n +"$((KEEP_IMAGES + 1))" \
  | while read -r old_tag; do
    docker image rm "$IMAGE_REPO:$old_tag" >/dev/null 2>&1 || true
  done
docker image prune -f >/dev/null || true
docker builder prune -f --filter until=168h >/dev/null 2>&1 || true

log "deployed $tag"
