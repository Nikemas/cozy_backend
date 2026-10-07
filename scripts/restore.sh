#!/usr/bin/env bash
# Restore a Postgres dump made by scripts/backup.sh (pg_dump -Fc --no-owner
# --no-acl) into an EXPLICITLY named database, optionally followed by the
# MinIO media mirror. See docs/deployment.md, section 8.
#
# Usage:
#   scripts/restore.sh --target DB [options] DUMP_FILE
#
# Options:
#   --target DB               database to restore into (required, no default)
#   --replace                 the target exists and has tables: replace it.
#                             The old database is NOT dropped — it is renamed
#                             to DB_old_<timestamp> and kept until you drop it
#   --i-know-this-is-prod     allow a target that looks like a live database
#                             ("cozy" or any name containing "prod"); you will
#                             also have to type <env>/<db> on the terminal
#                             (over ssh use `ssh -t`)
#   --allow-running-backend   compose mode: do not refuse a live-looking target
#                             while the backend container is running
#   --database-url URL        local mode: talk to this Postgres server with the
#                             host's psql/pg_restore instead of the compose
#                             `postgres` container, e.g.
#                             postgres://me@localhost:5432/postgres?sslmode=disable
#                             (the database in the URL is only used to connect;
#                             no password in the URL — use PGPASSFILE/PGPASSWORD)
#   --media DIR               compose mode only: after the database, mirror
#                             DIR/<bucket> (the backup.sh mirror,
#                             $BACKUP_DIR/minio) back into the MinIO bucket.
#                             Additive: nothing in the bucket is deleted. Asks
#                             to type the bucket name
#   -h, --help                this text
#
# How it works (the live database is never left empty):
#   1. every check runs first — arguments, confirmations, --media preflight —
#      before anything is created or renamed;
#   2. the dump goes into a fresh temporary database DB_restore_<timestamp>,
#      in one transaction with --exit-on-error. On failure only that
#      temporary database is dropped; the target is untouched;
#   3. only after a successful restore: connections to the target are
#      blocked and terminated, the target is renamed to DB_old_<timestamp>
#      and the temporary database is renamed to DB.
#
# Compose mode (default) uses the same stack as backup.sh: DEPLOY_ENV
# (staging | production) via scripts/env.sh, `docker compose exec postgres`,
# and takes the same locks as deploy.sh and backup.sh.
set -euo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DOCKER="${DOCKER:-docker}" # overridable only so the script can be dry-run tested
MC_IMAGE="${MC_IMAGE:-quay.io/minio/mc:RELEASE.2025-08-13T08-35-41Z}"
BACKUP_DIR="${BACKUP_DIR:-/var/backups/cozy}"
PG_USER=cozy                       # role used by docker-compose.prod.yml / backup.sh
DB_NAME_RE='^[a-z_][a-z0-9_]{0,39}$' # 40 max: room for _restore_/_old_ + timestamp (63 limit)
BUCKET_RE='^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$'
SWAP_WAIT_SECONDS=10
# libpq URL parameters allowed in --database-url; anything else (dbname=,
# options=, service=, password=, percent-encoded keys...) is refused.
URL_QUERY_KEYS="sslmode connect_timeout sslrootcert sslcert sslkey application_name target_session_attrs"
FREE_SPACE_FACTOR=3 # temp DB + old DB + dump on one disk

log() { echo "restore: $*"; }
die() {
  echo "restore: ОШИБКА: $*" >&2
  exit 1
}
usage() { sed -n '2,/^set -euo/{/^set -euo/d;s/^# \{0,1\}//;p;}' "${BASH_SOURCE[0]}"; }

# matches VALUE REGEX: byte-wise (LC_ALL=C), so locale ranges can't widen [a-z].
matches() {
  local LC_ALL=C
  [[ "$1" =~ $2 ]]
}

# --- arguments --------------------------------------------------------------
target=""
replace=0
prod_ok=0
allow_backend=0
database_url=""
media_dir=""
dump=""
while [ $# -gt 0 ]; do
  case "$1" in
    --target) target="${2:-}"; shift 2 || die "--target требует имя базы" ;;
    --target=*) target="${1#*=}"; shift ;;
    --replace) replace=1; shift ;;
    --i-know-this-is-prod) prod_ok=1; shift ;;
    --allow-running-backend) allow_backend=1; shift ;;
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
matches "$target" "$DB_NAME_RE" || die "недопустимое имя базы '$target' (a-z, 0-9, _, не с цифры, до 40 символов)"
case "$target" in
  postgres|template0|template1) die "в служебную базу '$target' восстанавливать нельзя" ;;
esac
[ -n "$dump" ] || die "не указан файл дампа (scripts/restore.sh --target <имя> <файл.dump>)"
[ -f "$dump" ] && [ -s "$dump" ] || die "файл дампа '$dump' не найден или пустой"
case "$dump" in *.partial) die "'$dump' — недописанный дамп (.partial), он не прошёл проверку backup.sh" ;; esac

stamp="$(date +%Y%m%d%H%M%S)"
tmp_db="${target}_restore_${stamp}"
old_db="${target}_old_${stamp}"

# --- environment: compose or plain URL --------------------------------------
check_database_url() {
  matches "$database_url" '^postgres(ql)?://[^/?#]+/[^?#]*(\?[^#]*)?$' \
    || die "--database-url должен быть вида postgres://user@host:port/dbname[?параметры]"
  local authority="${database_url#*://}"
  authority="${authority%%/*}"
  case "$authority" in
    *:*@*) die "пароль в --database-url не принимается (виден в ps и истории) — используйте PGPASSFILE (~/.pgpass) или PGPASSWORD" ;;
  esac
  local query="" pair key
  case "$database_url" in *\?*) query="${database_url#*\?}" ;; esac
  local IFS='&'
  for pair in $query; do
    key="${pair%%=*}"
    case " $URL_QUERY_KEYS " in
      *" $key "*) ;;
      *) die "параметр '$key' в --database-url не разрешён (допустимы: $URL_QUERY_KEYS); \
база задаётся только --target, пароль — через PGPASSFILE/PGPASSWORD" ;;
    esac
  done
}

if [ -n "$database_url" ]; then
  mode=local
  env_label=local
  [ -z "$media_dir" ] || die "--media работает только в режиме docker compose (без --database-url)"
  check_database_url
  for bin in psql pg_restore; do
    command -v "$bin" >/dev/null 2>&1 || die "'$bin' не установлен (нужен клиент PostgreSQL 16+)"
  done
  url_base="${database_url%%\?*}"
  url_query=""
  [ "$url_base" = "$database_url" ] || url_query="?${database_url#*\?}"
  url_prefix="${url_base%/*}"
  log "сервер: ${url_prefix#*://} (режим --database-url)"
else
  mode=compose
  # shellcheck source=scripts/env.sh
  . "$REPO_DIR/scripts/env.sh"
  COMPOSE=("$DOCKER" "${COMPOSE[@]:1}")
  env_label="$DEPLOY_ENV"
  compose_project="docker (по умолчанию)"
  [ "${#project_args[@]}" -eq 0 ] || compose_project="${project_args[1]}"
  [ -f "$ENV_PATH" ] || die "$ENV_PATH не найден"
  log "окружение: $DEPLOY_ENV, compose-проект: $compose_project, env-файл: $ENV_PATH"
  [ -n "$("${COMPOSE[@]}" ps -q postgres)" ] || die "контейнер postgres не запущен ($DEPLOY_ENV)"
fi

# url_for DB: libpq URL of database DB on the --database-url server.
url_for() { echo "$url_prefix/$1$url_query"; }

# psql_on DB SQL: one value per line, stops on the first SQL error.
psql_on() {
  if [ "$mode" = local ]; then
    psql -X -q -v ON_ERROR_STOP=1 -At -d "$(url_for "$1")" -c "$2"
  else
    "${COMPOSE[@]}" exec -T postgres psql -X -q -v ON_ERROR_STOP=1 -At -U "$PG_USER" -d "$1" -c "$2"
  fi
}

# pg_restore_into DB ARGS...: pg_restore into DB, dump on stdin.
pg_restore_into() {
  local db="$1"
  shift
  if [ "$mode" = local ]; then
    pg_restore "$@" -d "$(url_for "$db")"
  else
    "${COMPOSE[@]}" exec -T postgres pg_restore "$@" -U "$PG_USER" -d "$db"
  fi
}

pg_list() {
  if [ "$mode" = local ]; then
    pg_restore --list
  else
    "${COMPOSE[@]}" exec -T postgres pg_restore --list
  fi
}

# --- locks: no deploy/backup/second restore while we work --------------------
take_locks() {
  [ "$mode" = compose ] || return 0
  if ! command -v flock >/dev/null 2>&1; then
    log "flock не установлен — блокировка от параллельного deploy/backup не взята"
    return 0
  fi
  exec 9>"${TMPDIR:-/tmp}/$LOCK_NAME"
  flock -n 9 || die "идёт deploy или другое восстановление (блокировка $LOCK_NAME)"
  if [ -d "$BACKUP_DIR" ] && [ -w "$BACKUP_DIR" ]; then
    exec 8>"$BACKUP_DIR/.lock"
    flock -n 8 || die "идёт backup.sh (блокировка $BACKUP_DIR/.lock) — дождитесь окончания"
  fi
}

# --- safety checks (nothing is changed until all of them pass) ---------------
looks_like_prod() {
  case "$1" in
    cozy|*prod*) return 0 ;;
  esac
  return 1
}

require_tty() {
  { : </dev/tty; } 2>/dev/null \
    || die "нет терминала для подтверждения ($1) — запустите вручную (по ssh: ssh -t), не из cron/CI"
}

# confirm_typed WHAT VALUE: the operator must type VALUE on the terminal.
confirm_typed() {
  local answer
  printf 'restore: введите %s "%s" для подтверждения: ' "$1" "$2" >/dev/tty
  read -r answer </dev/tty || answer=""
  [ "$answer" = "$2" ] || die "подтверждение не совпало — ничего не изменено"
}

check_prod_target() {
  looks_like_prod "$target" || return 0
  [ "$prod_ok" = 1 ] || die "база '$target' похожа на рабочую (prod/staging). Если это действительно нужно — \
остановите backend и добавьте --i-know-this-is-prod"
  if [ "$mode" = compose ] && [ "$allow_backend" != 1 ] && [ -n "$("${COMPOSE[@]}" ps -q backend)" ]; then
    die "контейнер backend запущен — сначала остановите его (docker compose ... stop backend) \
или добавьте --allow-running-backend"
  fi
  require_tty "имя базы"
  log "ВНИМАНИЕ: восстановление в рабочую базу '$target' (окружение $env_label${compose_project:+, compose-проект $compose_project})"
  confirm_typed "окружение/базу" "$env_label/$target"
}

media_preflight() {
  [ -n "$media_dir" ] || return 0
  bucket="${MINIO_BUCKET:-$(envfile_val MINIO_BUCKET)}"
  bucket="${bucket:-cozy-media}"
  matches "$bucket" "$BUCKET_RE" || die "недопустимое имя бакета '$bucket'"
  [ -d "$media_dir/$bucket" ] || die "нет каталога $media_dir/$bucket (зеркало backup.sh: \$BACKUP_DIR/minio)"
  media_abs="$(cd "$media_dir" && pwd)"
  minio_access="$(envfile_val MINIO_ACCESS_KEY)"
  minio_secret="$(envfile_val MINIO_SECRET_KEY)"
  [ -n "$minio_access" ] && [ -n "$minio_secret" ] || die "MINIO_ACCESS_KEY/MINIO_SECRET_KEY не найдены в $ENV_PATH"
  minio_id="$("${COMPOSE[@]}" ps -q minio)"
  [ -n "$minio_id" ] || die "контейнер minio не запущен"
  require_tty "имя бакета"
  confirm_typed "имя бакета" "$bucket"
}

check_dump() {
  local toc entries
  log "проверка дампа $dump"
  toc="$(pg_list <"$dump")" \
    || die "'$dump' не читается pg_restore --list (обрезан или повреждён) — возьмите другой дамп"
  entries="$(printf '%s\n' "$toc" | grep -cv '^;' || true)"
  [ "${entries:-0}" -gt 0 ] || die "'$dump' пустой по pg_restore --list — это не дамп backup.sh (pg_dump -Fc)"
  log "дамп читается, объектов: $entries"
}

# check_target_state: sets target_exists=0|1; refuses a non-empty target without --replace.
check_target_state() {
  local tables
  for db in "$tmp_db" "$old_db"; do
    [ -z "$(psql_on postgres "SELECT 1 FROM pg_database WHERE datname = '$db'")" ] \
      || die "база '$db' уже существует — повторите через секунду"
  done
  target_exists=0
  [ "$(psql_on postgres "SELECT 1 FROM pg_database WHERE datname = '$target'")" = 1 ] || return 0
  target_exists=1
  tables="$(psql_on "$target" "SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
    WHERE c.relkind IN ('r','p') AND n.nspname NOT IN ('pg_catalog','information_schema') AND n.nspname NOT LIKE 'pg_toast%'")"
  if [ "$tables" -gt 0 ] && [ "$replace" != 1 ]; then
    die "база '$target' существует и не пустая ($tables табл.). Добавьте --replace (старая база сохранится как ${target}_old_<время>)"
  fi
  log "база '$target' существует ($tables табл.) — после восстановления станет '$old_db'"
}

# check_free_space: compose mode only — warn (not refuse) when the postgres
# volume has less than FREE_SPACE_FACTOR x dump size free.
check_free_space() {
  [ "$mode" = compose ] || return 0
  local dump_kb free_kb
  dump_kb=$(( ($(wc -c <"$dump") + 1023) / 1024 ))
  free_kb="$("${COMPOSE[@]}" exec -T postgres df -Pk /var/lib/postgresql/data 2>/dev/null | awk 'NR==2 {print $4}' || true)"
  case "$free_kb" in
    ''|*[!0-9]*) log "свободное место на томе postgres измерить не удалось — проверьте вручную (df -h)"; return 0 ;;
  esac
  log "свободно на томе postgres: $((free_kb / 1024)) МБ, дамп: $((dump_kb / 1024)) МБ"
  if [ "$free_kb" -lt $((dump_kb * FREE_SPACE_FACTOR)) ]; then
    log "ВНИМАНИЕ: свободного места меньше ${FREE_SPACE_FACTOR}× размера дампа — на время восстановления на диске будут"
    log "  старая база, временная база и дамп; при нехватке места восстановление упадёт (целевая база не пострадает)"
  fi
}

# --- restore into a temporary database ----------------------------------------
restore_to_temp() {
  local started=$SECONDS
  log "создаю временную базу '$tmp_db'"
  psql_on postgres "CREATE DATABASE \"$tmp_db\""
  log "восстанавливаю $dump -> $tmp_db (одна транзакция)"
  if ! pg_restore_into "$tmp_db" --no-owner --no-acl --exit-on-error --single-transaction <"$dump"; then
    # Only the temporary database is dropped; the target was never touched.
    psql_on postgres "DROP DATABASE IF EXISTS \"$tmp_db\"" \
      || echo "restore: не удалось удалить '$tmp_db' — удалите вручную" >&2
    die "pg_restore завершился с ошибкой — временная база удалена, '$target' не изменена"
  fi
  log "дамп восстановлен во временную базу за $((SECONDS - started)) с"
}

print_summary() {
  local migr n
  migr="$(psql_on "$1" "SELECT version || CASE WHEN dirty THEN ' (dirty)' ELSE '' END FROM schema_migrations" 2>/dev/null || true)"
  log "schema_migrations: ${migr:-нет таблицы}"
  for t in products product_variants orders order_items customers staff payments; do
    n="$(psql_on "$1" "SELECT count(*) FROM $t" 2>/dev/null || echo "—")"
    log "  $t: $n"
  done
}

# --- swap: target -> old, temp -> target ----------------------------------------
wait_for_no_sessions() {
  local waited=0
  while [ "$(psql_on postgres "SELECT count(*) FROM pg_stat_activity WHERE datname = '$target' AND pid <> pg_backend_pid()")" != 0 ]; do
    [ "$waited" -lt "$SWAP_WAIT_SECONDS" ] || return 1
    psql_on postgres "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = '$target' AND pid <> pg_backend_pid()" >/dev/null
    sleep 1
    waited=$((waited + 1))
  done
}

# While the target refuses connections (swap in progress), any exit —
# error, Ctrl-C, SIGTERM, lost SSH — must re-open it, or the shop stays
# down with a perfectly good database. swap_state: "" | blocked | done.
swap_state=""
reopen_connections() {
  local db
  for db in "$target" "$old_db"; do
    [ "$(psql_on postgres "SELECT 1 FROM pg_database WHERE datname = '$db' AND NOT datallowconn" 2>/dev/null)" = 1 ] \
      || continue
    if psql_on postgres "ALTER DATABASE \"$db\" WITH ALLOW_CONNECTIONS true" 2>/dev/null; then
      echo "restore: подключения к '$db' снова разрешены" >&2
    else
      echo "restore: ВНИМАНИЕ: не удалось разрешить подключения к '$db' — выполните вручную:" >&2
      echo "restore:   ALTER DATABASE \"$db\" WITH ALLOW_CONNECTIONS true;  (см. docs/deployment.md, 8.4)" >&2
    fi
  done
}
on_exit() {
  local rc=$?
  if [ "$swap_state" = blocked ]; then
    echo "restore: прервано во время переключения — возвращаю подключения" >&2
    reopen_connections
    echo "restore: '$target' не изменена; восстановленная копия осталась как '$tmp_db' (удалить: DROP DATABASE \"$tmp_db\")" >&2
  fi
  exit "$rc"
}
trap on_exit EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
trap 'exit 129' HUP

swap_into_place() {
  if [ "$target_exists" != 1 ]; then
    psql_on postgres "ALTER DATABASE \"$tmp_db\" RENAME TO \"$target\""
    log "база '$target' создана из дампа"
    return 0
  fi
  log "переключение: '$target' -> '$old_db', '$tmp_db' -> '$target'"
  swap_state=blocked # set first: a signal right after the ALTER is still covered
  psql_on postgres "ALTER DATABASE \"$target\" WITH ALLOW_CONNECTIONS false"
  if ! wait_for_no_sessions \
    || ! psql_on postgres "ALTER DATABASE \"$target\" RENAME TO \"$old_db\"; ALTER DATABASE \"$tmp_db\" RENAME TO \"$target\""; then
    die "не удалось переключить базы (остались подключения?). '$target' не изменена, восстановленная копия — '$tmp_db'"
  fi
  swap_state=done
  psql_on postgres "ALTER DATABASE \"$old_db\" WITH ALLOW_CONNECTIONS true" \
    || echo "restore: ВНИМАНИЕ: подключения к '$old_db' остались запрещены (на работу магазина не влияет); \
вернуть: ALTER DATABASE \"$old_db\" WITH ALLOW_CONNECTIONS true" >&2
  log "старая база сохранена как '$old_db'. Когда убедитесь, что всё в порядке, удалите её:"
  if [ "$mode" = compose ]; then
    log "  docker compose ... exec -T postgres dropdb -U $PG_USER $old_db"
  else
    log "  psql '$(url_for postgres)' -c 'DROP DATABASE \"$old_db\"'"
  fi
}

# --- media ------------------------------------------------------------------
restore_media() {
  [ -n "$media_dir" ] || return 0
  log "медиа: $media_abs/$bucket -> бакет $bucket (только добавление/перезапись, без удаления)"
  # Same pattern as backup.sh: credentials passed by name, never on argv.
  MC_HOST_cozy="http://${minio_access}:${minio_secret}@localhost:9000" "$DOCKER" run --rm \
    --network "container:$minio_id" \
    -e MC_HOST_cozy \
    -v "$media_abs:/backup:ro" \
    "$MC_IMAGE" mirror --overwrite --quiet "/backup/$bucket" "cozy/$bucket" \
    || die "mc mirror (восстановление медиа) не удался — база уже восстановлена"
  log "медиа восстановлены"
}

# --- main -----------------------------------------------------------------------
take_locks
check_prod_target
media_preflight
check_dump
check_target_state
check_free_space
restore_to_temp
print_summary "$tmp_db"
swap_into_place
restore_media

if [ "$mode" = compose ] && [ "$target" = cozy ]; then
  log "дальше: bash scripts/migrate.sh up (докатить миграции новее дампа), затем start backend и /readyz"
elif [ "$target" != cozy ]; then
  log "backend читает базу cozy — '$target' только для проверки"
fi
log "готово"
