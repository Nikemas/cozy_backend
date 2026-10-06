#!/usr/bin/env bash
# Deploy a commit to this server.
#
# Runs ON THE VPS (invoked over SSH by .github/workflows/deploy.yml for
# staging and .github/workflows/deploy-prod.yml for production, or by hand:
# `bash /opt/cozy/scripts/deploy.sh`). The image is built here from
# source — there is no registry in the loop, so the server needs nothing
# but git, docker (with the compose plugin), curl and the .env file that
# already lives next to this repo.
#
# Steps (the old backend keeps serving until step 6):
#   0. preflight: env file, git, docker, curl present; one deploy at a time
#   1. check out DEPLOY_REF (staging default origin/main; CI passes the exact
#      SHA it tested; production requires it explicitly). Hard reset: the
#      server is a deploy target, never a place to edit code; .env is
#      untracked and survives
#   2. if the environment's Caddyfile changed (production: or caddy is not
#      running yet, i.e. the first deploy), validate it with the pinned caddy
#      image
#   3. build the backend image as <image>:<sha>; remember the image the
#      running backend uses as <image>:previous (the rollback target)
#   4. start postgres, wait until healthy, refuse to continue on a dirty or
#      untracked migration state
#   5. apply migrations (scripts/migrate.sh: migrate/migrate, same image CI uses)
#   6. switch services to the new image (`up -d`), restart caddy if its
#      bind-mounted config changed
#   7. wait for /readyz through Caddy. Not ready -> print logs, put
#      <image>:previous back, exit 1
#   8. on success: tag the image :latest, drop old images beyond KEEP_IMAGES
#
# Rollback never reverts migrations (down migrations can drop data), so a
# migration must stay compatible with the previous release's code
# (expand/contract: add columns/tables first, remove them a release later).
#
# Environment (scripts/env.sh has the full per-environment table):
#   DEPLOY_ENV=staging     (default) exactly the historical behaviour: compose
#                          project "docker", .env, docker/Caddyfile, image
#                          cozy-backend, origin/main
#   DEPLOY_ENV=production  compose project cozy-prod (COMPOSE_PROJECT_NAME),
#                          docker/Caddyfile.prod with SITE_HOST/MEDIA_HOST
#                          from the env file, image cozy-backend-prod;
#                          DEPLOY_REF is required (a tag or SHA)
#
# Settings (env vars, all optional unless noted):
#   DEPLOY_REF     commit/ref to deploy        (staging default origin/main; required for production)
#   ENV_FILE       env file, relative to the repo (default .env)
#   HEALTH_URL     readiness URL through Caddy (staging default https://cozy.erpsystemsales.com/readyz,
#                                               production default https://$SITE_HOST/readyz)
#   HEALTH_TIMEOUT seconds to wait for it      (default 90)
#   KEEP_IMAGES    <image>:<sha> images to keep (default 5)
#   MIGRATE_IMAGE  default migrate/migrate:v4.18.3
set -euo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# scripts/env.sh is sourced BEFORE the checkout below, so take it from the
# commit being deployed: CI passes DEPLOY_REF and has already fetched it,
# while deploy.yml only refreshes scripts/deploy.sh itself (and on the first
# deploy after env.sh was introduced the old checkout has no env.sh at all).
if [ -n "${DEPLOY_REF:-}" ]; then
  git -C "$REPO_DIR" checkout -q "$DEPLOY_REF" -- scripts/env.sh 2>/dev/null || true
fi
[ -f "$REPO_DIR/scripts/env.sh" ] \
  || { echo "deploy: ERROR: scripts/env.sh is missing (set DEPLOY_REF or update the checkout)" >&2; exit 1; }
# shellcheck source=scripts/env.sh
. "$REPO_DIR/scripts/env.sh"
IMAGE_REPO="$BACKEND_IMAGE"
if [ "$DEPLOY_ENV" = staging ]; then
  DEPLOY_REF="${DEPLOY_REF:-origin/main}"
else
  DEPLOY_REF="${DEPLOY_REF:-}"
fi
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
[ -f "$ENV_PATH" ] || die "$ENV_PATH is missing — copy .env.example and fill it in first"
if [ "$DEPLOY_ENV" = production ]; then
  [ -n "$DEPLOY_REF" ] || die "DEPLOY_REF is required for production (a release tag or commit SHA)"
  for key in SITE_HOST MEDIA_HOST; do
    [ -n "$(envfile_val "$key")" ] || die "$key is not set in $ENV_PATH (docker/Caddyfile.prod needs it)"
  done
  [ "$(envfile_val APP_ENV)" = prod ] || die "APP_ENV in $ENV_PATH must be 'prod' for the production stack"
fi
[ -n "$HEALTH_URL" ] || die "HEALTH_URL is empty"
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
  exec 9>"${TMPDIR:-/tmp}/$LOCK_NAME"
  flock -n 9 || die "another deploy is running"
fi

# --- 1. checkout ---------------------------------------------------------------
log "environment: $DEPLOY_ENV"
old="$(git rev-parse HEAD)"
if [ "$DEPLOY_ENV" = staging ]; then
  git fetch -q origin main
else
  # Production deploys tags/SHAs that need not be on main's tip.
  git fetch -q --tags origin '+refs/heads/*:refs/remotes/origin/*'
fi
git reset -q --hard "$DEPLOY_REF"
new="$(git rev-parse HEAD)"
tag="${new:0:12}"
log "$old -> $new"
git --no-pager log --oneline "$old..$new" 2>/dev/null || true

changed() { [ "$old" != "$new" ] && git diff --name-only "$old" "$new" -- "$1" | grep -q .; }

# --- 2. Caddyfile --------------------------------------------------------------
# Production also validates on its first deploy (no caddy container yet):
# a fresh checkout has old == new, so `changed` alone would skip it.
first_prod_caddy() {
  [ "$DEPLOY_ENV" = production ] && [ -z "$("${COMPOSE[@]}" ps -q caddy 2>/dev/null || true)" ]
}

caddy_changed=0
if changed "docker/$CADDYFILE" || first_prod_caddy; then
  caddy_changed=1
  caddy_image="$(sed -n 's/^[[:space:]]*image:[[:space:]]*\(caddy:[^[:space:]]*\).*/\1/p' "$COMPOSE_FILE" | head -n1)"
  [ -n "$caddy_image" ] || die "could not find the caddy image in $COMPOSE_FILE"
  log "$CADDYFILE changed, validating with $caddy_image"
  caddy_env=()
  # Caddyfile.prod takes its hostnames from the env file.
  [ "$DEPLOY_ENV" = production ] && caddy_env=(--env-file "$ENV_PATH")
  docker run --rm ${caddy_env[@]+"${caddy_env[@]}"} -v "$REPO_DIR/docker/$CADDYFILE:/etc/caddy/Caddyfile:ro" "$caddy_image" \
    caddy validate --config /etc/caddy/Caddyfile --adapter caddyfile >/dev/null \
    || die "docker/$CADDYFILE is invalid — no container was touched"
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
  log "$CADDYFILE changed, restarting caddy"
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
