# Cozy — передача проекта заказчику

Документ для владельца магазина: что именно передаётся, где это лежит, какие
ключи и аккаунты нужно перевести на заказчика и в каком порядке. Здесь
**нет ни одного значения секрета** — только имена переменных и где их взять.

Пометка «уточнить» означает, что в репозиториях этого не видно и нужно
подтвердить у исполнителя до подписания Акта.

Связанные документы (подробности не дублируются):

- [`docs/deployment.md`](deployment.md) — развёртывание, переменные окружения, бэкапы;
- [`docs/admin-guide.md`](admin-guide.md) — инструкция для сотрудников магазина;
- `cozy_mobile/docs/build-and-release.md` — сборка и публикация приложения.

---

## 1. Состав поставки

### 1.1. Репозитории

| Репозиторий | Remote | Основная ветка | Что внутри |
|---|---|---|---|
| backend | `https://github.com/Nikemas/cozy_backend.git` | `main` | Go-сервер: сайт-магазин (`/`), админка (`/admin`), REST API для приложения (`/api/v1`), миграции БД, Docker/Caddy, скрипты деплоя и бэкапа, CI/CD |
| mobile | `https://github.com/Nikemas/cozy_flutter.git` | `main` | Flutter-приложение для Android и iOS (локальная папка называется `cozy_mobile`) |

- В backend, кроме `main`, на GitHub есть ветка `feat/backend-and-admin-panel`
  (историческая) — уточнить, нужна ли она, или удалить перед передачей.
- В mobile на GitHub только `main`.
- Аккаунт GitHub `Nikemas`, на котором лежат оба репозитория, — **уточнить**
  владельца (по договору репозитории должны оказаться у заказчика, см. раздел 4).
- Всё, что попадает в `main` backend, автоматически выкатывается на staging
  (раздел 2). Prod выкатывается только вручную.

### 1.2. Где что лежит (backend)

| Путь | Что |
|---|---|
| `cmd/server` | точка входа сервера, подкоманды (`create-owner` и др.) |
| `internal/` | бизнес-логика: заказы, оплата Bakai, SMS, push, Telegram, каталог, импорт |
| `web/`, `admin/` | шаблоны и статика сайта и админки |
| `locales/` | переводы RU / KY |
| `migrations/` | SQL-миграции базы данных |
| `docker/` | `Dockerfile`, compose-файлы, `Caddyfile` (staging) и `Caddyfile.prod` |
| `scripts/` | `deploy.sh`, `migrate.sh`, `backup.sh`, `env.sh`, `smoke.sh` |
| `.github/workflows/` | `ci.yml`, `deploy.yml` (staging), `deploy-prod.yml` (prod) |
| `openapi.yaml` | спецификация REST API для приложения |
| `.env.example` | шаблон всех переменных окружения (без значений) |

### 1.3. Документация

| Документ | Для кого |
|---|---|
| `cozy_backend/README.md` | разработчик: стек, локальный запуск, CI/CD, staging, прод |
| `cozy_backend/docs/deployment.md` | администратор сервера: установка с нуля, `.env`, деплой, откат, бэкапы, мониторинг |
| `cozy_backend/docs/admin-guide.md` | сотрудники магазина: работа в админке |
| `cozy_backend/docs/runbook-demo.md` | сценарий демонстрации |
| `cozy_backend/docs/performance.md` | замеры производительности |
| `cozy_backend/docs/handover.md` | этот документ |
| `cozy_backend/openapi.yaml` | REST API |
| `cozy_backend/THIRD_PARTY_LICENSES.md` | open-source лицензии backend (в т.ч. MinIO — AGPL-3.0, раздел 4 файла) |
| `cozy_mobile/README.md` | разработчик приложения |
| `cozy_mobile/docs/build-and-release.md` | сборка, подпись, Firebase, публикация; раздел 9 — что хранить у заказчика |
| `cozy_mobile/docs/release-checklist.md` | решения и доступы для выхода в сторы |
| `cozy_mobile/docs/store/listing.md`, `data-safety.md`, `review-notes.md`, `screenshots-plan.md`, `screenshots/` | материалы карточек App Store / Google Play |
| `cozy_mobile/THIRD_PARTY_LICENSES.md` | open-source лицензии приложения |

---

## 2. Инфраструктура

### 2.1. Как устроено

Один Go-сервер отдаёт сайт, админку и API на одном домене. На сервере всё
работает в Docker Compose (`docker/docker-compose.prod.yml`): `backend`,
`postgres` (16), `minio` (фото), `caddy` (HTTPS). Образ backend собирается
прямо на сервере из git-checkout `/opt/cozy`, registry не используется.
Наружу открыты только 22, 80, 443. TLS-сертификаты Let's Encrypt Caddy
получает и продлевает сам — отдельно покупать сертификат не нужно.

### 2.2. Staging и prod

| | staging (работает сейчас) | prod (к сдаче) |
|---|---|---|
| Домены | `cozy.erpsystemsales.com`, `media.cozy.erpsystemsales.com` | `cozy.kg`, `www.cozy.kg` (редирект), `media.cozy.kg` |
| Сервер | VPS `95.215.244.199`, `/opt/cozy` | отдельный VPS (ещё не создан), `/opt/cozy` |
| Caddy | `docker/Caddyfile` (+ `noindex`) | `docker/Caddyfile.prod` (хосты из `SITE_HOST`/`MEDIA_HOST`) |
| Compose-проект / образ | `docker` / `cozy-backend:<sha>` | `cozy-prod` / `cozy-backend-prod:<sha>` |
| Деплой | автоматически на каждый push в `main` (`deploy.yml`) | вручную: Actions → «Deploy production» → тег/SHA, подтверждение в Environment `production` (`deploy-prod.yml`) |
| Оплата / SMS | допускаются `PAYMENTS_PROVIDER=mock`, `SMS_MOCK_OTP=true` | только `bakai` и реальные SMS — иначе сервер не стартует |

Оба workflow сначала прогоняют полный CI (`ci.yml`), затем по SSH запускают
`scripts/deploy.sh`: сборка образа, миграции, переключение, ожидание `/readyz`,
откат на предыдущий образ при неудаче.

### 2.3. Домены и DNS

- `erpsystemsales.com` — домен **исполнителя**, DNS в Cloudflare. Используется
  только для staging. После передачи staging либо переводится на поддомен
  заказчика, либо выключается — **решение заказчика**.
- `cozy.kg` — домен заказчика. По `docs/deployment.md` (раздел 5.3) зона
  обслуживается Cloudflare; регистратор и владелец Cloudflare-аккаунта —
  **уточнить**. Перед первым prod-деплоем нужны A-записи `cozy.kg`,
  `www.cozy.kg`, `media.cozy.kg` на IP prod-сервера.
- Хостинг-провайдер staging-VPS и чей это аккаунт — **уточнить**.

---

## 3. Секреты и ключи

Значения хранятся только в `/opt/cozy/.env` на сервере (в git не попадает),
в секретах GitHub и в менеджере паролей заказчика. Полный список переменных —
`.env.example`, описание — `docs/deployment.md`, раздел 4.

Колонка «Владелец сейчас»: исп. — исполнитель, зак. — заказчик.

### 3.1. Переменные `.env` на сервере

| Переменная | Что это | Кто выдаёт | Владелец сейчас | Как сменить |
|---|---|---|---|---|
| `JWT_SECRET` | подпись токенов входа (≥32 байт) | генерируется: `openssl rand -base64 48` | исп. (генерировал) | новое значение в `.env` → redeploy; все покупатели и сотрудники разлогинятся |
| `POSTGRES_PASSWORD` | пароль БД, из него собирается `DATABASE_URL` | генерируется: `openssl rand -hex 32` | исп. | `ALTER ROLE cozy PASSWORD …` в Postgres, затем `.env` → redeploy |
| `MINIO_ACCESS_KEY`, `MINIO_SECRET_KEY` | root-логин хранилища фото | генерируется: `openssl rand -hex 20` | исп. | сменить в `.env` и пересоздать контейнеры `minio` + `backend` (проверить на staging) |
| `BAKAI_API_TOKEN` | токен Bakai OpenBanking для ссылки на оплату | Bakai, кабинет мерчанта (`POST /Auth/Login`) | исп. (по списку задач — на аккаунте исполнителя/Дордой) | перевыпустить на мерчанта заказчика → `.env` → redeploy |
| `BAKAI_QR_TOKEN`, `BAKAI_ACCOUNT_NO` | кнопка «Показать QR» на сайте | Bakai | исп. — уточнить | так же, как `BAKAI_API_TOKEN` |
| `BAKAI_WEBHOOK_TOKEN` | общий секрет вебхука оплаты (Caddy подставляет его в `X-Webhook-Token`) | генерируется: `openssl rand -hex 32` | исп. | `.env` → redeploy (перезапускаются `caddy` и `backend`); URL в кабинете Bakai не меняется |
| `NIKITA_API_KEY` | SMS-коды входа (smspro.nikita.kg, «СЕРВИС OTP») | Nikita | уточнить (по списку задач ключ ещё не получен) | новый ключ в кабинете Nikita → `.env` → redeploy |
| `REVIEW_PHONE`, `REVIEW_OTP_CODE` | тестовый вход для ревьюеров сторов | задаётся вручную | — | очистить обе после прохождения ревью |
| `FCM_CREDENTIALS_FILE` (+ файл `secrets/firebase-service-account.json`), `FCM_PROJECT_ID` | отправка push через Firebase | Firebase Console → service account | исп. (проект `cozy-7f364`) | создать новый ключ service account → заменить файл → redeploy → удалить старый ключ в IAM |
| `TELEGRAM_BOT_TOKEN`, `TELEGRAM_CHAT_ID` | уведомление персоналу о новом заказе | @BotFather / id группы | уточнить | `/revoke` в @BotFather или новый бот заказчика → `.env` → redeploy |
| `SHOP_*` (`SHOP_PHONE`, `SHOP_EMAIL`, `SHOP_INN`, `SHOP_BANK_DETAILS` …) | контакты и реквизиты на сайте | заказчик | зак. (сейчас пустые на staging) | не секрет; `.env` → redeploy |
| `SITE_HOST`, `MEDIA_HOST`, `PUBLIC_BASE_URL`, `MINIO_PUBLIC_ENDPOINT` | домены prod | — | — | не секрет |

### 3.2. Вне `.env` (cron и сервер)

| Имя | Что это | Владелец сейчас | Как сменить |
|---|---|---|---|
| `BACKUP_RCLONE_REMOTE` + `rclone.conf` на VPS | off-site копия бэкапов (Backblaze B2 / S3) | уточнить (staging использует remote `b2:cozy-backups/staging`) | бакет и ключ **на аккаунт заказчика** (`docs/deployment.md` 8.2) → `rclone config` |
| `BACKUP_HEALTHCHECK_URL` | алерт, если ночной бэкап не прошёл (healthchecks.io) | уточнить | новый чек на аккаунте заказчика → строка cron |
| SSH-ключи в `/home/deploy/.ssh/authorized_keys` | вход на сервер | исп. | удалить ключи исполнителя, добавить ключи заказчика |
| deploy key репозитория на сервере | `git fetch` из приватного репо | исп. | новый read-only deploy key в новом репозитории |
| пароль владельца админки | вход в `/admin` | исп. создавал | сменить в админке; первый владелец prod — `server create-owner` |

### 3.3. Секреты GitHub Actions (backend)

| Секрет | Где | Что это | Как сменить |
|---|---|---|---|
| `DEPLOY_HOST` | repo secrets | IP/хост staging | при переезде |
| `DEPLOY_USER` | repo secrets | SSH-пользователь (`deploy`) | — |
| `DEPLOY_SSH_KEY` | repo secrets | приватный ключ деплоя (только для GitHub) | новая пара ed25519 → публичную в `authorized_keys`, приватную сюда, старую удалить с сервера |
| `DEPLOY_SSH_FINGERPRINT` | repo secrets | отпечаток host key сервера (обязателен) | при смене сервера: `ssh-keygen -lf /etc/ssh/ssh_host_ecdsa_key.pub` |
| `DEPLOY_PATH` | repo secrets | `/opt/cozy` | — |
| `DEPLOY_SSH_PORT` | repo secrets, опц. | порт SSH (по умолчанию 22) | — |
| `PROD_DEPLOY_HOST`, `PROD_DEPLOY_USER`, `PROD_DEPLOY_SSH_KEY`, `PROD_DEPLOY_SSH_FINGERPRINT`, `PROD_DEPLOY_PATH`, `PROD_DEPLOY_SSH_PORT` | Environment `production` | то же для prod-сервера | задаются заказчиком при создании prod |

`ci.yml` секретов не использует. В репозитории mobile GitHub Actions нет.

### 3.4. Мобильное приложение

| Имя | Что это | Владелец сейчас | Примечание |
|---|---|---|---|
| `--dart-define=COZY_API_BASE_URL` (синоним `API_BASE_URL`) | адрес API в сборке | — | не секрет; release без него не собирается. Debug по умолчанию ходит на staging (`kStagingApiBaseUrl` в `lib/core/network/dio_client.dart`) |
| `--dart-define=COZY_USE_REAL_BACKEND` | демо-режим без сервера | — | не секрет |
| `android/key.properties` (`storeFile`, `storePassword`, `keyAlias`, `keyPassword`) + upload keystore `.jks` | подпись Android | по списку задач ещё не создан | создавать сразу у заказчика; потеря = нельзя обновлять приложение |
| `android/app/google-services.json` | конфиг Firebase Android (проект `cozy-7f364`) | исп. | не секрет, лежит в git; после переноса проекта — ограничить API-ключ в Google Cloud Console |
| `ios/Runner/GoogleService-Info.plist` | конфиг Firebase iOS | **нет файла** | push и Crashlytics на iOS не работают до его появления |
| APNs Auth Key (`.p8`) | push на iOS через Firebase | не создан | создать в Apple Developer заказчика, загрузить в Firebase |
| `COZY_ASC_KEY_ID`, `COZY_ASC_ISSUER_ID` + `AuthKey_<KEY_ID>.p8` | загрузка в TestFlight (`scripts/ios_testflight.sh`) | исп. — уточнить | выпустить новый ключ в App Store Connect заказчика, старый отозвать |
| `COZY_TEAM_ID` в `ios/Flutter/Signing.xcconfig` | Apple Team, которым подписывается приложение | сейчас `2LKUAG7BRP` — команда исполнителя | заменить на Team ID заказчика (`build-and-release.md` 4.2) |

Ключа Google Maps нет: карта филиалов — keyless iframe
`maps.google.com/maps?...&output=embed` (сервер собирает ссылку,
`internal/storefront/branches.go`). Передавать нечего.

---

## 4. Аккаунты к переводу на заказчика

| Сервис | Что сейчас | Что сделать | Статус |
|---|---|---|---|
| GitHub | репозитории у аккаунта `Nikemas` | Transfer ownership в аккаунт/организацию заказчика (Settings → Danger Zone) или передача архива + push в новый репо; затем заново завести секреты Actions и Environment `production` | уточнить владельца `Nikemas` |
| VPS staging | `95.215.244.199` | перенос на аккаунт заказчика или выключение после запуска prod | провайдер уточнить |
| VPS prod | нет | заказчик арендует на своё имя (2 vCPU / 4 ГБ / 40+ ГБ, Ubuntu LTS) | не создан |
| Домен `cozy.kg` и DNS | у заказчика, Cloudflare | дать доступ к DNS на время настройки, потом забрать | регистратор уточнить |
| Домен `erpsystemsales.com` | исполнителя | не передаётся; staging с него снять | — |
| Bakai (мерчант) | токен на аккаунте исполнителя/Дордой | мерчант-договор на заказчика, перевыпуск `BAKAI_*`, регистрация вебхука `https://cozy.kg/api/v1/payments/bakai/webhook` | открыто |
| Nikita SMSPro | — | кабинет на заказчика, подключить «СЕРВИС OTP», выпустить `NIKITA_API_KEY` | уточнить |
| Firebase `cozy-7f364` (Android + iOS) | исполнитель | добавить Google-аккаунт заказчика как Owner в IAM, затем убрать исполнителя; добавить iOS-приложение `kg.cozy.cozyMobile` | открыто |
| Apple Developer / App Store Connect | Team `2LKUAG7BRP` (исполнитель) | аккаунт заказчика (Individual/Organization), исполнитель — участник команды | решение заказчика |
| Google Play Console | нет | аккаунт заказчика (25 USD, верификация); исполнитель — пользователь с правами | решение заказчика |
| Telegram-бот | используется (`TELEGRAM_BOT_TOKEN`) | бот и группа персонала на телефоне заказчика | уточнить, чей бот |
| Backblaze B2 / S3 (бэкапы) | staging: `b2:cozy-backups/staging` | бакет на аккаунт заказчика | уточнить |
| healthchecks.io | опционально | чек на аккаунт заказчика | уточнить, настроен ли |
| E-mail | `SHOP_EMAIL` — почта магазина | ящик заказчика (нужен и для сторов, и для Firebase/Apple/Google) | значение даёт заказчик |
| Google Maps | keyless embed | ничего | — |

---

## 5. Чек-лист передачи

Порядок важен: сначала заказчик получает доступ, потом меняются ключи, и
только в самом конце исполнитель теряет доступ.

**Шаг 1. Аккаунты заказчика**

- [ ] Google-аккаунт / почта магазина для сервисов
- [ ] GitHub (аккаунт или организация)
- [ ] VPS для prod у провайдера на имя заказчика
- [ ] Доступ к DNS `cozy.kg`
- [ ] Мерчант Bakai, кабинет Nikita
- [ ] Apple Developer, Google Play Console
- [ ] Бакет для бэкапов (B2/S3), healthchecks.io (опционально)
- [ ] Менеджер паролей заказчика — сюда кладутся все значения ниже

**Шаг 2. Заказчик — владелец**

- [ ] Репозитории переданы заказчику (transfer), исполнитель — collaborator
- [ ] Firebase `cozy-7f364`: заказчик — Owner
- [ ] Исполнитель добавлен в Apple/Google как участник (не владелец)

**Шаг 3. Ротация секретов** (новые значения — сразу в менеджер паролей)

- [ ] Сгенерировать новые `JWT_SECRET`, `POSTGRES_PASSWORD`, `MINIO_ACCESS_KEY`/`MINIO_SECRET_KEY`, `BAKAI_WEBHOOK_TOKEN`
- [ ] Получить на заказчика `BAKAI_API_TOKEN` (+ `BAKAI_QR_TOKEN`, `BAKAI_ACCOUNT_NO`), `NIKITA_API_KEY`
- [ ] Новый ключ service account Firebase → `secrets/firebase-service-account.json`
- [ ] Бот Telegram заказчика → `TELEGRAM_BOT_TOKEN`, `TELEGRAM_CHAT_ID`
- [ ] Заполнить `SHOP_*`, `SITE_HOST`, `MEDIA_HOST`, `PUBLIC_BASE_URL`, `APP_STORE_URL_*`
- [ ] Новая пара SSH-ключей деплоя; ключи заказчика в `authorized_keys`
- [ ] Android upload keystore и `key.properties` у заказчика; APNs `.p8` и App Store Connect API key — у заказчика
- [ ] Сменить пароль владельца админки

**Шаг 4. Секреты GitHub**

- [ ] В новом репозитории: `DEPLOY_*` (staging, если остаётся)
- [ ] Environment `production` с Required reviewers и секретами `PROD_DEPLOY_*`
- [ ] Новый read-only deploy key репозитория на сервере (`git remote set-url` на новый адрес)

**Шаг 5. Деплой**

- [ ] DNS `cozy.kg`, `www.cozy.kg`, `media.cozy.kg` → IP prod
- [ ] Prod: установка по `docs/deployment.md` раздел 5, затем Actions → «Deploy production» с тегом релиза
- [ ] `server create-owner` — первый владелец админки prod
- [ ] Вебхук в кабинете Bakai, бэкапы по cron, off-site копия
- [ ] Приложение собрано с `COZY_API_BASE_URL=https://cozy.kg/api/v1` и Team ID заказчика

**Шаг 6. Проверка**

- [ ] `bash scripts/smoke.sh https://cozy.kg` — все PASS (код выхода 0)
- [ ] `/readyz` = 200
- [ ] Пробный заказ: SMS-вход, оплата Bakai на малую сумму и возврат, сообщение в Telegram, push на Android и iOS
- [ ] Пробный бэкап и восстановление (`docs/deployment.md` 8.4)

**Шаг 7. Отзыв доступа исполнителя**

- [ ] Удалить старые SSH-ключи исполнителя и старый deploy key
- [ ] Отозвать старые токены: Bakai, старый ключ service account Firebase, старый бот, старый App Store Connect key
- [ ] Убрать исполнителя из Owner в Firebase, Cloudflare, у провайдера VPS
- [ ] Выключить или перенести staging на `erpsystemsales.com`
- [ ] Отозвать сторонние ключи, которые использовались при разработке (по списку задач — ключ Gemini для демо-фото)

---

## 6. Что остаётся открытым на момент передачи

Решения владельца (по списку задач проекта на 06–07.10.2026):

- **MinIO.** Закреплённая версия `RELEASE.2025-09-07T16-13-09Z`; официальные
  образы MinIO больше не выходят, в используемой версии есть известная
  уязвимость. Варианты: оставить, собрать образ самим или заменить на другое
  S3-совместимое хранилище (код не меняется — `minio-go` работает с любым S3).
  Лицензия MinIO — AGPL-3.0, см. `THIRD_PARTY_LICENSES.md`.
- **iPad.** Сейчас `TARGETED_DEVICE_FAMILY = 1,2` (iPhone + iPad). С iPad
  App Store требует скриншоты 13" и ревью на iPad; если не нужен — оставить
  только iPhone.
- **Финальный bundle ID iOS** (`kg.cozy.cozyMobile`) — после публикации не меняется.
- **Вычитка кыргызского** носителем языка (файлы `ky-review-2026-10-05/07/08`
  в материалах проекта, плюс вопросы по названиям цветов).
- **Скриншоты сторов** — окончательные, на боевом каталоге
  (`cozy_mobile/docs/store/screenshots-plan.md`).
- **Импорт xlsx**: остаток «устанавливается» (заменяется) или «добавляется».
- Контакты и реквизиты магазина (`SHOP_*`) — не заполнены.

Технические пункты, зависящие от доступов заказчика: Firebase iOS
(`GoogleService-Info.plist`, APNs), ключ Nikita, prod-сервер и домен
`cozy.kg`, Android keystore, аккаунты Apple/Google.

По договору prod и передача кода выполняются после оплаты (п. 5.4–5.5 договора).
