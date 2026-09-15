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

Проверка: `curl localhost:8080/healthz` (liveness) и `curl localhost:8080/readyz` (проверяет доступность БД).

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

## Docker / CI-CD

Продовый образ — `docker/Dockerfile` (multi-stage: builder на `golang:1.26-alpine`, рантайм на `gcr.io/distroless/static-debian12`). Собрать локально:

```bash
docker build -f docker/Dockerfile -t cozy_backend .
```

(Это отдельный файл от `docker/docker-compose.yml`, который только для локальной разработки — postgres + minio.)

CI (`.github/workflows/ci.yml`) на каждый push/PR гоняет `gofmt -l .`, `go vet ./...`, `go test ./...` и `golangci-lint`. Деплой (`.github/workflows/deploy.yml`) — пока шаблон: собирает и пушит образ в GHCR и деплоит по SSH (`docker compose pull && up -d`), но реального VPS ещё нет — см. комментарий в начале файла, какие секреты репозитория нужно завести, когда он появится.

## API-документация

Полная OpenAPI 3.0-спецификация всех реализованных эндпоинтов — [`openapi.yaml`](openapi.yaml) (§6 ТЗ). Открыть в любом Swagger UI/Redoc или сгенерировать клиент для Flutter. Обновлять по мере добавления новых эндпоинтов — файл описывает только то, что реально есть в коде.

## Статус

Готово: каркас (конфиг, роутер, `apperr`, подключение к Postgres), миграции всех таблиц из §3 ТЗ + `otp_codes`/`refresh_tokens`, вход покупателя по OTP (Nikita SMSPro) с JWT access/refresh.
Дальше по плану (§13 ТЗ): логин/RBAC для staff (админка) → каталог → MinIO → заказы → Bakai → остальной REST API.
