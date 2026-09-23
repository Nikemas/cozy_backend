#!/usr/bin/env bash
# Deploy the current origin/main to this server.
#
# Runs ON THE VPS (invoked over SSH by .github/workflows/deploy.yml, or by
# hand: `bash /opt/cozy/scripts/deploy.sh`). The image is built here from
# source — there is no registry in the loop, so the server needs nothing
# but git, docker and the .env file that already lives next to this repo.
#
# Steps:
#   1. fast-forward the checkout to origin/main (hard reset: the server is
#      a deploy target, never a place to edit code; .env is untracked and
#      survives)
#   2. rebuild + restart the backend (postgres/minio/caddy are untouched
#      unless their compose definition changed)
#   3. restart caddy only if docker/Caddyfile changed — it is bind-mounted,
#      so `up -d` alone would not reload it
#   4. prune dangling images left by the rebuild
#   5. wait for /readyz through Caddy; fail loudly if it never comes up
set -euo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
COMPOSE=(docker compose -f docker/docker-compose.prod.yml --env-file .env)
HEALTH_URL="${HEALTH_URL:-https://cozy.erpsystemsales.com/readyz}"

cd "$REPO_DIR"

if [ ! -f .env ]; then
  echo "deploy: $REPO_DIR/.env is missing — copy .env.example and fill it in first" >&2
  exit 1
fi

old="$(git rev-parse HEAD)"
git fetch -q origin main
git reset -q --hard origin/main
new="$(git rev-parse HEAD)"
echo "deploy: $old -> $new"
git --no-pager log --oneline "$old..$new" || true

# Migrations run before the new backend starts: if one fails, the deploy
# stops here and the previous backend keeps serving against the old schema.
# (2026-09-23: they used to be applied by hand; a deploy shipped code that
# needed 000021 before anyone ran it and every /orders call returned 500.)
"${COMPOSE[@]}" up -d postgres
for i in $(seq 1 30); do
  "${COMPOSE[@]}" exec -T postgres pg_isready -U cozy -d cozy >/dev/null 2>&1 && break
  sleep 2
done
pg_container="$("${COMPOSE[@]}" ps -q postgres)"
pg_password="$(sed -n 's/^POSTGRES_PASSWORD=//p' .env)"
echo "deploy: applying migrations"
docker run --rm --network "container:$pg_container" \
  -v "$REPO_DIR/migrations:/migrations:ro" \
  migrate/migrate:v4.18.3 -path=/migrations \
  -database "postgres://cozy:${pg_password}@localhost:5432/cozy?sslmode=disable" up

"${COMPOSE[@]}" up -d --build

if [ "$old" != "$new" ] && git diff --name-only "$old" "$new" | grep -q '^docker/Caddyfile$'; then
  echo "deploy: Caddyfile changed, restarting caddy"
  "${COMPOSE[@]}" restart caddy
fi

docker image prune -f >/dev/null

if ! command -v curl >/dev/null 2>&1; then
  echo "deploy: curl not installed on the server — skipping the /readyz health check (apt install curl to enable it)" >&2
  exit 0
fi

echo "deploy: waiting for $HEALTH_URL"
for i in $(seq 1 20); do
  if curl -fsS -m 5 "$HEALTH_URL" >/dev/null 2>&1; then
    echo "deploy: healthy after ${i} attempt(s)"
    exit 0
  fi
  sleep 3
done

echo "deploy: backend did not become ready — last 50 log lines:" >&2
"${COMPOSE[@]}" logs --tail=50 backend >&2
exit 1
