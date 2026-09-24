#!/usr/bin/env bash
# Run golang-migrate against the postgres of the production compose stack.
#
# Runs ON THE VPS from the repo checkout (/opt/cozy). scripts/deploy.sh uses
# it for `up`; by hand it is the tool for inspecting and repairing the
# migration state (README "Миграции: dirty и восстановление"):
#
#   bash scripts/migrate.sh version      # current version, "(dirty)" if a migration failed
#   bash scripts/migrate.sh up           # apply pending migrations
#   bash scripts/migrate.sh force 21     # record version 21 as applied and clean, run nothing
#   bash scripts/migrate.sh down 1       # revert the newest migration (can drop data!)
#
# migrate runs in a throwaway container (same image/version as CI) sharing
# the postgres container's network namespace, so it reaches it on
# localhost:5432 without the port being published. The password is read
# from the postgres container itself and passed as PGPASSWORD by name, so it
# never appears on a command line (`ps`).
set -euo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
COMPOSE=(docker compose -f "$REPO_DIR/docker/docker-compose.prod.yml" --env-file "$REPO_DIR/.env")
MIGRATE_IMAGE="${MIGRATE_IMAGE:-migrate/migrate:v4.18.3}"

if [ $# -eq 0 ]; then
  sed -n '2,12p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
  exit 2
fi

pg_container="$("${COMPOSE[@]}" ps -q postgres)"
[ -n "$pg_container" ] || {
  echo "migrate: the postgres container is not running" >&2
  exit 1
}

PGPASSWORD="$("${COMPOSE[@]}" exec -T postgres printenv POSTGRES_PASSWORD)"
export PGPASSWORD

tty_flags=(-i)
[ -t 0 ] && [ -t 1 ] && tty_flags=(-it) # `down` without N asks for confirmation

exec docker run --rm "${tty_flags[@]}" --network "container:$pg_container" -e PGPASSWORD \
  -v "$REPO_DIR/migrations:/migrations:ro" \
  "$MIGRATE_IMAGE" -path=/migrations \
  -database "postgres://cozy@localhost:5432/cozy?sslmode=disable" "$@"
