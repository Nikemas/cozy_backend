# Сторонние компоненты и лицензии — cozy_backend

Перечень компонентов с открытым исходным кодом, использованных в backend Cozy
(сайт, админ-панель, REST API), — по п. 5.3 Договора.

Как составлен (06.10.2026): версии — из `go.mod` / `go list -deps ./cmd/server`;
лицензии — по файлам `LICENSE` в кэше модулей Go (`go env GOMODCACHE`),
для веб-ресурсов с CDN — по `package.json` / `LICENSE` пакета, для
серверного ПО — по официальным репозиториям. При обновлении зависимостей
список нужно пересобрать.

Все перечисленные лицензии (MIT, BSD, ISC, Apache-2.0, 0BSD, SIL OFL,
PostgreSQL License) разрешают коммерческое использование. Особое замечание —
по MinIO (AGPL-3.0), см. раздел 4.

## 1. Прямые зависимости Go (`go.mod`)

| Модуль | Версия | Лицензия | Назначение |
|---|---|---|---|
| github.com/jackc/pgx/v5 | v5.11.0 | MIT | драйвер PostgreSQL |
| github.com/minio/minio-go/v7 | v7.3.0 | Apache-2.0 | клиент S3/MinIO (фото) |
| github.com/xuri/excelize/v2 | v2.11.0 | BSD-3-Clause | импорт/экспорт Excel |
| github.com/golang-jwt/jwt/v5 | v5.3.1 | MIT | JWT-токены мобильного приложения |
| github.com/google/uuid | v1.6.0 | BSD-3-Clause | UUID |
| golang.org/x/crypto | v0.57.0 | BSD-3-Clause | bcrypt (пароли сотрудников) |
| golang.org/x/image | v0.38.0 | BSD-3-Clause | нормализация фото товаров и баннера (`internal/media`) |
| github.com/DATA-DOG/go-sqlmock | v1.5.2 | BSD-3-Clause | только тесты, в сборку не входит |

## 2. Транзитивные зависимости, входящие в серверный бинарник

| Модуль | Версия | Лицензия |
|---|---|---|
| github.com/cespare/xxhash/v2 | v2.3.0 | MIT |
| github.com/dustin/go-humanize | v1.0.1 | MIT |
| github.com/jackc/pgpassfile | v1.0.0 | MIT |
| github.com/jackc/pgservicefile | v0.0.0-20240606120523-5a60cdf6a761 | MIT |
| github.com/jackc/puddle/v2 | v2.2.2 | MIT |
| github.com/klauspost/compress | v1.19.2 | BSD-3-Clause, Apache-2.0, MIT (разные части пакета) |
| github.com/klauspost/cpuid/v2 | v2.4.0 | MIT |
| github.com/klauspost/crc32 | v1.3.0 | BSD-3-Clause |
| github.com/minio/crc64nvme | v1.1.1 | Apache-2.0 |
| github.com/minio/md5-simd | v1.1.2 | Apache-2.0 |
| github.com/philhofer/fwd | v1.2.0 | MIT |
| github.com/richardlehane/mscfb | v1.0.7 | Apache-2.0 |
| github.com/richardlehane/msoleps | v1.0.6 | Apache-2.0 |
| github.com/rs/xid | v1.6.0 | MIT |
| github.com/tiendc/go-deepcopy | v1.7.2 | MIT |
| github.com/tinylib/msgp | v1.6.4 | MIT |
| github.com/xuri/efp | v0.0.1 | BSD-3-Clause |
| github.com/xuri/nfp | v0.0.2-0.20250530014748-2ddeb826f9a9 | BSD-3-Clause |
| github.com/zeebo/xxh3 | v1.1.0 | BSD-2-Clause |
| go.yaml.in/yaml/v3 | v3.0.5 | MIT и Apache-2.0 |
| golang.org/x/net | v0.58.0 | BSD-3-Clause |
| golang.org/x/sync | v0.23.0 | BSD-3-Clause |
| golang.org/x/sys | v0.48.0 | BSD-3-Clause |
| golang.org/x/text | v0.42.0 | BSD-3-Clause |
| gopkg.in/ini.v1 | v1.67.3 | Apache-2.0 |

Компилятор и стандартная библиотека Go 1.26 — BSD-3-Clause.

## 3. Веб-ресурсы сайта и админки

Внешних CDN во время работы нет: всё, что нужно браузеру, отдаётся с нашего
домена (`/static/`, `/admin/static/`, хэш в `?v=`, кэш на год).

Собственные CSS/JS (`web/static`, `admin/static`) — код проекта, кроме перечисленного ниже.

Размещены у нас (self-hosted, в репозитории):

| Компонент | Версия | Лицензия | Где |
|---|---|---|---|
| htmx (`htmx.org`, файл `dist/htmx.min.js`, байт-в-байт с upstream — SRI `sha384-ujb1lZYy…` проверяется тестом) | 1.9.12 | 0BSD | `web/static/js/htmx.min.js`, `admin/static/js/htmx.min.js` |
| Шрифт Manrope, variable wght 200–800 (`@fontsource-variable/manrope`, © The Manrope Project Authors) — подмножества latin, cyrillic, cyrillic-ext | 5.3.0 | SIL Open Font License 1.1 | `{web,admin}/static/fonts/manrope-*.woff2`; текст лицензии — `{web,admin}/static/fonts/LICENSE-manrope.txt` |
| Шрифт Inter, variable wght 100–900 (`@fontsource-variable/inter`, © The Inter Project Authors) — только глифы Ң ң (U+04A2–04A3), которых нет в Manrope | 5.3.0 | SIL Open Font License 1.1 | `{web,admin}/static/fonts/inter-kyrgyz.woff2`; текст лицензии — `{web,admin}/static/fonts/LICENSE-inter.txt` |
| Tabler Icons webfont (`@tabler/icons-webfont`, © Paweł Kuna) — подмножество используемых иконок | 3.31.0 | MIT | `web/static/fonts/tabler-icons.woff2`, `web/static/css/icons.css`; текст лицензии — `web/static/fonts/LICENSE-tabler-icons.txt`; пересборка — `scripts/tabler-icons-subset.py` |

htmx и шрифты пересобираются `scripts/vendor-web-assets.py` (скачивает
закреплённые версии, сверяет хэши целостности, режет Inter до Ң/ң). Шрифты
Inter/Manrope в изменённом (подмножество) виде распространяются по OFL 1.1
под исходными именами семейств — OFL это допускает, т.к. Reserved Font Name
у них не объявлен.

## 4. Серверное ПО (отдельные процессы в Docker, не линкуется в код)

| Компонент | Версия / образ | Лицензия | Замечание |
|---|---|---|---|
| PostgreSQL | `postgres:16-alpine` | PostgreSQL License | |
| MinIO Server | `quay.io/minio/minio:RELEASE.2025-09-07T16-13-09Z` | **AGPL-3.0** | используется как отдельный немодифицированный сервис хранения файлов; код Cozy с ним не линкуется (общение по S3 API через `minio-go`, Apache-2.0). Требования AGPL по раскрытию кода относятся к изменениям самого MinIO — их нет. При желании полностью исключить AGPL MinIO можно заменить на любое S3-совместимое хранилище без изменения кода |
| MinIO Client `mc` | `quay.io/minio/mc:RELEASE.2025-08-13T08-35-41Z` | AGPL-3.0 | только в `scripts/backup.sh` (зеркалирование бэкапа) |
| Caddy | `caddy:2.11.4-alpine` | Apache-2.0 | |
| golang-migrate | `migrate/migrate:v4.18.3` | MIT | миграции БД |
| rclone | `rclone/rclone:1.75.1` (или пакет ОС) | MIT | off-site копия бэкапов, опционально |
| Базовый образ distroless | `gcr.io/distroless/static-debian12:nonroot` | Apache-2.0 (пакеты Debian внутри — их лицензии) | |
| Docker Engine, Compose | — | Apache-2.0 | |

## 5. Инструменты разработки и CI (в продукт не входят)

golangci-lint (GPL-3.0, только как внешний инструмент проверки кода в CI,
в продукт не входит), GitHub Actions `actions/checkout`, `actions/setup-go`
(MIT), `golangci/golangci-lint-action` (MIT), `appleboy/ssh-action` (MIT).

## 6. Внешние платные/облачные сервисы

Не являются open-source компонентами, используются по договорам/условиям
провайдеров на аккаунтах заказчика: Bakai OpenBanking, Nikita SMSPro,
Firebase Cloud Messaging (Google), Telegram Bot API, Let's Encrypt,
Cloudflare DNS.
