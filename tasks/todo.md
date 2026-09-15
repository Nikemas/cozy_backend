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
- [x] Ревью с пользователем перед следующей волной (admin CRUD каталога, MinIO, заказы, Bakai) — плюс отдельное параллельное ревью безопасности/корректности всех трёх потоков (см. коммиты "review A/B/C"), нашли и починили timing-атаку на staff-логин, integer overflow в пагинации, null-vs-[] в JSON, permissions в CI — всё смержено, `golangci-lint` 0 issues

---

## Task E: MinIO-интеграция, presigned upload/get (шаг 6 ТЗ) — DONE

**Description:** `internal/media` — клиент MinIO (S3-совместимо) для загрузки/выдачи фото товаров, per §10 (интеграции) и §8 (админка, загрузка фото) ТЗ. Админка запрашивает у бэкенда presigned PUT-ссылку и сама грузит файл напрямую в MinIO; что происходит с `object_key` дальше (запись в `product_images`) — забота admin-CRUD задачи каталога, вне этой задачи.

**Acceptance criteria:**
- [x] `internal/media/client.go` — `Client`, обёртка над `*minio.Client`, конструируется из `internal/config` (`MinIOEndpoint/AccessKey/SecretKey/Bucket/UseSSL`)
- [x] `Client.EnsureBucket(ctx)` — идемпотентная проверка-и-создание бакета при старте, не падает на гонке "уже существует" (`BucketAlreadyOwnedByYou`/`BucketAlreadyExists`)
- [x] `Client.PresignPut(ctx, objectKey, ttl)` / `Client.PresignGet(ctx, objectKey, ttl)` — presigned URL на PUT/GET
- [x] `object_key` генерируется на сервере (UUID + расширение по `content_type`), клиент не может задать произвольный путь; `content_type` валидируется по allow-list (`image/jpeg`, `image/png`, `image/webp`) — иначе `apperr.BadRequest`
- [x] `POST /admin/api/media/presign-upload` (`internal/media/routes.go`) — только `owner`/`manager` (`staffSvc.RequireRole(...)`), тело `{content_type}`, ответ `{upload_url, object_key}`, TTL 10 минут
- [x] `cmd/server/main.go`: `media.RegisterRoutes(...)` внутри `registerAdminRoutes`; `mediaClient.EnsureBucket(...)` в `run()` после ping БД
- [x] Валидация content-type → extension и генерация `object_key` — чистые функции, покрыты юнит-тестами без живого MinIO (`internal/media/objectkey_test.go`)

**Verification:**
- [x] `gofmt -l .`, `go build ./...`, `go vet ./...`, `go test ./...` — чисто
- [x] `golangci-lint run ./internal/media/... ./cmd/server/...` — 0 замечаний. `golangci-lint run ./...` на весь репозиторий падает на **одном** предсуществующем замечании в `internal/i18n/i18n.go:83` (`f.Close` не проверен, errcheck) — из параллельной задачи web-каркаса (коммит `9eab9b7`), не тронуто этой задачей по инструкции; вынесено отдельной задачей
- [ ] Нет локального MinIO/Docker на этой машине — сам presigned-раунд-трип (реальный PUT/GET в MinIO) не прогнан, только `EnsureBucket`/`PresignPut`/`PresignGet` через SDK-код, который не был выполнен против живого сервера

**Dependencies:** Task A (RBAC, `staff.Service.RequireRole`), `internal/config` (MinIO env-переменные — уже были)

**Files touched:**
- `internal/media/{client,objectkey,routes}.go` + `internal/media/objectkey_test.go`
- `cmd/server/main.go` — импорт `internal/media`, сборка `media.Client` + `EnsureBucket` в `run()`, `registerAdminRoutes` получил третий параметр `mediaClient *media.Client` и вызывает `media.RegisterRoutes(...)`
- `go.mod`/`go.sum` — новая прямая зависимость `github.com/minio/minio-go/v7` (плюс её транзитивные зависимости и `github.com/google/uuid`, который был транзитивной зависимостью minio-go и стал прямой — используется для генерации `object_key`)

**Deviations:**
- §8 ТЗ описывает загрузку фото как `multipart/form-data` → сохранение в MinIO на бэкенде. Вместо этого реализован **presigned direct upload**: админка получает presigned PUT-ссылку и грузит файл в MinIO напрямую, минуя бэкенд как прокси для байтов файла. Это соответствует уже принятому в ТЗ (§10) паттерну "presigned URL" и было явным требованием этой задачи — бэкенд не должен пропускать через себя тело файла.
- `registerAdminRoutes(mux, db)` → `registerAdminRoutes(mux, db, mediaClient *media.Client)` — минимальное изменение сигнатуры (не тела/структуры функции), т.к. `media.Client` строится из `cfg`, которого раньше не было в этой функции; аналогично тому, как `authSvc`/`cfg` уже передаются в `registerWebRoutes`.
- Решение по MinIO при старте: `EnsureBucket` при недоступном MinIO **логирует warning и не останавливает загрузку сервера** — сайт/API продолжают работать, ломается только загрузка фото. Выбрано намеренно (см. бриф задачи): в отличие от `DATABASE_URL`/`db.PingContext`, MinIO — не критичная для боота зависимость.
- Ключ объекта кладётся под фиксированный префикс `products/` (`products/<uuid><ext>`) — не было явно указано в задаче, но снижает риск коллизий/перезаписи и облегчает будущую политику доступа/жизненного цикла бакета по префиксу.

---

## Wave 2 (параллельно, 2 агента — заказы/Bakai сознательно исключены)

**Почему не 4 потока:** параллельная сессия (`kozy-01`) уже строит `internal/orders/{cart,order}.go` + миграцию `cart_items` для сайта (checkout-флоу) — независимая реализация заказов с нашей стороны прямо сейчас была бы третьей подряд коллизией. Заказы (шаг 7) и Bakai (шаг 8, зависит от заказов) — отложены до её коммита. Также фоновой задачей (`task_293f5b84`, отдельная сессия пользователя) чинится утечка сырых ошибок через `apperr.Internal` — не пересекается с этой волной по файлам.

### Task D: Admin CRUD каталога (шаг 12 ТЗ)

**Description:** Запись поверх уже готового read-only `internal/catalog` (категории/товары/вариации/остатки), под RBAC из Task A. Загрузка фото — НЕ в этой задаче (см. Task E): эндпоинт товара принимает уже готовые `object_key` (строки), которые фронт админки получает от MinIO-эндпоинтов Task E отдельным вызовом — так Task D и Task E не пересекаются по файлам и не зависят друг от друга по реализации.

**Acceptance criteria:**
- [ ] `POST/PUT/DELETE /admin/api/categories(/:id)` — owner/manager
- [ ] `POST/PUT/DELETE /admin/api/products(/:id)` — owner/manager; тело включает `category_id`, `name_ru/ky`, `description_ru/ky`, `brand`, `base_price`, `is_active`
- [ ] `POST/PUT/DELETE /admin/api/products/:id/variants(/:variantId)` — owner/manager (size/color/sku/price_override)
- [ ] `PUT /admin/api/products/:id/variants/:variantId/images` — owner/manager; принимает список `object_key` (+ `sort_order`), пишет в `product_images`
- [ ] `PUT /admin/api/stock/:variantId/:pointId` — owner/manager (любая точка) ИЛИ point_staff **только если `pointId == staff.PointID` из сессии** — иначе 403, даже если формально прошёл `RequireRole`

**Verification:**
- [ ] `gofmt -l .`, `go build ./...`, `go vet ./...`, `go test ./...`, `golangci-lint run ./...` — чисто
- [ ] Тесты на RBAC-нюанс point_staff+чужая точка (403) и point_staff+своя точка (200) — без реальной БД, через fake-репозитории (см. `internal/staff/fakes_test.go` как образец)
- [ ] Manual: живой Postgres недоступен на этой машине — не прогнано

**Dependencies:** None (каталог и RBAC уже смержены в `main`)

**Files likely touched:**
- `internal/catalog/{category,product,variant,stock}.go` — добавить write-методы (Create/Update/Delete) рядом с существующими read-методами, не переписывая их
- `internal/httpapi/admin_catalog.go` — новый файл, защищённые роуты
- `cmd/server/main.go` — вызов регистрации внутри `registerAdminRoutes`

**Estimated scope:** Medium/Large (может стоит разбить на под-агента, если разрастётся)

---

### Task E: MinIO-интеграция (шаг 6 ТЗ) — DONE, см. полное описание выше ("Task E: MinIO-интеграция, presigned upload/get")

---

## Checkpoint: после Task D, E

- [x] Task E смержен в `main` (коммит "Add MinIO media integration (Task E, wave 2)")
- [ ] Task D ещё в работе — смержить, когда будет готов
- [x] `go build ./...`, `go vet ./...`, `go test ./...`, `gofmt -l .` — чисто на текущем `main` (с Task E)
- [ ] `golangci-lint run ./...` на весь репозиторий — 0 issues кроме одного предсуществующего в `internal/i18n/i18n.go` (не наша задача, вынесено отдельно как `task_2f5cfe35`)
- [ ] Ревью с пользователем после Task D; затем — заказы/Bakai, когда `kozy-01` закоммитит cart/orders
