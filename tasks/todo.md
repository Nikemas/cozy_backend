# cozy_backend — todo

См. `tasks/plan.md` для контекста и порядка. Готово: шаги 1–3 ТЗ (каркас, миграции, OTP-вход покупателя), и весь этот файл (шаги 4, 5, 16 — смержены в `main`).

## Task A: Staff auth & RBAC (шаг 4 ТЗ) — DONE

**Description:** Вход сотрудников в админку (`staff.phone` + `password_hash` bcrypt) с server-side сессией (cookie) и RBAC-мидлварью по `staff.role` (owner/manager/point_staff), как описано в §5 ТЗ.

**Acceptance criteria:**
- [x] `POST /admin/api/login` — валидный телефон+пароль → устанавливает httpOnly cookie с сессией; неверный пароль → 401 через `apperr`
- [x] `POST /admin/api/logout` — отзывает сессию
- [x] `RequireRole(roles ...staff_role)` middleware — блокирует недостаточную роль 403, пропускает достаточную

**Verification:**
- [x] `go build ./...`, `go vet ./...`, `go test ./...` — чисто (10 тестов на чистую логику через fake-репозитории)
- [ ] Manual: создать сотрудника вручную в БД (bcrypt-хеш), залогиниться curl'ом, дёрнуть защищённый тестовый роут с/без нужной роли (нет локального Postgres/Docker на этой машине — не прогнано)

**Dependencies:** None (staff-таблица уже смигрирована)

**Files touched:**
- `migrations/000017_create_staff_sessions.up.sql` / `.down.sql`
- `internal/staff/{staff,session_repo,token,service,middleware,routes}.go` + тесты
- `cmd/server/main.go` — вызов `staff.RegisterRoutes(...)` внутри `registerAdminRoutes`

**Deviations:** `RequireRole` реализован как метод `(*Service).RequireRole(roles ...Role)`, а не свободная функция; `Role` — типизированная `string` локально в `internal/staff` (те же значения `owner`/`manager`/`point_staff`), чтобы не тащить Postgres-специфичное имя enum в Go-код. Добавлена новая прямая зависимость `golang.org/x/crypto` (bcrypt).

---

## Task B: Catalog domain + публичные read-эндпоинты (шаг 5 ТЗ, часть) — DONE

**Description:** Repository-слой для категорий (дерево), товаров, вариаций и остатков + публичные (без авторизации) REST-эндпоинты чтения каталога. Admin-эндпоинты записи (CRUD) — вне этой задачи, следующая волна поверх RBAC из Task A.

**Acceptance criteria:**
- [x] `GET /api/v1/categories` — дерево категорий (по `parent_id`)
- [x] `GET /api/v1/products` — список с фильтрами `category` (id или slug), `size`, `color`, `price_min/max`, `q`, `sort`, `page`
- [x] `GET /api/v1/products/:id` — карточка товара + вариации + остатки по точкам

**Verification:**
- [x] `go build ./...`, `go vet ./...`, `go test ./...` — чисто (тесты на построение дерева категорий и парсинг фильтров)
- [ ] Manual: заполнить БД парой категорий/товаров вручную, дёрнуть все три эндпоинта curl'ом (нет локального Postgres/Docker на этой машине — не прогнано)

**Dependencies:** None (таблицы `categories`/`products`/`product_variants`/`stock` уже смигрированы)

**Files touched:**
- `internal/catalog/{category,product,variant,stock}.go` + тест на дерево категорий
- `internal/httpapi/catalog.go` + тест на парсинг фильтров
- `cmd/server/main.go` — вызов `httpapi.RegisterCatalogRoutes(...)` внутри `registerAPIRoutes`

**Deviations:** остатки по вариациям выбираются через `IN ($1,$2,...)`, а не `= ANY($1)` — осознанный выбор, чтобы не полагаться на непроверенное (без живой БД) поведение pgx с массив-параметрами. `product_images` не включены в ответ `/products/:id` — не было в acceptance criteria, добавить в следующей волне при необходимости.

---

## Task C: Docker + CI/CD (шаг 16 ТЗ) — DONE

**Description:** Production Dockerfile (multi-stage) для Go-бэкенда и GitHub Actions: `ci.yml` (`go vet` + `go test ./...` + `golangci-lint`) на каждый push/PR, `deploy.yml` — шаблон деплоя по SSH (плейсхолдеры для секретов, реальный VPS ещё не поднят).

**Acceptance criteria:**
- [x] `docker/Dockerfile` — multi-stage build (builder `golang:1.26-alpine` + рантайм `gcr.io/distroless/static-debian12:nonroot`); реальный `docker build` не прогнан — Docker недоступен на этой машине, см. Verification
- [x] `.github/workflows/ci.yml` — `gofmt -l .` + vet + test + `golangci-lint`, запускается на push/PR
- [x] `.github/workflows/deploy.yml` — шаблон SSH-деплоя (сборка образа → GHCR → SSH `docker compose pull && up -d`) с явно помеченными плейсхолдер-секретами, задокументировано что нужно завести в репозитории

**Verification:**
- [x] `go build ./...`, `go vet ./...`, `go test ./...`, `gofmt -l .` — чисто (Go-код не менялся)
- [x] Синтаксис workflow проверен `actionlint` (v1.7.12, поставлен через `go install` только для проверки) — чисто на обоих файлах. Версии Actions (`actions/checkout@v7.0.1`, `actions/setup-go@v6.5.0`, `golangci/golangci-lint-action@v9.3.0` + линтер `v2.13.2`, `docker/login-action@v4.6.0`, `docker/build-push-action@v7.4.0`, `appleboy/ssh-action@v1.2.5`) и базовые образы Dockerfile сверены как реально существующие через GitHub/Docker Hub/GCR API
- [ ] Docker недоступен на этой машине — `docker build` не прогнан, только статическое ревью. `migrations/` подтверждены существующими в репо; `web/`/`admin/`/`locales/` намеренно НЕ копируются в образ (их нет в репозитории, `go:embed` не используется, `registerWebRoutes`/`registerAdminRoutes` — заглушки) — см. комментарий в `docker/Dockerfile`

**Dependencies:** None

**Files touched:**
- `docker/Dockerfile`
- `.github/workflows/ci.yml`, `.github/workflows/deploy.yml`
- `README.md` — секция «Docker / CI-CD»

---

## Checkpoint: после Task A, B, C

- [x] Все три ветки смержены в `main` (`git merge --no-ff` x3)
- [x] `go build ./...`, `go vet ./...`, `go test ./...`, `gofmt -l .` — чисто на объединённом коде
- [x] Конфликт в `cmd/server/main.go` — не возник, авто-мерж; конфликт был только в этом файле (`tasks/todo.md`) и `tasks/plan.md`, разрешён вручную
- [ ] Ревью с пользователем перед следующей волной (admin CRUD каталога, MinIO, заказы, Bakai)
