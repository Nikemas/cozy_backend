# cozy_backend

Go-бэкенд Cozy: REST API (`/api/v1/*`) для мобильного приложения, сайт (`/`) и админка (`/admin`) на `html/template`. Подробности — в [`../docs/Cozy — Технический ТЗ для разработчика.md`](../docs/Cozy%20—%20Технический%20ТЗ%20для%20разработчика.md).

## Стек

- Go (`net/http` + `http.ServeMux`, паттерны маршрутов из Go 1.22+ — без стороннего роутера)
- PostgreSQL через `pgx/v5` (`database/sql` + `stdlib`-драйвер)
- Миграции — `golang-migrate`
- MinIO (S3-совместимое хранилище медиа)

## Локальный запуск

```bash
cp .env.example .env
make dev-up        # поднимает postgres + minio
make migrate-up    # накатывает миграции из migrations/
make run           # запускает сервер на :8080
```

Проверка: `curl localhost:8080/healthz` (liveness) и `curl localhost:8080/readyz` (readiness: пинг Postgres + `GET /minio/health/live` у MinIO, таймаут 2 с на проверку; 503 с JSON `{"status":"unavailable","checks":{"db":"ok","minio":"error"}}`, если что-то недоступно).

## Эксплуатация: логи, лимиты, кэш

- **Request ID.** Каждый запрос получает `X-Request-ID` (берётся из входящего заголовка, если он корректный, иначе генерируется) — он возвращается в ответе, попадает в каждую строку лога (`request_id=…`) и в JSON-тело ошибок `apperr` (`"request_id"`). Пользователь присылает скрин ошибки → ищем по ID в `docker compose logs backend`.
- **Access log.** Одна строка на запрос: method, path, status, duration, bytes, remote (кроме `/healthz`, `/readyz`). `LOG_FORMAT=json` — JSON-строки вместо текста.
- **Паники** в хендлерах перехватываются: в лог — стек, клиенту — обычный `500 internal_error` с `request_id`.
- **Пул БД и таймауты HTTP** настраиваются через env (значения по умолчанию подходят для staging/прода):

| Переменная | По умолчанию | Что |
|---|---|---|
| `DB_MAX_OPEN_CONNS` | 25 | максимум соединений к Postgres (у Postgres по умолчанию `max_connections=100`) |
| `DB_MAX_IDLE_CONNS` | 10 | простаивающих соединений в пуле |
| `DB_CONN_MAX_LIFETIME` | 30m | пересоздавать соединение не реже |
| `DB_CONN_MAX_IDLE_TIME` | 5m | закрывать простаивающее соединение через |
| `HTTP_READ_HEADER_TIMEOUT` | 5s | защита от slowloris |
| `HTTP_READ_TIMEOUT` | 60s | чтение всего запроса (в т.ч. загрузка .xlsx импорта) |
| `HTTP_WRITE_TIMEOUT` | 60s | запись ответа |
| `HTTP_IDLE_TIMEOUT` | 120s | keep-alive |
| `HTTP_SHUTDOWN_TIMEOUT` | 10s | graceful shutdown при деплое |

- **Кэширование.** `GET /api/v1/categories`, `/api/v1/points` — `Cache-Control: public, max-age=60`, `GET /api/v1/products[/{id}]` — `max-age=30` (только ответы 200; ошибки не кэшируются). `/static/*` и `/admin/static/*` — `max-age=600` + `ETag` (повторный запрос → `304`). URL статики без хеша, поэтому max-age короткий. Сжатие gzip/zstd делает Caddy (`encode` в `docker/Caddyfile`), не Go.

## Структура

См. `cmd/server`, `internal/*` (по одному пакету на предметную область — `catalog`, `orders`, `payments`, `auth`, ...), `migrations/`, `web/templates`, `admin/templates`. Соглашения по ошибкам (`apperr`) и транзакциям (`withTx`) — см. §4 технического ТЗ.

## Auth: вход по OTP

Покупатели входят по номеру телефона с подтверждением SMS-кодом. Код генерирует, хранит и проверяет провайдер **Nikita SMSPro** (`smspro.nikita.kg`, OTP API) — наш backend хранит только `transaction_id`/`token` для связи запроса и проверки (`internal/auth`, `internal/notify`). См. §5 технического ТЗ.

```
POST /api/v1/auth/otp/request   { "phone": "+996700123456" }
POST /api/v1/auth/otp/verify    { "phone": "+996700123456", "code": "123456" }
POST /api/v1/auth/refresh       { "refresh_token": "..." }
```

Нужен `NIKITA_API_KEY` в `.env` (Личный кабинет Nikita → вкладка «СЕРВИС OTP»). Rate-limit на номер — не чаще раза в 60 сек, максимум 5 запросов в час (см. `internal/auth/service.go`), чтобы стоимость SMS не стала вектором злоупотребления.

## Уведомления: push и Telegram

После коммита заказа `internal/orders` вызывает `orders.Notifier` (реализация — `internal/notifications`), доставка идёт в фоне: не больше 16 задач одновременно, лишние события отбрасываются с предупреждением в логе, на одну задачу даётся 20 секунд. Ошибка доставки только логируется (slog) и никогда не ломает заказ или смену статуса.

- **Push покупателю** — при смене статуса заказа в админке (`confirmed`, `courier_assigned`, `delivered`, `cancelled`). Отправляется через FCM HTTP v1 (`internal/push`) на все устройства покупателя из `device_tokens`. Токены, которые FCM называет `UNREGISTERED`, `SENDER_ID_MISMATCH` или недействительными, удаляются. Тексты на русском, кыргызский тоже готов, но язык покупателя пока нигде не хранится, поэтому всегда уходит русский. Payload для приложения:
  `data = {"type":"order_status","order_id":"<uuid>","status":"<статус из БД>"}` плюс `notification.title/body`, Android channel `orders`.
- **Telegram персоналу** — при новом заказе (сайт, «Заказать сразу», мобильное API). В сообщении номер, сумма, оплата, доставка с адресом или самовывоз с точкой, покупатель, товары и ссылка на `/admin/orders/{id}`.

| Переменная | Зачем | Если пусто |
|---|---|---|
| `FCM_CREDENTIALS_FILE` | путь к JSON-ключу service account Firebase (на сервере — `/secrets/firebase-service-account.json`, папка `./secrets` рядом с репо монтируется read-only; файл должен быть читаем для контейнера, который работает от nonroot, т.е. `chmod 644`) | push только пишется в лог |
| `FCM_PROJECT_ID` | переопределить `project_id` из ключа | берётся из ключа |
| `TELEGRAM_BOT_TOKEN`, `TELEGRAM_CHAT_ID` | бот от @BotFather и id группы персонала | сообщение только пишется в лог |
| `PUBLIC_BASE_URL` | внешний адрес сайта для ссылки на заказ | ссылки в сообщении нет |
| `APP_MIN_VERSION`, `APP_LATEST_VERSION`, `APP_STORE_URL_IOS`, `APP_STORE_URL_ANDROID` | ответ `GET /api/v1/app/config` (минимальная и последняя версия приложения, ссылки на сторы) | `1.0.0` / `1.0.0` / `""` / `""` |

Если ключ FCM задан, но не читается или битый, сервер всё равно стартует: в лог уходит `push: FCM misconfigured`, а push работает как no-op.

## Docker / CI-CD

Продовый образ — `docker/Dockerfile` (multi-stage: builder на `golang:1.26-alpine`, рантайм на `gcr.io/distroless/static-debian12`). Собрать локально:

```bash
docker build -f docker/Dockerfile -t cozy_backend .
```

(Это отдельный файл от `docker/docker-compose.yml`, который только для локальной разработки — postgres + minio.)

CI (`.github/workflows/ci.yml`) на каждый push/PR гоняет `gofmt -l .`, `go vet ./...`, `go test ./...` и `golangci-lint`. Деплой (`.github/workflows/deploy.yml`) — на каждый push в `main` после тестов по SSH запускает `scripts/deploy.sh` на staging-VPS (сборка образа прямо на сервере, без registry). Подробнее — раздел «Staging-сервер → Деплой».

## Staging-сервер

VPS `95.215.244.199`, поднят через `docker/docker-compose.prod.yml` + `docker/Caddyfile`. Пока заказчик не передал `cozy.kg`, сервер живёт на нашем поддомене (DNS — Cloudflare, зона `erpsystemsales.com`):

| Хост | Что | Куда проксирует |
|------|-----|-----------------|
| `https://cozy.erpsystemsales.com` | сайт, `/admin`, `/api/v1/*` | `backend:8080` |
| `https://media.cozy.erpsystemsales.com` | MinIO S3 API (presigned-ссылки на фото) | `minio:9000` |

Сертификаты Let's Encrypt Caddy получает сам. В серверном `.env` обязательно `MINIO_PUBLIC_ENDPOINT=media.cozy.erpsystemsales.com` и `MINIO_PUBLIC_USE_SSL=true`, иначе загрузка фото в админке ломается. Порт 9000 наружу не публикуется. Когда появится `cozy.kg` — раскомментировать его блок в `Caddyfile`, staging-хост оставить.

### Деплой

Автоматически: каждый push в `main` → `.github/workflows/deploy.yml` прогоняет `go vet`/`go test`, затем по SSH запускает на сервере `scripts/deploy.sh` (fast-forward до `origin/main`, `docker compose up -d --build`, рестарт Caddy при изменении `Caddyfile`, ожидание `/readyz`). Нужные секреты репозитория — в шапке `deploy.yml`. Статус — вкладка Actions, environment `staging`.

Вручную (то же самое, что делает CI):

```bash
ssh -i ~/.ssh/cozy_vps root@95.215.244.199 'bash /opt/cozy/scripts/deploy.sh'
```

### Бэкапы

`scripts/backup.sh` (на сервере, из `/opt/cozy`):

1. `pg_dump -Fc` базы `cozy` из контейнера `postgres` → `$BACKUP_DIR/postgres/cozy_YYYYMMDD_HHMMSS.dump` (сначала `.partial`, проверка `pg_restore --list`, потом переименование — битый дамп никогда не выглядит как хороший);
2. удаляет дампы старше `KEEP_DAYS` дней (только после успешного шага 1);
3. при `MINIO_MIRROR=1` — `mc mirror` бакета медиа в `$BACKUP_DIR/minio/<bucket>/` (инкрементально; удалённые в MinIO файлы в зеркале остаются). `mc` запускается одноразовым контейнером `quay.io/minio/mc` в сетевом namespace контейнера `minio`, ставить `mc` на хост не нужно. Ключи берутся из `.env`; `MINIO_SECRET_KEY` не должен содержать `/`, `@`, `:`.

Переменные: `BACKUP_DIR` (по умолчанию `/var/backups/cozy`), `KEEP_DAYS` (14), `MINIO_MIRROR` (0), `MINIO_BUCKET` (из `.env`), `MC_IMAGE`. Ненулевой код выхода при любой ошибке.

Cron (root, каждую ночь в 03:30 по времени сервера; лог — в файл):

```cron
30 3 * * * MINIO_MIRROR=1 KEEP_DAYS=14 /opt/cozy/scripts/backup.sh >> /var/log/cozy-backup.log 2>&1
```

Бэкап на том же диске, что и база, не переживёт потерю VPS — дополнительно копируйте `$BACKUP_DIR` на другую машину (`rsync`/`rclone` отдельным cron-заданием).

**Восстановление Postgres** (перезаписывает текущие данные — сначала сделайте свежий дамп):

```bash
cd /opt/cozy
C="docker compose -f docker/docker-compose.prod.yml --env-file .env"
$C stop backend                                   # никто не пишет в базу
$C exec -T postgres dropdb -U cozy cozy
$C exec -T postgres createdb -U cozy cozy
$C exec -T postgres pg_restore -U cozy -d cozy --no-owner --no-acl --exit-on-error \
  < /var/backups/cozy/postgres/cozy_YYYYMMDD_HHMMSS.dump
$C start backend
curl -fsS https://cozy.erpsystemsales.com/readyz
```

Дамп включает таблицу `schema_migrations` golang-migrate, так что после восстановления `migrate up` докатит только миграции новее дампа. Проверить дамп без восстановления: `pg_restore --list <файл>`; восстановить в отдельную базу для проверки — `createdb cozy_check` и тот же `pg_restore -d cozy_check`.

**Восстановление медиа** (из зеркала обратно в бакет):

```bash
MINIO_ID=$($C ps -q minio)
MC_HOST_cozy="http://$MINIO_ACCESS_KEY:$MINIO_SECRET_KEY@localhost:9000" \
  docker run --rm --network container:$MINIO_ID -e MC_HOST_cozy -v /var/backups/cozy/minio:/backup \
  quay.io/minio/mc mirror --overwrite /backup/cozy-media cozy/cozy-media
```

## API-документация

Полная OpenAPI 3.0-спецификация всех реализованных эндпоинтов — [`openapi.yaml`](openapi.yaml) (§6 ТЗ). Открыть в любом Swagger UI/Redoc или сгенерировать клиент для Flutter. Обновлять по мере добавления новых эндпоинтов — файл описывает только то, что реально есть в коде.

## Статус

Готово: каркас (конфиг, роутер, `apperr`, подключение к Postgres), миграции всех таблиц из §3 ТЗ + `otp_codes`/`refresh_tokens`, вход покупателя по OTP (Nikita SMSPro) с JWT access/refresh.
Дальше по плану (§13 ТЗ): логин/RBAC для staff (админка) → каталог → MinIO → заказы → Bakai → остальной REST API.
