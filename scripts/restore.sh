#!/usr/bin/env bash
# Restore a Postgres dump made by scripts/backup.sh (pg_dump -Fc --no-owner
# --no-acl) into an EXPLICITLY named database, optionally followed by the
# MinIO media mirror. See docs/deployment.md, section 8.
#
# Usage:
#   scripts/restore.sh --target DB [options] DUMP_FILE
#
# Options:
#   --target DB             database to restore into (required, no default)
#   --replace               target exists and has tables: drop and recreate it
#                           (WITH (FORCE) — open connections are terminated)
#   --i-know-this-is-prod   allow a target that looks like a live database
#                           ("cozy" or any name containing "prod"); you will
#                           also have to type the database name on the terminal
#   --database-url URL      local mode: talk to this Postgres server with the
#                           host's psql/pg_restore instead of the compose
#                           `postgres` container, e.g.
#                           postgres://me@localhost:5432/postgres?sslmode=disable
#                           (the database in the URL is only used to connect;
#                           the target is always --target)
#   --media DIR             compose mode only: after the database, mirror
#                           DIR/<bucket> (the backup.sh mirror,
#                           $BACKUP_DIR/minio) back into the MinIO bucket.
#                           Additive: nothing in the bucket is deleted. Asks to
#                           type the bucket name
#   -h, --help              this text
#
# Compose mode (default) uses the same stack as backup.sh: DEPLOY_ENV
# (staging | production) via scripts/env.sh, `docker compose exec postgres`.
#
# The restore runs in a single transaction with --exit-on-error: a broken
# dump leaves the target empty, never half-restored.
set -euo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DOCKER="${DOCKER:-docker}" # overridable only so the script can be dry-run tested
MC_IMAGE="${MC_IMAGE:-quay.io/minio/mc:RELEASE.2025-08-13T08-35-41Z}"
PG_USER=cozy               # role used by docker-compose.prod.yml / backup.sh
DB_NAME_RE='^[a-z_][a-z0-9_]{0,62}$'

log() { echo "restore: $*"; }
die() {
  echo "restore: ОШИБКА: $*" >&2
  exit 1
}
usage() { sed -n '2,/^set -euo/{/^set -euo/d;s/^# \{0,1\}//;p;}' "${BASH_SOURCE[0]}"; }

# --- arguments --------------------------------------------------------------
target=""
replace=0
prod_ok=0
database_url=""
media_dir=""
dump=""
while [ $# -gt 0 ]; do
  case "$1" in
    --target) target="${2:-}"; shift 2 || die "--target требует имя базы" ;;
    --target=*) target="${1#*=}"; shift ;;
    --replace) replace=1; shift ;;
    --i-know-this-is-prod) prod_ok=1; shift ;;
    --database-url) database_url="${2:-}"; shift 2 || die "--database-url требует URL" ;;
    --database-url=*) database_url="${1#*=}"; shift ;;
    --media) media_dir="${2:-}"; shift 2 || die "--media требует каталог" ;;
    --media=*) media_dir="${1#*=}"; shift ;;
    -h|--help) usage; exit 0 ;;
    -*) die "неизвестный параметр $1 (см. --help)" ;;
    *) [ -z "$dump" ] || die "указано больше одного файла дампа"; dump="$1"; shift ;;
  esac
done

[ -n "$target" ] || die "не указана целевая база: --target <имя> (по умолчанию ничего не восстанавливается)"
[[ "$target" =~ $DB_NAME_RE ]] || die "недопустимое имя базы '$target' (только a-z, 0-9, _ и не с цифры)"
case "$target" in
  postgres|template0|template1) die "в служебную базу '$target' восстанавливать нельзя" ;;
esac
[ -n "$dump" ] || die "не указан файл дампа (scripts/restore.sh --target <имя> <файл.dump>)"
[ -f "$dump" ] && [ -s "$dump" ] || die "файл дампа '$dump' не найден или пустой"
case "$dump" in *.partial) die "'$dump' — недописанный дамп (.partial), он не прошёл проверку backup.sh" ;; esac

# --- environment: compose or plain URL --------------------------------------
if [ -n "$database_url" ]; then
  mode=local
  [ -z "$media_dir" ] || die "--media работает только в режиме docker compose (без --database-url)"
  [[ "$database_url" =~ ^postgres(ql)?://[^/]+/ ]] \
    || die "--database-url должен быть вида postgres://user@host:port/dbname[?параметры]"
  for bin in psql pg_restore; do
    command -v "$bin" >/dev/null 2>&1 || die "'$bin' не установлен (нужен клиент PostgreSQL 16+)"
  done
  url_base="${database_url%%\?*}"
  url_query=""
  [ "$url_base" = "$database_url" ] || url_query="?${database_url#*\?}"
  url_prefix="${url_base%/*}"
  admin_url="$url_prefix/postgres$url_query"
  target_url="$url_prefix/$target$url_query"
else
  mode=compose
  # shellcheck source=scripts/env.sh
  . "$REPO_DIR/scripts/env.sh"
  COMPOSE=("$DOCKER" "${COMPOSE[@]:1}")
  [ -f "$ENV_PATH" ] || die "$ENV_PATH не найден"
  [ -n "$("${COMPOSE[@]}" ps -q postgres)" ] || die "контейнер postgres не запущен ($DEPLOY_ENV)"
fi

# psql_on DB SQL: one value per line, stops on the first SQL error.
psql_on() {
  if [ "$mode" = local ]; then
    local url="$admin_url"
    [ "$1" = postgres ] || url="$target_url"
    psql -X -q -v ON_ERROR_STOP=1 -At -d "$url" -c "$2"
  else
    "${COMPOSE[@]}" exec -T postgres psql -X -q -v ON_ERROR_STOP=1 -At -U "$PG_USER" -d "$1" -c "$2"
  fi
}

# pg_restore_target ARGS...: pg_restore against the target, dump on stdin.
pg_restore_target() {
  if [ "$mode" = local ]; then
    pg_restore "$@" -d "$target_url"
  else
    "${COMPOSE[@]}" exec -T postgres pg_restore "$@" -U "$PG_USER" -d "$target"
  fi
}

pg_list() {
  if [ "$mode" = local ]; then
    pg_restore --list
  else
    "${COMPOSE[@]}" exec -T postgres pg_restore --list
  fi
}

# --- safety checks ------------------------------------------------------------
looks_like_prod() {
  case "$1" in
    cozy|*prod*) return 0 ;;
  esac
  return 1
}

# confirm_typed WHAT VALUE: the operator must type VALUE on the terminal.
confirm_typed() {
  { : </dev/tty; } 2>/dev/null || die "нет терминала для подтверждения ($1) — запустите скрипт вручную, не из cron/CI"
  local answer
  printf 'restore: введите %s "%s" для подтверждения: ' "$1" "$2" >/dev/tty
  read -r answer </dev/tty || answer=""
  [ "$answer" = "$2" ] || die "подтверждение не совпало — ничего не изменено"
}

if looks_like_prod "$target"; then
  [ "$prod_ok" = 1 ] || die "база '$target' похожа на рабочую (prod/staging). Если это действительно нужно — \
добавьте --i-know-this-is-prod и остановите backend (docker compose ... stop backend)"
  log "ВНИМАНИЕ: восстановление в рабочую базу '$target'${DEPLOY_ENV:+ (DEPLOY_ENV=$DEPLOY_ENV)}"
  confirm_typed "имя базы" "$target"
fi

log "проверка дампа $dump"
toc="$(pg_list <"$dump")" \
  || die "'$dump' не читается pg_restore --list (обрезан или повреждён) — возьмите другой дамп"
entries="$(printf '%s\n' "$toc" | grep -cv '^;' || true)"
[ "${entries:-0}" -gt 0 ] || die "'$dump' не читается pg_restore --list — это не дамп backup.sh (pg_dump -Fc)"
log "дамп читается, объектов: $entries"

exists="$(psql_on postgres "SELECT 1 FROM pg_database WHERE datname = '$target'")"
if [ "$exists" = 1 ]; then
  tables="$(psql_on "$target" "SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
    WHERE c.relkind IN ('r','p') AND n.nspname NOT IN ('pg_catalog','information_schema') AND n.nspname NOT LIKE 'pg_toast%'")"
  if [ "$tables" -gt 0 ]; then
    [ "$replace" = 1 ] || die "база '$target' существует и не пустая ($tables табл.). Добавьте --replace, чтобы удалить её и восстановить заново"
    log "удаляю базу '$target' ($tables табл.) — --replace"
    psql_on postgres "DROP DATABASE \"$target\" WITH (FORCE)"
    exists=""
  else
    log "база '$target' существует и пустая — восстанавливаю в неё"
  fi
fi
created=0
if [ "$exists" != 1 ]; then
  log "создаю базу '$target'"
  psql_on postgres "CREATE DATABASE \"$target\""
  created=1
fi

# --- restore ----------------------------------------------------------------
started=$SECONDS
log "восстанавливаю $dump -> $target (одна транзакция)"
if ! pg_restore_target --no-owner --no-acl --exit-on-error --single-transaction <"$dump"; then
  # Single transaction: the target is empty again. Drop it only if this run
  # created it, so a retry starts clean and nothing pre-existing disappears.
  [ "$created" = 1 ] && psql_on postgres "DROP DATABASE \"$target\"" || true
  die "pg_restore завершился с ошибкой — транзакция откатена, данные в '$target' не попали"
fi
log "база восстановлена за $((SECONDS - started)) с"

migr="$(psql_on "$target" "SELECT version || CASE WHEN dirty THEN ' (dirty)' ELSE '' END FROM schema_migrations" 2>/dev/null || true)"
log "schema_migrations: ${migr:-нет таблицы}"
for t in products product_variants orders customers payments; do
  n="$(psql_on "$target" "SELECT count(*) FROM $t" 2>/dev/null || echo "—")"
  log "  $t: $n"
done

# --- media ------------------------------------------------------------------
if [ -n "$media_dir" ]; then
  bucket="${MINIO_BUCKET:-$(envfile_val MINIO_BUCKET)}"
  bucket="${bucket:-cozy-media}"
  [ -d "$media_dir/$bucket" ] || die "нет каталога $media_dir/$bucket (зеркало backup.sh: \$BACKUP_DIR/minio)"
  access="$(envfile_val MINIO_ACCESS_KEY)"
  secret="$(envfile_val MINIO_SECRET_KEY)"
  [ -n "$access" ] && [ -n "$secret" ] || die "MINIO_ACCESS_KEY/MINIO_SECRET_KEY не найдены в $ENV_PATH"
  minio_id="$("${COMPOSE[@]}" ps -q minio)"
  [ -n "$minio_id" ] || die "контейнер minio не запущен"
  confirm_typed "имя бакета" "$bucket"
  media_abs="$(cd "$media_dir" && pwd)"
  log "медиа: $media_abs/$bucket -> бакет $bucket (только добавление/перезапись, без удаления)"
  # Same pattern as backup.sh: credentials passed by name, never on argv.
  MC_HOST_cozy="http://${access}:${secret}@localhost:9000" "$DOCKER" run --rm \
    --network "container:$minio_id" \
    -e MC_HOST_cozy \
    -v "$media_abs:/backup:ro" \
    "$MC_IMAGE" mirror --overwrite --quiet "/backup/$bucket" "cozy/$bucket" \
    || die "mc mirror (восстановление медиа) не удался"
  log "медиа восстановлены"
fi

if [ "$mode" = compose ] && [ "$target" = cozy ]; then
  log "дальше: bash scripts/migrate.sh up (докатить миграции новее дампа), затем start backend и /readyz"
fi
if [ "$target" != cozy ]; then
  log "backend читает базу cozy — '$target' только для проверки; удалить: DROP DATABASE \"$target\""
fi
log "готово"
