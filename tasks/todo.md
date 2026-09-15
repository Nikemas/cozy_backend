# cozy_backend — todo

См. `tasks/plan.md` для контекста и порядка. Готово: шаги 1–3 ТЗ (каркас, миграции, OTP-вход покупателя).

## Task A: Staff auth & RBAC (шаг 4 ТЗ)

**Description:** Вход сотрудников в админку (`staff.phone` + `password_hash` bcrypt) с server-side сессией (cookie) и RBAC-мидлварью по `staff.role` (owner/manager/point_staff), как описано в §5 ТЗ.

**Acceptance criteria:**
- [ ] `POST /admin/api/login` — валидный телефон+пароль → устанавливает httpOnly cookie с сессией; неверный пароль → 401 через `apperr`
- [ ] `POST /admin/api/logout` — отзывает сессию
- [ ] `RequireRole(roles ...staff_role)` middleware — блокирует недостаточную роль 403, пропускает достаточную

**Verification:**
- [ ] `go build ./...`, `go vet ./...`, `go test ./...` — чисто
- [ ] Manual: создать сотрудника вручную в БД (bcrypt-хеш), залогиниться curl'ом, дёрнуть защищённый тестовый роут с/без нужной роли

**Dependencies:** None (staff-таблица уже смигрирована)

**Files likely touched:**
- `migrations/000017_create_staff_sessions.up.sql` / `.down.sql`
- `internal/staff/staff.go`, `internal/staff/session_repo.go`, `internal/staff/service.go`
- `internal/staff/middleware.go` (`RequireRole`)
- `internal/staff/routes.go` (`RegisterRoutes(mux *http.ServeMux, svc *Service)`)
- `cmd/server/main.go` — один вызов `staff.RegisterRoutes(...)` внутри `registerAdminRoutes`

**Estimated scope:** Medium (5 файлов)

---

## Task B: Catalog domain + публичные read-эндпоинты (шаг 5 ТЗ, часть)

**Description:** Repository-слой для категорий (дерево), товаров, вариаций и остатков + публичные (без авторизации) REST-эндпоинты чтения каталога. Admin-эндпоинты записи (CRUD) — вне этой задачи, они пойдут следующей волной поверх RBAC из Task A.

**Acceptance criteria:**
- [ ] `GET /api/v1/categories` — дерево категорий (по `parent_id`)
- [ ] `GET /api/v1/products` — список с фильтрами `category`, `size`, `color`, `price_min/max`, `q`, `sort`, `page`
- [ ] `GET /api/v1/products/:id` — карточка товара + вариации + остатки по точкам

**Verification:**
- [ ] `go build ./...`, `go vet ./...`, `go test ./...` — чисто
- [ ] Manual: заполнить БД парой категорий/товаров вручную, дёрнуть все три эндпоинта curl'ом

**Dependencies:** None (таблицы `categories`/`products`/`product_variants`/`stock` уже смигрированы)

**Files likely touched:**
- `internal/catalog/category.go`, `internal/catalog/product.go`, `internal/catalog/variant.go`, `internal/catalog/stock.go`
- `internal/httpapi/catalog.go`
- `cmd/server/main.go` — вызовы регистрации внутри `registerAPIRoutes`

**Estimated scope:** Medium (5 файлов)

---

## Task C: Docker + CI/CD (шаг 16 ТЗ)

**Description:** Production Dockerfile (multi-stage) для Go-бэкенда и GitHub Actions: `ci.yml` (`go vet` + `go test ./...` + `golangci-lint`) на каждый push/PR, `deploy.yml` — шаблон деплоя по SSH (плейсхолдеры для секретов, реальный VPS ещё не поднят).

**Acceptance criteria:**
- [x] `docker/Dockerfile` — multi-stage build (builder + минимальный рантайм-образ), не ломает `docker build` по синтаксису (реальный `docker build` не прогнан — Docker недоступен на этой машине, см. Verification)
- [x] `.github/workflows/ci.yml` — vet+test+lint, запускается на push/PR
- [x] `.github/workflows/deploy.yml` — шаблон SSH-деплоя с явно помеченными плейсхолдер-секретами, задокументировано что нужно завести в репозитории

**Verification:**
- [x] `go build ./...`, `go vet ./...`, `go test ./...` — чисто
- [x] Ручная проверка синтаксиса workflow: `actionlint` (v1.7.12, поставлен через `go install` только для этой проверки) прошёл чисто на обоих файлах. Дополнительно все pinned версии Actions (`actions/checkout@v7.0.1`, `actions/setup-go@v6.5.0`, `golangci/golangci-lint-action@v9.3.0` + линтер `v2.13.2`, `docker/login-action@v4.6.0`, `docker/build-push-action@v7.4.0`, `appleboy/ssh-action@v1.2.5`) и оба базовых образа Dockerfile (`golang:1.26-alpine`, `gcr.io/distroless/static-debian12:nonroot`) сверены как реально существующие через GitHub/Docker Hub/GCR API (сеть была доступна в этой сессии)
- [ ] Docker недоступен на этой машине — `docker build` не прогнан, только статическое ревью Dockerfile. Пути `migrations/` в образе подтверждены существующими в репо; `web/`/`admin/`/`locales/` намеренно НЕ копируются в образ — их нет в этой ветке, `go:embed` нигде не используется, и `registerWebRoutes`/`registerAdminRoutes` в `cmd/server/main.go` — заглушки, ничего с диска не читают (см. комментарий в `docker/Dockerfile`)

**Dependencies:** None

**Files likely touched:**
- `docker/Dockerfile`
- `.github/workflows/ci.yml`
- `.github/workflows/deploy.yml`

**Estimated scope:** Small (3 файла)

---

## Checkpoint: после Task A, B, C

- [ ] Все три ветки смержены в `cozy_backend`
- [ ] `go build ./...`, `go vet ./...`, `go test ./...` — чисто на объединённом коде
- [ ] Конфликт в `cmd/server/main.go` (если возник) разрешён вручную
- [ ] Ревью с пользователем перед следующей волной (admin CRUD, MinIO, заказы, Bakai)
