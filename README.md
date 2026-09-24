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
make migrate-up    # накатывает миграции из migrations/ (golang-migrate v4.18.3 через go run)
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

CI (`.github/workflows/ci.yml`) на каждый push/PR:

- **vet, test, fmt, lint** — `gofmt -l .`, `go vet ./...`, `go test ./...`, `golangci-lint`;
- **migrations up/down/up + integration tests** — поднимает Postgres 16 (service container), тем же образом `migrate/migrate:v4.18.3`, что и деплой, накатывает все миграции, откатывает все до нуля, накатывает снова (и проверяет итоговую версию), затем `go test -tags integration ./internal/integration/...`.

Интеграционные тесты (`internal/integration`, build tag `integration`) создают свою одноразовую базу `cozy_it_*`, накатывают миграции и гоняют настоящий код `orders` против настоящего Postgres: создание заказа (самовывоз/доставка) со списанием остатка, откат при нехватке остатка, гонка за последнюю единицу, онлайн-заказ + платёж + компенсирующая отмена с возвратом остатка, смена статуса в админке, корзина. Локально:

```bash
make dev-up                 # postgres + minio из docker/docker-compose.yml
make test-integration       # TEST_DATABASE_URL по умолчанию — postgres этого compose
```

Деплой (`.github/workflows/deploy.yml`) — на каждый push в `main` сначала целиком прогоняет `ci.yml` (reusable workflow), затем по SSH запускает `scripts/deploy.sh` на staging-VPS для ровно того коммита, который прошёл CI (сборка образа прямо на сервере, без registry). Подробнее — «Staging-сервер → Деплой».

## Staging-сервер

VPS `95.215.244.199`, поднят через `docker/docker-compose.prod.yml` + `docker/Caddyfile`. Пока заказчик не передал `cozy.kg`, сервер живёт на нашем поддомене (DNS — Cloudflare, зона `erpsystemsales.com`):

| Хост | Что | Куда проксирует |
|------|-----|-----------------|
| `https://cozy.erpsystemsales.com` | сайт, `/admin`, `/api/v1/*` | `backend:8080` |
| `https://media.cozy.erpsystemsales.com` | MinIO S3 API (presigned-ссылки на фото), только `GET`/`HEAD` | `minio:9000` |

Сертификаты Let's Encrypt Caddy получает сам. В серверном `.env` обязательно `MINIO_PUBLIC_ENDPOINT=media.cozy.erpsystemsales.com` и `MINIO_PUBLIC_USE_SSL=true`, иначе загрузка фото в админке ломается. Порт 9000 наружу не публикуется.

Что делает Caddy на всех хостах (`docker/Caddyfile`):

- заголовки безопасности: `Strict-Transport-Security` (1 год, без `includeSubDomains`), `X-Content-Type-Options: nosniff`, а также по умолчанию — если бэкенд не прислал свой — `X-Frame-Options: DENY`, `Content-Security-Policy: frame-ancestors 'none'`, `Referrer-Policy: strict-origin-when-cross-origin`;
- `X-Robots-Tag: noindex, nofollow` — staging не должен попадать в поиск;
- тело запроса до 20 МБ (загрузки в админке, импорт `.xlsx`), больше — `413`;
- пока `deploy.sh` пересоздаёт контейнер бэкенда, Caddy до 15 с повторяет подключение вместо мгновенного `502`;
- на media-хосте всё, кроме `GET`/`HEAD`, — `405`, служебный API MinIO `/minio/*` — `404`.

Версии образов в compose закреплены (`caddy:2.11.4-alpine`, `quay.io/minio/minio:RELEASE.2025-09-07T16-13-09Z`, `postgres:16-alpine`); обновлять — осознанно, правкой тега. У всех сервисов ротация логов (5 × 10 МБ) и `stop_grace_period: 30s`, у Postgres — healthcheck, бэкенд стартует только после `healthy`.

### Деплой

Автоматически: push в `main` → CI → `scripts/deploy.sh` на сервере. Шаги скрипта (старый бэкенд работает до шага 6):

1. проверки: есть `.env`, `git`, `docker compose`, `curl` (без `curl` — ошибка, а не тихий выход); одновременно идёт только один деплой (`flock`);
2. `git reset --hard` на проверенный коммит (`DEPLOY_REF`, из CI — `github.sha`);
3. если изменился `Caddyfile` — `caddy validate` закреплённым образом; невалидный конфиг — деплой прерывается;
4. сборка образа `cozy-backend:<sha12>`; образ, на котором бэкенд работал до деплоя, получает тег `cozy-backend:previous`;
5. Postgres поднят и `healthy`; если состояние миграций `dirty` или таблицы есть, а `schema_migrations` нет, — деплой останавливается с инструкцией (см. «Миграции»);
6. `scripts/migrate.sh up` (образ `migrate/migrate:v4.18.3`; пароль берётся из контейнера Postgres и передаётся через `PGPASSWORD`, в `ps` не виден). Упала миграция — деплой останавливается, старый бэкенд продолжает работать;
7. `docker compose up -d` с новым образом, рестарт Caddy, если менялся `Caddyfile`;
8. ожидание `/readyz` через Caddy (до 90 с). Не поднялся — логи бэкенда, **автоматический откат** на `cozy-backend:previous`, выход с ошибкой. Миграции при откате **не** откатываются;
9. успех — новый образ дополнительно получает тег `:latest`; хранятся 5 последних `cozy-backend:<sha>`, чистятся висячие слои и build-кэш старше недели.

Отсюда правило для миграций: новая миграция должна быть совместима с кодом предыдущего релиза (expand/contract — сначала добавить колонку/таблицу, удалять старое только релизом позже), иначе автоматический откат кода не спасёт.

Вручную (то же самое, что делает CI):

```bash
ssh deploy@95.215.244.199 'bash /opt/cozy/scripts/deploy.sh'
```

Ручной откат кода на предыдущий или любой из оставшихся образов:

```bash
cd /opt/cozy
docker image ls cozy-backend          # previous, latest и <sha12> последних деплоев
BACKEND_TAG=previous docker compose -f docker/docker-compose.prod.yml --env-file .env up -d --no-build --no-deps backend
curl -fsS https://cozy.erpsystemsales.com/readyz
```

Не откатывайте перезапуском старого workflow в Actions: у старого коммита нет файлов новых миграций, и `migrate up` на базе с более новой версией завершится ошибкой.

### Секреты деплоя и пользователь `deploy`

Секреты репозитория (Settings → Secrets and variables → Actions) — описаны в шапке `deploy.yml`: `DEPLOY_HOST`, `DEPLOY_USER`, `DEPLOY_SSH_KEY`, `DEPLOY_SSH_FINGERPRINT` (обязателен — без него деплой-джоба падает), `DEPLOY_PATH`, опционально `DEPLOY_SSH_PORT`.

`DEPLOY_SSH_FINGERPRINT` — отпечаток **host key сервера** в формате `SHA256:…`. Надёжнее всего взять его на самом сервере (не через сеть, где его можно подменить). SSH-клиент, который использует `appleboy/ssh-action`, предпочитает ECDSA-ключ хоста, поэтому нужен именно он:

```bash
ssh-keygen -lf /etc/ssh/ssh_host_ecdsa_key.pub | awk '{print $2}'    # на VPS
```

Если ECDSA-ключа на сервере нет, берите RSA (`ssh_host_rsa_key.pub`), а если нет и его — ED25519. При несовпадении джоба падает с `ssh: host key fingerprint mismatch`.

Деплоить лучше не под `root`. Один раз на VPS (под root):

```bash
adduser --disabled-password --gecos "" deploy
usermod -aG docker deploy                       # docker compose без sudo
install -d -m 700 -o deploy -g deploy /home/deploy/.ssh
echo 'ssh-ed25519 AAAA… github-actions-deploy' >> /home/deploy/.ssh/authorized_keys   # публичная половина DEPLOY_SSH_KEY
chown deploy:deploy /home/deploy/.ssh/authorized_keys && chmod 600 /home/deploy/.ssh/authorized_keys
chown -R deploy:deploy /opt/cozy                # git reset/сборка от имени deploy
# если /opt/cozy тянет origin по SSH с deploy-key репозитория — перенести ключ и known_hosts:
#   cp /root/.ssh/<ключ> /root/.ssh/known_hosts /home/deploy/.ssh/ && chown deploy:deploy /home/deploy/.ssh/*
su - deploy -c 'cd /opt/cozy && git fetch origin main && docker compose -f docker/docker-compose.prod.yml --env-file .env ps'
```

После проверки — секрет `DEPLOY_USER=deploy`. Группа `docker` по сути равна root на этом хосте, так что выигрыш в том, что можно запретить вход root по SSH (`PermitRootLogin no` — только убедившись, что есть другой способ войти с sudo) и что ключ GitHub не даёт интерактивного root-доступа.

### Миграции: dirty и восстановление

`golang-migrate` хранит состояние в таблице `schema_migrations` (`version`, `dirty`). Каждый файл миграции выполняется одним запросом, т. е. одной транзакцией: если он упал, из него не применилось ничего, но версия записана как `N (dirty)`, и дальше `migrate` ничего делать не будет. `deploy.sh` в этом состоянии останавливается сам.

На сервере (`/opt/cozy`) есть обёртка `scripts/migrate.sh` (тот же образ, что в CI; пароль из контейнера Postgres):

```bash
bash scripts/migrate.sh version       # "23 (dirty)" — миграция 23 упала
# 1. прочитать ошибку в логе деплоя (Actions) и файл migrations/000023_*.up.sql
# 2. миграция транзакционная -> схема осталась на версии 22:
bash scripts/migrate.sh force 22      # записать "22, clean", ничего не выполняя
# 3. исправить миграцию новым коммитом и задеплоить снова
```

Если в файле был свой `COMMIT` или нетранзакционная команда, сначала проверьте руками (`docker compose … exec postgres psql -U cozy cozy`), что успело примениться, и выберите версию по факту.

Если таблицы есть, а `schema_migrations` нет (инцидент 2026-09 на staging), `migrate up` попытается начать с 000001 и упадёт на «already exists». Определите последнюю применённую миграцию, сравнив схему (`\d имя_таблицы`) с `migrations/*.up.sql`, и выполните `bash scripts/migrate.sh force <её номер>`.

Локально то же самое: `make migrate-version`, `make migrate-force version=22`.

### Бэкапы

`scripts/backup.sh` (на сервере, из `/opt/cozy`):

1. `pg_dump -Fc` базы `cozy` из контейнера `postgres` → `$BACKUP_DIR/postgres/cozy_YYYYMMDD_HHMMSS.dump` (сначала `.partial`, проверка `pg_restore --list`, потом переименование — битый дамп никогда не выглядит как хороший);
2. удаляет локальные дампы старше `KEEP_DAYS` дней (только после успешного шага 1);
3. `mc mirror` бакета медиа в `$BACKUP_DIR/minio/<bucket>/` (инкрементально; удалённые в MinIO файлы в зеркале остаются) — по умолчанию включено, `MINIO_MIRROR=0` отключает. `mc` запускается одноразовым контейнером `quay.io/minio/mc` (версия закреплена) в сетевом namespace контейнера `minio`, ставить `mc` на хост не нужно. Ключи берутся из `.env`; `MINIO_SECRET_KEY` не должен содержать `/`, `@`, `:`;
4. если задан `BACKUP_RCLONE_REMOTE` — `rclone copy` дампов и зеркала медиа за пределы VPS (копирование, не синхронизация: локальная ротация off-site ничего не удаляет); дампы off-site старше `BACKUP_RCLONE_KEEP_DAYS` (60) удаляются. `rclone` с хоста, а если его нет — образ `rclone/rclone:1.75.1` с конфигом из `RCLONE_CONFIG`;
5. если задан `BACKUP_HEALTHCHECK_URL` (например, чек на healthchecks.io) — пинги `/start`, успех, `/fail` с хвостом лога. Сервис сам пришлёт алерт и если бэкап вообще не запустился.

Весь вывод дублируется в `BACKUP_LOG` (по умолчанию `$BACKUP_DIR/backup.log`), ошибки остаются в stderr. Ненулевой код выхода при любой ошибке.

Настройка off-site (один раз): завести бакет (Backblaze B2 / любой S3) с ключом только на этот бакет, на VPS `apt install rclone` и `rclone config` → remote, например `b2`. Проверить: `rclone lsd b2:`.

Cron (root, каждую ночь в 03:30 по времени сервера). stdout отбрасывается (он уже в `backup.log`), stderr не перенаправлен — при настроенной почте cron пришлёт ошибки на `MAILTO`; основной алерт — healthchecks:

```cron
MAILTO=ops@example.com
30 3 * * * BACKUP_HEALTHCHECK_URL=https://hc-ping.com/<uuid> BACKUP_RCLONE_REMOTE=b2:cozy-backups/staging KEEP_DAYS=14 /opt/cozy/scripts/backup.sh >/dev/null
```

На healthchecks.io для чека: период 1 день, grace 2 часа.

**Восстановление Postgres** (перезаписывает текущие данные — сначала сделайте свежий дамп):

```bash
cd /opt/cozy
C="docker compose -f docker/docker-compose.prod.yml --env-file .env"
# дамп с другой машины, если VPS потерян:  rclone copy b2:cozy-backups/staging/postgres/cozy_YYYYMMDD_HHMMSS.dump /var/backups/cozy/postgres/
$C stop backend                                   # никто не пишет в базу
$C exec -T postgres dropdb -U cozy cozy
$C exec -T postgres createdb -U cozy cozy
$C exec -T postgres pg_restore -U cozy -d cozy --no-owner --no-acl --exit-on-error \
  < /var/backups/cozy/postgres/cozy_YYYYMMDD_HHMMSS.dump
bash scripts/migrate.sh version                   # версия из дампа, не dirty
bash scripts/migrate.sh up                        # докатить миграции новее дампа
$C start backend
curl -fsS https://cozy.erpsystemsales.com/readyz
```

Дамп включает таблицу `schema_migrations`, так что `migrate up` докатит только миграции новее дампа. Проверить дамп без восстановления: `pg_restore --list <файл>`; восстановить в отдельную базу для проверки — `createdb cozy_check` и тот же `pg_restore -d cozy_check`. Хотя бы раз в месяц делайте такую пробную проверку.

**Восстановление медиа** (из зеркала обратно в бакет; при потере VPS сначала `rclone copy b2:cozy-backups/staging/minio /var/backups/cozy/minio`):

```bash
MINIO_ID=$($C ps -q minio)
MC_HOST_cozy="http://$MINIO_ACCESS_KEY:$MINIO_SECRET_KEY@localhost:9000" \
  docker run --rm --network container:$MINIO_ID -e MC_HOST_cozy -v /var/backups/cozy/minio:/backup \
  quay.io/minio/mc:RELEASE.2025-08-13T08-35-41Z mirror --overwrite /backup/cozy-media cozy/cozy-media
```

## Прод (cozy.kg)

`cozy.kg` **не** добавляется ещё одним хостом в staging-`Caddyfile`: это обслуживало бы реальных покупателей из staging-базы с mock-оплатой. Прод — отдельный стек: лучше отдельный VPS (или минимум отдельный каталог, отдельное имя compose-проекта `-p cozy-prod`, свои volumes, свой `.env` и свой `Caddyfile` с блоками `cozy.kg, www.cozy.kg` и `media.cozy.kg` без `noindex`), отдельный deploy-workflow/environment `production` с ручным подтверждением, свои бэкапы с другим `BACKUP_RCLONE_REMOTE` и отдельным чеком healthchecks.

Чеклист `.env` прода (полный список переменных — `.env.example`):

- [ ] `APP_ENV=prod`
- [ ] `JWT_SECRET` — случайный, не меньше 32 байт: `openssl rand -base64 48`; не совпадает со staging
- [ ] `POSTGRES_PASSWORD` — `openssl rand -hex 32` (hex: пароль подставляется в `DATABASE_URL`, спецсимволы `@ : / ?` его ломают)
- [ ] `MINIO_ACCESS_KEY` / `MINIO_SECRET_KEY` — случайные, без `/ @ :` (`openssl rand -hex 20`); `MINIO_BUCKET`
- [ ] `MINIO_PUBLIC_ENDPOINT=media.cozy.kg`, `MINIO_PUBLIC_USE_SSL=true`
- [ ] `PUBLIC_BASE_URL=https://cozy.kg`
- [ ] `PAYMENTS_PROVIDER=bakai` (никогда `mock`) и боевые реквизиты Bakai; `BAKAI_WEBHOOK_TOKEN` — случайный (`openssl rand -hex 32`)
- [ ] `NIKITA_API_KEY` — боевой, `SMS_MOCK_OTP=false`
- [ ] `FCM_CREDENTIALS_FILE=/secrets/firebase-service-account.json` (файл `chmod 644` в `./secrets`), `TELEGRAM_BOT_TOKEN`, `TELEGRAM_CHAT_ID` — группа персонала прода, не тестовая
- [ ] `APP_MIN_VERSION`, `APP_LATEST_VERSION`, `APP_STORE_URL_IOS`, `APP_STORE_URL_ANDROID`
- [ ] `DELIVERY_FEE_SOM` и прочие новые переменные из `.env.example`
- [ ] `LOG_FORMAT=json`
- [ ] `.env` принадлежит root/deploy, `chmod 600`; в git не попадает
- [ ] после первого запуска: `/readyz` отвечает 200, пробный заказ, пробный бэкап + пробное восстановление в `cozy_check`

## API-документация

Полная OpenAPI 3.0-спецификация всех реализованных эндпоинтов — [`openapi.yaml`](openapi.yaml) (§6 ТЗ). Открыть в любом Swagger UI/Redoc или сгенерировать клиент для Flutter. Обновлять по мере добавления новых эндпоинтов — файл описывает только то, что реально есть в коде.

## Статус

Готово: каркас (конфиг, роутер, `apperr`, подключение к Postgres), миграции всех таблиц из §3 ТЗ + `otp_codes`/`refresh_tokens`, вход покупателя по OTP (Nikita SMSPro) с JWT access/refresh.
Дальше по плану (§13 ТЗ): логин/RBAC для staff (админка) → каталог → MinIO → заказы → Bakai → остальной REST API.
