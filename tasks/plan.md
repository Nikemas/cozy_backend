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

## Wave 4 (не начинать в этой сессии): internal/admin — HTML/HTMX-слой поверх Wave 3

Страница логина, layout с сайдбаром (ролевая видимость пунктов меню — принцип «недоступное не показывается, а не дизейблится», см. [[cozy-project]]), экраны Товары/Заказы/Отчёты/Точки/Сотрудники/Импорт поверх API из Wave 3. Стартует после чек-пойнта Wave 3 и решения по первому Open Question ниже. Сознательно не разбито на задачи в этой сессии — пользователь явно попросил начать с бека.

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
