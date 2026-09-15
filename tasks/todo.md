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

---

### Task E: MinIO-интеграция (шаг 6 ТЗ) — DONE, см. полное описание выше ("Task E: MinIO-интеграция, presigned upload/get")

---

## Checkpoint: после Task D, E

- [x] Обе ветки смержены в `main`
- [x] `go build ./...`, `go vet ./...`, `go test ./...`, `gofmt -l .` — чисто
- [x] Конфликт в `cmd/server/main.go` (обе строки в `registerAdminRoutes`) и в этом файле — разрешён вручную
- [x] `golangci-lint run ./...` на весь репозиторий — 0 issues (замечание в `internal/i18n/i18n.go` закрыто фоновой задачей `task_2f5cfe35`)
- [ ] Ревью с пользователем; затем — заказы/Bakai, когда `kozy-01` закоммитит cart/orders

---

## Task F: OpenAPI-спецификация (§6 ТЗ) — DONE

**Description:** `openapi.yaml` в корне `cozy_backend`, документирующий все реально существующие эндпоинты `/api/v1/*` и `/admin/api/*` (Auth, Catalog, Admin — Staff/Catalog/Media, System) — не заявляет ничего, чего ещё нет в коде. Отложено сознательно: `/api/v1/{favorites,addresses,orders,devices}`, `/api/v1/payments/bakai/webhook`, `/admin/api/{orders,reports,points,staff}` — добавить по мере реализации в следующих волнах.

**Acceptance criteria:**
- [x] Все 19 текущих путей (grep по `mux.Handle(` в `internal/httpapi`, `internal/staff`, `internal/media`, `cmd/server`) описаны — методы, параметры, тела запросов/ответов, коды ошибок из реального кода `apperr.New(...)` в каждом хендлере
- [x] Схемы `Category`/`Product`/`Variant`/`StockEntry`/`ProductImage` и их `*Input`-варианты сверены построчно с Go-структурами (`json`-теги) в `internal/catalog`
- [x] Security schemes: `staffSessionCookie` (cookie `staff_session`) для admin-роутов, `customerBearerAuth` — задокументирован для будущих customer-scoped роутов, пока нигде не применяется (ни один текущий `/api/v1` эндпоинт не требует JWT)

**Verification:**
- [x] `python3 -m openapi_spec_validator openapi.yaml` — `OK`
- [x] Ручная сверка каждого пути с исходным кодом хендлера (не сгенерировано вслепую)

**Files touched:**
- `openapi.yaml` (новый)
- `README.md` — секция «API-документация»

---

## Wave 3: Admin-панель — backend JSON API (см. `tasks/plan.md` → «Wave 3»)

Пользователь явно попросил начать реализацию админки с бека. HTML/HTMX-слой (`internal/admin`) — не в этой волне, см. Wave 4 ниже.

- [ ] **Task G** — Admin Points of Sale API (`/admin/api/points`, CRUD, только owner). [детали](plan.md#task-g-admin-points-of-sale-api)
- [ ] **Task H** — Admin Staff API (`/admin/api/staff`, CRUD, только owner, инвариант «последний owner»). [детали](plan.md#task-h-admin-staff-api)
- [ ] **Task I** — Admin Orders API (`/admin/api/orders`, список/детали/смена статуса, RBAC по точке для point_staff). [детали](plan.md#task-i-admin-orders-api)
- [x] **Task J** (в брифе этой сессии — «Task O») — Admin Reports API (`/admin/api/reports/sales`, JSON + Excel-экспорт через excelize). [детали](#task-j-admin-reports-api-задача-о-в-брифе--done)
- [ ] **Task K** — Импорт товаров (`POST /admin/products/import`, CSV/Excel, построчный отчёт об ошибках). [детали](plan.md#task-k-импорт-товаров)

Задачи независимы по коду (см. Architecture Decisions в plan.md) — можно запускать параллельно в отдельных git worktree, как Task A/B/C в Wave 1. Единственная точка соприкосновения — `go.mod`/`go.sum` у Task J и Task K (оба тянут `excelize`) и по одной строке в `cmd/server/main.go` (`registerAdminRoutes`) у каждой задачи, кроме Task H.

### Checkpoint: после Task G–K

- [ ] Все 5 веток смержены в `main`
- [ ] `go build ./...`, `go vet ./...`, `go test ./...`, `gofmt -l .` — чисто
- [ ] `go.mod`/`go.sum` — одна версия `excelize`, `go mod tidy` прогнан
- [ ] `openapi.yaml` дополнен новыми путями (отдельная маленькая задача по аналогии с Task F)
- [ ] Ревью с пользователем перед Wave 4

## Wave 4 (не начата): internal/admin — HTML/HTMX-слой

Страница логина, layout+сайдбар с ролевой видимостью, экраны Товары/Заказы/Отчёты/Точки/Сотрудники/Импорт поверх API из Wave 3. Стартует после чек-пойнта Wave 3 — см. Open Questions в `plan.md`.

---

## Task H: Customer-facing orders + cart JSON API (§6 ТЗ, мобильное приложение) — DONE

**Description:** `/api/v1/{orders,cart}` — JSON REST для залогиненного покупателя (Flutter mobile), поверх уже готового домена `internal/orders` (`Service.{CreateOrder,ListOrders,GetOrder}`, `CartRepo.{List,Add,UpdateQty,Remove}` — реализованы и покрыты тестами параллельной задачей, домен не менялся) и только что добавленной JWT-мидлвари `auth.Service.RequireCustomer`. Корзина монтируется в этом же файле/функции, т.к. она customer-scoped JSON того же вида, что и заказы, и по доккомменту `orders.CartItem` явно designed для переиспользования web+mobile.

**Acceptance criteria:**
- [x] `POST /api/v1/orders` — тело `{items:[{variant_id,quantity}], address_id?, pickup_point_id?}` → `orders.OrderItemInput` + прямой проброс `address_id`/`pickup_point_id` в `CreateOrder` (валидация "ровно один из двух" — целиком в `orders.Service.CreateOrder`, хендлер её не дублирует)
- [x] `GET /api/v1/orders` — история заказов покупателя (`ListOrders`, новые сверху, с `Items`)
- [x] `GET /api/v1/orders/{id}` — `{id}` = UUID или `order_number` (`COZY-YYYYMMDD-NNN`), передаётся в `GetOrder` как есть — она сама разбирает оба варианта и скоупит по `customerID`
- [x] `GET /api/v1/cart`, `POST /api/v1/cart/{variantId}` (body `{quantity}`), `PUT /api/v1/cart/{variantId}` (body `{quantity}`), `DELETE /api/v1/cart/{variantId}` — тонкие обёртки над `CartRepo`
- [x] Все шесть роутов — за `authSvc.RequireCustomer(...)`; `customerID` берётся из `auth.CustomerIDFromContext`, ни один хендлер не доверяет `customer_id` в теле/пути
- [x] `RegisterOrderRoutes(mux, db, authSvc)` — единая точка входа; подключена в `cmd/server/main.go` (`registerAPIRoutes`) одной строкой — оказалась тривиальной и бесконфликтной на момент коммита, так что подключена, как разрешено в задании

**Verification:**
- [x] `gofmt -l .` — чисто
- [x] `go build ./...`, `go vet ./...` — чисто
- [x] `go test ./...` — чисто; новые тесты в `internal/httpapi/orders_test.go` — фейки `fakeOrderService`/`fakeCartService` за небольшими интерфейсами (`orderService`/`cartService`, по образцу `stockUpserter` из `admin_catalog.go`), без обращения к БД: маппинг `items`→`OrderItemInput`, проброс `address_id`/`pickup_point_id` как есть (в т.ч. явный тест, что хендлер НЕ повторяет валидацию "ровно один способ получения" — просто пропускает ошибку `invalid_fulfillment` от сервиса), 401 без аутентификации, 400 на битый JSON, скоуп по `customerID` для orders/cart
- [x] `golangci-lint run ./internal/httpapi/... ./internal/auth/...` — 0 issues; `golangci-lint run ./...` на весь репозиторий — 1 issue, предсуществующий (`internal/i18n/i18n.go:83`, errcheck на `f.Close`), не относится к этой задаче — см. пометку в Task E выше
- [ ] Живого Postgres нет на этой машине — ручной curl-прогон не сделан; сам `internal/orders` уже покрыт sqlmock-тестами параллельной задачей, эта задача только добавляет HTTP-слой поверх него

**Dependencies:** `internal/orders` (домен заказов/корзины, смержен параллельной задачей — не менялся), `auth.Service.RequireCustomer`/`auth.CustomerIDFromContext` (только что смержены)

**Files touched:**
- `internal/httpapi/orders.go` (новый) — `RegisterOrderRoutes` + хендлеры orders/cart
- `internal/httpapi/orders_test.go` (новый)
- `internal/auth/middleware.go` — добавлен `NewContextWithCustomerID(ctx, customerID) context.Context`, по образцу `staff.NewContextWithStaff` — нужен, чтобы тесты хендлеров в `internal/httpapi` могли положить `customerID` в контекст напрямую, не поднимая настоящий JWT/OTP-флоу
- `cmd/server/main.go` — одна строка `httpapi.RegisterOrderRoutes(mux, db, authSvc)` внутри `registerAPIRoutes`

**Deviations:** нет отклонений от постановки. Единственное самостоятельное решение — добавить `auth.NewContextWithCustomerID` (в задании не упоминался): без него хендлер-тесты в другом пакете не могли положить customerID в приватный ключ контекста `auth`-пакета, а гонять их через настоящий `RequireCustomer`+подписанный JWT было бы избыточно (эта мидлварь уже покрыта своими тестами в `internal/auth/middleware_test.go`) — паттерн 1-в-1 повторяет уже существующий `staff.NewContextWithStaff`.

---

## Task G: Customer JSON API — favorites, addresses, device tokens (§6 ТЗ) — DONE

**Description:** JSON REST API под `/api/v1/*` для залогиненного покупателя (Flutter-приложение): избранное, адреса доставки и регистрация FCM device token. Доменная логика избранного и адресов уже существовала (`internal/storefront/{favorites,addresses}.go`, сделано параллельной сессией для сайта) — эта задача только оборачивает её JSON-хендлерами поверх новой `auth.Service.RequireCustomer` мидлвари. Device tokens не имели репозитория вообще (таблица `device_tokens` существовала с миграции 000014, но никто её не читал/писал) — репозиторий сделан в рамках этой задачи.

**Acceptance criteria:**
- [x] `GET /api/v1/favorites` — список избранных товаров (полные объекты `catalog.Product`, не голые id — см. Design decisions)
- [x] `POST /api/v1/favorites/{productId}` — добавить в избранное, идемпотентно (204)
- [x] `DELETE /api/v1/favorites/{productId}` — убрать из избранного, идемпотентно (204)
- [x] `GET /api/v1/addresses` — список адресов покупателя
- [x] `POST /api/v1/addresses` — создать адрес
- [x] `PUT /api/v1/addresses/{id}` — обновить адрес
- [x] `DELETE /api/v1/addresses/{id}` — удалить адрес
- [x] Все адресные операции скоуплены по `customerID` через сам `AddressRepo` (никогда не доверяем одному только id из URL) — покрыто тестом (`TestUpdateAddressHandlerPropagatesNotFound`)
- [x] `internal/storefront/devices.go` — новый `DeviceTokenRepo.Register(ctx, customerID, fcmToken, platform)`, upsert по `fcm_token` (UNIQUE): при переносе токена на другого покупателя (переустановка приложения / смена аккаунта на устройстве) `customer_id` перезаписывается на нового владельца, а не падает конфликтом — иначе токен молча остался бы привязан к предыдущему покупателю и пуши бы утекали не туда
- [x] Валидация `platform` (`ios`/`android`, иначе `apperr.BadRequest`) — чистая функция, покрыта юнит-тестом без БД
- [x] `POST /api/v1/devices` — тело `{fcm_token, platform}`
- [x] Все роуты этой задачи — за `authSvc.RequireCustomer(...)`, анонимного доступа нет нигде
- [x] Единая точка регистрации — `httpapi.RegisterCustomerRoutes(mux, db, authSvc)` (внутри вызывает по одному `register*Routes` на домен: favorites/addresses/devices — разбито для читаемости файлов, но наружу торчит один exported вход)
- [x] Тесты в стиле `admin_catalog_test.go` (фейковые репозитории через интерфейсы `favoriteLister`/`favoriteWriter`/`favoriteProductGetter`/`addressStore`/`deviceTokenRegisterer`, без живой БД) и в стиле `addresses_test.go` (чистая логика `validPlatform` в `internal/storefront/devices_test.go`)

**Verification:**
- [x] `gofmt -l .` — чисто
- [x] `go build ./...` — чисто
- [x] `go vet ./...` — чисто
- [x] `go test ./...` — чисто, все пакеты `ok`
- [x] `golangci-lint run ./...` — **1 предсуществующее** замечание (`internal/i18n/i18n.go:83`, `f.Close` не проверен, errcheck) — тот же файл параллельного потока `internal/web`/`internal/i18n`, который по инструкции этой задачи трогать нельзя; не создано и не изменено этой задачей (подтверждено: файл отсутствует в `git status` после всех правок). `golangci-lint run ./internal/httpapi/... ./internal/storefront/... ./internal/auth/... ./cmd/...` — **0 issues**
- [ ] Нет локального Postgres на этой машине — SQL в `DeviceTokenRepo.Register` (`INSERT ... ON CONFLICT (fcm_token) DO UPDATE`) не прогнан против живой БД, только вычитан построчно; `favorites.go`/`addresses.go` домены уже были покрыты этим предупреждением в Task 4 (см. выше)

**Dependencies:** `internal/auth.RequireCustomer` (только что добавлена в `main`), `internal/storefront.{FavoriteRepo,AddressRepo}` (домены Task 4), `internal/catalog.ProductRepo` (Task B)

**Files touched:**
- `internal/storefront/devices.go` (новый) — `DeviceTokenRepo`, `PlatformIOS`/`PlatformAndroid`, `validPlatform`
- `internal/storefront/devices_test.go` (новый) — тест на `validPlatform` + что невалидная платформа отсекается до обращения к `r.db` (nil `*sql.DB` не паникует)
- `internal/httpapi/favorites.go` (новый) — `registerFavoritesRoutes` + хендлеры GET/POST/DELETE
- `internal/httpapi/favorites_test.go` (новый)
- `internal/httpapi/addresses.go` (новый) — `registerAddressesRoutes` + хендлеры + `addressResponse` DTO (см. Design decisions)
- `internal/httpapi/addresses_test.go` (новый)
- `internal/httpapi/devices.go` (новый) — `registerDevicesRoutes` + хендлер
- `internal/httpapi/devices_test.go` (новый)
- `internal/httpapi/customer.go` (новый) — единственная экспортируемая точка входа `RegisterCustomerRoutes(mux, db, authSvc)`
- `internal/auth/middleware.go` — добавлен `NewContextWithCustomerID(ctx, customerID) context.Context`, симметрично `staff.NewContextWithStaff` из Task D — нужен тестам `internal/httpapi`, чтобы вызывать customer-scoped хендлеры без реального JWT (`customerIDKey` не экспортирован)
- `cmd/server/main.go` — одна строка `httpapi.RegisterCustomerRoutes(mux, db, authSvc)` внутри `registerAPIRoutes`

**Design decisions:**
- **Избранное отдаёт полные `catalog.Product`, а не голые id.** Экран избранного во Flutter должен отрисовать карточки товара (имя/цена/фото), так что голые id заставили бы приложение делать второй раунд-трип. Плата — цикл `ProductRepo.GetByID` по каждому id (N+1-ish): batch-метода `GetByIDs([]string)` в `internal/catalog` пока нет. При текущих объёмах избранного (десятки, не тысячи товаров у одного покупателя) это приемлемо; если списки вырастут, стоит добавить batch-запрос в `catalog.ProductRepo`. Товар, который с тех пор стал неактивным/удалённым, молча пропускается в ответе (та же семантика, что и в `internal/web`'s `loadFavoriteCards`), а не роняет весь запрос.
- **`addressResponse` — отдельный DTO с `json`-тегами**, а не прямая отдача `storefront.Address`. У `storefront.Address` нет своих json-тегов (он существовал только для рендеринга через `html/template` в `internal/web`, где имена полей Go читаются напрямую), так что прямая сериализация дала бы `PascalCase`-ключи (`ID`, `AddressText`, ...) вместо `snake_case`, которым пользуется остальной `/api/v1/*` (см. `catalog.Product`). Решено завести handler-локальный DTO с явными тегами вместо добавления json-тегов в общий доменный тип `internal/storefront` (который правит параллельная сессия) — меньше риск конфликта при мерже и чище разделение "домен / HTTP-представление".
- **Device token upsert перезаписывает `customer_id`, а не конфликтует.** Формулировка задачи это явно требует: один и тот же физический токен FCM может "переехать" к другому покупателю (переустановка приложения, смена аккаунта на устройстве), и застрявший на старом покупателе токен тихо ломает пуши для нового владельца устройства — поэтому `ON CONFLICT (fcm_token) DO UPDATE SET customer_id = ..., platform = ...`, без каких-либо дополнительных проверок владения.
- **204 No Content** для `POST/DELETE /api/v1/favorites/{productId}` и `POST /api/v1/devices` — тела ответа нет и не нужно, симметрично `DELETE`-хендлерам в `admin_catalog.go`.
- **Один exported вход, три internal register-функции** — `RegisterCustomerRoutes` в `internal/httpapi/customer.go` вызывает по одной непубличной `register{Favorites,Addresses,Devices}Routes` на файл; снаружи пакета торчит только один вызов для `cmd/server/main.go`, как просили в задаче.
- **`cmd/server/main.go` подключён этой же задачей** (одна строка в `registerAPIRoutes`, рядом с уже существующими `RegisterAuthRoutes`/`RegisterCatalogRoutes`) — конфликт с параллельным потоком заказов маловероятен: это независимая строка в маленькой функции, а не структурная правка.

**Deviations:** нет отклонений от acceptance criteria брифа. Добавлен один небольшой экспорт (`auth.NewContextWithCustomerID`), не описанный явно в брифе, но по прямой аналогии с уже принятым в кодовой базе паттерном (`staff.NewContextWithStaff`, Task D) — нужен исключительно для тестируемости хендлеров без живого JWT/БД. Примечание: Task H независимо добавила ту же функцию в параллельной ветке — при мерже обеих задач в `main` осталась одна копия (идентичны по поведению, отличались только комментарием).

---

## Task J: Admin Reports API («Task O» в брифе этой сессии) — DONE

**Description:** Отчёт по продажам для админки (§8/§14 ТЗ): агрегация заказов по дню/товару/точке продаж за диапазон дат, отдаётся как JSON и как настоящий `.xlsx` (через `excelize`). Доступ — только `owner`/`manager` (`point_staff` получает 403). Бриф этой сессии называл задачу «Task O»; в `tasks/plan.md`/`todo.md` тот же пункт Wave 3 значится как «Task J» — использовано название репозитория, чтобы не плодить дублирующиеся номера задач.

**Acceptance criteria:**
- [x] Чистая функция агрегации (`reports.AggregateSales(orders []orders.Order, groupBy reports.GroupBy, pointNames map[string]string) ([]reports.Row, error)`) — без обращения к БД, тестируется полностью на литералах `orders.Order`
- [x] Отдельный repo/query слой (`internal/reports/repo.go`, `*Repo`) грузит заказы (с items) за `[from, to)` из Postgres, по образцу `orders.(*Service).ListOrders`/`attachItems`, но с фильтром по `created_at`, а не по `customer_id` — репорт общеадминский, не привязан к покупателю
- [x] `GET /admin/api/reports/sales?from=&to=&group_by=day|product|point` → JSON-агрегат; `from`/`to` в формате `YYYY-MM-DD`
- [x] Валидация: `from <= to` и `group_by ∈ {day, product, point}` — иначе `apperr.BadRequest`; обе проверки — чистые функции (`reports.ValidateRange`, `reports.ParseGroupBy`), протестированы без HTTP/БД
- [x] Пустой диапазон / нет заказов → валидный пустой отчёт (`200`, `rows: []`), не ошибка
- [x] `GET /admin/api/reports/sales.xlsx?...` — тот же отчёт и те же параметры, но настоящий `.xlsx` через `github.com/xuri/excelize/v2`, с `Content-Type: application/vnd.openxmlformats-officedocument.spreadsheetml.sheet` и `Content-Disposition: attachment; filename="sales_report.xlsx"`
- [x] Оба эндпоинта — за `staffSvc.RequireRole(staff.RoleOwner, staff.RoleManager)`; `point_staff` получает 403 (обеспечивается тем же `RequireRole`, что уже покрыт своими тестами в `internal/staff/middleware_test.go` — отдельного RBAC-теста в `internal/httpapi` для этой задачи не заводилось, по аналогии с тем, как `admin_catalog_test.go` не дублирует такой тест для роутов категорий/товаров, а тестирует только настоящую RBAC-нюансировку stock-роута)
- [x] `RegisterAdminReportsRoutes(mux *http.ServeMux, db *sql.DB, staffSvc *staff.Service)` — единая точка входа
- [ ] **Не подключено в `cmd/server/main.go`** — сознательно, по прямому указанию брифа: `registerAdminRoutes` в этот момент активно правится параллельной сессией (Task G/H/I этой же волны), so подключение оставлено на усмотрение чек-пойнта после мержа всех веток Wave 3, а не сделано втихую здесь

**Verification:**
- [x] `gofmt -l .` — чисто
- [x] `go build ./...`, `go vet ./...` — чисто
- [x] `go test ./...` — чисто, все пакеты `ok`; `internal/reports/sales_test.go` — table-driven тесты на `ParseGroupBy`/`ParseReportDate`/`ValidateRange`/`AggregateSales` (день/товар/точка, пустой вход, все заказы отменены, сортировка, fallback имени точки, «один и тот же товар двумя строками одного заказа считает заказ один раз»); `internal/httpapi/admin_reports_test.go` — хендлеры через фейковый `salesRepo` (без БД), включая проверку, что XLSX-хендлер реально читается обратно через `excelize.OpenReader` (заголовки колонок + значения строк совпадают)
- [x] `go mod tidy` прогнан **после** того, как код, импортирующий `excelize`, уже был написан (как и предупреждал бриф — иначе `tidy` вычистил бы неиспользуемую зависимость обратно); добавил `github.com/xuri/excelize/v2` в `go.mod`/`go.sum` — при мерже с параллельной веткой Task K (тоже тянет `excelize`) ожидается тривиальный конфликт go.sum/go.mod, разрешаемый как «оставить обе/одну версию», как и предупреждал бриф
- [x] `golangci-lint run ./...` — **1 предсуществующее** замечание (`internal/i18n/i18n.go:83`, errcheck на `f.Close`) — не создано и не изменено этой задачей, файл не трогался; `golangci-lint run ./internal/reports/... ./internal/httpapi/...` — **0 issues**
- [ ] Нет локального Postgres на этой машине — SQL в `reports.Repo.{LoadOrders,attachItems,PointNames}` вычитан построчно (шаблон 1-в-1 повторяет уже рабочий `orders.(*Service).ListOrders`/`attachItems`, только с другим WHERE), но не прогнан против живой БД; `.xlsx`-эндпоинт не открывался в настоящем Excel — вместо этого сгенерированный в тесте файл распарсен обратно тем же `excelize.OpenReader` и сверен по заголовкам/ячейкам (см. `TestSalesReportXLSXHandlerProducesReadableWorkbook`), что подтверждает корректность ZIP/XLSX-структуры и данных, но не заменяет ручное открытие в Excel/LibreOffice

**Dependencies:** `internal/orders` (типы `Order`/`OrderItem`, не менялись), `internal/staff.Service.RequireRole` (не менялся), новая прямая зависимость `github.com/xuri/excelize/v2`

**Files touched:**
- `internal/reports/sales.go` (новый) — `GroupBy`, `ParseGroupBy`, `ParseReportDate`, `ValidateRange`, `Row`, `AggregateSales` + внутренние `aggregateByOrder`/`aggregateByItem`/`sortedRows` — вся логика чистая, без БД
- `internal/reports/repo.go` (новый) — `Repo`, `NewRepo`, `LoadOrders`, `attachItems`, `PointNames`
- `internal/reports/sales_test.go` (новый) — table-driven тесты на всё вышеперечисленное
- `internal/httpapi/admin_reports.go` (новый) — `salesRepo` интерфейс, `RegisterAdminReportsRoutes`, `salesReportJSONHandler`, `salesReportXLSXHandler` + JSON/XLSX-представления
- `internal/httpapi/admin_reports_test.go` (новый) — хендлер-тесты на фейковом `salesRepo`
- `go.mod`/`go.sum` — добавлен `github.com/xuri/excelize/v2` и его транзитивные зависимости

**Design decisions:**
- **Отменённые (`cancelled`) заказы полностью исключены из отчёта** — не только из выручки (как буквально сказано в брифе), но и из `order_count`/`item_count` тоже. Обоснование: строка отчёта вида «3 заказа, 0 сом выручки» читается как противоречие, а не как полезные данные; более простое и последовательное чтение брифа — «отменённые заказы не в отчёте вообще». Если впоследствии понадобится отдельная метрика по отменам (например, % отмен), это отдельная фича поверх той же `Repo.LoadOrders`, не переиспользующая `AggregateSales`.
- **Разный «естественный юнит» агрегации в зависимости от `group_by`.** Для `day`/`point` юнит агрегации — целый заказ (`order.TotalAmount` уже посчитан и округлён при создании заказа, пересчитывать его суммированием строк было бы двойной работой и источником рассинхрона). Для `product` юнит — строка заказа (`order_item`), потому что один заказ может содержать несколько разных товаров, и разложить его выручку по товарам можно только на уровне строк (`item.Price * item.Quantity`). `order_count` для `product`-группировки — количество *уникальных* заказов, содержащих товар (через `map[orderID]bool`), а не количество строк — иначе один заказ с двумя вариациями одного товара считался бы дважды.
- **Ключ группировки `product` — `ProductNameSnapshot`, а не `VariantID`.** §8/§14 ТЗ говорит про отчёт «по товару», а не «по вариации» (размер/цвет) — `product_name_snapshot` уже денормализован в `order_items` именно для таких отчётов, независимо от того, что случилось с товаром в каталоге позже.
- **Ключ группировки `point` резолвится в человекочитаемое имя через отдельный `Repo.PointNames()`**, а не через `internal/pos`/`internal/points` (такого пакета в `main` на момент этой задачи не существует — админка точек продаж это Task G/I той же волны, разрабатывается параллельно). `PointNames` читает `points_of_sale` напрямую (таблица существует с миграции 000003) — минимальная независимая зависимость вместо ожидания чужой ветки. Заказ без точки (`point_id IS NULL`) или с точкой, не найденной в `PointNames` (устарело/удалено), получает ключ `"unknown"`/сырой id соответственно, а не роняет отчёт.
- **`to` в query-параметрах — включительно (календарный день), а `Repo.LoadOrders` — `[from, to)` исключительно по правому краю.** Хендлер сдвигает верхнюю границу на `+1 день` перед вызовом `LoadOrders`, чтобы `to=2026-01-31` включал весь день 31 января, а не только его полночь. Задокументировано в докстрингах `LoadOrders` и `loadSalesRows`.
- **`cmd/server/main.go` НЕ подключён** — по прямому указанию брифа: `registerAdminRoutes` сейчас активно правится параллельной сессией (Task G/H/I той же волны), риск конфликта высокий и явно назван в задаче как повод не трогать этот файл. `RegisterAdminReportsRoutes(mux, db, staffSvc)` готов к подключению одной строкой при следующем чек-пойнте волны.

**Deviations:**
1. **Нумерация задачи.** Бриф этой сессии называет задачу «Task O», но в `tasks/plan.md`/`tasks/todo.md` (Wave 3 checklist) тот же пункт значится как «Task J» — сама секция с детальным описанием акцептанс-критериев по анкорной ссылке `plan.md#task-j-admin-reports-api` в `plan.md` на момент начала работы отсутствовала (файл короче, чем ссылки на него в `todo.md` подразумевают — видимо, ещё не дописан для Wave 3). Использованы акцептанс-критерии из брифа сессии (они были исчерпывающими) и название «Task J» для соответствия уже существующему чек-листу Wave 3 в `todo.md`, чтобы не плодить два номера для одной и той же фичи.
2. **Отменённые заказы исключены полностью, не только из выручки** — см. Design decisions выше; явно вызвано брифом как решение на усмотрение исполнителя.
3. **Ветка была отстроена от устаревшего `HEAD`** (репозиторий рабочей копии на момент старта отставал от `main`: `internal/orders`/`internal/staff`, упомянутые в брифе, отсутствовали). Перед началом работы ветка перебазирована (`git rebase main`) на актуальный `main` — без этого шага задача была невыполнима (нужные пакеты физически отсутствовали в дереве).
