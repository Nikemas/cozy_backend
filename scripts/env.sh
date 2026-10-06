# shellcheck shell=bash
# Per-environment settings shared by deploy.sh, migrate.sh and backup.sh.
# Sourced, not executed. Expects REPO_DIR to be set by the caller.
#
# DEPLOY_ENV selects the stack (default: staging):
#
#   staging     the current staging VPS, exactly as before this file existed:
#               compose project name left to compose's default ("docker",
#               the compose file's directory — renaming it would orphan the
#               existing docker_postgres_data/docker_minio_data volumes),
#               .env, docker/Caddyfile, image cozy-backend, health check
#               https://cozy.erpsystemsales.com/readyz.
#   production  the live shop (cozy.kg), on its OWN server/checkout:
#               compose project COMPOSE_PROJECT_NAME (default cozy-prod) so
#               containers/volumes never collide with staging, ENV_FILE
#               (default .env), docker/Caddyfile.prod (hosts from SITE_HOST /
#               MEDIA_HOST in the env file, www.SITE_HOST redirects to
#               SITE_HOST), image cozy-backend-prod, health check
#               https://$SITE_HOST/readyz.
#
# Overridable for both: ENV_FILE (path relative to the repo root),
# HEALTH_URL. Sets: DEPLOY_ENV, ENV_FILE, ENV_PATH, CADDYFILE, BACKEND_IMAGE,
# COMPOSE_FILE, COMPOSE (array), LOCK_NAME, HEALTH_URL, plus envfile_val().

DEPLOY_ENV="${DEPLOY_ENV:-staging}"
COMPOSE_FILE=docker/docker-compose.prod.yml

# envfile_val KEY: value of KEY from the env file (first match, surrounding
# quotes stripped; empty if absent — never a failure under pipefail). grep
# instead of `source`, so a shell metacharacter in an unrelated secret can't
# execute anything.
envfile_val() {
  { grep -E "^$1=" "$ENV_PATH" 2>/dev/null || true; } | head -n1 | cut -d= -f2- | sed -e 's/^["'\'']//' -e 's/["'\'']$//'
}

case "$DEPLOY_ENV" in
  staging)
    ENV_FILE="${ENV_FILE:-.env}"
    CADDYFILE=Caddyfile
    BACKEND_IMAGE=cozy-backend
    LOCK_NAME=cozy-deploy.lock
    project_args=()
    default_health_url=https://cozy.erpsystemsales.com/readyz
    ;;
  production)
    ENV_FILE="${ENV_FILE:-.env}"
    CADDYFILE=Caddyfile.prod
    BACKEND_IMAGE=cozy-backend-prod
    LOCK_NAME=cozy-deploy-production.lock
    project_args=(-p "${COMPOSE_PROJECT_NAME:-cozy-prod}")
    default_health_url=""
    ;;
  *)
    echo "DEPLOY_ENV must be 'staging' or 'production', got '$DEPLOY_ENV'" >&2
    exit 2
    ;;
esac

ENV_PATH="$REPO_DIR/$ENV_FILE"

if [ "$DEPLOY_ENV" = production ] && [ -f "$ENV_PATH" ]; then
  site_host="$(envfile_val SITE_HOST)"
  [ -n "$site_host" ] && default_health_url="https://$site_host/readyz"
fi
HEALTH_URL="${HEALTH_URL:-$default_health_url}"

# Interpolated by docker/docker-compose.prod.yml (env_file, caddy mount,
# image name). Staging values equal the compose file's defaults.
export DEPLOY_ENV ENV_FILE CADDYFILE BACKEND_IMAGE

# ${a[@]+"${a[@]}"}: an empty array under `set -u` is an error in bash < 4.4.
COMPOSE=(docker compose ${project_args[@]+"${project_args[@]}"} -f "$REPO_DIR/$COMPOSE_FILE" --env-file "$ENV_PATH")
