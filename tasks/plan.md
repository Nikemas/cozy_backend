# Implementation Plan: cozy_backend — параллельные потоки после auth

## Overview

Каркас, миграции и вход покупателя по OTP (шаги 1–3 из §13 ТЗ) готовы и закоммичены (`3b6f6b5 first deploy`). Следующие три потока (RBAC для сотрудников — 4, каталог — 5, Docker/CI — 16) не зависели друг от друга и не пересекались по файлам — сделаны параллельно тремя агентами в изолированных git worktree и смержены в `main`. Остальные шаги (MinIO, заказы, Bakai, REST-обёртка, сайт, админка) зависят от каталога и/или RBAC и идут следующей волной.

## Architecture Decisions

- Каждый поток регистрирует свои роуты через собственную функцию `RegisterRoutes(mux, ...)` в своём пакете (`internal/staff`, `internal/httpapi`), а не пишет напрямую в `main.go` — вызов этой функции добавлен в `main.go` одной строкой на поток, конфликт при мерже был минимальным.
- Staff-сессии — server-side (cookie), таблица `staff_sessions` по аналогии с `refresh_tokens` (hash токена, не сырое значение).
- Публичные read-эндпоинты каталога (`GET /categories`, `/products`, `/products/:id`) не требуют RBAC. Admin-эндпоинты каталога (создание/редактирование, защищённые `RequireRole`) — следующая волна, поверх смерженного RBAC.
- Docker/CI не имеет доступа к реальному VPS заказчика — `deploy.yml` сделан шаблоном с плейсхолдер-секретами, без реального деплоя.

## Task List

### Phase: Parallel (3 агента, git worktrees) — ЗАВЕРШЕНО, смержено в main

- [x] **Task A — Staff auth & RBAC** (шаг 4 ТЗ)
- [x] **Task B — Catalog domain + публичные read-эндпоинты** (шаг 5 ТЗ, частично)
- [x] **Task C — Docker + CI/CD** (шаг 16 ТЗ)

### Checkpoint: После мержа A, B, C
- [x] Все три ветки смержены в `main` (`git merge --no-ff` x3, конфликт только в `tasks/*.md`, разрешён вручную)
- [x] `go build ./...`, `go vet ./...`, `go test ./...`, `gofmt -l .` — чисто на объединённом коде
- [x] `cmd/server/main.go` — авто-мерж без конфликтов (каждый поток трогал свою функцию)
- [ ] Ревью с пользователем перед следующей волной

### Phase: Следующая волна (после чекпоинта, не параллелится сразу)
- [ ] Admin CRUD для каталога (защищено RBAC из Task A) — шаг 12
- [ ] MinIO-интеграция (шаг 6, зависит от Task B)
- [ ] Заказы + списание остатков через `withTx` (шаг 7, зависит от Task B)
- [ ] Bakai webhook (шаг 8, зависит от заказов)

## Risks and Mitigations

| Risk | Impact | Mitigation |
|---|---|---|
| Три ветки правят `cmd/server/main.go` в одном файле | Низкий (конфликт мержа) | Подтвердилось на практике: авто-мерж прошёл чисто, каждый поток менял свою функцию |
| `tasks/plan.md`/`todo.md` не были закоммичены до старта агентов → два потока независимо их закоммитили | Средний (add/add конфликт при мерже) | Разрешено вручную после мержа всех трёх веток — этот файл сейчас и есть итоговая версия |
| Нет Docker/Postgres на этой машине | Средний (нет E2E-проверки) | Каждый агент прогнал `go build`/`go vet`/`go test` как обязательную проверку; ручной прогон `make dev-up && make migrate-up && make run` + `docker build` — на пользователе |
| Admin-каталог (создание/редактирование) требует и RBAC, и каталог одновременно | Низкий | Вынесен в следующую волну, не в параллельную — избегает скрытой зависимости между A и B |

## Open Questions

- Нужна ли для Task C реальная привязка к VPS (SSH-ключ, хост) уже сейчас, или ограничиться шаблоном `deploy.yml` до готовности инфраструктуры? — пока шаблон, секреты (`DEPLOY_HOST`, `DEPLOY_USER`, `DEPLOY_SSH_KEY`, `DEPLOY_SSH_PORT`, `DEPLOY_PATH`) документированы в самом `.github/workflows/deploy.yml`.

---

## Wave 3: Admin-панель — backend JSON API (§6/§8 ТЗ, RBAC-запись поверх Task A/D)

### Overview

RBAC (Task A), товарный CRUD (Task D) и заказы (`internal/orders`, влиты веб-потоком, коммит `6bd5e7b`) уже готовы. Остаётся дописать пять недостающих admin-API-поверхностей из таблицы §6 ТЗ: заказы (список/статус), точки продаж, сотрудники, отчёты по продажам (Excel), импорт товаров (CSV/Excel). Пользователь явно попросил начать с бека — HTML/HTMX-слой (`internal/admin`, экраны поверх этих API) сознательно вынесен в отдельную Wave 4 *после* этой волны, а не сделан параллельно: дизайн админки (Claude Design canvas) ещё не создан на момент старта (`docs/Cozy — Админ-панель — ТЗ для дизайнера.md` — только бриф), и без него вёрстка рискует стать одноразовой. API же от дизайна и от HTML-слоя не зависит — блокировать её незачем.

### Architecture Decisions

- **5 независимых потоков** (как в Wave 1: A/B/C) — каждый в своём git worktree/ветке, свой Go-пакет, своя `RegisterXxxRoutes(mux, db, staffSvc)`-функция, вызов которой добавляется одной строкой в `registerAdminRoutes` (`cmd/server/main.go`) — тот же паттерн, что дал чистый автомердж в Wave 1/2.
- **Task L (points) и Task M (staff CRUD) не зависят друг от друга по коду.** Staff CRUD валидирует `point_id` через существующий FK (`staff.point_id → points_of_sale.id`) и перехват SQLSTATE `23503` в `apperr` (тот же приём, что `internal/catalog/pgerr.go` уже использует для категорий/вариаций) — не импортирует пакет `internal/points` из Task L, чтобы не создавать компилируемую зависимость между двумя параллельными ветками.
- **Task N (admin orders) не создаёт новый пакет** — расширяет существующий `internal/orders` репозиторным слоем для списка/деталей/смены статуса. Не зависит от Task D и ничем не блокируется.
- **Task O (reports) и Task P (import) — оба тянут новую зависимость `github.com/xuri/excelize/v2`.** Риск конфликта `go.mod`/`go.sum` при мердже — тот же класс риска, что уже был решён в Wave 1/2 для `cmd/server/main.go` (не блокировать параллельный старт; разрешить вручную на чек-пойнте — сохранить объединение зависимостей, прогнать `go mod tidy` после второго мерджа).
- **`internal/admin` (HTML/HTMX) — явно НЕ в этой волне.** Вынесен в Wave 4, стартует после чек-пойнта Wave 3 и решения по Open Questions (вёрстка без готового дизайна).

### Task List

#### Task L: Admin Points of Sale API

**Description:** CRUD точек продаж (`/admin/api/points`), доступ только `owner`.

**Acceptance criteria:**
- [ ] `GET /admin/api/points` — список всех точек, включая неактивные (админке нужно видеть всё, в отличие от публичного каталога)
- [ ] `POST /admin/api/points` — создание (`name`, `address`), только `owner`
- [ ] `PUT /admin/api/points/{id}` — редактирование (`name`, `address`, `is_active`), только `owner`
- [ ] `DELETE /admin/api/points/{id}` — только `owner`; если точка используется (`staff.point_id`/`stock.point_id`/`orders.point_id`) — SQLSTATE `23503` → `apperr.Conflict` (409), не 500 и не тихое каскадное удаление (паттерн Task D)

**Verification:**
- [ ] `gofmt -l .`, `go build ./...`, `go vet ./...`, `go test ./...` — чисто
- [ ] Юнит-тесты на валидацию входа (пустое имя/адрес) без БД
- [ ] Manual: нет живого Postgres на этой машине — не прогнано, пометить открытым

**Dependencies:** None (таблица `points_of_sale` уже смигрирована, `staff.Service.RequireRole` уже есть)

**Files likely touched:**
- `internal/points/{points,routes}.go` (новый пакет) + тесты
- `cmd/server/main.go` — одна строка `points.RegisterRoutes(...)` в `registerAdminRoutes`

**Estimated scope:** Small

---

#### Task M: Admin Staff API

**Description:** CRUD сотрудников (`/admin/api/staff`), только `owner`. Создание — bcrypt-хеш пароля (`golang.org/x/crypto/bcrypt`, уже зависимость с Task A). Обязательный инвариант: нельзя деактивировать/понизить роль последнего активного `owner`.

**Acceptance criteria:**
- [ ] `GET /admin/api/staff` — список сотрудников без `password_hash` в ответе, только `owner`
- [ ] `POST /admin/api/staff` — создание (`phone`, `password`, `name`, `role`, `point_id` для `point_staff`), только `owner`; пароль хешируется bcrypt
- [ ] `PUT /admin/api/staff/{id}` — редактирование (`name`, `role`, `point_id`, `is_active`, опционально новый `password`), только `owner`
- [ ] Инвариант «последний owner»: попытка деактивировать/понизить роль единственного активного `owner` → `apperr.Conflict` (409)
- [ ] `point_id` обязателен для `role=point_staff` и запрещён (`NULL`) для `owner`/`manager` — валидация на входе, не только через БД-constraint
- [ ] Несуществующий `point_id` → `23503` перехватывается в `apperr.BadRequest` (тот же приём, что Task L)

**Verification:**
- [ ] `gofmt -l .`, `go build ./...`, `go vet ./...`, `go test ./...` — чисто
- [ ] Юнит-тесты: невозможность понизить последнего owner, обязательность `point_id` для `point_staff`, валидация телефона
- [ ] Manual: нет живого Postgres — не прогнано

**Dependencies:** None (не зависит от Task L — валидация `point_id` через FK/`23503`, без импорта `internal/points`)

**Files likely touched:**
- `internal/staff/{staff,service,routes}.go` — расширение существующего пакета
- `internal/staff/*_test.go` — новые тесты
- `cmd/server/main.go` — без изменений (CRUD добавляется в уже существующий `staff.RegisterRoutes`)

**Estimated scope:** Medium (инвариант «последний owner» — единственная нетривиальная бизнес-логика; см. риск гонки в таблице ниже)

---

#### Task N: Admin Orders API

**Description:** Список заказов с фильтрами и деталями для админки (`/admin/api/orders`), плюс смена статуса. RBAC-нюанс: `owner`/`manager` видят все точки, `point_staff` — только заказы своей точки (паттерн уже есть для остатков, Task D).

**Acceptance criteria:**
- [ ] `GET /admin/api/orders` — фильтры `status`, `point_id`, `from`/`to` (по `created_at`), `q` (поиск по `order_number`), пагинация (`page`); `point_staff` — результат всегда ограничен своей точкой независимо от переданного `point_id`
- [ ] `GET /admin/api/orders/{id}` — детали + позиции (`order_items`); `point_staff` на чужую точку — 403 (консистентно с `apperr.Forbidden` на остатках чужой точки в Task D, см. Open Questions)
- [ ] `PUT /admin/api/orders/{id}/status` — смена статуса по валидному переходу (`placed → confirmed → courier_assigned → delivered`, `cancelled` — из любого состояния кроме `delivered`); неверный переход → `apperr.BadRequest`
- [ ] `point_staff` не может менять статус заказа чужой точки (403)

**Verification:**
- [ ] `gofmt -l .`, `go build ./...`, `go vet ./...`, `go test ./...` — чисто
- [ ] Юнит-тесты: RBAC-нюанс (своя/чужая точка, по аналогии с `internal/httpapi/admin_catalog_test.go`), таблица валидных/невалидных переходов статуса
- [ ] Manual: нет живого Postgres — не прогнано

**Dependencies:** None (`internal/orders` и `staff.RequireRole`/`staff.FromContext` уже есть)

**Files likely touched:**
- `internal/orders/admin.go` — репозиторный слой для списка/деталей/смены статуса
- `internal/httpapi/admin_orders.go` + тест — хендлеры и RBAC
- `cmd/server/main.go` — одна строка `httpapi.RegisterAdminOrdersRoutes(...)` в `registerAdminRoutes`

**Estimated scope:** Medium

---

#### Task O: Admin Reports API

**Description:** Отчёты по продажам (`/admin/api/reports/sales`) — по периоду/товару/точке, экспорт в Excel (`excelize`). Только `owner`/`manager` (§5 ТЗ — отчёты не для `point_staff`).

**Acceptance criteria:**
- [ ] `GET /admin/api/reports/sales?from=&to=&group_by=day|product|point` — JSON-агрегат (сумма, количество заказов/позиций); заказы `cancelled` исключены из выручки
- [ ] `GET /admin/api/reports/sales.xlsx?...` (те же параметры) — тот же отчёт как `.xlsx` через `excelize` (корректные `Content-Type`/`Content-Disposition`)
- [ ] Только `owner`/`manager` (`RequireRole`), `point_staff` → 403
- [ ] Пустой диапазон/нет заказов за период → пустой, но валидный отчёт (0 строк), не ошибка

**Verification:**
- [ ] `gofmt -l .`, `go build ./...`, `go vet ./...`, `go test ./...` — чисто
- [ ] Юнит-тесты на агрегацию (чистая функция от списка заказов, без БД) и валидацию параметров (`from > to`, неизвестный `group_by`)
- [ ] Manual: нет живого Postgres — не прогнано; открыть сгенерированный `.xlsx` вручную при живой проверке

**Dependencies:** None (новая зависимость `excelize` — см. риск ниже)

**Files likely touched:**
- `internal/reports/{sales,sales_test}.go` (новый пакет) — агрегация
- `internal/httpapi/admin_reports.go` + тест — хендлеры (JSON + xlsx)
- `cmd/server/main.go` — одна строка в `registerAdminRoutes`
- `go.mod`/`go.sum` — новая зависимость `github.com/xuri/excelize/v2`

**Estimated scope:** Medium

---

#### Task P: Импорт товаров

**Description:** Массовая загрузка товаров — `POST /admin/products/import`, CSV или Excel (`excelize`/`encoding/csv`), построчный отчёт об ошибках. Доступ `owner`/`manager`.

**Acceptance criteria:**
- [ ] `POST /admin/products/import` (`multipart/form-data`, файл `.csv` или `.xlsx`) — построчно парсит `name_ru`, `name_ky`, `category` (slug/id), `price`, опционально размеры/цвета/остатки по точкам
- [ ] Формат файла определяется по расширению/`Content-Type`, неизвестный формат → `apperr.BadRequest` до парсинга
- [ ] Валидные строки создаются через уже существующие `catalog.ProductRepo`/`VariantRepo.Create` (Task D) — не дублирует их бизнес-логику
- [ ] Невалидные строки не прерывают импорт остальных — ответ: `{ imported: N, errors: [{row, message}] }`
- [ ] Только `owner`/`manager`

**Verification:**
- [ ] `gofmt -l .`, `go build ./...`, `go vet ./...`, `go test ./...` — чисто
- [ ] Юнит-тесты: парсинг валидного/невалидного CSV (табличные кейсы), частичный успех
- [ ] Manual: нет живого Postgres — не прогнано

**Dependencies:** Task D (использует существующие `catalog.ProductRepo`/`VariantRepo` — уже смержено, не блокирует старт)

**Files likely touched:**
- `internal/catalog/import.go` (парсинг+валидация, тестируется без `multipart`) + `internal/httpapi/admin_import.go` (HTTP-хендлер) + тесты
- `cmd/server/main.go` — одна строка в `registerAdminRoutes`
- `go.mod`/`go.sum` — та же зависимость `excelize`, что в Task O (см. риск мерджа)

**Estimated scope:** Medium/Large — при необходимости разбить на «парсинг+валидация» и «HTTP-хендлер+multipart»

---

### Checkpoint: после Task L–K

- [ ] Все 5 веток смержены в `main`
- [ ] `go build ./...`, `go vet ./...`, `go test ./...`, `gofmt -l .` — чисто на объединённом коде
- [ ] `go.mod`/`go.sum` — одна версия `excelize` после мерджа Task O и Task P, `go mod tidy` прогнан
- [ ] `cmd/server/main.go` — конфликты (если есть) только аддитивные в `registerAdminRoutes`, разрешены вручную
- [ ] `openapi.yaml` дополнен новыми путями (`/admin/api/{orders,points,staff,reports}`, `/admin/products/import`) — отдельная маленькая задача после чек-пойнта, по аналогии с Task F
- [ ] Ревью с пользователем перед Wave 4 (HTML/HTMX-слой `internal/admin`)

---

## Wave 4: internal/admin — HTML/HTMX-слой поверх Wave 3

### Overview

Дизайн-канвас админки готов и получен 2026-09-15 через Claude Design (проект `20e92866-8abe-4e59-bf60-7c5190724978`, файл `Cozy Admin.dc.html`) — сохранён в `design/Cozy Admin/Cozy Admin.dc.html` (+ `design/Cozy Admin/img/cozy-logo.png`). 10 экранов: Вход, layout (сайдбар+хедер), Товары (таблица/карточки + форма + импорт), Заказы (список + детально), Отчёты, Точки продаж, Сотрудники — все уже видны в канвасе как `sc-if`-блоки (`isLogin`, `isApp`→`isProducts`/`isForm`/`isImport`/`isOrders`/`isDetail`/`isReports`/`isPoints`/`isStaff`) плюс модалки (`modalOpen`/`pointModal`/`staffModal`/`subModal`/`confirmModal`) и toast. Канвас размечен почти целиком инлайн-стилями (не классами) — переносится в Go-шаблоны почти дословно, меняются только плейсхолдеры `{{ x }}` (React-canvas биндинги) на реальные `html/template`-действия и данные из Wave 3 API. Токены совпадают с уже задокументированными в `design-system/tokens.css` (primary `#FF7A1A`, border `#E5E5E3`) плюс админские размерные токены из брифа дизайнера: `sidebarWidth 240px`, `rowHeight 56px`, `radiusCard 16px`, `radiusField 10px`, `radiusChip 8px`.

Все backend JSON API (`/admin/api/{categories,products,variants,images,stock,points,staff,orders,reports}`, `/admin/products/import`) и логин/сессия (`/admin/api/login`/`logout`) уже готовы (Wave 3). Эта волна — только слой `internal/admin` (html/template + HTMX/чуть JS), который их использует.

### Architecture Decisions

- **Двухфазная структура, как в Wave 1 cozy_mobile**: Task 1 (Foundation) — соло, блокирует остальное; Task 2–5 — параллельно в git worktree поверх смерженного Task 1.
- **Foundation фиксирует layout/shell/auth-gate/токены** — Task 2–5 их не трогают, только добавляют свои страницы внутрь `<div style="padding: 24px; flex: 1">...</div>` контентной зоны и регистрируют свои роуты.
- **HTML-аутентификация — не `staffSvc.RequireRole` напрямую.** Существующий `RequireRole` (Wave 3) отдаёт JSON-ошибку (`apperr.Wrap`) — для HTML-страниц нужен отдельный gate-хелпер в `internal/admin`, который при отсутствии/невалидной сессии редиректит на `/admin/login`, а при недостаточной роли — либо редирект на первый доступный по роли раздел, либо страница 403 (Foundation решает, как именно, единообразно для всех страниц).
- **Ролевая видимость меню** — по таблице §5 ТЗ: `owner` видит всё (Товары/Заказы/Отчёты/Точки/Сотрудники); `manager` — Товары/Заказы/Отчёты (без Точек/Сотрудников); `point_staff` — только Заказы (и, если решит Foundation, урезанный вид Товаров под остатки своей точки — см. Open Questions). Пункты недоступных по роли разделов **не рендерятся**, не дизейблятся (см. [[cozy-project]]).
- **HTMX для интерактивности без полной перезагрузки** (фильтры каталога/заказов, открытие форм, модалки, тосты) — по архитектуре сайта (§7 ТЗ); минимум vanilla JS/Alpine.js там, где HTMX неудобен (открытие/закрытие модалки, drag-drop фото).
- **Фото товара — presigned direct upload**, уже реализовано на бэке (Task E, Wave 2): страница получает `{upload_url, object_key}` от `/admin/api/media/presign-upload`, грузит файл в MinIO напрямую через JS (`fetch(uploadUrl, {method:'PUT', body: file})`), затем отправляет `object_key` в `/admin/api/products/{id}/images`. `<image-slot>` из канваса — компонент только для превью в самом канвасе, в реальной вёрстке — обычный `<input type="file">`/drag-drop.
- **Экспорт отчёта в Excel** — обычная ссылка `<a href="/admin/api/reports/sales.xlsx?...">`, браузер скачивает файл, никакого JS не требуется.

### Task List

#### Task 1: Foundation — layout, auth, токены, роутинг `internal/admin`

**Description:** Каркас `internal/admin`: страница входа (`/admin/login`), shared layout (сайдбар с ролевой видимостью + хедер), auth-gate для HTML-страниц, базовые CSS-токены, регистрация в `cmd/server/main.go`. Экраны-заглушки (просто заголовок) для Товаров/Заказов/Отчётов/Точек/Сотрудников — чтобы Task 2–5 подключали свои реальные страницы, не трогая layout.

**Acceptance criteria:**
- [ ] `GET /admin/login` — страница входа 1:1 по канвасу (строки 28–54 `Cozy Admin.dc.html`): логотип, телефон, пароль (показать/скрыть), ошибка, кнопка «Войти»
- [ ] `POST /admin/login` — вызывает существующий `staffSvc.Login(...)`, при успехе ставит cookie и редиректит на первый доступный по роли раздел; при ошибке — та же страница с `err`
- [ ] `POST /admin/logout` — вызывает `staffSvc.Logout(...)`, редирект на `/admin/login`
- [ ] Layout (строки 56–115, 660–669 канваса): сайдбар (`aside`) с логотипом, nav-пунктами (иконка+лейбл), инициалами/именем/ролью пользователя внизу, кнопкой «Выйти»; хедер (заголовок страницы, поиск/back — по месту использования)
- [ ] Ролевая видимость nav-пунктов по таблице §5 ТЗ (см. Architecture Decisions) — реализована один раз в layout, не дублируется на каждой странице
- [ ] Auth-gate: нет/невалидная сессия → редирект `/admin/login`; недостаточная роль на конкретный раздел → редирект/403 (единообразно)
- [ ] Базовые CSS-токены вынесены в один файл/блок (не переизобретать `design-system/tokens.css`, а расширить его или завести `admin/static/css/admin.css` — на усмотрение исполнителя, задокументировать решение)
- [ ] Toast-компонент (канвас, строки ~785+) и generic confirm-модалка (канвас, строки ~672–683) — общие partial-шаблоны, готовые к переиспользованию в Task 2–5
- [ ] Заглушки экранов Товары/Заказы/Отчёты/Точки/Сотрудники — просто рендерят заголовок раздела внутри layout, без реальных данных (значит `RegisterRoutes` регистрирует все 6 путей `/admin/products`, `/admin/orders`, `/admin/reports`, `/admin/points`, `/admin/staff`, `/admin/products/import` сразу, но с заглушечными хендлерами — Task 2–5 их заменяют, не создавая новые пути)

**Verification:**
- [ ] `gofmt -l .`, `go build ./...`, `go vet ./...`, `go test ./...` — чисто
- [ ] Юнит-тесты на auth-gate (нет сессии → редирект; недостаточная роль → редирект/403; нужная роль → пропускает) — по образцу `internal/staff/middleware_test.go`, без живой БД
- [ ] Manual: нет живого Postgres на этой машине — залогиниться curl'ом не получится без данных; ограничиться `go build`/юнит-тестами и визуальной сверкой вёрстки (например, статичным рендером шаблона в тесте) с канвасом

**Dependencies:** None (использует уже готовые `staff.Service`, `staff.RequireRole`/`FromContext`)

**Files likely touched:**
- `admin/templates/{layout,login,_sidebar,_header,_toast,_confirm_modal}.gohtml` (новые, по аналогии с `web/templates/`)
- `internal/admin/{routes,auth_gate,handlers}.go` (новый пакет)
- `admin/static/css/admin.css` или расширение `design-system/tokens.css` — на усмотрение исполнителя
- `cmd/server/main.go` — `admin.RegisterRoutes(mux, db, staffSvc)` в `registerAdminRoutes`

**Estimated scope:** Large (единственная соло-задача волны, блокирует остальное — как Task 1 в cozy_mobile Wave 1)

---

### Checkpoint: после Task 1

- [ ] `go build ./...`/`go vet ./...`/`go test ./...` чисто
- [ ] Вход/выход работают (можно проверить без живой БД — только что нет 500/паники на GET-страницах)
- [ ] Ролевая видимость сайдбара реализована и покрыта тестами
- [ ] Смержено в `main`, ветки Task 2–5 стартуют от этого коммита

---

#### Task 2: Товары — список, форма, импорт

**Description:** Экраны `isProducts` (канвас, строки 116–252), `isForm` (254–374), `isImport` (376–404) поверх Wave 3 API (`internal/httpapi/admin_catalog.go`, `admin_import.go`, `internal/media`).

**Acceptance criteria:**
- [ ] Список: таблица (desktop) + карточки (мобильная ширина) с чипами категорий/подкатегорий, сортировкой по колонкам, skeleton-загрузкой, пустым состоянием, меню строки (редактировать/деактивировать/удалить — `canDelete` только `owner`)
- [ ] Форма создания/редактирования: RU/KY табы названия/описания, категория/подкатегория (+ модалка создания новой подкатегории), бренд, цена, фото (реальная загрузка через presign+PUT в MinIO, не `<image-slot>` из канваса), таблица вариаций (размер/цвет/остаток/уровень) с добавлением/удалением строк
- [ ] Импорт: drag-drop/выбор файла CSV/XLSX → `POST /admin/products/import`, построчный отчёт об ошибках на той же странице
- [ ] `canEdit`/`canDelete`-видимость кнопок — по роли (owner/manager видят «Добавить»/«Импорт»/меню строки); `point_staff` не имеет доступа к разделу Товары вообще (решено ниже — §5 ТЗ описывает его как «заказы и остатки только своей точки», канвас не рисует отдельный urls/вид Товаров под эту роль, и заводить недизайненный экран не входит в эту волну)

**Verification:** `gofmt -l .`, `go build ./...`, `go vet ./...`, `go test ./...` — чисто; юнит-тесты на серверный рендеринг ключевых состояний (пусто/загрузка/есть данные) по образцу существующих `internal/web` тестов

**Dependencies:** Task 1

**Files likely touched:** `admin/templates/products/**`, `internal/admin/products.go` (+тесты)

**Estimated scope:** Large (3 экрана; при необходимости разбить на «список» и «форма+импорт»)

---

#### Task 3: Заказы — список и детально

**Description:** Экраны `isOrders` (405–497) и `isDetail` (499–556) поверх `internal/httpapi/admin_orders.go`.

**Acceptance criteria:**
- [ ] Список: чипы статусов, выбор периода, таблица/карточки, skeleton, пустое состояние; `point_staff` видит только заказы своей точки (уже скоупится на бэкенде — страница просто передаёт сессию)
- [ ] Детально: покупатель/доставка/состав заказа/итого, кнопки смены статуса (только валидные переходы — сервер уже валидирует, кнопки можно строить из текущего статуса на клиенте по той же state-machine логике, задокументированной в Task N)

**Verification:** `gofmt -l .`, `go build ./...`, `go vet ./...`, `go test ./...` — чисто; юнит-тесты на рендеринг состояний

**Dependencies:** Task 1

**Files likely touched:** `admin/templates/orders/**`, `internal/admin/orders.go` (+тесты)

**Estimated scope:** Medium

---

#### Task 4: Отчёты

**Description:** Экран `isReports` (558–617) поверх `internal/httpapi/admin_reports.go`.

**Acceptance criteria:**
- [ ] Селектор периода, 4 stat-карточки, бар-чарт продаж по дням (чистый CSS/HTML, без JS-чарт-библиотеки — высоты столбиков считает сервер), топ-товары, топ-бренды
- [ ] Кнопка «Экспорт в Excel» — прямая ссылка на `/admin/api/reports/sales.xlsx?...` с текущими параметрами периода
- [ ] Доступ — только `owner`/`manager` (уже гарантирует auth-gate из Task 1 + `RequireRole` на API)

**Verification:** `gofmt -l .`, `go build ./...`, `go vet ./...`, `go test ./...` — чисто

**Dependencies:** Task 1

**Files likely touched:** `admin/templates/reports/**`, `internal/admin/reports.go` (+тесты)

**Estimated scope:** Small/Medium

---

#### Task 5: Точки продаж + Сотрудники

**Description:** Экраны `isPoints` (620–642) и `isStaff` (644–664) + их модалки (`pointModal` ~684, `staffModal` ~706) поверх `internal/points` и `internal/staff` (admin CRUD). Только `owner`.

**Acceptance criteria:**
- [ ] Точки: список + модалка добавления (имя/адрес), toggle активности
- [ ] Сотрудники: список (роль-чип, точка/scope, toggle активности) + модалка добавления (телефон/пароль/имя/роль/точка); инвариант «последний owner» с бэка — при ошибке 409 показать её текст, не молча игнорировать
- [ ] Оба экрана целиком скрыты по сайдбару (Task 1) и по auth-gate для `manager`/`point_staff` — прямой заход по URL тоже должен быть закрыт (не полагаться только на скрытый пункт меню)

**Verification:** `gofmt -l .`, `go build ./...`, `go vet ./...`, `go test ./...` — чисто; тест на прямой заход по URL без роли owner → редирект/403

**Dependencies:** Task 1

**Files likely touched:** `admin/templates/{points,staff}/**`, `internal/admin/{points,staff}.go` (+тесты)

**Estimated scope:** Medium

---

### Checkpoint: после Task 2–5

- [ ] Все 4 ветки смержены в `main`
- [ ] `go build ./...`/`go vet ./...`/`go test ./...`/`gofmt -l .` чисто на объединённом коде
- [ ] Полный обход всех 10 экранов вручную (или через тесты рендеринга) без 500/паники
- [ ] Визуальная сверка с `Cozy Admin.dc.html` — ревью с пользователем

### Risks and Mitigations (Wave 4)

| Risk | Impact | Mitigation |
|---|---|---|
| Task 2–5 расходятся в мелких деталях вёрстки (отступы/радиусы), т.к. каждая копирует инлайн-стили из канваса независимо | Низкий | Канвас — источник истины, копировать стили дословно, а не «по памяти»; ревью на чек-пойнте |
| Auth-gate из Task 1 спроектирован неудачно (например, только скрывает меню, не блокирует прямой URL) | Высокий (дыра в RBAC) | Explicit acceptance criterion в Task 1 и повторная проверка в Task 5 (единственная задача с owner-only контентом, естественная точка проверить прямой заход) |
| point_staff-вид Товаров — решено оставить его без доступа к разделу вообще (см. Open Questions), не изобретать недизайненный экран | Низкий (решено, не блокирует) | Task 2 просто не включает `point_staff` в `canView`-роли раздела Товары |

- **Решено:** `point_staff` не получает пункт «Товары» в сайдбаре и не имеет доступа к разделу вообще в этой волне — канвас не рисует отдельный урезанный экран остатков под эту роль, а §5 ТЗ описывает её зону как «заказы и остатки только своей точки» без явного упоминания UI каталога. Экран управления остатками именно своей точки для `point_staff` — отдельная, недизайненная задача за рамками Wave 4, JSON API (`PUT /admin/api/stock/...`) под это уже есть.
- Экспорт Excel — открывать в новой вкладке или сразу качать (`download`-атрибут)? Мелочь на усмотрение Task 4.

### Risks and Mitigations

| Risk | Impact | Mitigation |
|---|---|---|
| Task O и Task P оба добавляют `excelize` в `go.mod` | Средний (конфликт/дублирование версий при мердже) | Второй мерджащийся поток запускает `go mod tidy` после мерджа первого; зафиксировать одну версию на чек-пойнте |
| Task M («последний owner») — гонка при параллельных запросах на понижение двух последних owner одновременно | Низкий (2–5 точек продаж у заказчика, маловероятный сценарий) | Проверка количества активных owner в той же транзакции, что и update (локальная транзакция, как в Task D `ImageRepo.ReplaceForProduct` — общего `withTx`-хелпера в кодовой базе ещё нет) |
| Task N: RBAC для `point_staff` на заказы без `point_id` (`orders.point_id` — nullable, "откуда комплектуется") | Средний (неочевидно, кто видит ещё не привязанный к точке заказ) | Решить как acceptance criterion самой задачи: `point_staff` не видит заказы с `point_id IS NULL` (видны только owner/manager до привязки к точке); задокументировать в Deviations |
| Импорт товаров (Task P) — большой файл, нет стриминга | Низкий (2–5 точек продаж, каталог не миллионы SKU) | Не оптимизировать заранее; батчинг/стриминг — отдельная задача при необходимости |
| `internal/admin` (Wave 4) без готового дизайна рискует стать одноразовой вёрсткой | Средний | Сознательно вынесено в отдельную волну после чек-пойнта Wave 3 — см. Open Questions |

### Open Questions

- Task N: точный код ответа при обращении `point_staff` к чужому заказу — 403 (раскрывает существование) или 404 (скрывает)? Не зафиксировано в ТЗ; решить консистентно с уже принятым в Task D паттерном (`apperr.Forbidden` для остатков чужой точки).
- Wave 4 (`internal/admin`) — начинать функциональной вёрсткой без дизайна сейчас, или ждать Claude Design канвас админки (ещё не заказан)? Не решено в этой сессии по явной инструкции пользователя «начнём с бека».
- Нужен ли общий `withTx`-хелпер (упоминается в §4 ТЗ, ещё не реализован ни в одном пакете) — Task M (last-owner invariant) первая задача, которой он реально понадобится; завести его как побочный продукт Task M, или оставить локальную транзакцию по образцу Task D?
