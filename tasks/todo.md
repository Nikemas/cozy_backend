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

---

## Wave 2

### Task D: Admin CRUD для каталога (шаг 12 ТЗ, RBAC-запись поверх Task A+B) — DONE

**Description:** Write-сторона каталога поверх read-only репозиториев из Task B и RBAC-мидлвари из Task A: CRUD категорий/товаров/вариаций, замена списка фото товара (только запись `object_key` в `product_images`, без обращения к MinIO — это отдельная задача) и запись остатков по точкам, с RBAC-нюансом для `point_staff` (см. §5 ТЗ).

**Acceptance criteria:**
- [x] `POST/PUT/DELETE /admin/api/categories` и `/admin/api/categories/{id}` — owner/manager
- [x] `POST/PUT/DELETE /admin/api/products` и `/admin/api/products/{id}` — owner/manager
- [x] `POST/PUT/DELETE /admin/api/products/{id}/variants` и `/admin/api/products/{id}/variants/{variantId}` — owner/manager
- [x] `PUT /admin/api/products/{id}/images` — owner/manager, тело — список `{object_key, sort_order}`, только запись строк в `product_images`
- [x] `PUT /admin/api/stock/{variantId}/{pointId}` — owner/manager (любая точка) и point_staff (**только своя точка**, `staff.PointID`, иначе `apperr.Forbidden`)
- [x] Тесты на RBAC-нюанс остатков: point_staff на своей точке (200) и на чужой (403) — `internal/httpapi/admin_catalog_test.go`

**Verification:**
- [x] `gofmt -l .`, `go build ./...`, `go vet ./...`, `go test ./...`, `golangci-lint run ./...` — чисто
- [ ] Manual: нет локального Postgres/Docker на этой машине — live-DB тест не прогнан, только ревью SQL

**Dependencies:** Task A (RBAC), Task B (read-репозитории каталога)

**Files touched:**
- `internal/catalog/{category,product,variant,stock}.go` — добавлены Create/Update/Delete (и `GetByIDAny` у продукта, `GetByID` у вариации), без изменения существующих read-методов
- `internal/catalog/image.go` — новый `ImageRepo.ReplaceForProduct` (delete+insert в одной транзакции)
- `internal/catalog/pgerr.go` — общий хелпер разбора `*pgconn.PgError` (unique/FK-violation) в `apperr`
- `internal/catalog/{category,product,variant,stock,image,pgerr}_write_test.go` — тесты на чистую валидацию (без БД, через `NewXRepo(nil)`, т.к. валидация всегда выполняется до обращения к `r.db`)
- `internal/httpapi/admin_catalog.go` — `RegisterAdminCatalogRoutes(mux, db, staffSvc)` + все хендлеры
- `internal/httpapi/admin_catalog_test.go` — handler-level тесты RBAC-нюанса остатков (fake `stockUpserter`, `staff.NewContextWithStaff`)
- `internal/staff/middleware.go` — добавлен `NewContextWithStaff` (симметрично `FromContext`), нужен тестам в других пакетах для проверки хендлеров без реальной сессии
- `cmd/server/main.go` — одна строка в `registerAdminRoutes`

**Design decisions:**
- **Soft-delete для товаров** (`is_active = false` вместо `DELETE`) — как и предполагалось в брифе: `order_items` хранит только снапшот (`product_name_snapshot` и т.п.), но `order_items.variant_id` — живой FK в `product_variants`, так что жёсткое удаление уже заказанного товара либо упёрлось бы в FK, либо (при каскадном удалении вариаций) снесло бы историю. `is_active` уже используется публичным чтением, так что soft-delete просто скрывает товар из каталога; `Update` может его же реактивировать.
- **Категории — hard delete.** У `categories` нет колонки `is_active`, добавлять её ради этой волны — расширение схемы за рамками брифа. Вместо этого: обычный `DELETE`, а нарушение FK (категория используется в `products.category_id` или как `parent_id` у подкатегории) перехватывается по SQLSTATE `23503` и превращается в `apperr.Conflict` (409), а не падает 500-кой или удаляет молча.
- **Вариации — тоже hard delete, с той же перехваткой FK.** У `product_variants` тоже нет `is_active`; но `order_items.variant_id` — живой FK без `ON DELETE`, так что удалить вариацию, по которой уже есть заказы, физически нельзя — Postgres сам защищает историю, а мы просто транслируем `23503` в 409 вместо 500. Компромисс: админ не может удалить вариацию, у которой когда-либо был заказ (может только обнулить остаток) — если это неприемлемо, нужна отдельная миграция с `is_active` у `product_variants`, сознательно не делал её в этой волне.
- **`is_active` товара — `*bool` на входе (HTTP-уровень).** Если поле не передано в JSON — по умолчанию `true` (и на create, и на update), чтобы отсутствие поля не превращалось в Go zero-value `false` и не деактивировало товар незаметно для админа.
- **Остатки — upsert (`INSERT ... ON CONFLICT DO UPDATE`)**, не отдельные Create/Update — PK `(variant_id, point_id)` и естественная семантика «выставить количество» делают различие create/update у остатков бессмысленным для клиента.
- **Транзакция для замены фото — не через общий `withTx`**, которого пока не существует в кодовой базе (упоминается в §4 ТЗ, но ещё не реализован ни в одном пакете) — вместо того чтобы придумывать общий хелпер, который может пересечься с тем, что параллельно делает поток `internal/orders`, транзакция открыта локально внутри `ImageRepo.ReplaceForProduct`.
- **`NewContextWithStaff` в `internal/staff`** — минимальное добавление (симметричное уже существующему `FromContext`), нужно ровно для того, чтобы `internal/httpapi` мог тестировать `updateStockHandler` (в частности RBAC-нюанс point_staff) без живой БД/сессии — `staff.Service`/`staff.Staff` не даёт собрать контекст извне пакета иначе.

**Deviations:** нет отклонений от acceptance criteria брифа; решения по soft/hard-delete и умолчанию `is_active` — сознательные и описаны выше, т.к. бриф оставлял их на усмотрение исполнителя.
